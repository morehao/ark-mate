package svccapability

import (
	"encoding/json"

	dommcp "github.com/morehao/ark-mate/internal/domain/mcp"
	"github.com/morehao/ark-mate/internal/contract/dtocapability"
)

// ——— MCP 域视图 → 对外 DTO 的投影（v3.2 刀 5）———
//
// 同 `skillview.go`：逐字段搬运、不重命名。没有 `omitempty` 的切片一律 `make(...,0,len)`
// ——空集合必须序列化成 `[]` 而不是 `null`，那是对外契约（契约基线钉着）。

func mcpTools(vs []dommcp.ToolEntry) []dtocapability.ToolResp {
	out := make([]dtocapability.ToolResp, 0, len(vs))
	for _, v := range vs {
		out = append(out, dtocapability.ToolResp{
			Name:        v.Name,
			Description: v.Description,
			Source:      v.Source,
			McpServer:   v.McpServer,
			Schema:      v.Schema,
		})
	}
	return out
}

func mcpWriteResp(v *dommcp.WriteResult) *dtocapability.McpWriteResp {
	if v == nil {
		return nil
	}
	return &dtocapability.McpWriteResp{
		ID:                   v.ID,
		Path:                 v.Path,
		Created:              v.Created,
		UnresolvedSecretKeys: append([]string(nil), v.UnresolvedSecretKeys...),
		NextAction:           v.NextAction,
	}
}

func mcpDeleteResp(v *dommcp.DeleteResult) *dtocapability.McpDeleteResp {
	if v == nil {
		return nil
	}
	return &dtocapability.McpDeleteResp{
		ID:           v.ID,
		Deleted:      v.Deleted,
		Path:         v.Path,
		ToolsRemoved: v.ToolsRemoved,
		NextAction:   v.NextAction,
	}
}

func mcpProbeResp(v *dommcp.ProbeResult) *dtocapability.McpProbeResp {
	if v == nil {
		return nil
	}
	return &dtocapability.McpProbeResp{
		ID:            v.ID,
		Transport:     v.Transport,
		ServerName:    v.ServerName,
		ServerVersion: v.ServerVersion,
		ProbedAt:      v.ProbedAt,
		Added:         v.Added,
		Removed:       v.Removed,
		Skipped:       v.Skipped,
		ToolCount:     v.ToolCount,
		Tools:         mcpTools(v.Tools),
	}
}

func mcpToolCallResp(v *dommcp.ToolCallResult) *dtocapability.McpToolCallResp {
	if v == nil {
		return nil
	}
	content := make([]json.RawMessage, 0, len(v.Content))
	content = append(content, v.Content...)
	return &dtocapability.McpToolCallResp{
		ServerID:          v.ServerID,
		Tool:              v.Tool,
		IsError:           v.IsError,
		Content:           content,
		Text:              v.Text,
		StructuredContent: v.StructuredContent,
		ServerName:        v.ServerName,
		ServerVersion:     v.ServerVersion,
		DurationMS:        v.DurationMS,
	}
}

func mcpResourcesResp(v *dommcp.ResourcesResult) *dtocapability.McpResourcesResp {
	if v == nil {
		return nil
	}
	resources := make([]dtocapability.McpResourceResp, 0, len(v.Resources))
	for _, r := range v.Resources {
		resources = append(resources, dtocapability.McpResourceResp{
			URI:         r.URI,
			Name:        r.Name,
			Title:       r.Title,
			Description: r.Description,
			MimeType:    r.MimeType,
			Size:        r.Size,
		})
	}
	var templates []dtocapability.McpResourceTemplateResp
	if len(v.Templates) > 0 {
		templates = make([]dtocapability.McpResourceTemplateResp, 0, len(v.Templates))
		for _, t := range v.Templates {
			templates = append(templates, dtocapability.McpResourceTemplateResp{
				URITemplate: t.URITemplate,
				Name:        t.Name,
				Title:       t.Title,
				Description: t.Description,
				MimeType:    t.MimeType,
			})
		}
	}
	return &dtocapability.McpResourcesResp{
		ServerID:       v.ServerID,
		Resources:      resources,
		Templates:      templates,
		TemplatesError: v.TemplatesError,
		ServerName:     v.ServerName,
		ServerVersion:  v.ServerVersion,
	}
}

func mcpResourceReadResp(v *dommcp.ResourceReadResult) *dtocapability.McpResourceReadResp {
	if v == nil {
		return nil
	}
	contents := make([]dtocapability.McpResourceContent, 0, len(v.Contents))
	for _, c := range v.Contents {
		contents = append(contents, dtocapability.McpResourceContent{
			URI:      c.URI,
			MimeType: c.MimeType,
			Text:     c.Text,
			Blob:     c.Blob,
		})
	}
	return &dtocapability.McpResourceReadResp{ServerID: v.ServerID, URI: v.URI, Contents: contents}
}

func mcpPromptsResp(v *dommcp.PromptsResult) *dtocapability.McpPromptsResp {
	if v == nil {
		return nil
	}
	prompts := make([]dtocapability.McpPromptResp, 0, len(v.Prompts))
	for _, p := range v.Prompts {
		var args []dtocapability.McpPromptArgumentResp
		if len(p.Arguments) > 0 {
			args = make([]dtocapability.McpPromptArgumentResp, 0, len(p.Arguments))
			for _, a := range p.Arguments {
				args = append(args, dtocapability.McpPromptArgumentResp{
					Name:        a.Name,
					Title:       a.Title,
					Description: a.Description,
					Required:    a.Required,
				})
			}
		}
		prompts = append(prompts, dtocapability.McpPromptResp{
			Name:        p.Name,
			Title:       p.Title,
			Description: p.Description,
			Arguments:   args,
		})
	}
	return &dtocapability.McpPromptsResp{ServerID: v.ServerID, Prompts: prompts, ServerName: v.ServerName}
}

func mcpPromptGetResp(v *dommcp.PromptGetResult) *dtocapability.McpPromptGetResp {
	if v == nil {
		return nil
	}
	messages := make([]dtocapability.McpPromptMessage, 0, len(v.Messages))
	for _, m := range v.Messages {
		messages = append(messages, dtocapability.McpPromptMessage{Role: m.Role, Content: m.Content})
	}
	return &dtocapability.McpPromptGetResp{
		ServerID:    v.ServerID,
		Name:        v.Name,
		Description: v.Description,
		Messages:    messages,
	}
}
