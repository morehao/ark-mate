package manager

import (
	"fmt"
	"time"

	"github.com/morehao/ark-forge/internal/dto/dtocapability"
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/code"
)

// LoadCapabilityDefinitions 加载能力数据面目录并合并（dir 为空 = 仅代码注入/空表）。
// 返回三类合计加载数（坏文件跳过，对齐 LoadAgentDefinitions 容错）。
func (s *Service) LoadCapabilityDefinitions(dir string) int {
	if s.caps == nil {
		return 0
	}
	return s.caps.LoadDir(dir).Total()
}

// ReconcileSkills 把能力数据面里已实证 format 的 skill 与目标根目录做重校
// （§5.4/§16.5：物化缺失/更新 + 删除联动——只删带 forge 归属标记且已不在注册表的目录，
// 用户自有目录绝不动）。支持两种已实证 format：`SKILL.md`（opencode 原样）与
// `SKILL.md+frontmatter`（dsh 需 YAML frontmatter）。返回 (written, removed)。
// serve 启动装载能力后、FORGE_SKILLS_MATERIALIZE_DIR 非空时调用。
// 物化默认不写用户目录——由调用方显式 enable。
func (s *Service) ReconcileSkills(root string) (int, int, error) {
	if s.caps == nil || root == "" {
		return 0, 0, nil
	}
	return capability.ReconcileSkills(root, s.caps.ListSkills())
}

// ReconcileSkillsFor 按 runtime 分 root 重校（C4 多 root 投影）：只物化声明了该 runtime 的 skill，
// 删除联动也只动本 root 下该 runtime 的标记目录。runtime 为空 → 等价 ReconcileSkills。
func (s *Service) ReconcileSkillsFor(root, runtime string) (int, int, error) {
	if s.caps == nil || root == "" {
		return 0, 0, nil
	}
	return capability.ReconcileSkillsFor(root, runtime, s.caps.ListSkills())
}

// RegisterTool 注册/覆盖一个工具目录条目（平台 builtin / 观察兜底 / P2 MCP live）。
func (s *Service) RegisterTool(t capability.Tool) error {
	return s.caps.RegisterTool(&t)
}

// ListSkills 返回 Skill 目录（按 ID 排序）。
func (s *Service) ListSkills() ([]dtocapability.SkillResp, error) {
	items := s.caps.ListSkills()
	out := make([]dtocapability.SkillResp, 0, len(items))
	for _, sk := range items {
		src := make([]dtocapability.SkillSourceResp, 0, len(sk.Sources))
		for _, so := range sk.Sources {
			src = append(src, dtocapability.SkillSourceResp{Runtime: so.Runtime, Path: so.Path, Format: so.Format})
		}
		out = append(out, dtocapability.SkillResp{
			ID: sk.ID, Name: sk.Name, Description: sk.Description, Body: sk.Body, Sources: src,
		})
	}
	return out, nil
}

// ListMcpServers 返回 MCP 注册表（按 ID 排序）。headers/凭证只存内部供 P2 注入，不读回。
// 附 live 探测状态（tool_count / last_probe_at / last_probe_error，P2 §5.2）。
func (s *Service) ListMcpServers() ([]dtocapability.McpServerResp, error) {
	items := s.caps.ListMcpServers()
	counts := s.mcpToolCounts()
	probes := s.mcpProbeSnapshot()
	out := make([]dtocapability.McpServerResp, 0, len(items))
	for _, m := range items {
		resp := dtocapability.McpServerResp{
			ID: m.ID, Name: m.Name, Transport: m.Transport,
			Command: m.Command, Args: m.Args, URL: m.URL,
			ToolCount: counts[m.ID],
		}
		if st, ok := probes[m.ID]; ok {
			if !st.At.IsZero() {
				resp.LastProbeAt = st.At.UTC().Format(time.RFC3339)
			}
			resp.LastProbeErr = st.Err
		}
		out = append(out, resp)
	}
	return out, nil
}

// ListTools 返回 Tool 目录（按 name 排序）。
func (s *Service) ListTools() ([]dtocapability.ToolResp, error) {
	items := s.caps.ListTools()
	out := make([]dtocapability.ToolResp, 0, len(items))
	for _, t := range items {
		out = append(out, dtocapability.ToolResp{
			Name: t.Name, Description: t.Description, Source: t.Source,
			McpServer: t.McpServer, Schema: t.Schema,
		})
	}
	return out, nil
}

// supportedMcpTransports 返回 runtime 声明支持的 MCP 传输（未声明 → 仅 stdio，ACP 全 agent 必须支持）。
// 未知 runtime → ok=false。
func (s *Service) supportedMcpTransports(runtimeID string) ([]string, bool) {
	spec, ok := s.reg.Get(runtimeID)
	if !ok {
		return nil, false
	}
	if len(spec.McpTransports) == 0 {
		return []string{capability.TransportStdio}, true
	}
	return spec.McpTransports, true
}

// AgentCapabilities 组装某 Agent 的统一能力目录（§8.1；可见性规则见 dtocapability.AgentCapabilitiesResp）。
// 只读聚合：不触发物化/注入/探测，全部依据注册表 + 候选 runtime 声明推导。未知 agent → agent_not_found。
func (s *Service) AgentCapabilities(agentID string) (*dtocapability.AgentCapabilitiesResp, error) {
	a, ok := s.AgentByID(agentID)
	if !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "agent_not_found",
			fmt.Sprintf("未知 agent %q", agentID), nil))
	}
	runtimes := append([]string(nil), a.Runtimes...)
	inAgent := make(map[string]bool, len(runtimes))
	for _, r := range runtimes {
		inAgent[r] = true
	}
	notes := make([]string, 0, 2)
	if len(runtimes) == 0 {
		notes = append(notes, "该 agent 未声明候选 runtime，能力目录为空")
	}
	// agent 白名单语义：nil = 未声明（全启用）；非 nil = 命中制（空数组 = 禁用 MCP 注入）。
	enabled := func(id string) bool {
		if a.Mcps == nil {
			return true
		}
		for _, x := range *a.Mcps {
			if x == id {
				return true
			}
		}
		return false
	}
	if a.Mcps != nil && len(*a.Mcps) == 0 {
		notes = append(notes, "该 agent 声明 mcps: []（禁用 MCP 注入）")
	}

	// 候选 runtime → 支持的 MCP 传输（未知 runtime 记降级说明，不臆造能力）。
	transports := make(map[string][]string, len(runtimes))
	for _, r := range runtimes {
		ts, known := s.supportedMcpTransports(r)
		if !known {
			notes = append(notes, fmt.Sprintf("候选 runtime %q 未注册（按声明过滤，不推导其能力）", r))
			continue
		}
		transports[r] = ts
	}

	// MCP：注册表全量 + 每条的 enabled/runtimes（候选里支持该传输的子集，保持候选声明顺序）。
	servers := s.caps.ListMcpServers()
	mcpOut := make([]dtocapability.AgentMcpServerResp, 0, len(servers))
	carried := make(map[string]bool, len(servers)) // server id → 至少一个候选可承载
	for _, m := range servers {
		carriers := make([]string, 0, len(runtimes))
		for _, r := range runtimes {
			for _, t := range transports[r] {
				if t == m.Transport {
					carriers = append(carriers, r)
					break
				}
			}
		}
		if len(carriers) > 0 {
			carried[m.ID] = true
		}
		mcpOut = append(mcpOut, dtocapability.AgentMcpServerResp{
			ID: m.ID, Name: m.Name, Transport: m.Transport,
			Enabled: enabled(m.ID), Runtimes: carriers,
		})
	}

	// Skill：至少一个候选 runtime 有物化源；sources 裁剪到候选 runtime。
	skills := s.caps.ListSkills()
	skillOut := make([]dtocapability.AgentSkillResp, 0, len(skills))
	for _, sk := range skills {
		src := make([]dtocapability.SkillSourceResp, 0, len(sk.Sources))
		for _, so := range sk.Sources {
			if inAgent[so.Runtime] {
				src = append(src, dtocapability.SkillSourceResp{Runtime: so.Runtime, Path: so.Path, Format: so.Format})
			}
		}
		if len(src) == 0 {
			continue
		}
		skillOut = append(skillOut, dtocapability.AgentSkillResp{
			ID: sk.ID, Name: sk.Name, Description: sk.Description, Sources: src,
		})
	}

	// Tool：builtin/observed 恒可见；mcp 条目须归属 server 对该 agent 启用且至少一个候选可承载。
	tools := s.caps.ListTools()
	toolOut := make([]dtocapability.ToolResp, 0, len(tools))
	for _, t := range tools {
		if t.Source == capability.ToolSourceMCP && t.McpServer != "" {
			if !enabled(t.McpServer) || !carried[t.McpServer] {
				continue
			}
		}
		toolOut = append(toolOut, dtocapability.ToolResp{
			Name: t.Name, Description: t.Description, Source: t.Source,
			McpServer: t.McpServer, Schema: t.Schema,
		})
	}

	return &dtocapability.AgentCapabilitiesResp{
		AgentID: a.ID, Name: a.Name, Runtimes: runtimes,
		Skills: skillOut, McpServers: mcpOut, Tools: toolOut, Notes: notes,
	}, nil
}

// McpServersFor 选出可注入某 agent×runtime 的 MCP server 子集（P2 §5.2/§5.3）：
// 注册表全量 → 该 runtime 的 McpTransports 过滤 → agent.Mcps 白名单（nil = 未声明 → 全量；
// 非 nil = 白名单；空数组 = 本 agent 禁用 MCP 注入）。headers/凭证一并携带（走 ACP wire）。
// 未知 runtime → nil（维持现状不下发）。
func (s *Service) McpServersFor(agent *AgentRec, runtimeID string) []acl.McpServer {
	supported, ok := s.supportedMcpTransports(runtimeID)
	if !ok {
		return nil
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
