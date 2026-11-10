package assembly

// ——— WP4：app 管理面 MCP 工具域（进 `mate mcp platform` 的 app 域）———
//
// `infra/appmcp` 只认自声明窄端口 `appmcp.Service`（不 import 用例层），由装配根在这里把
// svcapp 适配成该端口，再交给 `platformmcp` 聚合器（domains map 的 `"app"` 键，见 platform_mcp.go）。
//
// `svcapp.AppSvc` 的六个方法（ListApps/GetApp/WriteApp/DeleteApp/SetAppEnabled，全用 `dtoapp` 值）
// 与 `appmcp.Service` **签名逐字相同**，故无需额外适配类型——直接把 `svcapp.NewApp(st)` 的返回值
// 交给 `appmcp.New` 即可。`*store.Store` 结构上满足 `svcapp.Store`（持久化端口）。
//
// 边界：本子进程只碰库（无会话宿主端口）；app 定义 CRUD 完全不碰会话。

import (
	"github.com/morehao/ark-mate/internal/infra/appmcp"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/service/svcapp"
)

// newAppServiceHandler 构造 app 管理面的 MCP Handler（`appmcp.Service` 由 svcapp 实现）。
//
// st 由调用方（PlatformMCPHandler）打开并负责生命周期；本函数只借用它构造用例层。
func newAppServiceHandler(st *store.Store) *appmcp.Handler {
	return appmcp.New(svcapp.NewApp(st))
}
