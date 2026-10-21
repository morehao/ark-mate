package router_test

import (
	"sync"

	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/runtime"
	apprun "github.com/morehao/ark-mate/internal/app/run"
)

// ——— 路由级测试的 Service 建造点（阶段 0.2）———
//
// 生产代码不再有 `planesOf(Service)`：执行面只认 `PlaneFacts`。路由测试要用具体平面做
// 前置（工作空间策略、供应商目录、凭据分层…），所以在**测试包**里留一份旁路表。
// 它与 `sessionruntime` 包内夹具的 `planesOf` 同形，只是各自服务于自己的测试包。

var routerTestPlanes sync.Map // map[*apprun.Service]*registry.Planes

// newSessionService 造一个带真实领域平面的执行面（路由测试统一入口）。
func newSessionService(reg *runtime.Registry, bus *eventbus.Bus) *apprun.Service {
	planes := registry.NewPlanes(reg)
	svc := apprun.NewService(reg, bus, apprun.WithPlaneFacts(planeFactsOf(planes)))
	routerTestPlanes.Store(svc, planes)
	return svc
}

// planesOf 取该 Service 的具体平面（未登记 → 明确失败，而不是空指针）。
func planesOf(svc *apprun.Service) *registry.Planes {
	v, ok := routerTestPlanes.Load(svc)
	if !ok {
		panic("planesOf: 该 Service 不是 newSessionService 造的（没有具体平面记录）")
	}
	return v.(*registry.Planes)
}

// planeFactsOf 把具体平面折成按面端口（与 assembly.planeFacts 同形）。
func planeFactsOf(p *registry.Planes) apprun.PlaneFacts {
	return apprun.PlaneFacts{
		Providers:    p.Providers,
		Capabilities: p.Capabilities,
		Skills:       p.Skills,
		McpSecrets:   p.McpSecrets,
		Workspaces:   p.Workspaces,
		Credentials:  p.Credentials,
		Automation:   p.Automation,
		Identity:     p.Identity,
		Gateway:      p,
	}
}
