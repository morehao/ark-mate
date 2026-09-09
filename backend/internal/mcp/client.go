// Package mcp 是最小 MCP（Model Context Protocol）客户端：只为「平台侧 live 探测 tools/list」服务。
//
// 范围（design doc §5.2/§5.3、§19.2）：
//   - 传输：stdio（换行分隔 JSON-RPC 2.0）与 http/streamable_http（POST + JSON 或 SSE 响应）；
//     sse（legacy HTTP+SSE 双向通道）首版明确不支持 → mcp_transport_unsupported，不臆造。
//   - 只实现探测所需最小流程：initialize 握手（声明空 capabilities）→ notifications/initialized
//     → tools/list（含 nextCursor 翻页）。
//   - 不实现：tools/call、resources/prompts、sampling/roots、日志订阅、断线重连。
//
// 依赖克制：不引第三方 MCP SDK，协议面只覆盖探测需要的子集。
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/morehao/ark-forge/internal/capability"
)

const (
	// protocolVersion 协商版本（客户端声明；服务端返回其支持版本，不强制相等——探测只读）。
	protocolVersion = "2025-06-18"
	clientName      = "ark-forge"
	clientVersion   = "0.1.0"

	// maxResponseBytes 单条消息上限（防御异常 server 打爆内存）。
	maxResponseBytes = 4 << 20
	// maxToolPages tools/list 翻页上限（防御 nextCursor 死循环）。
	maxToolPages = 20
)

// Server 是探测输入（由 capability.McpServer 映射，避免包间循环依赖）。
type Server struct {
	ID        string
	Transport string
	Command   string
	Args      []string
	URL       string
	Headers   map[string]string
}

// Tool 是 tools/list 返回的一条工具（inputSchema 原样保真）。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// Result 一次探测的结果：工具列表 + 握手拿到的服务端自述（排障/状态展示用）。
type Result struct {
	Tools         []Tool
	ServerName    string
	ServerVersion string
}

// Error 带稳定 code 的探测错误：manager 依 code 决定 REST 语义/日志，msg 面向用户可读。
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// CodeOf 取探测错误的稳定 code（非 *Error → ""）。
func CodeOf(err error) string {
	var me *Error
	if errors.As(err, &me) {
		return me.Code
	}
	return ""
}

// ListTools 探测一个 MCP server 的 tools/list（含 initialize 握手）。
// 传输不支持 → mcp_transport_unsupported；握手/列举失败 → mcp_initialize_failed / mcp_tools_list_failed。
func ListTools(ctx context.Context, s Server) (Result, error) {
	switch s.Transport {
	case capability.TransportStdio:
		return listToolsStdio(ctx, s)
	case capability.TransportHTTP, capability.TransportStreamableHTTP:
		return listToolsHTTP(ctx, s)
	case capability.TransportSSE:
		return Result{}, errf("mcp_transport_unsupported",
			"MCP sse 传输暂不支持探测（首版仅 stdio/http/streamable_http）")
	default:
		return Result{}, errf("mcp_transport_unsupported", "未知 MCP 传输 %q", s.Transport)
	}
}

// ——— JSON-RPC 2.0 最小消息面 ———

type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) String() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("code=%d message=%q", e.Code, e.Message)
}

// transport 是探测所需的传输抽象（一次探测内串行调用：请求→响应配对按 id）。
type transport interface {
	call(ctx context.Context, method string, params any, out any) error
	notify(ctx context.Context, method string, params any) error
	close()
}

// initializeParams 客户端握手声明：能力集为空（只做探测，不接受服务端反向调用）。
type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

type listToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// probe 跑完整探测流程：initialize → initialized 通知 → tools/list（翻页）。
func probe(ctx context.Context, tr transport) (Result, error) {
	defer tr.close()

	var p initializeParams
	p.ProtocolVersion = protocolVersion
	p.Capabilities = map[string]any{}
	p.ClientInfo.Name = clientName
	p.ClientInfo.Version = clientVersion

	var init initializeResult
	if err := tr.call(ctx, "initialize", p, &init); err != nil {
		return Result{}, wrapProbe(ctx, "mcp_initialize_failed", "initialize 握手失败", err)
	}
	if err := tr.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return Result{}, wrapProbe(ctx, "mcp_initialize_failed", "initialized 通知失败", err)
	}

	res := Result{ServerName: init.ServerInfo.Name, ServerVersion: init.ServerInfo.Version}
	cursor := ""
	for page := 0; ; page++ {
		if page >= maxToolPages {
			return Result{}, errf("mcp_tools_list_failed", "tools/list 分页超过 %d 页（疑似 nextCursor 死循环）", maxToolPages)
		}
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var out listToolsResult
		if err := tr.call(ctx, "tools/list", params, &out); err != nil {
			return Result{}, wrapProbe(ctx, "mcp_tools_list_failed", "tools/list 失败", err)
		}
		for _, tl := range out.Tools {
			if tl.Name == "" {
				continue // 无名字段无法目录化，跳过（不臆造）
			}
			res.Tools = append(res.Tools, tl)
		}
		if out.NextCursor == "" {
			break
		}
		cursor = out.NextCursor
	}
	return res, nil
}

// wrapProbe 把底层错误包成带 code 的探测错误；ctx 超时/取消优先报 mcp_timeout。
func wrapProbe(ctx context.Context, code, what string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return errf("mcp_timeout", "%s：%v", what, ctxErr)
	}
	return errf(code, "%s：%v", what, err)
}
