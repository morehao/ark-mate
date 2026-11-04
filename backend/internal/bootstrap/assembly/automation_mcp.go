package assembly

// ——— `mate mcp automation` 的装配入口（CLI 维护面用它，避免 maintenance 直接 import 用例层）———
//
// 与 `mate mcp material` 同款：maintenance 与 automationmcp 都不允许 import `svc*`（层规：只有
// 装配根与控制器可以），所以在这里（装配根）构造 svcautomation，并把它包成 automationmcp.Service
// 交给 Handler。CLI 只拿一个 `mcp.Handler` + 关闭函数。
//
// **这个子进程没有会话宿主端口**：`delivery=session` 的写面"会话是否存在"校验在 hostOf()==nil
// 时自动跳过（见 domain/automation 的 WriteAutomation）——子进程只做定义 CRUD，真正的会话存在性
// 与触发执行由 daemon 侧（有会话平面与 scheduler）负责。这是与 material 子进程一致的取舍：
// 子进程只碰库，跨进程生效靠 daemon 调度循环每 tick 的 ReloadDefinitionsForTick。

import (
	"context"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/infra/automationmcp"
	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/service/svcautomation"
)

// AutomationMCPHandler 以给定配置构造定时任务 MCP 工具面的 Handler（含它依赖的库/定义/用例）。
//
// 返回的 cleanup 负责关库；调用方（`mate mcp automation`）在进程退出前调用它。
func AutomationMCPHandler(cfg config.Config) (mcp.Handler, func() error, error) {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	plane := automation.NewPlane()
	plane.SetDefinitionFiles(registry.AutomationDefinitionFiles{})
	plane.SetStore(st) // *store.Store 结构上满足 automation.AutomationStore
	plane.LoadAutomationDefinitionsFromStore(context.Background(), cfg.AutomationDir)
	svc := svcautomation.New(plane)
	return automationmcp.New(svc), func() error { return st.Close() }, nil
}
