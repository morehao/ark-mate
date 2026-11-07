package assembly

// ——— `mate mcp platform` 的装配入口（CLI 维护面用它，避免 maintenance 直接 import 用例层）———
//
// 与 `AutomationMCPHandler`（mate mcp automation）/ `MaterialMCPHandler` 同款：maintenance 与
// platformmcp/capabilitymcp/automationmcp 都不允许 import `svc*`（层规：只有装配根与控制器可以），
// 所以在这里构造 svcautomation + svccapability，包成各域 Service 端口，再交给聚合 handler。
//
// 本子进程**没有会话宿主端口**：定时任务的 `delivery=session` 会话存在性校验在 hostOf()==nil 时
// 自动跳过；专家的定义 CRUD 完全不碰会话。真正的会话存在性与触发执行由 daemon 侧负责——子进程
// 只碰库，跨进程生效靠 daemon 调度循环每 tick 的 ReloadDefinitionsForTick。

import (
	"context"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/domain/expert"
	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/infra/automationmcp"
	"github.com/morehao/ark-mate/internal/infra/capabilitymcp"
	"github.com/morehao/ark-mate/internal/infra/platformmcp"
	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/service/svcautomation"
	"github.com/morehao/ark-mate/internal/service/svccapability"
)

// PlatformMCPHandler 构造 `mate mcp platform` 的工具面：聚合 automation + expert 两域。
//
// 返回的 cleanup 负责关库；调用方（`mate mcp platform`）在进程退出前调用它。
func PlatformMCPHandler(cfg config.Config) (mcp.Handler, func() error, error) {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}

	// —— 定时任务域（与 AutomationMCPHandler 同款）——
	autoPlane := automation.NewPlane()
	autoPlane.SetDefinitionFiles(registry.AutomationDefinitionFiles{})
	autoPlane.SetStore(st)
	autoPlane.LoadAutomationDefinitionsFromStore(context.Background(), cfg.AutomationDir)
	autoSvc := svcautomation.New(autoPlane)

	// —— 专家域 ——
	// expert.Plane.Write/Delete/SetEnabled 会调 p.caps.RegisterExpert/RemoveExpert，
	// 所以 caps 必须是真的 Registry（不能 nil，否则写面 panic）；List/Get 只碰 store。
	expertPlane := expert.NewPlane(capability.NewRegistry())
	expertPlane.SetStore(st) // *store.Store 满足 expert.Store（四个专家方法都在其上）
	// svccapability 的六个专家方法只解引用 experts；skills/mcp/tools/catalog 传 nil 不影响专家域。
	capSvc := svccapability.NewCapability(nil, nil, expertPlane, nil, nil)

	// —— 资料库域（library_*）——
	// 与 `mate mcp material` 共用同一套装配（buildMaterialHandler），只是并进同一个工具面。
	libraryHandler, err := buildMaterialHandler(cfg, st)
	if err != nil {
		_ = st.Close()
		return nil, nil, err
	}

	handler, err := platformmcp.New(map[string]mcp.Handler{
		"automation": automationmcp.New(autoSvc),
		"expert":     capabilitymcp.New(capSvc),
		"library":    libraryHandler,
	})
	if err != nil {
		_ = st.Close()
		return nil, nil, err
	}
	return handler, func() error { return st.Close() }, nil
}
