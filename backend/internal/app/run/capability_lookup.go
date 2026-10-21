package run

import (
	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/domain/session"
)

// ——— P3c：能力定义查表与可达性事实的装配适配器（域端口 `session.CapabilityLookup`） ———
//
// 选定校验的**判断**在域内（`Plane.ValidateSelection` + `session.SkillVerdict`/`McpVerdict`），
// 而"某个 id 在不在册""这个 runtime 承载哪些传输""网关开着吗""skill 投影解析出来了吗"这些
// **事实**只有装配侧知道。这里把它们如实交出，不做任何判断。

// LookupSkill 实现 `session.CapabilityLookup`。
func (s *Service) LookupSkill(id string) (capability.Skill, bool) {
	if s.facts.Capabilities == nil {
		return capability.Skill{}, false
	}
	return s.facts.Capabilities.SkillByID(id)
}

// LookupMcp 实现 `session.CapabilityLookup`。
func (s *Service) LookupMcp(id string) (capability.McpServer, bool) {
	if s.facts.Capabilities == nil {
		return capability.McpServer{}, false
	}
	return s.facts.Capabilities.GetMcpServer(id)
}

// SkillVerdictFacts 实现 `session.CapabilityLookup`。
func (s *Service) SkillVerdictFacts(runtimeID string) session.SkillVerdictFacts {
	facts := session.SkillVerdictFacts{Runtime: runtimeID}
	if p, ok := s.facts.Skills.Projections().For(runtimeID); ok {
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
		GatewayEnabled: s.mcpGatewayEnabled(),
	}
}
