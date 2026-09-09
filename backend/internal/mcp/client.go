// Package mcp 是最小 MCP（Model Context Protocol）客户端：只为「平台侧 live 探测 tools/list」服务。
//
// 范围（design doc §5.2/§5.3、§19.2）：
//   - 传输：stdio（换行分隔 JSON-RPC 2.0）与 http/streamable_http（POST + JSON 或 SSE 响应）；
//     sse（legacy HTTP+SSE 双向通道）首版明确不支持 → mcp_transport_unsupported，不臆造。
//   - 实现最小流程：initialize 握手（声明空 capabilities）→ notifications/initialized
//     → tools/list（含 nextCursor 翻页）/ tools/call（B1）。
//   - 不实现：resources/prompts、sampling/roots、日志订阅、断线重连（后续切片）。
//
// 依赖克制：不引第三方 MCP SDK，协议面只覆盖探测需要的子集。
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

// CallResult 是一次 tools/call 的结果。
//
// Content 保持 MCP 的联合类型原样（文本/图片/资源链接…）：平台不解释内容语义，
// 只透传给调用方（前端/脚本），避免臆造字段。StructuredContent 为 MCP 2025-06-18 起的
// 结构化输出（服务端可选提供），同样原样透传。
type CallResult struct {
	Content           []json.RawMessage `json:"content"`
	IsError           bool              `json:"isError,omitempty"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	ServerName        string            `json:"-"`
	ServerVersion     string            `json:"-"`
}

// TextOf 把结果里的文本块拼成一段可读文本（非文本块跳过；无文本块返回空串）。
func (r CallResult) TextOf() string {
	var b strings.Builder
	for _, raw := range r.Content {
		var blk struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &blk); err != nil || blk.Type != "text" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(blk.Text)
	}
	return b.String()
}

// ListTools 探测一个 MCP server 的 tools/list（含 initialize 握手）。
// 传输不支持 → mcp_transport_unsupported；握手/列举失败 → mcp_initialize_failed / mcp_tools_list_failed。
func ListTools(ctx context.Context, s Server) (Result, error) {
	var res Result
	_, err := session(ctx, s, func(ctx context.Context, tr transport, init initializeResult) error {
		res.ServerName, res.ServerVersion = init.ServerInfo.Name, init.ServerInfo.Version
		tools, err := listToolsPages(ctx, tr)
		if err != nil {
			return err
		}
		res.Tools = tools
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// CallTool 调用一个 MCP server 的工具（tools/call）。
//
// 每次调用都是「建连 → initialize → initialized → tools/call → 关闭」的独立会话：
// 不做连接池/会话复用（MCP 会话是服务端有状态资源，池化的失效/并发语义需要单独设计，
// 首版不臆造）。args 为工具的 arguments 对象（nil = 无参数）。
// 错误码：mcp_transport_unsupported / mcp_initialize_failed / mcp_tools_call_failed / mcp_timeout。
func CallTool(ctx context.Context, s Server, name string, args json.RawMessage) (CallResult, error) {
	if strings.TrimSpace(name) == "" {
		return CallResult{}, errf("mcp_tool_name_required", "工具名必填")
	}
	var out CallResult
	_, err := session(ctx, s, func(ctx context.Context, tr transport, init initializeResult) error {
		out.ServerName, out.ServerVersion = init.ServerInfo.Name, init.ServerInfo.Version
		params := map[string]any{"name": name}
		if len(args) > 0 {
			// MCP 规定 arguments 是对象（键值表）；非对象（字符串/数组/null 以外的标量）在本地拒绝，
			// 不把协议外载荷透传给服务端。
			var obj map[string]any
			if err := json.Unmarshal(args, &obj); err != nil {
				return errf("mcp_tool_args_invalid", "arguments 须为 JSON 对象：%v", err)
			}
			if obj != nil {
				params["arguments"] = obj
			}
		}
		var res struct {
			Content           []json.RawMessage `json:"content"`
			IsError           bool              `json:"isError"`
			StructuredContent json.RawMessage   `json:"structuredContent"`
		}
		if err := tr.call(ctx, "tools/call", params, &res); err != nil {
			return wrapProbe(ctx, "mcp_tools_call_failed", "tools/call 失败", err)
		}
		out.Content, out.IsError, out.StructuredContent = res.Content, res.IsError, res.StructuredContent
		return nil
	})
	if err != nil {
		return CallResult{}, err
	}
	return out, nil
}

// session 建立一次 MCP 会话（initialize + notifications/initialized），把传输交给 fn，
// 结束后统一关闭传输。所有「需要握手」的能力（tools/list、tools/call、后续 resources/prompts）
// 都走这里，避免每加一个能力就复制一遍传输分派与握手代码。
func session(ctx context.Context, s Server, fn func(ctx context.Context, tr transport, init initializeResult) error) (initializeResult, error) {
	tr, err := dial(ctx, s)
	if err != nil {
		return initializeResult{}, err
	}
	defer tr.close()

	var p initializeParams
	p.ProtocolVersion = protocolVersion
	p.Capabilities = map[string]any{}
	p.ClientInfo.Name = clientName
	p.ClientInfo.Version = clientVersion

	var init initializeResult
	if err := tr.call(ctx, "initialize", p, &init); err != nil {
		return initializeResult{}, wrapProbe(ctx, "mcp_initialize_failed", "initialize 握手失败", err)
	}
	if err := tr.notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return initializeResult{}, wrapProbe(ctx, "mcp_initialize_failed", "initialized 通知失败", err)
	}
	if err := fn(ctx, tr, init); err != nil {
		return initializeResult{}, err
	}
	return init, nil
}

// dial 按传输形态建连（sse 明确不支持，返回 mcp_transport_unsupported）。
func dial(ctx context.Context, s Server) (transport, error) {
	switch s.Transport {
	case capability.TransportStdio:
		return dialStdio(ctx, s)
	case capability.TransportHTTP, capability.TransportStreamableHTTP:
		return dialHTTP(s), nil
	case capability.TransportSSE:
		return nil, errf("mcp_transport_unsupported",
			"MCP sse 传输暂不支持（首版仅 stdio/http/streamable_http）")
	default:
		return nil, errf("mcp_transport_unsupported", "未知 MCP 传输 %q", s.Transport)
	}
}

// listToolsPages tools/list 翻页（cursor 上限见 maxToolPages）。
func listToolsPages(ctx context.Context, tr transport) ([]Tool, error) {
	out := make([]Tool, 0, 8)
	cursor := ""
	for page := 0; ; page++ {
		if page >= maxToolPages {
			return nil, errf("mcp_tools_list_failed", "tools/list 分页超过 %d 页（疑似 nextCursor 死循环）", maxToolPages)
		}
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res listToolsResult
		if err := tr.call(ctx, "tools/list", params, &res); err != nil {
			return nil, wrapProbe(ctx, "mcp_tools_list_failed", "tools/list 失败", err)
		}
		for _, tl := range res.Tools {
			if tl.Name == "" {
				continue // 无名字段无法目录化，跳过（不臆造）
			}
			out = append(out, tl)
		}
		if res.NextCursor == "" {
			return out, nil
		}
		cursor = res.NextCursor
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

// wrapProbe 把底层错误包成带 code 的探测错误；ctx 超时/取消优先报 mcp_timeout。
func wrapProbe(ctx context.Context, code, what string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return errf("mcp_timeout", "%s：%v", what, ctxErr)
	}
	return errf(code, "%s：%v", what, err)
}
