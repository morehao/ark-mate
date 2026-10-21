package capability

import (
	"github.com/morehao/ark-mate/internal/platform/capability"
	"github.com/morehao/ark-mate/internal/contract/dtocapability"
)

// ToolCatalog 工具目录（能力数据面的**读模型**）：平台 builtin / 观察兜底 / MCP live 的并集。
//
// 与 `CapabilitySvc` 分开按面声明（P4a 正文第十二刀）：工具目录只有"读"这一件事，
// 而能力数据面的其余 16 个方法还压在过渡端口上。不按面切，这一面就只能等整体完工。
type ToolCatalog interface {
	ListTools() ([]dtocapability.ToolResp, error)
}

// Tools 工具目录用例：只依赖**能力注册表**这一个事实（`internal/platform/capability`）。
//
// 为什么在这里而不是在控制器：`capability.Tool` → `dtocapability.ToolResp` 是**响应装配**，
// 而控制器只做参数绑定与回响应；`pkg/` 又不能 import `internal/contract`（分层方向），
// 所以投影只能发生在用例层——这正是 `svclibrary`（第六刀）定下的落点。
type Tools struct {
	caps *capability.Registry
}

// NewTools 构造工具目录用例（`caps` 是装配点交下来的依赖事实，不是服务实例）。
func NewTools(caps *capability.Registry) ToolCatalog { return &Tools{caps: caps} }

var _ ToolCatalog = (*Tools)(nil)

// ListTools 返回 Tool 目录（按 name 排序；顺序由注册表保证，这里不再排一次）。
func (t *Tools) ListTools() ([]dtocapability.ToolResp, error) {
	items := t.caps.ListTools()
	out := make([]dtocapability.ToolResp, 0, len(items))
	for _, x := range items {
		out = append(out, dtocapability.ToolResp{
			Name: x.Name, Description: x.Description, Source: x.Source,
			McpServer: x.McpServer, Schema: x.Schema,
		})
	}
	return out, nil
}
