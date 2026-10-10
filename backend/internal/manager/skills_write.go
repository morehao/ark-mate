package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/golib/glog"
)

// ——— M2-2/M2-3：能力定义的写面（Skill / MCP） ———
//
// 写面只做"校验 → 提交 → 更新内存"，但顺序与失败处理是全部的难点：
//
//	① **先校验**（`ValidateSkill`/`ValidateMcpServer` 是唯一闸门：正文扫描、transport 枚举、
//	   stdio 绝对路径、密钥引用词法、明文与密钥键不重复），且校验必须看到**合并后**的完整定义；
//	② 通过后才提交给载体（C9 的文件纪律 / DB 的 revision CAS，见 capability_repo.go）；
//	③ 提交成功后才更新内存——顺序倒过来就会留下"内存里有、载体上没有"的幻影
//	   （本进程可用、重启即消失，是最难查的一类不一致）。
//
// 顺序不能换：先提交再校验，坏定义就短暂落下过；只改内存不提交，重启回到旧值而用户以为改成了。
// 为此 `repo.open` 只准备草稿不提交，由本文件按序驱动校验与提交。
//
// **校验与提交分开之后，回滚路径消失了**：M6-3 之前是"先 `RegisterSkill` 入表、提交失败再回滚
// 内存"，而回滚正是最容易漏的一条（漏了就成了幻影）。现在闸门是纯函数（`ValidateSkill`），
// 同一份输入刚验过就必然能再入表，所以提交成功后那次 `RegisterSkill` 是**必然成功**的。

// rawPatch 把写面的原文 JSON 对象转成落盘层要的 patch。
//
// `null` 在这里被解释成**删掉这个键**（而不是写一个 null 值）：定义文件的键都是 omitempty 语义，
// 写 null 与删键在装载后等价，但磁盘上留一个 null 会让手读文件的人以为"这个键有值"。
func rawPatch(req map[string]json.RawMessage) map[string]any {
	out := make(map[string]any, len(req))
	for k, raw := range req {
		t := bytes.TrimSpace(raw)
		if len(t) == 0 || string(t) == "null" {
			out[k] = nil
			continue
		}
		// json.RawMessage 实现 MarshalJSON → DefinitionFile 原样写入这一段字节：
		// 嵌套对象/数组不会在"解出结构体再序列化"的过程中丢键或变形。
		out[k] = raw
	}
	return out
}

// rawString 取原文对象里某个键的字符串值（不存在/为 null → ok=false）。类型不符 → 40000。
func rawString(req map[string]json.RawMessage, key string) (string, bool, error) {
	raw, ok := req[key]
	if !ok || string(bytes.TrimSpace(raw)) == "null" {
		return "", false, nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", true, code.Error(code.InvalidParam, fmt.Sprintf("字段 %q 须为字符串", key))
	}
	return v, true, nil
}

// capWriteErrParam 参数类写面错误（缺失/类型/取值不合法）→ 40000。
//
// 与"语义被拒"（40001，如正文扫描 critical）分开：前者改参数就好，后者要改内容。
func capWriteErrParam(msg string) error { return code.Error(code.InvalidParam, msg) }

// materializeTally 一次物化重校的计数与失败原因（写面/删除共用；两边 DTO 字段名不同，
// 所以用这个中性载体而不是直接往 DTO 上写）。
type materializeTally struct {
	Written int
	Removed int
	Errors  []string
}

// reconcileSkillProjections 写面之后立刻重校物化（与 §13.4 S4 的启停共用同一套逻辑）。
//
// 失败**不回滚定义**：定义是对的，错的是某个 runtime 的目录（权限/占位/只读），如实把原因回带，
// 让用户知道"定义生效了，但那个底座没写出去"。
func (s *Service) reconcileSkillProjections() materializeTally {
	var t materializeTally
	for _, p := range s.skillProjectionsSnapshot() {
		w, r, err := s.ReconcileSkillProjection(p)
		t.Written += w
		t.Removed += r
		if err != nil {
			t.Errors = append(t.Errors, fmt.Sprintf("物化重校失败（root=%s runtime=%s）：%v",
				p.Projection.Root, p.Runtime, err))
		}
	}
	return t
}

// CreateSkill 新建一个 skill 定义（POST /skills）：id 必填，已存在 → `skill_exists`(40901)。
func (s *Service) CreateSkill(ctx context.Context, req dtocapability.SkillWriteReq) (*dtocapability.SkillWriteResp, error) {
	return s.writeSkill(ctx, "", req, true)
}

// UpdateSkill 覆盖写一个 skill 定义（PUT /skills/:id）。
//
// 语义：请求体里**出现的键**才改（未出现的保留原值）；`null` 删键；`""`/`[]` 显式清空。
// 路径 id 与 body id 不一致 → 40000（id 不可改：改 id 等于换一个资源）。
func (s *Service) UpdateSkill(ctx context.Context, id string, req dtocapability.SkillWriteReq) (*dtocapability.SkillWriteResp, error) {
	return s.writeSkill(ctx, id, req, false)
}

// writeSkill 写面主体（create / update 共用的全部顺序与失败处理）。
func (s *Service) writeSkill(ctx context.Context, pathID string, req dtocapability.SkillWriteReq, create bool) (*dtocapability.SkillWriteResp, error) {
	if s.caps == nil {
		return nil, errCapabilityUnavailable()
	}
	repo, err := s.definitionRepoFor(capSubSkills)
	if err != nil {
		return nil, err
	}
	patch := rawPatch(req)

	id := pathID
	bodyID, hasBodyID, err := rawString(req, "id")
	if err != nil {
		return nil, err
	}
	if create {
		if !hasBodyID || strings.TrimSpace(bodyID) == "" {
			return nil, capWriteErrParam("新建 skill 必须带 id")
		}
		id = bodyID
	} else {
		if hasBodyID && bodyID != pathID {
			return nil, capWriteErrParam(fmt.Sprintf(
				"路径 id %q 与 body id %q 不一致（id 不可改：要换 id 就删掉重建）", pathID, bodyID))
		}
		// 定义必须自带 id：手写文件漏了 id 时顺手补上（否则装载时该文件只能被跳过）。
		patch["id"] = pathID
	}

	if !validDefinitionID(id) {
		if create {
			return nil, invalidDefinitionIDErr("skill", id)
		}
		// update 的幂等友好处理：id 非法时它**不可能**存在，404 比 400 更准确。
		if _, in := s.caps.SkillByID(id); !in {
			return nil, skillNotFoundErr(id)
		}
		return nil, invalidDefinitionIDErr("skill", id)
	}

	_, inReg := s.caps.SkillByID(id)
	stored, err := repo.exists(ctx, id)
	if err != nil {
		return nil, err
	}

	if create {
		// 已存在 → 冲突，**不覆盖**。查两处是因为"存在"有两个来源：注册表（内存，可能来自
		// 代码注入或本进程刚建的）与载体（文件是手工建的、本进程还没装载；DB 行是导入进来的）。
		if inReg || stored {
			return nil, skillExistsErr(id)
		}
	} else if !inReg && !stored {
		// PUT 是"改一个已存在的定义"：两者都没有就是不存在（不是"顺便新建"，那样打错 id
		// 会静默建出一个空技能）。只有载体、或只有内存条目，都算存在（后者是"把代码注入的
		// 技能落成定义"这条修复路径）。
		return nil, skillNotFoundErr(id)
	}

	doc, rev, existed, err := repo.open(ctx, id, patch)
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
		return nil, capWriteErrParam(fmt.Sprintf("skill 定义无法解析：%v", err))
	}
	if sk.ID == "" {
		sk.ID = id
	}
	sk.SourceFile = repo.sourceFile(id) // 文件载体：事实源文件（Library 的 origin/source_file 依据）

	if err := capability.ValidateSkill(&sk); err != nil {
		// 闸门拒绝（critical 正文 / id 非法 / scope 非法 / runtimes 重复）→ 40001。
		// **零字节写入**：patch 只在草稿里，草稿从未提交。
		return nil, acl.NewError(acl.ClassRequest, "definition_invalid",
			fmt.Sprintf("skill %q 的定义被拒绝：%v", id, err), err)
	}
	created, conflict, err := repo.commit(ctx, id, doc, rev, existed)
	if err != nil {
		return nil, err
	}
	if conflict {
		// 载体的乐观锁没中：文件被手改 / 行 revision 变了（含"我以为新建、别人抢先建出来"）。
		// 语义与 40901 的老文案一致——刷新重试，绝不 last-write-wins。
		return nil, definitionChangedErr(repo.locator(id))
	}
	// 提交成功后才入表：同一份定义刚过闸门（纯函数），这一次必然成功；真出错也只可能来自
	// "注册表被并发改坏"，那种情况下**不能**谎报写失败（载体上已经写成功了），记 WARN 更诚实。
	if err := s.caps.RegisterSkill(&sk); err != nil {
		glog.Warnw(ctx, "capability: skill 定义已提交但入表失败（内存视图可能落后）", "id", id, "err", err)
	}

	resp := &dtocapability.SkillWriteResp{
		ID: sk.ID, Path: repo.locator(id), Created: create || created,
		Warnings: skillWarnings(sk),
	}
	t := s.reconcileSkillProjections()
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
func (s *Service) DeleteSkill(ctx context.Context, id string) (*dtocapability.SkillDeleteResp, error) {
	if s.caps == nil {
		return nil, errCapabilityUnavailable()
	}
	repo, err := s.definitionRepoFor(capSubSkills)
	if err != nil {
		return nil, err
	}
	if !validDefinitionID(id) {
		if _, in := s.caps.SkillByID(id); !in {
			return skillDeleteNoop(id, ""), nil // 非法 id 不可能存在 → 幂等无操作
		}
		return nil, invalidDefinitionIDErr("skill", id)
	}
	_, inReg := s.caps.SkillByID(id)
	stored, err := repo.exists(ctx, id)
	if err != nil {
		return nil, err
	}
	locator := repo.locator(id)
	if !inReg && !stored {
		return skillDeleteNoop(id, locator), nil
	}

	deleted, conflict, err := repo.remove(ctx, id)
	if err != nil {
		return nil, err
	}
	if conflict {
		return nil, definitionChangedErr(locator)
	}
	s.caps.RemoveSkill(id)
	resp := &dtocapability.SkillDeleteResp{ID: id, Deleted: deleted, Path: locator}
	// 删定义必须一起撤回已物化副本：否则底座目录里还留着它，agent 照旧看得见。
	t := s.reconcileSkillProjections()
	resp.Removed, resp.MaterializeErrors = t.Removed, t.Errors
	resp.NextAction = "已删除：定义已移除，已物化的副本已撤回（正在跑的会话不受影响，下一轮/下次拉起不再注入）"
	return resp, nil
}

// skillDeleteNoop 幂等删除的空结果。
func skillDeleteNoop(id, path string) *dtocapability.SkillDeleteResp {
	return &dtocapability.SkillDeleteResp{ID: id, Deleted: false, Path: path,
		NextAction: "该 skill 本来就不存在，无需删除"}
}

// skillExistsErr 新建时 id 已占用（40901：与当前状态冲突——参数没问题，是时机/状态问题）。
func skillExistsErr(id string) error {
	return acl.NewError(acl.ClassLimit, "skill_exists",
		fmt.Sprintf("skill %q 已存在：改它用 PUT /skills/%s，或换一个 id", id, id), nil)
}

// skillNotFoundErr 目标 skill 不存在（40401，由 pkg/code 的 notFoundCodes 提档）。
func skillNotFoundErr(id string) error {
	return acl.NewError(acl.ClassRequest, "skill_not_found", fmt.Sprintf("未知 skill %q", id), nil)
}
