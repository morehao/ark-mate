package manager

import (
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/pkg/capability"
)

// ——— P3c：能力定义查表与可达性事实的装配适配器（域端口 `session.CapabilityLookup`） ———
//
// 选定校验的**判断**在域内（`Plane.ValidateSelection` + `session.SkillVerdict`/`McpVerdict`），
// 而"某个 id 在不在册""这个 runtime 承载哪些传输""网关开着吗""skill 投影解析出来了吗"这些
// **事实**只有装配侧知道。这里把它们如实交出，不做任何判断。

// LookupSkill 实现 `session.CapabilityLookup`。
func (s *Service) LookupSkill(id string) (capability.Skill, bool) {
	if s.caps == nil {
		return capability.Skill{}, false
	}
	return s.caps.SkillByID(id)
}

// LookupMcp 实现 `session.CapabilityLookup`。
func (s *Service) LookupMcp(id string) (capability.McpServer, bool) {
	if s.caps == nil {
		return capability.McpServer{}, false
	}
	return s.caps.GetMcpServer(id)
}

// SkillVerdictFacts 实现 `session.CapabilityLookup`。
func (s *Service) SkillVerdictFacts(runtimeID string) session.SkillVerdictFacts {
	facts := session.SkillVerdictFacts{Runtime: runtimeID}
	if p, ok := s.skillProjectionFor(runtimeID); ok {
		facts.Projection = session.ProjectedSkillOf(runtimeID, p.Projection, p.Err)
		facts.ProjectionOK = true
	}
	return facts
}

// McpVerdictFacts 实现 `session.CapabilityLookup`。
func (s *Service) McpVerdictFacts(runtimeID string) session.McpVerdictFacts {
	supported, known := s.reg.McpTransportsFor(runtimeID)
	// **不做默认化**：`supportedMcpTransports` 已经实现"声明为空 → 只收 stdio"那条约定，并且在
	// 未知 runtime 上返回**空列表**（那正是"这个 runtime 什么传输都不承载"这个结论）。在这里再
	// 默认一次会把未知 runtime 变成"只收 stdio"，于是"未注册 runtime 应不可达"这条判据静默失效。
	return session.McpVerdictFacts{
		Runtime:        runtimeID,
		Supported:      supported,
		SupportedKnown: known,
		GatewayEnabled: s.McpGatewayEnabled(),
	}
}

// validateSelection 选定对该 runtime 可达吗（P3c：判断已搬进域，这里只剩一行转发）。
//
// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) validateSelection(sel *CapabilitySelection, runtimeID string) error {
	return s.sessionPlane.ValidateSelection(sel, runtimeID)
}

// selectionProblems 把选定拆成「不存在」与「不可达」两组。
//
// TODO(P4b): 删转发（调用点直接依赖 session.Plane）
func (s *Service) selectionProblems(sel *CapabilitySelection, runtimeID string) (unknown, unreachable []session.SelectionProblem) {
	return s.sessionPlane.SelectionProblemsOf(sel, runtimeID)
}
