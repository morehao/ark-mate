package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/core/capability"
	defn "github.com/morehao/ark-mate/internal/core/definition"
	"github.com/morehao/ark-mate/internal/infra/acp"
	"github.com/morehao/golib/glog"
)

// ——— 技能定义的写面（新建 / 覆盖写 / 删除） ———
//
// 写面只做"校验 → 提交 → 更新内存"，但顺序与失败处理是全部的难点：
//
//	① **先校验**（`capability.ValidateSkill` 是唯一闸门：正文扫描、scope 枚举、id 规则、
//	   runtimes 重复），且校验必须看到**合并后**的完整定义；
//	② 通过后才提交给载体（C9 的文件纪律 / DB 的 revision CAS，见 `mechanism`）；
//	③ 提交成功后才更新内存——顺序倒过来就会留下"内存里有、载体上没有"的幻影
//	   （本进程可用、重启即消失，是最难查的一类不一致）。
//
// 顺序不能换：先提交再校验，坏定义就短暂落下过；只改内存不提交，重启回到旧值而用户以为改成了。
// 为此 `repo.Open` 只准备草稿不提交，由本文件按序驱动校验与提交。
//
// **校验与提交分开之后，回滚路径消失了**：M6-3 之前是"先 `RegisterSkill` 入表、提交失败再回滚
// 内存"，而回滚正是最容易漏的一条（漏了就成了幻影）。现在闸门是纯函数（`ValidateSkill`），
// 同一份输入刚验过就必然能再入表，所以提交成功后那次 `RegisterSkill` 是**必然成功**的。

// WriteSkill 写面主体（create / update 共用的全部顺序与失败处理）。
func (m *Plane) WriteSkill(ctx context.Context, pathID string, req WriteRequest, create bool) (*WriteResult, error) {
	if m.caps == nil {
		return nil, defn.ErrCapabilityUnavailable()
	}
	repo, err := m.defs.RepoFor(defn.CapSubSkills)
	if err != nil {
		return nil, err
	}
	patch := defn.RawPatch(req)

	id := pathID
	bodyID, hasBodyID, err := defn.RawString(req, "id")
	if err != nil {
		return nil, err
	}
	if create {
		if !hasBodyID || strings.TrimSpace(bodyID) == "" {
			return nil, defn.InvalidParamErr("新建 skill 必须带 id")
		}
		id = bodyID
	} else {
		if hasBodyID && bodyID != pathID {
			return nil, defn.InvalidParamErr(fmt.Sprintf(
				"路径 id %q 与 body id %q 不一致（id 不可改：要换 id 就删掉重建）", pathID, bodyID))
		}
		// 定义必须自带 id：手写文件漏了 id 时顺手补上（否则装载时该文件只能被跳过）。
		patch["id"] = pathID
	}

	if !defn.ValidDefinitionID(id) {
		if create {
			return nil, defn.InvalidDefinitionIDErr("skill", id)
		}
		// update 的幂等友好处理：id 非法时它**不可能**存在，404 比 400 更准确。
		if _, in := m.caps.SkillByID(id); !in {
			return nil, notFoundErr(id)
		}
		return nil, defn.InvalidDefinitionIDErr("skill", id)
	}

	_, inReg := m.caps.SkillByID(id)
	stored, err := repo.Exists(ctx, id)
	if err != nil {
		return nil, err
	}

	if create {
		// 已存在 → 冲突，**不覆盖**。查两处是因为"存在"有两个来源：注册表（内存，可能来自
		// 代码注入或本进程刚建的）与载体（文件是手工建的、本进程还没装载；DB 行是导入进来的）。
		if inReg || stored {
			return nil, existsErr(id)
		}
	} else if !inReg && !stored {
		// PUT 是"改一个已存在的定义"：两者都没有就是不存在（不是"顺便新建"，那样打错 id
		// 会静默建出一个空技能）。只有载体、或只有内存条目，都算存在（后者是"把代码注入的
		// 技能落成定义"这条修复路径）。
		return nil, notFoundErr(id)
	}

	doc, rev, existed, err := repo.Open(ctx, id, patch)
	if err != nil {
		return nil, err
	}
	// 合并后的强类型视图：正文扫描闸门看的是**最终会提交的内容**，而不是请求体那一段
	// （`PUT {"name":"x"}` 不该让正文凭空变空从而绕过扫描）。
	merged, err := doc.Marshal()
	if err != nil {
		return nil, err
	}
	var sk capability.Skill
	if err := json.Unmarshal(merged, &sk); err != nil {
		return nil, defn.InvalidParamErr(fmt.Sprintf("skill 定义无法解析：%v", err))
	}
	if sk.ID == "" {
		sk.ID = id
	}
	sk.SourceFile = repo.SourceFile(id) // 文件载体：事实源文件（Library 的 origin/source_file 依据）

	if err := capability.ValidateSkill(&sk); err != nil {
		// 闸门拒绝（critical 正文 / id 非法 / scope 非法 / runtimes 重复）→ 40001。
		// **零字节写入**：patch 只在草稿里，草稿从未提交。
		return nil, acp.NewError(acp.ClassRequest, "definition_invalid",
			fmt.Sprintf("skill %q 的定义被拒绝：%v", id, err), err)
	}
	created, conflict, err := repo.Commit(ctx, id, doc, rev, existed)
	if err != nil {
		return nil, err
	}
	if conflict {
		// 载体的乐观锁没中：文件被手改 / 行 revision 变了（含"我以为新建、别人抢先建出来"）。
		// 语义与 40901 的老文案一致——刷新重试，绝不 last-write-wins。
		return nil, defn.DefinitionChangedErr(repo.Locator(id))
	}
	// 提交成功后才入表：同一份定义刚过闸门（纯函数），这一次必然成功；真出错也只可能来自
	// "注册表被并发改坏"，那种情况下**不能**谎报写失败（载体上已经写成功了），记 WARN 更诚实。
	NormalizeRuntimeDecls(&sk)
	if err := m.caps.RegisterSkill(&sk); err != nil {
		glog.Warnw(ctx, "capability: skill 定义已提交但入表失败（内存视图可能落后）", "id", id, "err", err)
	}

	resp := &WriteResult{
		ID: sk.ID, Path: repo.Locator(id), Created: create || created,
		Warnings: Warnings(sk),
	}
	t := m.ReconcileAll()
	resp.Written, resp.Removed, resp.MaterializeErrors = t.Written, t.Removed, t.Errors
	switch {
	case create:
		resp.NextAction = "已新建：声明了候选 runtime 的 agent 下次拉起即可见；已物化的副本已按当前投影写出"
	case created:
		resp.NextAction = "已把该 skill 落成定义（此前只存在于内存/代码注入）；此后的修改都可持久化"
	default:
		resp.NextAction = "已覆盖写：只改了请求体里出现的键（其余键与未知键原样保留）；物化副本已按当前投影重校"
	}
	return resp, nil
}

// DeleteSkill 删除一个 skill 定义（DELETE /skills/:id）：删定义 + 撤回物化副本 + 摘内存条目。
//
// 幂等：定义本来就不存在 → `deleted:false` 且无错（"让它不在"不要求"它曾在"）。
func (m *Plane) DeleteSkill(ctx context.Context, id string) (*DeleteResult, error) {
	if m.caps == nil {
		return nil, defn.ErrCapabilityUnavailable()
	}
	repo, err := m.defs.RepoFor(defn.CapSubSkills)
	if err != nil {
		return nil, err
	}
	if !defn.ValidDefinitionID(id) {
		if _, in := m.caps.SkillByID(id); !in {
			return deleteNoop(id, ""), nil // 非法 id 不可能存在 → 幂等无操作
		}
		return nil, defn.InvalidDefinitionIDErr("skill", id)
	}
	_, inReg := m.caps.SkillByID(id)
	stored, err := repo.Exists(ctx, id)
	if err != nil {
		return nil, err
	}
	locator := repo.Locator(id)
	if !inReg && !stored {
		return deleteNoop(id, locator), nil
	}

	deleted, conflict, err := repo.Remove(ctx, id)
	if err != nil {
		return nil, err
	}
	if conflict {
		return nil, defn.DefinitionChangedErr(locator)
	}
	m.caps.RemoveSkill(id)
	resp := &DeleteResult{ID: id, Deleted: deleted, Path: locator}
	// 删定义必须一起撤回已物化副本：否则底座目录里还留着它，agent 照旧看得见。
	t := m.ReconcileAll()
	resp.Removed, resp.MaterializeErrors = t.Removed, t.Errors
	resp.NextAction = "已删除：定义已移除，已物化的副本已撤回（正在跑的会话不受影响，下一轮/下次拉起不再注入）"
	return resp, nil
}

// deleteNoop 幂等删除的空结果。
func deleteNoop(id, path string) *DeleteResult {
	return &DeleteResult{ID: id, Deleted: false, Path: path,
		NextAction: "该 skill 本来就不存在，无需删除"}
}

// existsErr 新建时 id 已占用（40901：与当前状态冲突——参数没问题，是时机/状态问题）。
func existsErr(id string) error {
	return acp.NewError(acp.ClassLimit, "skill_exists",
		fmt.Sprintf("skill %q 已存在：改它用 PUT /skills/%s，或换一个 id", id, id), nil)
}

// notFoundErr 目标 skill 不存在（40401，由 pkg/code 的 notFoundCodes 提档）。
func notFoundErr(id string) error {
	return acp.NewError(acp.ClassRequest, "skill_not_found", fmt.Sprintf("未知 skill %q", id), nil)
}
