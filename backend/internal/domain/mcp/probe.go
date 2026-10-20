package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/code"
	mcpclient "github.com/morehao/ark-mate/internal/infra/mcp"
	"github.com/morehao/golib/glog"
)

// probeClass 探测错误 → acp 分类（决定 REST 信封码，见 restErr）。
func probeClass(err error) code.Class {
	switch mcpclient.CodeOf(err) {
	case "mcp_transport_unsupported":
		return code.ClassUnsupported
	case "mcp_command_invalid", "mcp_url_invalid", "mcp_secret_unresolved":
		// 密钥引用解析不了是**配置问题**（改引用或补齐库/环境），不是服务端故障 ⇒ 40001。
		return code.ClassRequest
	default:
		return code.ClassService
	}
}

// Probe 对某 MCP server 做一次 live tools/list 探测（P2 §5.2）：
// 建连（stdio/http/streamable_http；sse 明确不支持）→ initialize 握手 → tools/list（翻页）
// → **整体替换**该 server 归属的 Tool 目录条目（tools/list 是权威快照）。
// 成功/失败都会记录探测状态（GET /mcps 可读回 last_probe_*）。未知 server → 40401。
func (p *Plane) Probe(ctx context.Context, id string) (*ProbeResult, error) {
	server, ok := p.caps.GetMcpServer(id)
	if !ok {
		return nil, code.FromError(notFoundErr(id))
	}
	pctx, cancel := context.WithTimeout(ctx, p.ProbeBudget())
	defer cancel()

	// 密钥型绑定在这里**解析成明文**再下发（与注入面 McpServersFor 同一份 `Secrets.Resolve`）。
	// 此前本路径连 `mcp.Server.Env` 字段都没有 ⇒ 靠 env 拿凭据的 stdio server 必然探测失败，
	// 而同一个 server 在会话里（走 ACP 注入面）却是好的——2026-10-09 实测坐实。
	env, headers, problems := p.secrets.Resolve(server)
	if len(problems) > 0 {
		// 解析失败就**不拿空凭证去探**（同注入面纪律）：否则用户看到的是一个远处的 401，
		// 而"哪条引用解析不了"是当下就能说清的事实。文案只含键名与引用，绝不含值。
		// msg 里带 [:code] 是同一条排障约定（信封 code 只有分类粒度）。
		err := code.NewError(code.ClassRequest, "mcp_secret_unresolved", fmt.Sprintf("[mcp_secret_unresolved] 密钥引用解析失败（%s）", strings.Join(problems, "；")), nil)
		p.recordProbe(id, err)
		return nil, code.FromError(err)
	}

	res, err := mcpclient.ListTools(pctx, mcpclient.Server{
		ID: server.ID, Transport: server.Transport, Command: server.Command,
		Args: server.Args, URL: server.URL, Headers: headers, Env: env,
	})
	if err != nil {
		p.recordProbe(id, err)
		// 信封 msg 带稳定 code（[mcp_timeout]/[mcp_spawn_failed]…）：信封 code 只有分类粒度，
		// 排障需要精确原因（同 last_probe_error 可经 GET /mcps 读回）。
		return nil, code.FromError(code.NewError(probeClass(err), mcpclient.CodeOf(err), fmt.Sprintf("[%s] %s", mcpclient.CodeOf(err), err.Error()), nil))
	}

	tools := make([]capability.Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		if t.Name == "" {
			continue // 无名工具无法被调用，不进面（与网关口径一致）
		}
		name := t.Name
		if capability.IsRemoteTransport(server.Transport) {
			// **与网关同一命名策略**（同一个 `GatewayToolName` 实现）：远程工具在模型眼里叫
			// `<serverID>__<tool>`（网关代连时统一加前缀）。面板若显示原名，用户就没法把面板上
			// 看到的工具名拿去调——"探测面与真实面同源"就是 M7-④ 这条对齐要东西。
			//
			// 已知偏差（写下来免得被当成 bug）：`MATE_MCP_GATEWAY=off`，或该 server 最终被注入给
			// 一个**远程 ACP 端点**时，底座看到的是原名（那时没有网关条目）。目录是全平台一张表、
			// 不含"按 runtime 的注入形态"，为这两种回退形态单开一列不值得；默认形态（本机 runtime +
			// 网关 auto）下两者逐字一致。
			name = capability.GatewayToolName(id, t.Name)
		}
		tools = append(tools, capability.Tool{
			Name: name, Description: t.Description,
			Source: capability.ToolSourceMCP, McpServer: id, Schema: t.InputSchema,
		})
	}
	added, removed, skipped := p.caps.ReplaceToolsForMcpServer(id, tools)
	p.recordProbe(id, nil)

	out := &ProbeResult{
		ID: id, Transport: server.Transport,
		ServerName: res.ServerName, ServerVersion: res.ServerVersion,
		ProbedAt: time.Now().UTC().Format(time.RFC3339),
		Added:    added, Removed: removed, Skipped: skipped,
	}
	for _, t := range p.caps.ListTools() {
		if t.McpServer == id {
			out.Tools = append(out.Tools, ToolEntry{
				Name: t.Name, Description: t.Description, Source: t.Source,
				McpServer: t.McpServer, Schema: t.Schema,
			})
		}
	}
	out.ToolCount = len(out.Tools)
	return out, nil
}

// AutoProbe 后台探测全部已注册 MCP server（B4：`mcp_auto_probe=startup`）。
//
// 语义：
//   - **尽力而为**：逐个探测（串行，避免同时拉起大量子进程），单个失败只记状态与日志，
//     不中断其余；返回值仅表示「遍历是否被 ctx 打断」；
//   - 结果与 `POST /mcps/:id/probe` 完全一致（写 Tool 目录 + last_probe_at/error）；
//   - 探测预算沿用 `ProbeBudget()`（默认 15s/server）。
func (p *Plane) AutoProbe(ctx context.Context) error {
	servers := p.caps.ListMcpServers()
	if len(servers) == 0 {
		glog.Infow(ctx, "mcp 自动探测：无已注册 server，跳过")
		return nil
	}
	glog.Infow(ctx, "mcp 自动探测开始", "servers", len(servers))
	var okCount, failCount int
	for _, sv := range servers {
		if err := ctx.Err(); err != nil {
			glog.Warnw(ctx, "mcp 自动探测被中断", "done", okCount+failCount, "total", len(servers), "err", err)
			return err
		}
		res, err := p.Probe(ctx, sv.ID)
		if err != nil {
			failCount++
			glog.Warnw(ctx, "mcp 自动探测失败", "server", sv.ID, "err", err)
			continue
		}
		okCount++
		glog.Infow(ctx, "mcp 自动探测完成", "server", sv.ID,
			"tools", res.ToolCount, "added", res.Added, "removed", res.Removed)
	}
	glog.Infow(ctx, "mcp 自动探测结束", "ok", okCount, "failed", failCount)
	return nil
}
