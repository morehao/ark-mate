// Package mcp 是最小 MCP（Model Context Protocol）客户端：只为「平台侧 live 探测 tools/list」服务。
//
// 范围（design doc §5.2/§5.3、§19.2）：
//   - 传输：stdio（换行分隔 JSON-RPC 2.0）、http/streamable_http（POST + JSON 或 SSE 响应）、
//     sse（legacy HTTP+SSE 双向通道，B3）。
//   - 实现最小流程：initialize 握手（声明空 capabilities）→ notifications/initialized
//     → tools/list（含 nextCursor 翻页）/ tools/call（B1）
//     / resources/list + resources/templates/list + resources/read（B2）
//     / prompts/list + prompts/get（B2）。
//   - 不实现：sampling/roots、日志订阅、断线重连（后续切片）。
//
// 依赖克制：不引第三方 MCP SDK，协议面只覆盖探测需要的子集。
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/morehao/ark-forge/pkg/capability"
)

const (
	// protocolVersion 协商版本（客户端声明；服务端返回其支持版本，不强制相等——探测只读）。
	protocolVersion = "2025-06-18"
	clientName      = "ark-forge"
	clientVersion   = "0.1.0"

	// maxResponseBytes 单条消息上限（防御异常 server 打爆内存）。
	maxResponseBytes = 4 << 20
	// maxListPages 列表类方法（tools/list、resources/list、prompts/list…）翻页上限
	// （防御 nextCursor 死循环）。
	maxListPages = 20
)

// Server 是探测输入（由 capability.McpServer 映射，避免包间循环依赖）。
type Server struct {
	ID        string
	Transport string
	Command   string
	Args      []string
	URL       string
	Headers   map[string]string
	// Env 追加到 stdio 子进程的环境（在进程环境之上；非 stdio 传输忽略）。
	//
	// 约定与 acl.McpServer.Env 一致：**调用方已把密钥型绑定（`vault:`/`env:`）解析成明文**，
	// 本包只负责下发。缺了它，靠 env 拿凭据的 stdio server 会"会话里能用、平台探测/调用必失败"
	// （2026-10-09 实测：同一个 server，探测路径起的进程拿不到 env、ACP 注入路径拿得到）。
	//
	// 纪律：任何错误文案、日志、诊断都**不得包含这里的值**（键名可以）——本包是密钥经过的一层。
	Env map[string]string
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

// dial 按传输形态建连；未知传输 → mcp_transport_unsupported（不臆造）。
func dial(ctx context.Context, s Server) (transport, error) {
	switch s.Transport {
	case capability.TransportStdio:
		return dialStdio(ctx, s)
	case capability.TransportHTTP, capability.TransportStreamableHTTP:
		return dialHTTP(s), nil
	case capability.TransportSSE:
		return dialSSE(ctx, s)
	default:
		return nil, errf("mcp_transport_unsupported", "未知 MCP 传输 %q", s.Transport)
	}
}

// ——— B2：resources / prompts ———

// Resource 一条可读资源（resources/list）。
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	Size        *int64 `json:"size,omitempty"`
}

// ResourceTemplate 一条 URI 模板（resources/templates/list）。
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourceList resources/list + resources/templates/list 的结果。
//
// TemplatesError 非空表示该 server 不支持 templates（JSON-RPC -32601 等方法未找到）——
// 这是**服务端能力差异**，不是平台错误：资源列表照常返回，模板降级为空并如实记录原因。
type ResourceList struct {
	Resources      []Resource         `json:"resources"`
	Templates      []ResourceTemplate `json:"templates,omitempty"`
	TemplatesError string             `json:"templates_error,omitempty"`
	ServerName     string             `json:"-"`
	ServerVersion  string             `json:"-"`
}

// ResourceContent resources/read 的一条内容（text 与 blob 二选一，原样保真）。
type ResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// ReadResourceResult resources/read 的结果。
type ReadResourceResult struct {
	Contents []ResourceContent `json:"contents"`
}

// PromptArgument 提示词参数声明（prompts/list 的 arguments 项）。
type PromptArgument struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// Prompt 一条提示词模板（prompts/list）。
type Prompt struct {
	Name        string           `json:"name"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptMessage prompts/get 返回的一条消息（content 保持 MCP 联合类型原样）。
type PromptMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// GetPromptResult prompts/get 的结果。
type GetPromptResult struct {
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// ListResources 拉取某 MCP server 的资源列表（resources/list 翻页 + resources/templates/list）。
// 模板接口不支持时降级为空并记录 TemplatesError（不臆造）。
func ListResources(ctx context.Context, s Server) (ResourceList, error) {
	var out ResourceList
	_, err := session(ctx, s, func(ctx context.Context, tr transport, init initializeResult) error {
		out.ServerName, out.ServerVersion = init.ServerInfo.Name, init.ServerInfo.Version
		res, err := paginate(ctx, tr, "resources/list", "mcp_resources_list_failed", func(raw json.RawMessage) ([]Resource, string, error) {
			var page struct {
				Resources  []Resource `json:"resources"`
				NextCursor string     `json:"nextCursor"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", err
			}
			return page.Resources, page.NextCursor, nil
		})
		if err != nil {
			return err
		}
		out.Resources = res
		tpl, terr := paginate(ctx, tr, "resources/templates/list", "mcp_resources_list_failed", func(raw json.RawMessage) ([]ResourceTemplate, string, error) {
			var page struct {
				ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
				NextCursor        string             `json:"nextCursor"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", err
			}
			return page.ResourceTemplates, page.NextCursor, nil
		})
		if terr != nil {
			out.TemplatesError = terr.Error() // 服务端未实现 → 降级（资源列表仍可用）
			return nil
		}
		out.Templates = tpl
		return nil
	})
	if err != nil {
		return ResourceList{}, err
	}
	return out, nil
}

// ReadResource 读取一个资源（resources/read）。
func ReadResource(ctx context.Context, s Server, uri string) (ReadResourceResult, error) {
	if strings.TrimSpace(uri) == "" {
		return ReadResourceResult{}, errf("mcp_resource_uri_required", "资源 uri 必填")
	}
	var out ReadResourceResult
	_, err := session(ctx, s, func(ctx context.Context, tr transport, _ initializeResult) error {
		var res struct {
			Contents []ResourceContent `json:"contents"`
		}
		if err := tr.call(ctx, "resources/read", map[string]any{"uri": uri}, &res); err != nil {
			return wrapProbe(ctx, "mcp_resource_read_failed", "resources/read 失败", err)
		}
		out.Contents = res.Contents
		return nil
	})
	if err != nil {
		return ReadResourceResult{}, err
	}
	return out, nil
}

// ListPrompts 拉取某 MCP server 的提示词模板（prompts/list 翻页）。
func ListPrompts(ctx context.Context, s Server) ([]Prompt, error) {
	var out []Prompt
	_, err := session(ctx, s, func(ctx context.Context, tr transport, _ initializeResult) error {
		res, err := paginate(ctx, tr, "prompts/list", "mcp_prompts_list_failed", func(raw json.RawMessage) ([]Prompt, string, error) {
			var page struct {
				Prompts    []Prompt `json:"prompts"`
				NextCursor string   `json:"nextCursor"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, "", err
			}
			return page.Prompts, page.NextCursor, nil
		})
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetPrompt 取用一条提示词模板（prompts/get）；args 为模板参数（值必须是字符串，按 MCP 约定）。
func GetPrompt(ctx context.Context, s Server, name string, args map[string]string) (GetPromptResult, error) {
	if strings.TrimSpace(name) == "" {
		return GetPromptResult{}, errf("mcp_prompt_name_required", "提示词名必填")
	}
	var out GetPromptResult
	_, err := session(ctx, s, func(ctx context.Context, tr transport, _ initializeResult) error {
		params := map[string]any{"name": name}
		if len(args) > 0 {
			params["arguments"] = args
		}
		var res GetPromptResult
		if err := tr.call(ctx, "prompts/get", params, &res); err != nil {
			return wrapProbe(ctx, "mcp_prompt_get_failed", "prompts/get 失败", err)
		}
		out = res
		return nil
	})
	if err != nil {
		return GetPromptResult{}, err
	}
	return out, nil
}

// paginate 通用列表翻页：每页调用 method（带 cursor），extract 从 result 里取「条目 + nextCursor」。
// 翻页上限 maxListPages；错误码由调用方给出（如 mcp_resources_list_failed）。
func paginate[T any](ctx context.Context, tr transport, method, errCode string,
	extract func(raw json.RawMessage) ([]T, string, error)) ([]T, error) {
	out := make([]T, 0, 8)
	cursor := ""
	for page := 0; ; page++ {
		if page >= maxListPages {
			return nil, errf(errCode, "%s 分页超过 %d 页（疑似 nextCursor 死循环）", method, maxListPages)
		}
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var raw json.RawMessage
		if err := tr.call(ctx, method, params, &raw); err != nil {
			return nil, wrapProbe(ctx, errCode, method+" 失败", err)
		}
		items, next, err := extract(raw)
		if err != nil {
			return nil, wrapProbe(ctx, errCode, method+" 响应解析失败", err)
		}
		out = append(out, items...)
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

// listToolsPages tools/list 翻页（cursor 上限见 maxListPages）。
func listToolsPages(ctx context.Context, tr transport) ([]Tool, error) {
	return paginate(ctx, tr, "tools/list", "mcp_tools_list_failed", func(raw json.RawMessage) ([]Tool, string, error) {
		var page listToolsResult
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, "", err
		}
		items := make([]Tool, 0, len(page.Tools))
		for _, tl := range page.Tools {
			if tl.Name == "" {
				continue // 无名字段无法目录化，跳过（不臆造）
			}
			items = append(items, tl)
		}
		return items, page.NextCursor, nil
	})
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
