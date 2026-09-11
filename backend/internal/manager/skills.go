package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/morehao/ark-forge/internal/dto/dtocapability"
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/code"
)

// ——— §13.4 S4/S5：技能启停（overlay）与激活（正交） ———
//
// 两个概念分开，且都必须能对用户解释：
//
//	Enabled    平台要不要**投放**这个技能（定义文件里的 `enabled`；false = 不物化、不进 agent 能力目录）。
//	Activation 当前这个 agent 的环境**适不适用**（候选 runtime 里有没有可物化的 source）。
//	           —— 与 Enabled 正交：启用着的技能在某个 agent 上完全可能不适用，反之亦然。
//
// 禁用是 **overlay**：定义文件、正文、sources 一律保留，只把 `enabled` 写成 false。因此
// 「关掉一个技能」随时可逆（改回 true），不需要重新导入正文——这是与「删掉定义文件」的本质区别。
//
// 落盘 = 编辑用户文件（M10 纪律）：读原文件为 map → 只改 `enabled` 一个键 → 原子写回。
// **未知键原样保留**——用户在自己文件里加的字段不该被平台抹掉。

// SkillMaterializeTarget 一处 skill 物化目标（root + runtime；runtime 空 = 单 root 兼容路径）。
// serve 启动重校时由 app 登记，启停后可立刻对同一组目标重校，不必等下次启动。
type SkillMaterializeTarget struct {
	Root    string
	Runtime string
}

// SetSkillMaterializeTargets 登记物化目标（幂等覆盖；由 app 在启动重校时调用）。
func (s *Service) SetSkillMaterializeTargets(targets []SkillMaterializeTarget) {
	s.mu.Lock()
	s.skillTargets = append([]SkillMaterializeTarget(nil), targets...)
	s.mu.Unlock()
}

// skillTargetsSnapshot 取物化目标的副本。
func (s *Service) skillTargetsSnapshot() []SkillMaterializeTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SkillMaterializeTarget(nil), s.skillTargets...)
}

// capabilityDirSnapshot 能力数据面目录（启停写回定义文件用）。
func (s *Service) capabilityDirSnapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capabilityDir
}

// SetSkillEnabled 启停一个 skill（§13.4 S4）：写回定义文件的 `enabled` + 更新注册表 + 立刻重校物化。
//
// 三件事的顺序是刻意的：
//  1. 先在**磁盘**上改（事实源先变，失败即整体失败，内存不动）；
//  2. 再改内存注册表（此后 agent 能力目录/物化都按新值走）；
//  3. 最后重校物化目标（把已物化的副本写进去或撤下来），重校失败**不**回滚前两步——
//     定义文件已经改了，此时谎报「失败」会让用户以为没生效，而重启后照样生效。失败原因随响应回带。
func (s *Service) SetSkillEnabled(ctx context.Context, id string, enabled bool) (*dtocapability.SkillToggleResp, error) {
	if id == "" {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "skill_required", "skill id 必填", nil))
	}
	if s.caps == nil {
		return nil, code.FromError(acl.NewError(acl.ClassService, "capability_unavailable", "能力数据面未装配", nil))
	}
	if _, ok := s.caps.SkillByID(id); !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "skill_not_found",
			fmt.Sprintf("未知 skill %q", id), nil))
	}
	path, err := s.writeSkillEnabled(id, enabled)
	if err != nil {
		return nil, code.FromError(err)
	}
	after, _ := s.caps.SetSkillEnabled(id, enabled)

	resp := &dtocapability.SkillToggleResp{
		ID: after.ID, Enabled: after.IsEnabled(), Path: path,
	}
	// 立刻重校：禁用要**撤回**已物化的副本（否则 agent 下次拉起还看得见它），
	// 启用要把它写进去。重校按登记的目标逐个做，单个失败只记原因不中断。
	for _, tgt := range s.skillTargetsSnapshot() {
		w, r, mErr := s.ReconcileSkillsFor(tgt.Root, tgt.Runtime)
		resp.Written += w
		resp.Removed += r
		if mErr != nil {
			resp.MaterializeErrors = append(resp.MaterializeErrors,
				fmt.Sprintf("物化重校失败（root=%s runtime=%s）：%v", tgt.Root, tgt.Runtime, mErr))
		}
	}
	if enabled {
		resp.NextAction = "已启用：声明了候选 runtime 的 agent 下次拉起即可见；已物化的副本已按当前物化目标写出"
	} else {
		resp.NextAction = "已禁用：定义文件与正文保留（可随时改回 enabled:true 恢复）；已物化副本已撤回"
	}
	return resp, nil
}

// writeSkillEnabled 把 `enabled` 写进定义文件并返回文件路径。
// 读原文件为 map（保留未知键）→ 只改一个键 → 临时文件 + rename 原子替换。
func (s *Service) writeSkillEnabled(id string, enabled bool) (string, error) {
	dir := s.capabilityDirSnapshot()
	if dir == "" {
		return "", acl.NewError(acl.ClassRequest, "skill_not_persistable",
			fmt.Sprintf("skill %q 无法启停：本进程未装载能力数据面目录（FORGE_CAPABILITIES），没有可持久化的定义文件", id), nil)
	}
	path := filepath.Join(dir, "skills", id+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", acl.NewError(acl.ClassRequest, "skill_not_persistable",
				fmt.Sprintf("skill %q 没有定义文件 %s（代码注入/内存注册的技能无处写回）：请在能力目录里建成文件后再启停", id, path), nil)
		}
		return "", acl.NewError(acl.ClassService, "skill_write_failed",
			fmt.Sprintf("读取 skill 定义失败 %s：%v", path, err), nil)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", acl.NewError(acl.ClassRequest, "skill_invalid",
			fmt.Sprintf("skill 定义 %s 不是合法 JSON 对象：%v（先修好文件再启停）", path, err), nil)
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	flag, err := json.Marshal(enabled)
	if err != nil {
		return "", code.FromError(err)
	}
	doc["enabled"] = flag
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", code.FromError(err)
	}
	out = append(out, '\n')
	tmp := filepath.Join(dir, "skills", "."+id+".json.tmp")
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return "", acl.NewError(acl.ClassService, "skill_write_failed",
			fmt.Sprintf("写入 skill 定义失败 %s：%v", tmp, err), nil)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", acl.NewError(acl.ClassService, "skill_write_failed",
			fmt.Sprintf("替换 skill 定义失败 %s：%v", path, err), nil)
	}
	return path, nil
}

// SkillActivation 一个 skill 对某 agent 的适用性（§13.4 S5；Enabled 与 Activation 正交）。
type SkillActivation struct {
	Applicable bool
	Reasons    []string
}

// SkillActivationFor 计算某 skill 在某 agent 候選 runtime 下的适用性，并给出**人话原因**。
//
// 与「可见性过滤」的区别：过滤会让人看不见（"技能怎么没了？"），这里给出
// 「不适用 + 为什么 + 怎么改」——不适用不是错误，是环境事实。
// 判定分层（保持声明顺序，取第一条命中的原因即可解释；多条并列时全部列出）：
//
//  1. 已禁用（enabled:false）——平台不投放；
//  2. 候选 runtime 一个都没在 sources 里声明；
//  3. 声明了但 format 未实证（物化不出来）。
func SkillActivationFor(sk capability.Skill, runtimes []string, inAgent map[string]bool) SkillActivation {
	var reasons []string
	if !sk.IsEnabled() {
		reasons = append(reasons, "技能已被禁用（enabled:false）：改回 enabled:true 即可恢复（定义与正文一直保留）")
	}
	declared := make([]string, 0, len(sk.Sources))
	for _, src := range sk.Sources {
		declared = append(declared, src.Runtime)
	}
	sort.Strings(declared)
	if len(declared) == 0 {
		reasons = append(reasons, "技能未声明任何物化 source（sources 为空），没有 runtime 能装载它")
	} else {
		var matched, unproven []string
		for _, src := range sk.Sources {
			if !inAgent[src.Runtime] {
				continue
			}
			if capability.SupportedFormat(src.Format) {
				matched = append(matched, src.Runtime)
			} else {
				unproven = append(unproven, fmt.Sprintf("%s(format=%s)", src.Runtime, src.Format))
			}
		}
		if len(matched) == 0 {
			if len(unproven) > 0 {
				reasons = append(reasons, fmt.Sprintf(
					"候选 runtime 声明了 source 但 format 未经实证，无法物化：%v（已实证：%s）",
					unproven, declaredFormats()))
			} else {
				reasons = append(reasons, fmt.Sprintf(
					"候选 runtime %v 都没有在 sources 里声明（声明的是：%v）——要给某个 runtime 用，就在该技能的 sources 里加一条",
					runtimes, declared))
			}
		}
	}
	return SkillActivation{Applicable: len(reasons) == 0, Reasons: reasons}
}

// declaredFormats 已实证的物化 format 列表（错误文案用）。
func declaredFormats() string { return "SKILL.md / SKILL.md+frontmatter" }

// ——— §13.4 S2：物化体检（审计 / 复现面） ———

// SkillMaterialization 报告某 skill 在每一处已登记物化目标上的现状（只读，不写盘）。
//
// 为什么值得一个端点：物化是"平台事实源 → runtime 原生文件"的投影，投影完就没人知道
// "这个目录本该长什么样"。有了它就能回答三个具体问题：agent 实际读到的内容与注册表一致吗
// （`in_sync` / `tampered`）、产物是老版本还是当前版本（`stale`）、某处为什么没有产物
// （`missing` / `disabled` / `not_applicable` / `user_owned`）。修复不在这里做——重校与启停负责写。
func (s *Service) SkillMaterialization(id string) (*dtocapability.SkillMaterializationResp, error) {
	if s.caps == nil {
		return nil, code.FromError(acl.NewError(acl.ClassService, "capability_unavailable", "能力数据面未装配", nil))
	}
	sk, ok := s.caps.SkillByID(id)
	if !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "skill_not_found", fmt.Sprintf("未知 skill %q", id), nil))
	}
	resp := &dtocapability.SkillMaterializationResp{ID: sk.ID, Enabled: sk.IsEnabled()}
	targets := s.skillTargetsSnapshot()
	if len(targets) == 0 {
		resp.States = []dtocapability.SkillMaterializationState{}
		resp.Notes = append(resp.Notes,
			"本进程没有登记任何物化目标（FORGE_SKILLS_MATERIALIZE_DIR / FORGE_SKILLS_MATERIALIZE_DIRS 未开）："+
				"技能只存在于注册表，未投影到任何 runtime 目录")
		return resp, nil
	}
	states := make([]dtocapability.SkillMaterializationState, 0, len(targets))
	for _, tgt := range targets {
		states = append(states, skillMaterializationState(sk, tgt))
	}
	sort.SliceStable(states, func(i, j int) bool {
		if states[i].Root != states[j].Root {
			return states[i].Root < states[j].Root
		}
		return states[i].Runtime < states[j].Runtime
	})
	resp.States = states
	return resp, nil
}

// skillMaterializationState 判定单个物化目标的现状（判定顺序即优先级，见 DTO 的 status 说明）。
func skillMaterializationState(sk capability.Skill, tgt SkillMaterializeTarget) dtocapability.SkillMaterializationState {
	dir := filepath.Join(tgt.Root, sk.ID)
	st := dtocapability.SkillMaterializationState{Root: tgt.Root, Runtime: tgt.Runtime, Dir: dir}
	content, _, materializable := capability.MaterializedContentFor(sk, tgt.Runtime)
	if materializable {
		st.Hash = capability.PayloadHash(content)
	}
	_, statErr := os.Lstat(dir)
	exists := statErr == nil
	owners, managed := capability.ManagedDir(dir)
	st.Owners = owners

	switch {
	case !sk.IsEnabled():
		switch {
		case exists && managed:
			st.Status = "stale"
			st.Detail = "技能已禁用，但平台物化的副本还在（下一次重校撤回；也可再 PATCH enabled:true 恢复）"
		case exists:
			st.Status = "user_owned"
			st.Detail = "技能已禁用；该目录不带平台归属标记（用户自有内容，平台不动）"
		default:
			st.Status = "disabled"
			st.Detail = "技能已禁用：不投放，本就不该有产物"
		}
	case !materializable:
		st.Status = "not_applicable"
		st.Detail = "该 runtime 没有已实证 format 的 source：物化不出来（补 sources 或改用已实证 format）"
	case !exists:
		st.Status = "missing"
		st.Detail = "该物化但没有产物：下一次重校会写出"
	case !managed:
		st.Status = "user_owned"
		st.Detail = "目录已存在但不带平台归属标记：平台拒绝覆盖（改名/移走它，或换一个物化根）"
	default:
		// 有归属标记：看摘要。ReadProvenance 失败 = 无 sidecar（老产物）或 sidecar 坏。
		prov, err := capability.ReadProvenance(dir)
		if err != nil {
			st.Status = "stale"
			st.Detail = "产物没有可读的摘要 sidecar（老版本产物或 sidecar 损坏）：下一次重校会重写并补上摘要"
			if actual, hErr := capability.ComputeDirHash(dir); hErr == nil {
				st.ActualHash = actual
			}
			break
		}
		st.WrittenAt = prov.WrittenAt.UTC().Format(time.RFC3339)
		actual, hashErr := capability.ComputeDirHash(dir)
		switch {
		case hashErr != nil:
			// 软链逃逸/未知文件类型：算不出摘要本身就是"产物被动了"的证据。
			st.Status = "tampered"
			st.Detail = fmt.Sprintf("产物无法计算摘要：%v", hashErr)
		case actual != prov.Hash:
			st.Status = "tampered"
			st.ActualHash = actual
		case prov.Hash != st.Hash:
			st.Status = "stale"
			st.ActualHash = actual
			st.Detail = "产物摘要是老版本/别的 runtime 写的：与当前注册表不一致，下一次重校会改写"
		default:
			st.Status = "in_sync"
			st.ActualHash = actual
		}
	}
	return st
}
