package router_test

import (
	"context"
	"testing"

	"github.com/morehao/ark-mate/internal/domain/automation"
	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/infra/dsh"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/bootstrap/assembly"
	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/platform/credential"
	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/delivery/middleware"
	"github.com/morehao/ark-mate/internal/delivery/router"
	"github.com/morehao/ark-mate/internal/adapters/outbound/runtimeconn"
	"github.com/morehao/ark-mate/internal/contract/dtosystem"
	goruntime "github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/app/system"
	apprun "github.com/morehao/ark-mate/internal/app/run"
	"github.com/morehao/ark-mate/internal/version"
)

// testRouterOpts 路由级测试的最小装配选项（其余事实用真实域平面 + 空会话）。
type testRouterOpts struct {
	web        router.AssetInfo
	assetWatch *router.AssetWatcher
	origin     *middleware.OriginPolicy
	creds      *credential.Set
	// metaSource 覆盖诊断面的值来源；nil 时用"资产已接上"的默认值。
	metaSource func() system.MetaSource
	// 复用一个已装配好的会话面（调用方要先设策略/凭据时传它）。
	service  *apprun.Service
	registry *goruntime.Registry
	bus      *eventbus.Bus
}

// testRouterDeps 用**真实装配**造一份 router.Deps（控制器来自 `assembly` 的同一条构造）。
//
// 为什么不手搭控制器：路由测试要对"路径/中间件/信封"负责，控制器集合与生产漂移会让
// "路由对、装配错"这类问题在测试里看不见。这里只替换需要断言的那几份事实（资产/来源/meta）。
func testRouterDeps(t *testing.T, o testRouterOpts) router.Deps {
	t.Helper()
	gin.SetMode(gin.TestMode)
	reg := o.registry
	if reg == nil {
		reg = dsh.Registry()
	}
	if o.registry == nil {
		reg.ApplyIsolationEnv(t.TempDir())
	}
	bus := o.bus
	if bus == nil {
		bus = eventbus.New()
	}
	svc := o.service
	if svc == nil {
		svc = newSessionService(reg, bus)
	}

	exec := runtimeconn.NewPlaneFor(reg)
	meta := o.metaSource
	if meta == nil {
		assets := o.web
		meta = func() system.MetaSource {
			// 与生产装配同形：版本/契约/产品面由装配根填，资产按已解析的那份填，
			// 底座身份取执行面此刻的事实。
			src := system.MetaSource{
				CoreVersion: version.Version,
				APIContract: version.APIContract,
				Payloads:    []dtosystem.PayloadInfo{},
				Product:     system.ProductInfoOf(version.Version),
			}
			if assets.Has() {
				src.Assets = system.MetaAssets{Path: assets.Path, Digest: assets.Digest, BuildID: assets.CurrentBuildID()}
			}
			if ri := exec.Identity(context.Background()); ri.ID != "" {
				src.Runtime = dtosystem.RuntimeInfo{ID: ri.ID, Name: ri.Name, Version: ri.Version}
			}
			return src
		}
	}
	services := assembly.Facts{
		Cfg:        config.Default(),
		Planes:     planesOf(svc),
		Session:    svc,
		Bus:        bus,
		Runtime:    reg,
		Execution:  exec,
		MetaSource: meta,
	}.BuildServices()
	return router.Deps{
		Controllers: services.BuildControllers(o.creds, o.origin, nil),
		Credentials: o.creds,
		Web:         o.web,
		AssetWatch:  o.assetWatch,
		Origin:      o.origin,
	}
}

// newAutomationPlaneForTest 造一个**已接线**的自动化平面（文件载体登记好）。
//
// 领域平面本身不带默认实现（nil = 未接线，用文件载体时当场空指针），所以测试要么走
// `registry.NewPlanes`，要么在这里把端口接上——不接的话测的是"配置错误"，不是行为。
func newAutomationPlaneForTest() *automation.Plane {
	p := automation.NewPlane()
	p.SetDefinitionFiles(registry.AutomationDefinitionFiles{})
	return p
}
