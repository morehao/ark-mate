package router_test

import (
	"github.com/morehao/ark-mate/internal/infra/dsh"
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/assembly"
	"github.com/morehao/ark-mate/internal/credential"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/dto/dtosystem"
	"github.com/morehao/ark-mate/internal/middleware"
	"github.com/morehao/ark-mate/internal/router"
	"github.com/morehao/ark-mate/internal/runtimeconn"
	"github.com/morehao/ark-mate/internal/service/svcsystem"
	"github.com/morehao/ark-mate/internal/sessionruntime"
	"github.com/morehao/ark-mate/internal/version"
	"github.com/morehao/ark-mate/internal/config"
	goruntime "github.com/morehao/ark-mate/internal/runtime"
)

// testRouterOpts 路由级测试的最小装配选项（其余事实用真实域平面 + 空会话）。
type testRouterOpts struct {
	web        router.AssetInfo
	assetWatch *router.AssetWatcher
	origin     *middleware.OriginPolicy
	creds      *credential.Set
	// metaSource 覆盖诊断面的值来源；nil 时用"资产已接上"的默认值。
	metaSource func() svcsystem.MetaSource
	// 复用一个已装配好的会话面（调用方要先设策略/凭据时传它）。
	service *sessionruntime.Service
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
		svc = sessionruntime.NewService(reg, bus)
	}

	exec := runtimeconn.NewPlaneFor(reg)
	meta := o.metaSource
	if meta == nil {
		assets := o.web
		meta = func() svcsystem.MetaSource {
			// 与生产装配同形：版本/契约/产品面由装配根填，资产按已解析的那份填，
			// 底座身份取执行面此刻的事实。
			src := svcsystem.MetaSource{
				CoreVersion: version.Version,
				APIContract: version.APIContract,
				Payloads:    []dtosystem.PayloadInfo{},
				Product:     svcsystem.ProductInfoOf(version.Version),
			}
			if assets.Has() {
				src.Assets = svcsystem.MetaAssets{Path: assets.Path, Digest: assets.Digest, BuildID: assets.CurrentBuildID()}
			}
			if ri := exec.Identity(context.Background()); ri.ID != "" {
				src.Runtime = dtosystem.RuntimeInfo{ID: ri.ID, Name: ri.Name, Version: ri.Version}
			}
			return src
		}
	}
	services := assembly.Facts{
		Cfg:        config.Default(),
		Planes:     svc.Planes(),
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
