package manager

import (
	"context"
	"fmt"
	"log/slog"
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

// MCP 工具调用预算（B1）：默认 30s（工具可能慢），上限 5min（防请求挂死）。
const (
	defaultMcpCallTimeout = 30 * time.Second
	maxMcpCallTimeout     = 5 * time.Minute
)

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

// CallMcpTool 调用某 MCP server 的工具（B1：tools/call）。
//
// 语义要点：
//   - 每次调用是独立会话（建连 → initialize → initialized → tools/call → 关闭）：不做连接池，
//     MCP 会话是有状态资源，池化的失效/并发语义需单独设计（不臆造）。
//   - 不要求先探测：server 才是工具存在性的权威（未探测也能调用；调用失败按 server 的
//     JSON-RPC 错误返回）。
//   - **工具级失败**（服务端 result.isError=true）不是平台错误：原样透传 is_error + content，
//     信封 code=0（调用方据 is_error 判断）；只有传输/协议/超时才映射为信封错误码。
func (s *Service) CallMcpTool(ctx context.Context, id, tool string, req handler.McpToolCallReq) (*handler.McpToolCallResp, error) {
	server, ok := s.caps.GetMcpServer(id)
	if !ok {
		return nil, s.restErr(acl.NewError(acl.ClassRequest, "mcp_not_found",
			fmt.Sprintf("未知 MCP server %q", id), nil))
	}
	budget := defaultMcpCallTimeout
	if req.TimeoutMS > 0 {
		budget = time.Duration(req.TimeoutMS) * time.Millisecond
		if budget > maxMcpCallTimeout {
			budget = maxMcpCallTimeout
		}
	}
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	start := time.Now()
	res, err := mcp.CallTool(cctx, mcp.Server{
		ID: server.ID, Transport: server.Transport, Command: server.Command,
		Args: server.Args, URL: server.URL, Headers: server.Headers,
	}, tool, req.Arguments)
	if err != nil {
		// 稳定 code 进信封 msg（同探测）：信封 code 只有分类粒度，排障需要精确原因。
		return nil, s.restErr(acl.NewError(mcpCallClass(err), mcp.CodeOf(err),
			fmt.Sprintf("[%s] %s", mcp.CodeOf(err), err.Error()), nil))
	}
	slog.Default().Info("mcp 工具调用完成", "server", id, "tool", tool,
		"is_error", res.IsError, "blocks", len(res.Content), "ms", time.Since(start).Milliseconds())
	return &handler.McpToolCallResp{
		ServerID: id, Tool: tool, IsError: res.IsError,
		Content: res.Content, Text: res.TextOf(), StructuredContent: res.StructuredContent,
		ServerName: res.ServerName, ServerVersion: res.ServerVersion,
		DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

// mcpCallClass 工具调用错误的 REST 分类（与探测同构：传输不支持→Unsupported，参数/超时→Request/Service）。
func mcpCallClass(err error) acl.Class {
	switch mcp.CodeOf(err) {
	case "mcp_transport_unsupported":
		return acl.ClassUnsupported
	case "mcp_command_invalid", "mcp_url_invalid", "mcp_tool_name_required", "mcp_tool_args_invalid",
		"mcp_resource_uri_required", "mcp_prompt_name_required":
		return acl.ClassRequest
	case "mcp_timeout":
		return acl.ClassLimit
	default:
		return acl.ClassService
	}
}

// ——— B2：MCP 资源与提示词（live 拉取，不落盘/不目录化） ———

// mcpCallBudget 资源/提示词拉取的统一预算（与探测同量级：一次建连 + 一次列表/读取）。
func (s *Service) mcpLiveServer(id string) (capability.McpServer, error) {
	server, ok := s.caps.GetMcpServer(id)
	if !ok {
		return capability.McpServer{}, s.restErr(acl.NewError(acl.ClassRequest, "mcp_not_found",
			fmt.Sprintf("未知 MCP server %q", id), nil))
	}
	return server, nil
}

// ListMcpResources 拉取某 MCP server 的资源列表（resources/list + resources/templates/list）。
func (s *Service) ListMcpResources(ctx context.Context, id string) (*handler.McpResourcesResp, error) {
	server, err := s.mcpLiveServer(id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, s.mcpProbeBudget())
	defer cancel()
	res, err := mcp.ListResources(rctx, mcpServerOf(server))
	if err != nil {
		return nil, s.mcpLiveErr(err, "mcp_resources_list_failed")
	}
	out := &handler.McpResourcesResp{
		ServerID: id, ServerName: res.ServerName, ServerVersion: res.ServerVersion,
		TemplatesError: res.TemplatesError,
		Resources:      make([]handler.McpResourceResp, 0, len(res.Resources)),
	}
	for _, r := range res.Resources {
		out.Resources = append(out.Resources, handler.McpResourceResp{
			URI: r.URI, Name: r.Name, Title: r.Title, Description: r.Description,
			MimeType: r.MimeType, Size: r.Size,
		})
	}
	for _, t := range res.Templates {
		out.Templates = append(out.Templates, handler.McpResourceTemplateResp{
			URITemplate: t.URITemplate, Name: t.Name, Title: t.Title,
			Description: t.Description, MimeType: t.MimeType,
		})
	}
	return out, nil
}

// ReadMcpResource 读取一个资源（resources/read）。
func (s *Service) ReadMcpResource(ctx context.Context, id, uri string) (*handler.McpResourceReadResp, error) {
	server, err := s.mcpLiveServer(id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, s.mcpProbeBudget())
	defer cancel()
	res, err := mcp.ReadResource(rctx, mcpServerOf(server), uri)
	if err != nil {
		return nil, s.mcpLiveErr(err, "mcp_resource_read_failed")
	}
	out := &handler.McpResourceReadResp{ServerID: id, URI: uri, Contents: make([]handler.McpResourceContent, 0, len(res.Contents))}
	for _, c := range res.Contents {
		out.Contents = append(out.Contents, handler.McpResourceContent{URI: c.URI, MimeType: c.MimeType, Text: c.Text, Blob: c.Blob})
	}
	return out, nil
}

// ListMcpPrompts 拉取某 MCP server 的提示词模板（prompts/list）。
func (s *Service) ListMcpPrompts(ctx context.Context, id string) (*handler.McpPromptsResp, error) {
	server, err := s.mcpLiveServer(id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, s.mcpProbeBudget())
	defer cancel()
	prompts, err := mcp.ListPrompts(rctx, mcpServerOf(server))
	if err != nil {
		return nil, s.mcpLiveErr(err, "mcp_prompts_list_failed")
	}
	out := &handler.McpPromptsResp{ServerID: id, Prompts: make([]handler.McpPromptResp, 0, len(prompts))}
	for _, p := range prompts {
		item := handler.McpPromptResp{Name: p.Name, Title: p.Title, Description: p.Description}
		for _, a := range p.Arguments {
			item.Arguments = append(item.Arguments, handler.McpPromptArgumentResp{
				Name: a.Name, Title: a.Title, Description: a.Description, Required: a.Required,
			})
		}
		out.Prompts = append(out.Prompts, item)
	}
	return out, nil
}

// GetMcpPrompt 取用一条提示词模板（prompts/get）。
func (s *Service) GetMcpPrompt(ctx context.Context, id, name string, req handler.McpPromptGetReq) (*handler.McpPromptGetResp, error) {
	server, err := s.mcpLiveServer(id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, s.mcpProbeBudget())
	defer cancel()
	res, err := mcp.GetPrompt(rctx, mcpServerOf(server), name, req.Arguments)
	if err != nil {
		return nil, s.mcpLiveErr(err, "mcp_prompt_get_failed")
	}
	out := &handler.McpPromptGetResp{ServerID: id, Name: name, Description: res.Description,
		Messages: make([]handler.McpPromptMessage, 0, len(res.Messages))}
	for _, m := range res.Messages {
		out.Messages = append(out.Messages, handler.McpPromptMessage{Role: m.Role, Content: m.Content})
	}
	return out, nil
}

// mcpServerOf 把能力目录条目映射为 mcp 包输入。
func mcpServerOf(server capability.McpServer) mcp.Server {
	return mcp.Server{
		ID: server.ID, Transport: server.Transport, Command: server.Command,
		Args: server.Args, URL: server.URL, Headers: server.Headers,
	}
}

// mcpLiveErr 把 mcp 层错误映射为 REST 信封错误（稳定 code 进 msg，分类决定信封码）。
func (s *Service) mcpLiveErr(err error, fallbackCode string) error {
	code := mcp.CodeOf(err)
	if code == "" {
		code = fallbackCode
	}
	return s.restErr(acl.NewError(mcpCallClass(err), code, fmt.Sprintf("[%s] %s", code, err.Error()), nil))
}

// AutoProbeMcpServers 后台探测全部已注册 MCP server（B4：`mcp_auto_probe=startup`）。
//
// 语义：
//   - **尽力而为**：逐个探测（串行，避免同时拉起大量子进程），单个失败只记状态与日志，
//     不中断其余；返回值仅表示「遍历是否被 ctx 打断」；
//   - 结果与 `POST /mcps/:id/probe` 完全一致（写 Tool 目录 + last_probe_at/error）；
//   - 探测预算沿用 `mcpProbeBudget()`（默认 15s/server）。
func (s *Service) AutoProbeMcpServers(ctx context.Context) error {
	servers := s.caps.ListMcpServers()
	if len(servers) == 0 {
		slog.Default().Info("mcp 自动探测：无已注册 server，跳过")
		return nil
	}
	slog.Default().Info("mcp 自动探测开始", "servers", len(servers))
	var okCount, failCount int
	for _, sv := range servers {
		if err := ctx.Err(); err != nil {
			slog.Default().Warn("mcp 自动探测被中断", "done", okCount+failCount, "total", len(servers), "err", err)
			return err
		}
		res, err := s.ProbeMcpServer(ctx, sv.ID)
		if err != nil {
			failCount++
			slog.Default().Warn("mcp 自动探测失败", "server", sv.ID, "err", err)
			continue
		}
		okCount++
		slog.Default().Info("mcp 自动探测完成", "server", sv.ID,
			"tools", res.ToolCount, "added", res.Added, "removed", res.Removed)
	}
	slog.Default().Info("mcp 自动探测结束", "ok", okCount, "failed", failCount)
	return nil
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
