package manager

import (
	"github.com/morehao/ark-forge/internal/capability"
	"github.com/morehao/ark-forge/internal/server/handler"
)

// ——— 能力数据面（P1，design doc §4.4/§11 P1） ———
//
// Service 持有 capability.Registry（Skill 目录 / MCP 注册表 / Tool 目录，平台唯一事实源），
// 数据来自 <CapabilityDir>/{skills,mcp,tools}/*.json（serve 装配时 LoadCapabilityDefinitions），
// 也可经 RegisterTool 等由代码/未来运行态注入。REST 只做「列出能力」；
// 注入（mcpServers → session/new）、skill 物化、工具 live 探测属 P2。

// LoadCapabilityDefinitions 加载能力数据面目录并合并（dir 为空 = 仅代码注入/空表）。
// 返回三类合计加载数（坏文件跳过，对齐 LoadAgentDefinitions 容错）。
func (s *Service) LoadCapabilityDefinitions(dir string) int {
	if s.caps == nil {
		return 0
	}
	return s.caps.LoadDir(dir).Total()
}

// RegisterTool 注册/覆盖一个工具目录条目（平台 builtin / 观察兜底 / P2 MCP live）。
func (s *Service) RegisterTool(t capability.Tool) error {
	return s.caps.RegisterTool(&t)
}

// ListSkills 返回 Skill 目录（按 ID 排序）。
func (s *Service) ListSkills() ([]handler.SkillResp, error) {
	items := s.caps.ListSkills()
	out := make([]handler.SkillResp, 0, len(items))
	for _, sk := range items {
		src := make([]handler.SkillSourceResp, 0, len(sk.Sources))
		for _, so := range sk.Sources {
			src = append(src, handler.SkillSourceResp{Runtime: so.Runtime, Path: so.Path, Format: so.Format})
		}
		out = append(out, handler.SkillResp{
			ID: sk.ID, Name: sk.Name, Description: sk.Description, Body: sk.Body, Sources: src,
		})
	}
	return out, nil
}

// ListMcpServers 返回 MCP 注册表（按 ID 排序）。headers/凭证只存内部供 P2 注入，不读回。
func (s *Service) ListMcpServers() ([]handler.McpServerResp, error) {
	items := s.caps.ListMcpServers()
	out := make([]handler.McpServerResp, 0, len(items))
	for _, m := range items {
		out = append(out, handler.McpServerResp{
			ID: m.ID, Name: m.Name, Transport: m.Transport,
			Command: m.Command, Args: m.Args, URL: m.URL,
		})
	}
	return out, nil
}

// ListTools 返回 Tool 目录（按 name 排序）。
func (s *Service) ListTools() ([]handler.ToolResp, error) {
	items := s.caps.ListTools()
	out := make([]handler.ToolResp, 0, len(items))
	for _, t := range items {
		out = append(out, handler.ToolResp{
			Name: t.Name, Description: t.Description, Source: t.Source, Schema: t.Schema,
		})
	}
	return out, nil
}
