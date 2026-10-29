package sessionruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 会话创建时的专家解析（P0-2）———
//
// 解析链**只在这里做一次**（设计 D2）：外层「整个 capabilities 缺省 → 工作空间默认」已在
// CreateSession 里由既有 `workspace.ResolveSessionDefaults` 处理；本文件负责内层
// 「逐 kind 显式 > 专家推荐 > 平台默认」。
//
// 关键两条：
//   - 专家只**填空缺**：`skills`/`mcps` 键缺失才吃专家推荐；显式 `[]` / 非空白名单永远赢；
//   - 专家推荐里不可达的项**跳过 + 告警**（不阻断创建）；用户**显式**选择的不可达项仍由
//     StartSession 的 ValidateSelection 拒绝（两条路径的文案与码不同）。
//
// 物化：由专家填充的技能/MCP 落库为**具体 id 列表**（不是「跟随专家」的活引用），因此改专家
// 定义不影响已建会话，旧二进制也能按同一套能力跑。

// expertResolution 专家解析的产物。
type expertResolution struct {
	// selection 物化后的选定（保留 Expert 指针；skills/mcps 可能是具体列表或仍为 nil）。
	selection *session.CapabilitySelection
	// model 专家推荐的模型（仅当请求未显式指定模型时生效；"" = 不推荐/不可达）。
	model string
	// persona 专家角色设定块（P1；空 = 无专家人格或 instructions 为空）。
	persona string
	// warnings 专家推荐里被跳过的项（kind、id、reason）。
	warnings []dtosession.CapabilityWarningResp
}

// resolveExpert 把选定里的专家展开成有效技能/MCP/模型。
//
// selection == nil（未声明能力选择）→ 原样返回，零副作用（缺省路径行为不变）。
func (s *Service) resolveExpert(ctx context.Context, selection *session.CapabilitySelection, reqModel string) (*expertResolution, error) {
	res := &expertResolution{selection: selection}
	if selection == nil || selection.Expert == nil {
		return res, nil
	}
	expertID := strings.TrimSpace(*selection.Expert)
	if expertID == "" {
		return res, nil // 明确不用专家
	}
	if s.planes.Capabilities == nil {
		return nil, failure.NewError(failure.ClassService, "capability_unavailable", "能力数据面未装配", nil)
	}
	expert, ok := s.planes.Capabilities.ExpertByID(expertID)
	if !ok {
		return nil, failure.NewError(failure.ClassRequest, "expert_not_found",
			fmt.Sprintf("未知 expert %q：刷新专家列表后重新选择", expertID), nil)
	}
	if !expert.IsEnabled() {
		return nil, failure.NewError(failure.ClassRequest, "expert_disabled",
			fmt.Sprintf("专家 %q 已被禁用：到「扩展 · 专家」启用它，或换一个", expertID), nil)
	}

	// 物化一份**副本**（不改调用方持有的选择；列表来源保持 nil 的语义另见下方）。
	out := &session.CapabilitySelection{Skills: selection.Skills, Mcps: selection.Mcps, Expert: selection.Expert}

	rt := runtime.SelectedRuntime()
	// skills：仅当请求未声明该 kind 时吃专家推荐。
	if out.Skills == nil {
		if reachable, warns := s.reachableSkills(rt, expert.Skills); len(expert.Skills) > 0 {
			out.Skills = &reachable
			res.warnings = append(res.warnings, warns...)
		}
	}
	// mcps：同理。
	if out.Mcps == nil {
		if reachable, warns := s.reachableMcps(rt, expert.Mcps); len(expert.Mcps) > 0 {
			out.Mcps = &reachable
			res.warnings = append(res.warnings, warns...)
		}
	}
	// model：仅当请求未显式指定模型时采用专家推荐；模型行不存在 → 跳过 + 告警，回落平台默认。
	if strings.TrimSpace(reqModel) == "" && strings.TrimSpace(expert.Model) != "" {
		if s.modelExists(ctx, expert.Model) {
			res.model = expert.Model
		} else {
			res.warnings = append(res.warnings, dtosession.CapabilityWarningResp{
				Kind: "model", ID: expert.Model, Reason: "专家推荐的模型行不存在，已回落到平台默认",
			})
		}
	}
	res.selection = out
	// 角色设定块（P1）：instructions 非空才构造；空 = 纯能力预设专家，不注入人格。
	res.persona = session.BuildExpertRoleBlock(expert.ID, expert.Name, expert.Instructions)
	return res, nil
}

// reachableSkills 过滤出对 rt 可达的推荐技能（保持升序），不可达项转成告警。
func (s *Service) reachableSkills(rt string, ids []string) ([]string, []dtosession.CapabilityWarningResp) {
	out := make([]string, 0, len(ids))
	var warns []dtosession.CapabilityWarningResp
	facts := s.SkillVerdictFacts(rt)
	for _, id := range ids {
		sk, ok := s.LookupSkill(id)
		if !ok {
			warns = append(warns, dtosession.CapabilityWarningResp{Kind: "skill", ID: id, Reason: "注册表里没有这个 skill，已跳过"})
			continue
		}
		if v := session.SkillVerdict(sk, facts); !v.OK {
			warns = append(warns, dtosession.CapabilityWarningResp{Kind: "skill", ID: id, Reason: v.Reason})
			continue
		}
		out = append(out, id)
	}
	return out, warns
}

// reachableMcps 过滤出对 rt 可达的推荐 MCP（保持升序），不可达项转成告警。
func (s *Service) reachableMcps(rt string, ids []string) ([]string, []dtosession.CapabilityWarningResp) {
	out := make([]string, 0, len(ids))
	var warns []dtosession.CapabilityWarningResp
	facts := s.McpVerdictFacts(rt)
	for _, id := range ids {
		m, ok := s.LookupMcp(id)
		if !ok {
			warns = append(warns, dtosession.CapabilityWarningResp{Kind: "mcp", ID: id, Reason: "注册表里没有这个 MCP server，已跳过"})
			continue
		}
		if v := session.McpVerdict(m, facts); !v.OK {
			warns = append(warns, dtosession.CapabilityWarningResp{Kind: "mcp", ID: id, Reason: v.Reason})
			continue
		}
		out = append(out, id)
	}
	return out, warns
}

// modelExists 专家推荐模型行是否存在（存在才采用；不存在按推荐不可达跳过）。
func (s *Service) modelExists(ctx context.Context, id string) bool {
	st := s.planes.Resources.Store()
	if st == nil {
		return false
	}
	_, ok, err := st.GetModelDefinition(ctx, id)
	return err == nil && ok
}

// createCapabilitiesResp 把创建时钉住的选定折叠成创建响应里的 capabilities（含被跳过的推荐告警）。
//
// sel == nil（请求未声明能力选择）→ nil：缺省路径的创建响应与引入本字段之前逐字节一致。
func createCapabilitiesResp(sel *session.CapabilitySelection, warns []dtosession.CapabilityWarningResp) *dtosession.SessionCapabilitiesResp {
	if sel == nil {
		return nil
	}
	resp := &dtosession.SessionCapabilitiesResp{Warnings: warns}
	if sel.Skills != nil {
		v := append([]string(nil), (*sel.Skills)...)
		resp.Skills = &v
	}
	if sel.Mcps != nil {
		v := append([]string(nil), (*sel.Mcps)...)
		resp.Mcps = &v
	}
	if sel.Expert != nil {
		v := *sel.Expert
		resp.Expert = &v
	}
	return resp
}
