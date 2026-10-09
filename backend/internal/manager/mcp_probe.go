package manager

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/morehao/ark-forge/internal/dto/dtocapability"
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/capability"
	"github.com/morehao/ark-forge/pkg/code"
	"github.com/morehao/ark-forge/pkg/mcp"
)

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
	case "mcp_command_invalid", "mcp_url_invalid", "mcp_secret_unresolved":
		// 密钥引用解析不了是**配置问题**（改引用或补 vault/env），不是服务端故障 ⇒ 40001。
		return acl.ClassRequest
	default:
		return acl.ClassService
	}
}

// ProbeMcpServer 对某 MCP server 做一次 live tools/list 探测（P2 §5.2）：
// 建连（stdio/http/streamable_http；sse 明确不支持）→ initialize 握手 → tools/list（翻页）
// → **整体替换**该 server 归属的 Tool 目录条目（tools/list 是权威快照）。
// 成功/失败都会记录探测状态（GET /mcps 可读回 last_probe_*）。未知 server → 40401。
func (s *Service) ProbeMcpServer(ctx context.Context, id string) (*dtocapability.McpProbeResp, error) {
	server, ok := s.caps.GetMcpServer(id)
	if !ok {
		return nil, code.FromError(acl.NewError(acl.ClassRequest, "mcp_not_found",
			fmt.Sprintf("未知 MCP server %q", id), nil))
	}
	pctx, cancel := context.WithTimeout(ctx, s.mcpProbeBudget())
	defer cancel()

	// 密钥型绑定在这里**解析成明文**再下发（与注入面 McpServersFor 同一个 resolveMcpSecrets）。
	// 此前本路径连 `mcp.Server.Env` 字段都没有 ⇒ 靠 env 拿凭据的 stdio server 必然探测失败，
	// 而同一个 server 在会话里（走 ACP 注入面）却是好的——2026-10-09 实测坐实。
	env, headers, problems := s.resolveMcpSecrets(server)
	if len(problems) > 0 {
		// 解析失败就**不拿空凭证去探**（同注入面纪律）：否则用户看到的是一个远处的 401，
		// 而"哪条引用解析不了"是当下就能说清的事实。文案只含键名与引用，绝不含值。
		// msg 里带 [:code] 是同一条排障约定（信封 code 只有分类粒度）。
		err := acl.NewError(acl.ClassRequest, "mcp_secret_unresolved",
			fmt.Sprintf("[mcp_secret_unresolved] 密钥引用解析失败（%s）", strings.Join(problems, "；")), nil)
		s.recordMcpProbe(id, err)
		return nil, code.FromError(err)
	}

	res, err := mcp.ListTools(pctx, mcp.Server{
		ID: server.ID, Transport: server.Transport, Command: server.Command,
		Args: server.Args, URL: server.URL, Headers: headers, Env: env,
	})
	if err != nil {
		s.recordMcpProbe(id, err)
		// 信封 msg 带稳定 code（[mcp_timeout]/[mcp_spawn_failed]…）：信封 code 只有分类粒度，
		// 排障需要精确原因（同 last_probe_error 可经 GET /mcps 读回）。
		return nil, code.FromError(acl.NewError(mcpProbeClass(err), mcp.CodeOf(err),
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

	out := &dtocapability.McpProbeResp{
		ID: id, Transport: server.Transport,
		ServerName: res.ServerName, ServerVersion: res.ServerVersion,
		ProbedAt: time.Now().UTC().Format(time.RFC3339),
		Added:    added, Removed: removed, Skipped: skipped,
	}
	for _, t := range s.caps.ListTools() {
		if t.McpServer == id {
			out.Tools = append(out.Tools, dtocapability.ToolResp{
				Name: t.Name, Description: t.Description, Source: t.Source,
				McpServer: t.McpServer, Schema: t.Schema,
			})
		}
	}
	out.ToolCount = len(out.Tools)
	return out, nil
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
