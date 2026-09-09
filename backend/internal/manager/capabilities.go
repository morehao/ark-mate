package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/capability"
	"github.com/morehao/ark-forge/internal/mcp"
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

// ReconcileSkills 把能力数据面里的 opencode SKILL.md 源 skill 与目标根目录做重校
// （§5.4/§16.5：物化缺失/更新 + 删除联动——只删带 forge 归属标记且已不在注册表的目录，
// 用户自有目录绝不动）。返回 (written, removed)。serve 启动装载能力后、
// FORGE_SKILLS_MATERIALIZE_DIR 非空时调用。物化默认不写用户目录——由调用方显式 enable。
func (s *Service) ReconcileSkills(root string) (int, int, error) {
	if s.caps == nil || root == "" {
		return 0, 0, nil
	}
	return capability.ReconcileSkills(root, s.caps.ListSkills())
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
// 附 live 探测状态（tool_count / last_probe_at / last_probe_error，P2 §5.2）。
func (s *Service) ListMcpServers() ([]handler.McpServerResp, error) {
	items := s.caps.ListMcpServers()
	counts := s.mcpToolCounts()
	probes := s.mcpProbeSnapshot()
	out := make([]handler.McpServerResp, 0, len(items))
	for _, m := range items {
		resp := handler.McpServerResp{
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

// ——— P2 §5.2 MCP live 探测（tools/list → Tool 目录） ———

// defaultMCPProbeTimeout 单次探测预算（覆盖 spawn + initialize + tools/list 翻页）。
const defaultMCPProbeTimeout = 15 * time.Second

// mcpProbeStatus 一个 server 的最近一次探测状态（At 零值 = 从未探测）。
type mcpProbeStatus struct {
	At  time.Time
	Err string // 空 = 成功
}

// SetMcpProbeTimeout 覆盖单次探测预算（<=0 → 默认 15s；测试注入短预算用）。
func (s *Service) SetMcpProbeTimeout(d time.Duration) {
	if d <= 0 {
		d = defaultMCPProbeTimeout
	}
	s.mcpMu.Lock()
	s.mcpProbeTimeout = d
	s.mcpMu.Unlock()
}

func (s *Service) mcpProbeBudget() time.Duration {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	if s.mcpProbeTimeout <= 0 {
		return defaultMCPProbeTimeout
	}
	return s.mcpProbeTimeout
}

func (s *Service) recordMcpProbe(id string, err error) {
	st := mcpProbeStatus{At: time.Now()}
	if err != nil {
		st.Err = err.Error()
	}
	s.mcpMu.Lock()
	s.mcpProbes[id] = st
	s.mcpMu.Unlock()
}

func (s *Service) mcpProbeSnapshot() map[string]mcpProbeStatus {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	out := make(map[string]mcpProbeStatus, len(s.mcpProbes))
	for k, v := range s.mcpProbes {
		out[k] = v
	}
	return out
}

// mcpToolCounts 统计各 server 当前归属的 Tool 条目数。
func (s *Service) mcpToolCounts() map[string]int {
	out := map[string]int{}
	for _, t := range s.caps.ListTools() {
		if t.Source == capability.ToolSourceMCP && t.McpServer != "" {
			out[t.McpServer]++
		}
	}
	return out
}

// mcpProbeClass 探测错误 → acl 分类（决定 REST 信封码，见 restErr）。
func mcpProbeClass(err error) acl.Class {
	switch mcp.CodeOf(err) {
	case "mcp_transport_unsupported":
		return acl.ClassUnsupported
	case "mcp_command_invalid", "mcp_url_invalid":
		return acl.ClassRequest
	default:
		return acl.ClassService
	}
}

// ProbeMcpServer 对某 MCP server 做一次 live tools/list 探测（P2 §5.2）：
// 建连（stdio/http/streamable_http；sse 明确不支持）→ initialize 握手 → tools/list（翻页）
// → **整体替换**该 server 归属的 Tool 目录条目（tools/list 是权威快照）。
// 成功/失败都会记录探测状态（GET /mcps 可读回 last_probe_*）。未知 server → 40401。
func (s *Service) ProbeMcpServer(ctx context.Context, id string) (*handler.McpProbeResp, error) {
	server, ok := s.caps.GetMcpServer(id)
	if !ok {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "mcp_not_found",
			fmt.Sprintf("未知 MCP server %q", id), nil))
	}
	pctx, cancel := context.WithTimeout(ctx, s.mcpProbeBudget())
	defer cancel()

	res, err := mcp.ListTools(pctx, mcp.Server{
		ID: server.ID, Transport: server.Transport, Command: server.Command,
		Args: server.Args, URL: server.URL, Headers: server.Headers,
	})
	if err != nil {
		s.recordMcpProbe(id, err)
		// 信封 msg 带稳定 code（[mcp_timeout]/[mcp_spawn_failed]…）：信封 code 只有分类粒度，
		// 排障需要精确原因（同 last_probe_error 可经 GET /mcps 读回）。
		return nil, s.restErr(acl.NewError(mcpProbeClass(err), mcp.CodeOf(err),
			fmt.Sprintf("[%s] %s", mcp.CodeOf(err), err.Error()), nil))
	}

	tools := make([]capability.Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		tools = append(tools, capability.Tool{
			Name: t.Name, Description: t.Description,
			Source: capability.ToolSourceMCP, McpServer: id, Schema: t.InputSchema,
		})
	}
	added, removed, skipped := s.caps.ReplaceToolsForMcpServer(id, tools)
	s.recordMcpProbe(id, nil)

	out := &handler.McpProbeResp{
		ID: id, Transport: server.Transport,
		ServerName: res.ServerName, ServerVersion: res.ServerVersion,
		ProbedAt: time.Now().UTC().Format(time.RFC3339),
		Added:    added, Removed: removed, Skipped: skipped,
	}
	for _, t := range s.caps.ListTools() {
		if t.McpServer == id {
			out.Tools = append(out.Tools, handler.ToolResp{
				Name: t.Name, Description: t.Description, Source: t.Source,
				McpServer: t.McpServer, Schema: t.Schema,
			})
		}
	}
	out.ToolCount = len(out.Tools)
	return out, nil
}

// ListTools 返回 Tool 目录（按 name 排序）。
func (s *Service) ListTools() ([]handler.ToolResp, error) {
	items := s.caps.ListTools()
	out := make([]handler.ToolResp, 0, len(items))
	for _, t := range items {
		out = append(out, handler.ToolResp{
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

// AgentCapabilities 组装某 Agent 的统一能力目录（§8.1；可见性规则见 handler.AgentCapabilitiesResp）。
// 只读聚合：不触发物化/注入/探测，全部依据注册表 + 候选 runtime 声明推导。未知 agent → agent_not_found。
func (s *Service) AgentCapabilities(agentID string) (*handler.AgentCapabilitiesResp, error) {
	a, ok := s.AgentByID(agentID)
	if !ok {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "agent_not_found",
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
	mcpOut := make([]handler.AgentMcpServerResp, 0, len(servers))
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
		mcpOut = append(mcpOut, handler.AgentMcpServerResp{
			ID: m.ID, Name: m.Name, Transport: m.Transport,
			Enabled: enabled(m.ID), Runtimes: carriers,
		})
	}

	// Skill：至少一个候选 runtime 有物化源；sources 裁剪到候选 runtime。
	skills := s.caps.ListSkills()
	skillOut := make([]handler.AgentSkillResp, 0, len(skills))
	for _, sk := range skills {
		src := make([]handler.SkillSourceResp, 0, len(sk.Sources))
		for _, so := range sk.Sources {
			if inAgent[so.Runtime] {
				src = append(src, handler.SkillSourceResp{Runtime: so.Runtime, Path: so.Path, Format: so.Format})
			}
		}
		if len(src) == 0 {
			continue
		}
		skillOut = append(skillOut, handler.AgentSkillResp{
			ID: sk.ID, Name: sk.Name, Description: sk.Description, Sources: src,
		})
	}

	// Tool：builtin/observed 恒可见；mcp 条目须归属 server 对该 agent 启用且至少一个候选可承载。
	tools := s.caps.ListTools()
	toolOut := make([]handler.ToolResp, 0, len(tools))
	for _, t := range tools {
		if t.Source == capability.ToolSourceMCP && t.McpServer != "" {
			if !enabled(t.McpServer) || !carried[t.McpServer] {
				continue
			}
		}
		toolOut = append(toolOut, handler.ToolResp{
			Name: t.Name, Description: t.Description, Source: t.Source,
			McpServer: t.McpServer, Schema: t.Schema,
		})
	}

	return &handler.AgentCapabilitiesResp{
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
