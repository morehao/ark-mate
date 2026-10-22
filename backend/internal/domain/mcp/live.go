package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/golib/glog"
)

// MCP 工具调用预算（B1）：默认 30s（工具可能慢），上限 5min（防请求挂死）。
const (
	defaultCallTimeout = 30 * time.Second
	maxCallTimeout     = 5 * time.Minute
)

// CallTool 调用某 MCP server 的工具（B1：tools/call）。
//
// 语义要点：
//   - 每次调用是独立会话（建连 → initialize → initialized → tools/call → 关闭）：不做连接池，
//     MCP 会话是有状态资源，池化的失效/并发语义需单独设计（不臆造）。
//   - 不要求先探测：server 才是工具存在性的权威（未探测也能调用；调用失败按 server 的
//     JSON-RPC 错误返回）。
//   - **工具级失败**（服务端 result.isError=true）不是平台错误：原样透传 is_error + content，
//     信封 code=0（调用方据 is_error 判断）；只有传输/协议/超时才映射为信封错误码。
func (p *Plane) CallTool(ctx context.Context, id, tool string, req ToolCallRequest) (*ToolCallResult, error) {
	cli, err := p.client()
	if err != nil {
		return nil, code.FromError(err)
	}
	server, err := p.LiveServer(id)
	if err != nil {
		return nil, err
	}
	budget := defaultCallTimeout
	if req.TimeoutMS > 0 {
		budget = time.Duration(req.TimeoutMS) * time.Millisecond
		if budget > maxCallTimeout {
			budget = maxCallTimeout
		}
	}
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	start := time.Now()
	res, err := cli.CallTool(cctx, server, tool, req.Arguments)
	if err != nil {
		// 稳定 code 进信封 msg（同探测）：信封 code 只有分类粒度，排障需要精确原因。
		return nil, code.FromError(failure.NewError(callClass(p.codeOf(err)), p.codeOf(err),
			fmt.Sprintf("[%s] %s", p.codeOf(err), err.Error()), nil))
	}
	glog.Infow(ctx, "mcp 工具调用完成", "server", id, "tool", tool,
		"is_error", res.IsError, "blocks", len(res.Content), "ms", time.Since(start).Milliseconds())
	return &ToolCallResult{
		ServerID: id, Tool: tool, IsError: res.IsError,
		Content: res.Content, Text: res.TextOf(), StructuredContent: res.StructuredContent,
		ServerName: res.ServerName, ServerVersion: res.ServerVersion,
		DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

// client 取 live 客户端实现体（A2 端口化）。
func (p *Plane) client() (LiveClient, error) {
	if p.live == nil {
		return nil, failure.NewError(failure.ClassService, "mcp_client_unavailable",
			"[mcp_client_unavailable] MCP 客户端未装配：live 面（探测/调用/资源/提示词）不可用", nil)
	}
	return p.live, nil
}

// codeOf 取实现侧的稳定错误 code（未装配客户端 → ""，让 live 面报错而不是 panic）。
func (p *Plane) codeOf(err error) string {
	if p.live == nil {
		return ""
	}
	return p.live.CodeOf(err)
}

// callClass 工具调用错误的 REST 分类（与探测同构：传输不支持→Unsupported，参数/超时→Request/Service）。
//
// 入参是稳定 code 而不是 error：分类是**词表到信封**的映射，与"哪个实现回答的"无关
// （A2 端口化之后域拿不到实现，只拿得到 code）。
func callClass(c string) failure.Class {
	switch c {
	case "mcp_transport_unsupported":
		return failure.ClassUnsupported
	case "mcp_command_invalid", "mcp_url_invalid", "mcp_tool_name_required", "mcp_tool_args_invalid",
		"mcp_resource_uri_required", "mcp_prompt_name_required":
		return failure.ClassRequest
	case "mcp_timeout":
		return failure.ClassLimit
	default:
		return failure.ClassService
	}
}

// LiveServer 取条目并解析成 MCP 客户端输入：**密钥型绑定（`env:`/`store:`）在这里解析成明文**，
// 与注入面（McpServersFor）共用同一份 `Secrets.Resolve`——两处各写一遍的后果就是今天这个缺陷：
// 注入面带 env、探测/调用面不带，靠 env 拿凭据的 stdio server 于是"会话里能用、探测必失败"
// （2026-10-09 实测）。
//
// 解析失败 → **当场报错**，不拿空凭证去连（同注入面的纪律）：半配置的 server 会让用户看到一个
// 远处的 401，而"哪条引用解析不了"是此刻就能说清的事实，且错误里只有键名与引用、没有值。
//
// 返回 `protocol.McpServer`（平台值，A2）：`Name` 取定义 id——它是客户端唯一需要的身份
// （日志/池键）。域不再认识协议包的 `Server` 结构。
func (p *Plane) LiveServer(id string) (protocol.McpServer, error) {
	server, ok := p.caps.GetMcpServer(id)
	if !ok {
		return protocol.McpServer{}, code.FromError(notFoundErr(id))
	}
	env, headers, problems := p.secrets.Resolve(server)
	if len(problems) > 0 {
		return protocol.McpServer{}, code.FromError(failure.NewError(failure.ClassRequest, "mcp_secret_unresolved",
			fmt.Sprintf("[mcp_secret_unresolved] 密钥引用解析失败（%s）", strings.Join(problems, "；")), nil))
	}
	return protocol.McpServer{
		Name: id, Transport: server.Transport, Command: server.Command,
		Args: server.Args, URL: server.URL, Headers: headers, Env: env,
	}, nil
}

// ListResources 拉取某 MCP server 的资源列表（resources/list + resources/templates/list）。
func (p *Plane) ListResources(ctx context.Context, id string) (*ResourcesResult, error) {
	cli, err := p.client()
	if err != nil {
		return nil, code.FromError(err)
	}
	server, err := p.LiveServer(id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, p.ProbeBudget())
	defer cancel()
	res, err := cli.ListResources(rctx, server)
	if err != nil {
		return nil, p.liveErr(err, "mcp_resources_list_failed")
	}
	out := &ResourcesResult{
		ServerID: id, ServerName: res.ServerName, ServerVersion: res.ServerVersion,
		TemplatesError: res.TemplatesError,
		Resources:      make([]ResourceItem, 0, len(res.Resources)),
	}
	for _, r := range res.Resources {
		out.Resources = append(out.Resources, ResourceItem{
			URI: r.URI, Name: r.Name, Title: r.Title, Description: r.Description,
			MimeType: r.MimeType, Size: r.Size,
		})
	}
	for _, t := range res.Templates {
		out.Templates = append(out.Templates, ResourceTemplate{
			URITemplate: t.URITemplate, Name: t.Name, Title: t.Title,
			Description: t.Description, MimeType: t.MimeType,
		})
	}
	return out, nil
}

// ReadResource 读取一个资源（resources/read）。
func (p *Plane) ReadResource(ctx context.Context, id, uri string) (*ResourceReadResult, error) {
	cli, err := p.client()
	if err != nil {
		return nil, code.FromError(err)
	}
	server, err := p.LiveServer(id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, p.ProbeBudget())
	defer cancel()
	res, err := cli.ReadResource(rctx, server, uri)
	if err != nil {
		return nil, p.liveErr(err, "mcp_resource_read_failed")
	}
	out := &ResourceReadResult{ServerID: id, URI: uri, Contents: make([]ResourceContent, 0, len(res.Contents))}
	for _, c := range res.Contents {
		out.Contents = append(out.Contents, ResourceContent{URI: c.URI, MimeType: c.MimeType, Text: c.Text, Blob: c.Blob})
	}
	return out, nil
}

// ListPrompts 拉取某 MCP server 的提示词模板（prompts/list）。
func (p *Plane) ListPrompts(ctx context.Context, id string) (*PromptsResult, error) {
	cli, err := p.client()
	if err != nil {
		return nil, code.FromError(err)
	}
	server, err := p.LiveServer(id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, p.ProbeBudget())
	defer cancel()
	prompts, err := cli.ListPrompts(rctx, server)
	if err != nil {
		return nil, p.liveErr(err, "mcp_prompts_list_failed")
	}
	out := &PromptsResult{ServerID: id, Prompts: make([]PromptItem, 0, len(prompts))}
	for _, pm := range prompts {
		item := PromptItem{Name: pm.Name, Title: pm.Title, Description: pm.Description}
		for _, a := range pm.Arguments {
			item.Arguments = append(item.Arguments, PromptArgument{
				Name: a.Name, Title: a.Title, Description: a.Description, Required: a.Required,
			})
		}
		out.Prompts = append(out.Prompts, item)
	}
	return out, nil
}

// GetPrompt 取用一条提示词模板（prompts/get）。
func (p *Plane) GetPrompt(ctx context.Context, id, name string, req PromptGetRequest) (*PromptGetResult, error) {
	cli, err := p.client()
	if err != nil {
		return nil, code.FromError(err)
	}
	server, err := p.LiveServer(id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, p.ProbeBudget())
	defer cancel()
	res, err := cli.GetPrompt(rctx, server, name, req.Arguments)
	if err != nil {
		return nil, p.liveErr(err, "mcp_prompt_get_failed")
	}
	out := &PromptGetResult{ServerID: id, Name: name, Description: res.Description,
		Messages: make([]PromptMessage, 0, len(res.Messages))}
	for _, m := range res.Messages {
		out.Messages = append(out.Messages, PromptMessage{Role: m.Role, Content: m.Content})
	}
	return out, nil
}

// liveErr 把 mcp 层错误映射为 REST 信封错误（稳定 code 进 msg，分类决定信封码）。
func (p *Plane) liveErr(err error, fallbackCode string) error {
	c := p.codeOf(err)
	if c == "" {
		c = fallbackCode
	}
	return code.FromError(failure.NewError(callClass(c), c, fmt.Sprintf("[%s] %s", c, err.Error()), nil))
}
