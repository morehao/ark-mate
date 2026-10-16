package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	mcpclient "github.com/morehao/ark-mate/pkg/mcp"
)

// ——— 连接器（MCP）实时面：P3d 过渡期的转发层 ———
//
// 实时面（工具调用、资源、提示词；以及"取条目 + 解析密钥成明文"的唯一入口 `liveServer`）
// 已落 `domain/mcp.Plane`（P2a-14）。这里只留一行转发，调用点（`ctrcapability` 与测试）不变。
//
// 为什么它和密钥面（`domain/mcp.Secrets`）分两个类型：密钥解析是**纯函数式**的
// （引用 → 明文，无状态），实时面要拿注册表里的条目、要按平面自己的预算发请求。合成一个
// 类型会让"只想解析一条引用"的调用方（网关、写面体检）也拿到客户端能力。

// TODO(P4b): 删转发（调用点直接依赖 mcp.Plane）
func (s *Service) CallMcpTool(ctx context.Context, id, tool string, req dtocapability.McpToolCallReq) (*dtocapability.McpToolCallResp, error) {
	return s.mcpPlane.CallTool(ctx, id, tool, req)
}

// TODO(P4b): 删转发（调用点直接依赖 mcp.Plane）
func (s *Service) ListMcpResources(ctx context.Context, id string) (*dtocapability.McpResourcesResp, error) {
	return s.mcpPlane.ListResources(ctx, id)
}

// TODO(P4b): 删转发（调用点直接依赖 mcp.Plane）
func (s *Service) ReadMcpResource(ctx context.Context, id, uri string) (*dtocapability.McpResourceReadResp, error) {
	return s.mcpPlane.ReadResource(ctx, id, uri)
}

// TODO(P4b): 删转发（调用点直接依赖 mcp.Plane）
func (s *Service) ListMcpPrompts(ctx context.Context, id string) (*dtocapability.McpPromptsResp, error) {
	return s.mcpPlane.ListPrompts(ctx, id)
}

// TODO(P4b): 删转发（调用点直接依赖 mcp.Plane）
func (s *Service) GetMcpPrompt(ctx context.Context, id, name string, req dtocapability.McpPromptGetReq) (*dtocapability.McpPromptGetResp, error) {
	return s.mcpPlane.GetPrompt(ctx, id, name, req)
}

// TODO(P4b): 删转发（**跨面集成测试**直接依赖 mcp.Plane.LiveServer）
//
// 为什么留这一条而不是让测试改指平面：`mcp_secret_test.go` 的
// `TestMcpLiveAndProbeCarryResolvedEnv` 钉的是一个**实测过的跨面缺陷**（2026-10-09）——"会话里
// 能用、探测必失败"只是因为探测/调用面不带 `Env`。它验的是**装配起来的那份 Service**（真注册表 +
// 真库 + 真凭据面），而"明文与解析后的密钥一起进客户端输入"这件事必须在**同一个**入口上被断言。
// 平面自己的单测（假注册表 + 假凭据面）无法替代它。
func (s *Service) mcpLiveServer(id string) (mcpclient.Server, error) {
	return s.mcpPlane.LiveServer(id)
}
