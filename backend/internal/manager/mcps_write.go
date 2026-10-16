package manager

import (
	"context"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
)

// ——— MCP server 定义写面：P3d 过渡期的转发层 ———
//
// 写面主体（闸门顺序、载体 CAS、内存入表、未解析密钥体检）已落 `domain/mcp.Plane`
// （P2a-15）。这里只留一行转发，调用点（`ctrcapability` 与测试）不变。
//
// 归属说明：MCP 定义"怎么生效"（它有哪些门、密钥怎么算没配、删掉时工具条目要不要一起摘）
// 是**连接器平面自己的规则**，与 skill 写面同形但不共享闸门——两个 `Validate*` 的门本就不同，
// 合成一处只会让"有凭据"这条不变量对技能也成立。

// TODO(P4b): 删转发（调用点直接依赖 mcp.Plane）
//
// CreateMcpServer 新建一个 MCP server 定义（POST /mcps）：id 必填，已存在 → `mcp_exists`(40901)。
func (s *Service) CreateMcpServer(ctx context.Context, req dtocapability.McpWriteReq) (*dtocapability.McpWriteResp, error) {
	return s.mcpPlane.WriteServer(ctx, "", req, true)
}

// TODO(P4b): 删转发（调用点直接依赖 mcp.Plane）
//
// UpdateMcpServer 覆盖写一个 MCP server 定义（PUT /mcps/:id）。键的出现与否有语义，同 skill 写面。
func (s *Service) UpdateMcpServer(ctx context.Context, id string, req dtocapability.McpWriteReq) (*dtocapability.McpWriteResp, error) {
	return s.mcpPlane.WriteServer(ctx, id, req, false)
}

// TODO(P4b): 删转发（调用点直接依赖 mcp.Plane）
//
// DeleteMcpServer 删除一个 MCP server 定义（DELETE /mcps/:id）：删定义 + 摘内存条目
// + **一并摘除该 server 归属的工具**。幂等：不存在 → `deleted:false` 且无错。
func (s *Service) DeleteMcpServer(ctx context.Context, id string) (*dtocapability.McpDeleteResp, error) {
	return s.mcpPlane.DeleteServer(ctx, id)
}
