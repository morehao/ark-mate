package svccapability

import (
	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/object/objtime"

	"github.com/morehao/ark-mate/internal/domain/mcp"
	"github.com/morehao/ark-mate/internal/dto/dtocapability"
)

// McpCatalog MCP 注册表读面（能力数据面的**读模型**）。
//
// 与 `ToolCatalog` 同为按面声明的接口：MCP server 目录只有"读"这一件事，
// 而能力数据面的其余方法还压在过渡端口上——不按面切，这一面只能等整体完工。
type McpCatalog interface {
	ListMcpServers() ([]dtocapability.McpServerResp, error)
}

// Servers MCP 注册表读模型：三个依赖全是**事实**——
// 能力注册表（条目本体）、MCP 平面（探测态与工具计数）、密钥面（悬空引用体检）。
//
// 为什么在用例层而不是"调用方直接用平面"：`capability.McpServer` → `dtocapability.McpServerResp`
// 是**响应装配**，且它的三个输入来自三个不同的源（注册表 / 平面 / 密钥面），
// `pkg/` 又不能 import `internal/dto`——与 `Tools`（第十二刀）同一条分界线。
type Servers struct {
	caps    *capability.Registry
	plane   *mcp.Plane
	secrets *mcp.Secrets
}

// NewServers 构造 MCP 注册表读模型（依赖事实由装配点交下来，用例实例在这里造）。
func NewServers(caps *capability.Registry, plane *mcp.Plane, secrets *mcp.Secrets) McpCatalog {
	return &Servers{caps: caps, plane: plane, secrets: secrets}
}

var _ McpCatalog = (*Servers)(nil)

// ListMcpServers 返回 MCP 注册表（按 ID 排序）。headers/凭证只存内部供 P2 注入，不读回。
// 附 live 探测状态（tool_count / last_probe_at / last_probe_error，P2 §5.2）。
func (s *Servers) ListMcpServers() ([]dtocapability.McpServerResp, error) {
	items := s.caps.ListMcpServers()
	counts := s.plane.ToolCounts()
	probes := s.plane.ProbeSnapshot()
	out := make([]dtocapability.McpServerResp, 0, len(items))
	for _, m := range items {
		resp := dtocapability.McpServerResp{
			ID: m.ID, Name: m.Name, Transport: m.Transport,
			Command: m.Command, Args: m.Args, URL: m.URL,
			Scope: m.EffectiveScope(), Enabled: m.IsEnabled(),
			EnvKeys:              capability.KeysOf(m.Env),
			SecretEnvKeys:        capability.KeysOf(m.SecretEnv),
			SecretHeaderKeys:     capability.KeysOf(m.SecretHeaders),
			UnresolvedSecretKeys: s.secrets.UnresolvedKeys(m),
			ToolCount:            counts[m.ID],
		}
		if st, ok := probes[m.ID]; ok {
			if !st.At.IsZero() {
				resp.LastProbeAt = objtime.Milli(st.At)
			}
			resp.LastProbeErr = st.Err
		}
		out = append(out, resp)
	}
	return out, nil
}
