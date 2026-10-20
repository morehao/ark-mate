package assembly

import (
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/drivers/registry"
	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/sessionruntime"
)

// ——— 接库端口的适配体（阶段 0.1）———
//
// `sessionruntime.EnableStore` 只收端口，具体句柄留在装配层。本文件是**唯一**把
// `*store.Store` / `*registry.Planes` 折成那两个端口的地方，因此也是 typed-nil 判定的唯一落点：
// `var st *store.Store = nil` 装进接口**不是 nil**，会话域里那句 `if pl.st == nil` 会失效，
// memory 模式会变成一次对 nil 的方法调用。
//
// 两个适配体都遵守同一条约定：**未接库就返回 nil**（而不是返回一个包着 nil 的接口）。

// sessionRuntimeStore 把装配根持有的库句柄折成会话域端口。
func (as *assembly) sessionRuntimeStore() session.Store {
	if as.st == nil {
		return nil
	}
	return as.st
}

// planeStore 领域平面的接库动作：把**同一份**库句柄接到全部领域平面。
//
// 它是函数型端口（`sessionruntime.PlaneStore`）的实现，句柄与平面都取自装配根——
// 执行面因此不需要认识 `*registry.Planes`。
type planeStore struct {
	planes *registry.Planes
	st     *store.Store
}

// EnableStore 实现 `sessionruntime.PlaneStore`。
func (p planeStore) EnableStore() {
	if p.planes == nil || p.st == nil {
		return
	}
	p.planes.EnableStore(p.st)
}

// PlaneFactsOf 把具体平面集合折成执行面的**按面端口**（阶段 0.2）。
//
// 这里是"具体平面 → 端口"的唯一折点：执行面因此不 import `drivers/registry`。
// 导出是为了让 `mate demo` 这类装配外入口复用同一份，而不是各写一遍九行字面量。
func PlaneFactsOf(p *registry.Planes) sessionruntime.PlaneFacts {
	if p == nil {
		return sessionruntime.PlaneFacts{}
	}
	return sessionruntime.PlaneFacts{
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

// NewSessionService 造"会话执行面"的最小装配：领域平面 + 执行面两半 + 五个来源接线。
//
// 与 `Build` 的构造段**同一份**：装配顺序（先平面/平面，后来源）漏一个的表现都是静默的
// （少注入密钥 / 代际键少一维 / 诊断永远报未连接）。`mate demo` 直接复用它。
func NewSessionService(reg *runtime.Registry, bus *eventbus.Bus) (*sessionruntime.Service, *registry.Planes, *runtimeconn.Plane, *runtimeconn.Pool) {
	planes := registry.NewPlanes(reg)
	plane := runtimeconn.NewPlaneFor(reg)
	pool := runtimeconn.NewPool(plane)
	svc := sessionruntime.NewService(reg, bus,
		sessionruntime.WithPlaneFacts(PlaneFactsOf(planes)),
		sessionruntime.WithRuntimePlane(plane),
		sessionruntime.WithConnPool(pool))
	plane.SetCredentialSource(svc)
	pool.SetProviderEnvSource(svc)
	pool.SetConfigDigestSource(planes.Resources)
	pool.SetNativeEnvSource(svc)
	plane.SetLiveClientSource(pool)
	return svc, planes, plane, pool
}

// resourceStore 资源同步面的窄库口（未接库 → nil）。
func (as *assembly) resourceStore() sessionruntime.ResourceStore {
	if as.st == nil {
		return nil
	}
	return as.st
}

// planeFacts 把具体平面集合折成执行面要的**按面端口**（阶段 0.2）。
//
// 这里是"具体平面 → 端口"的唯一折点：执行面因此不 import `drivers/registry`。
func (as *assembly) planeFacts() sessionruntime.PlaneFacts { return PlaneFactsOf(as.planes) }

// planeStore 折出领域平面的接库动作（未接库时返回 nil）。
func (as *assembly) planeStore() sessionruntime.PlaneStore {
	if as.st == nil || as.planes == nil {
		return nil
	}
	return planeStore{planes: as.planes, st: as.st}.EnableStore
}
