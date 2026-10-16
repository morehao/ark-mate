package manager

import (
	"context"
	"time"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
)

// ——— 连接器（MCP）探测面：P3d 过渡期的转发层 ———
//
// 探测主体（预算、状态记账、tools/list 权威快照替换 Tool 目录、启动期自动探测）已落
// `domain/mcp.Plane`（P2a-14）。这里只留一行转发，调用点（`ctrcapability` / `app` / 测试）不变。
//
// **状态也一起搬走了**：`Service.mcpMu` / `mcpProbes` / `mcpProbeTimeout` 三个字段已删，
// 改由平面自持一把只保护这三个字段的锁（与 `skill.Plane` / `domain/interaction` 同形）——
// 它们此前就与 `Service.mu` 无关，搬走顺带消掉一处"同一结构体上两把无关的锁"。

// TODO(P4b): 删转发（调用点直接依赖 mcp.Plane）
func (s *Service) SetMcpProbeTimeout(d time.Duration) { s.mcpPlane.SetProbeTimeout(d) }

// TODO(P4b): 删转发（调用点直接依赖 mcp.Plane）
func (s *Service) ProbeMcpServer(ctx context.Context, id string) (*dtocapability.McpProbeResp, error) {
	return s.mcpPlane.Probe(ctx, id)
}

// TODO(P4b): 删转发（调用点直接依赖 mcp.Plane）
func (s *Service) AutoProbeMcpServers(ctx context.Context) error { return s.mcpPlane.AutoProbe(ctx) }
