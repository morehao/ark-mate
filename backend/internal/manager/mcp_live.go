package manager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/code"
	"github.com/morehao/ark-mate/pkg/mcp"
	"github.com/morehao/golib/glog"
)

// MCP 工具调用预算（B1）：默认 30s（工具可能慢），上限 5min（防请求挂死）。
const (
	defaultMcpCallTimeout = 30 * time.Second
	maxMcpCallTimeout     = 5 * time.Minute
)

// CallMcpTool 调用某 MCP server 的工具（B1：tools/call）。
//
// 语义要点：
//   - 每次调用是独立会话（建连 → initialize → initialized → tools/call → 关闭）：不做连接池，
//     MCP 会话是有状态资源，池化的失效/并发语义需单独设计（不臆造）。
//   - 不要求先探测：server 才是工具存在性的权威（未探测也能调用；调用失败按 server 的
//     JSON-RPC 错误返回）。
//   - **工具级失败**（服务端 result.isError=true）不是平台错误：原样透传 is_error + content，
//     信封 code=0（调用方据 is_error 判断）；只有传输/协议/超时才映射为信封错误码。
func (s *Service) CallMcpTool(ctx context.Context, id, tool string, req dtocapability.McpToolCallReq) (*dtocapability.McpToolCallResp, error) {
	server, err := s.mcpLiveServer(id)
	if err != nil {
		return nil, err
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
	res, err := mcp.CallTool(cctx, server, tool, req.Arguments)
	if err != nil {
		// 稳定 code 进信封 msg（同探测）：信封 code 只有分类粒度，排障需要精确原因。
		return nil, code.FromError(acl.NewError(mcpCallClass(err), mcp.CodeOf(err),
			fmt.Sprintf("[%s] %s", mcp.CodeOf(err), err.Error()), nil))
	}
	glog.Infow(ctx, "mcp 工具调用完成", "server", id, "tool", tool,
		"is_error", res.IsError, "blocks", len(res.Content), "ms", time.Since(start).Milliseconds())
	return &dtocapability.McpToolCallResp{
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

// mcpLiveServer 取条目并解析成 mcp 包输入：**密钥型绑定（`env:`/`vault:`）在这里解析成明文**，
// 与注入面（McpServersFor）共用同一个 resolveMcpSecrets——两处各写一遍的后果就是今天这个缺陷：
// 注入面带 env、探测/调用面不带，靠 env 拿凭据的 stdio server 于是"会话里能用、探测必失败"
// （2026-10-09 实测）。
//
// 解析失败 → **当场报错**，不拿空凭证去连（同注入面的纪律）：半配置的 server 会让用户看到一个
// 远处的 401，而"哪条引用解析不了"是此刻就能说清的事实，且错误里只有键名与引用、没有值。
func (s *Service) mcpLiveServer(id string) (mcp.Server, error) {
	server, ok := s.caps.GetMcpServer(id)
	if !ok {
		return mcp.Server{}, code.FromError(acl.NewError(acl.ClassRequest, "mcp_not_found",
			fmt.Sprintf("未知 MCP server %q", id), nil))
	}
	env, headers, problems := s.resolveMcpSecrets(server)
	if len(problems) > 0 {
		return mcp.Server{}, code.FromError(acl.NewError(acl.ClassRequest, "mcp_secret_unresolved",
			fmt.Sprintf("[mcp_secret_unresolved] 密钥引用解析失败（%s）", strings.Join(problems, "；")), nil))
	}
	return mcp.Server{
		ID: server.ID, Transport: server.Transport, Command: server.Command,
		Args: server.Args, URL: server.URL, Headers: headers, Env: env,
	}, nil
}

// ListMcpResources 拉取某 MCP server 的资源列表（resources/list + resources/templates/list）。
func (s *Service) ListMcpResources(ctx context.Context, id string) (*dtocapability.McpResourcesResp, error) {
	server, err := s.mcpLiveServer(id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, s.mcpProbeBudget())
	defer cancel()
	res, err := mcp.ListResources(rctx, server)
	if err != nil {
		return nil, s.mcpLiveErr(err, "mcp_resources_list_failed")
	}
	out := &dtocapability.McpResourcesResp{
		ServerID: id, ServerName: res.ServerName, ServerVersion: res.ServerVersion,
		TemplatesError: res.TemplatesError,
		Resources:      make([]dtocapability.McpResourceResp, 0, len(res.Resources)),
	}
	for _, r := range res.Resources {
		out.Resources = append(out.Resources, dtocapability.McpResourceResp{
			URI: r.URI, Name: r.Name, Title: r.Title, Description: r.Description,
			MimeType: r.MimeType, Size: r.Size,
		})
	}
	for _, t := range res.Templates {
		out.Templates = append(out.Templates, dtocapability.McpResourceTemplateResp{
			URITemplate: t.URITemplate, Name: t.Name, Title: t.Title,
			Description: t.Description, MimeType: t.MimeType,
		})
	}
	return out, nil
}

// ReadMcpResource 读取一个资源（resources/read）。
func (s *Service) ReadMcpResource(ctx context.Context, id, uri string) (*dtocapability.McpResourceReadResp, error) {
	server, err := s.mcpLiveServer(id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, s.mcpProbeBudget())
	defer cancel()
	res, err := mcp.ReadResource(rctx, server, uri)
	if err != nil {
		return nil, s.mcpLiveErr(err, "mcp_resource_read_failed")
	}
	out := &dtocapability.McpResourceReadResp{ServerID: id, URI: uri, Contents: make([]dtocapability.McpResourceContent, 0, len(res.Contents))}
	for _, c := range res.Contents {
		out.Contents = append(out.Contents, dtocapability.McpResourceContent{URI: c.URI, MimeType: c.MimeType, Text: c.Text, Blob: c.Blob})
	}
	return out, nil
}

// ListMcpPrompts 拉取某 MCP server 的提示词模板（prompts/list）。
func (s *Service) ListMcpPrompts(ctx context.Context, id string) (*dtocapability.McpPromptsResp, error) {
	server, err := s.mcpLiveServer(id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, s.mcpProbeBudget())
	defer cancel()
	prompts, err := mcp.ListPrompts(rctx, server)
	if err != nil {
		return nil, s.mcpLiveErr(err, "mcp_prompts_list_failed")
	}
	out := &dtocapability.McpPromptsResp{ServerID: id, Prompts: make([]dtocapability.McpPromptResp, 0, len(prompts))}
	for _, p := range prompts {
		item := dtocapability.McpPromptResp{Name: p.Name, Title: p.Title, Description: p.Description}
		for _, a := range p.Arguments {
			item.Arguments = append(item.Arguments, dtocapability.McpPromptArgumentResp{
				Name: a.Name, Title: a.Title, Description: a.Description, Required: a.Required,
			})
		}
		out.Prompts = append(out.Prompts, item)
	}
	return out, nil
}

// GetMcpPrompt 取用一条提示词模板（prompts/get）。
func (s *Service) GetMcpPrompt(ctx context.Context, id, name string, req dtocapability.McpPromptGetReq) (*dtocapability.McpPromptGetResp, error) {
	server, err := s.mcpLiveServer(id)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, s.mcpProbeBudget())
	defer cancel()
	res, err := mcp.GetPrompt(rctx, server, name, req.Arguments)
	if err != nil {
		return nil, s.mcpLiveErr(err, "mcp_prompt_get_failed")
	}
	out := &dtocapability.McpPromptGetResp{ServerID: id, Name: name, Description: res.Description,
		Messages: make([]dtocapability.McpPromptMessage, 0, len(res.Messages))}
	for _, m := range res.Messages {
		out.Messages = append(out.Messages, dtocapability.McpPromptMessage{Role: m.Role, Content: m.Content})
	}
	return out, nil
}

// mcpLiveErr 把 mcp 层错误映射为 REST 信封错误（稳定 code 进 msg，分类决定信封码）。
func (s *Service) mcpLiveErr(err error, fallbackCode string) error {
	c := mcp.CodeOf(err)
	if c == "" {
		c = fallbackCode
	}
	return code.FromError(acl.NewError(mcpCallClass(err), c, fmt.Sprintf("[%s] %s", c, err.Error()), nil))
}
