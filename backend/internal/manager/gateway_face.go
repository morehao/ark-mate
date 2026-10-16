package manager

import (
	"github.com/morehao/ark-mate/pkg/capability"
)

// ——— 平台侧 MCP 网关复用的窄出口：P3d 过渡期的转发层 ———
//
// 选址（哪些远程 server 进网关）与密钥明文解析都已在 `domain/mcp.Plane`（P2a-15）；
// 这里保留**同名公开方法**，因为唯一消费方是 `internal/gateway/source.go` 的装配面
// （它拿到的是 `*Service`，P5a 之后才会改成直接依赖平面）。

// TODO(P4b): 删转发（网关装配直接依赖 mcp.Plane）
//
// GatewayMcpFace 返回网关应当暴露的**远程** server 定义（未解析密钥，顺序稳定）。
// 与注入面的两处有意差异（不按 transport 过滤、远程限定）见 `domain/mcp.Plane.GatewayFace`。
func (s *Service) GatewayMcpFace(runtimeID string, mcpsPinned *[]string) ([]capability.McpServer, error) {
	return s.mcpPlane.GatewayFace(runtimeID, mcpsPinned)
}

// TODO(P4b): 删转发（网关装配直接依赖 mcp.Plane）
//
// ResolveMcpSecrets 把定义里的密钥引用解析成最终注入值（网关侧调用点）。
func (s *Service) ResolveMcpSecrets(m capability.McpServer) (env, headers map[string]string, problems []string) {
	return s.mcpPlane.ResolveSecrets(m)
}
