package protocol

import (
	"encoding/json"
	"strings"
)

// ——— MCP 客户端结果值与工具面（A2，2026-10-22）———
//
// 这是**平台自己认的形状**：一次 `tools/list` / `tools/call` / `resources/*` / `prompts/*` 的
// 结果长什么样。事实源原先在 `infra/protocol/mcp`（客户端实现里），于是 `domain/mcp` 直连协议包
// ——域的每一条 live 路径（探测、工具调用、资源、提示词、网关）都为了一份返回值形状去认识
// "谁在跟 MCP server 说话"。搬出来之后：域只认识本包 + 自己声明的窄端口，实现体在装配根注入
// （分层方案 §3 的 W3）。
//
// 内容纪律（与 `CallResult.Content` 那半直接相关）：
//   - **原样保真**：`Content` 的联合类型（文本/图片/资源链接…）与 `StructuredContent` 都保持
//     MCP 的原始 JSON 块，平台不解释语义、不臆造字段——解释会随协议版本漂移，而透传不会。
//   - **纯值 + 纯计算**：本文件只有标准库依赖；`TextOf` 这类方法是纯函数（不改状态、不 I/O）。
//
// 判据：本包**不得** import 任何 `infra/*`（G5），所以这里不会长出"顺手调一下客户端"的路。

// McpTool 是 `tools/list` 返回的一条工具（`inputSchema` 原样保真）。
type McpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// McpToolList 一次 `tools/list` 的结果：工具列表 + 握手拿到的服务端自述（排障/状态展示用）。
type McpToolList struct {
	Tools         []McpTool
	ServerName    string
	ServerVersion string
}

// McpCallResult 是一次 `tools/call` 的结果。
//
// Content 保持 MCP 的联合类型原样（文本/图片/资源链接…）：平台不解释内容语义，
// 只透传给调用方（前端/脚本），避免臆造字段。StructuredContent 为 MCP 2025-06-18 起的
// 结构化输出（服务端可选提供），同样原样透传。
type McpCallResult struct {
	Content           []json.RawMessage `json:"content"`
	IsError           bool              `json:"isError,omitempty"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	ServerName        string            `json:"-"`
	ServerVersion     string            `json:"-"`
}

// TextOf 把结果里的文本块拼成一段可读文本（非文本块跳过；无文本块返回空串）。
func (r McpCallResult) TextOf() string {
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

// McpResource 一条可读资源（`resources/list`）。
type McpResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	Size        *int64 `json:"size,omitempty"`
}

// McpResourceTemplate 一条 URI 模板（`resources/templates/list`）。
type McpResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// McpResourceList `resources/list` + `resources/templates/list` 的结果。
//
// TemplatesError 非空表示该 server 不支持 templates（JSON-RPC -32601 等方法未找到）——
// 这是**服务端能力差异**，不是平台错误：资源列表照常返回，模板降级为空并如实记录原因。
type McpResourceList struct {
	Resources      []McpResource         `json:"resources"`
	Templates      []McpResourceTemplate `json:"templates,omitempty"`
	TemplatesError string                `json:"templates_error,omitempty"`
	ServerName     string                `json:"-"`
	ServerVersion  string                `json:"-"`
}

// McpResourceContent `resources/read` 的一条内容（text 与 blob 二选一，原样保真）。
type McpResourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// McpReadResourceResult `resources/read` 的结果。
type McpReadResourceResult struct {
	Contents []McpResourceContent `json:"contents"`
}

// McpPromptArgument 提示词参数声明（`prompts/list` 的 arguments 项）。
type McpPromptArgument struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// McpPrompt 一条提示词模板（`prompts/list`）。
type McpPrompt struct {
	Name        string              `json:"name"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Arguments   []McpPromptArgument `json:"arguments,omitempty"`
}

// McpPromptMessage `prompts/get` 返回的一条消息（content 保持 MCP 联合类型原样）。
type McpPromptMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// McpGetPromptResult `prompts/get` 的结果。
type McpGetPromptResult struct {
	Description string             `json:"description,omitempty"`
	Messages    []McpPromptMessage `json:"messages"`
}
