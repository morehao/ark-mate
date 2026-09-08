package manager

import (
	"github.com/morehao/ark-forge/internal/acl"
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

// McpServersFor 选出可注入某 agent×runtime 的 MCP server 子集（P2 §5.2/§5.3）：
// 注册表全量 → 该 runtime 的 McpTransports 过滤 → agent.Mcps 白名单（nil = 未声明 → 全量；
// 非 nil = 白名单；空数组 = 本 agent 禁用 MCP 注入）。headers/凭证一并携带（走 ACP wire）。
// 未知 runtime → nil（维持现状不下发）。
func (s *Service) McpServersFor(agent *AgentRec, runtimeID string) []acl.McpServer {
	spec, ok := s.reg.Get(runtimeID)
	if !ok {
		return nil
	}
	supported := spec.McpTransports
	if len(supported) == 0 {
		supported = []string{capability.TransportStdio} // ACP 全 agent 必须支持 stdio
	}
	want := make(map[string]bool, len(supported))
	for _, t := range supported {
		want[t] = true
	}
	var allow map[string]bool
	if agent != nil && agent.Mcps != nil {
		allow = make(map[string]bool, len(*agent.Mcps))
		for _, id := range *agent.Mcps {
			allow[id] = true
		}
	}
	servers := s.caps.ListMcpServers()
	out := make([]acl.McpServer, 0, len(servers))
	for _, m := range servers {
		if !want[m.Transport] {
			continue
		}
		if allow != nil && !allow[m.ID] {
			continue // agent 白名单未启用该 server（含空白名单 = 全部跳过）
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, acl.McpServer{
			Name: name, Transport: m.Transport,
			Command: m.Command, Args: m.Args, URL: m.URL, Headers: m.Headers,
		})
	}
	return out
}
