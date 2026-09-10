// Package svccapability 定义能力数据面的服务端口：
// Skill / MCP server / Tool 目录，以及 MCP 的 live 探测、资源与提示词。
package svccapability

import (
	"context"

	"github.com/morehao/ark-forge/internal/dto/dtocapability"
)

// CapabilitySvc 能力数据面的服务端口。
//
// 实现方（internal/manager）以编译期断言 var _ CapabilitySvc = (*T)(nil) 保证一致性。
type CapabilitySvc interface {
	// ListSkills 返回 Skill 目录（P1 能力数据面）。
	ListSkills() ([]dtocapability.SkillResp, error)

	// ListMcpServers 返回 MCP 注册表（P1；headers/凭证不读回）。
	ListMcpServers() ([]dtocapability.McpServerResp, error)

	// ListTools 返回 Tool 目录（P1；静态/平台/观察条目）。
	ListTools() ([]dtocapability.ToolResp, error)

	// AgentCapabilities 返回某 Agent 的统一能力目录（P2 §8.1：按候选 runtime 聚合 skill/MCP/tool）。
	AgentCapabilities(agentID string) (*dtocapability.AgentCapabilitiesResp, error)

	// ProbeMcpServer 对某 MCP server 做一次 live tools/list 探测并写 Tool 目录（P2 §5.2）。
	ProbeMcpServer(ctx context.Context, id string) (*dtocapability.McpProbeResp, error)

	// CallMcpTool 调用某 MCP server 的工具（B1：tools/call；独立会话，不做连接池）。
	CallMcpTool(ctx context.Context, id, tool string, req dtocapability.McpToolCallReq) (*dtocapability.McpToolCallResp, error)

	// B2：MCP 资源与提示词（live 拉取，不落盘/不目录化）。
	ListMcpResources(ctx context.Context, id string) (*dtocapability.McpResourcesResp, error)

	ReadMcpResource(ctx context.Context, id, uri string) (*dtocapability.McpResourceReadResp, error)

	ListMcpPrompts(ctx context.Context, id string) (*dtocapability.McpPromptsResp, error)

	GetMcpPrompt(ctx context.Context, id, name string, req dtocapability.McpPromptGetReq) (*dtocapability.McpPromptGetResp, error)
}
