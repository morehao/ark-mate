package assembly

import (
	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/adapters/outbound/registry"
	"github.com/morehao/ark-mate/internal/adapters/outbound/runtimeconn"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/ark-mate/internal/runtime"
	apprun "github.com/morehao/ark-mate/internal/app/run"
)

// ——— 接库端口的适配体（阶段 0.1）———
//
// `apprun.EnableStore` 只收端口，具体句柄留在装配层。本文件是**唯一**把
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
// 它是函数型端口（`apprun.PlaneStore`）的实现，句柄与平面都取自装配根——
// 执行面因此不需要认识 `*registry.Planes`。
type planeStore struct {
	planes *registry.Planes
	st     *store.Store
}

// EnableStore 实现 `apprun.PlaneStore`。
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
func PlaneFactsOf(p *registry.Planes) apprun.PlaneFacts {
	if p == nil {
		return apprun.PlaneFacts{}
	}
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

// NewSessionService 造"会话执行面"的最小装配：领域平面 + 执行面两半 + 五个来源接线。
//
// 与 `Build` 的构造段**同一份**：装配顺序（先平面/平面，后来源）漏一个的表现都是静默的
// （少注入密钥 / 代际键少一维 / 诊断永远报未连接）。`mate demo` 直接复用它。
func NewSessionService(reg *runtime.Registry, bus *eventbus.Bus) (*apprun.Service, *registry.Planes, *runtimeconn.Plane, *runtimeconn.Pool) {
	return NewSessionServiceWith(reg, bus, nil)
}

// NewSessionServiceWith 同 `NewSessionService`，但可以把"取连接"这一半换成**别的实现**
// （`connPool` 非 nil 时用它，装配根不再往它上面接 ACP 的来源）。
//
// 唯一用途是 C2 §3.4 的验收缝：装配一个**不认识协议**的纯内存连接池，控制面契约与会话
// 事件序列必须逐字不变（那是"换底座不动框架"的机器证明）。生产路径一律传 nil。
//
// 传了它，`runtimeconn` 的两半**仍然造出来**：进程注册、身份面、诊断面还挂在它们上面，
// 换掉的只有"会话从哪条连接来"。
func NewSessionServiceWith(reg *runtime.Registry, bus *eventbus.Bus,
	connPool session.ConnPool) (*apprun.Service, *registry.Planes, *runtimeconn.Plane, *runtimeconn.Pool) {
	planes := registry.NewPlanes(reg)
	plane := runtimeconn.NewPlaneFor(reg)
	pool := runtimeconn.NewPool(plane)
	exec := session.ConnPool(pool)
	if connPool != nil {
		exec = connPool
	}
	svc := apprun.NewService(reg, bus,
		apprun.WithPlaneFacts(PlaneFactsOf(planes)),
		apprun.WithRuntimePlane(plane),
		apprun.WithConnPool(exec))
	plane.SetCredentialSource(svc)
	// 来源接线是 `runtimeconn.Pool` 的具体面（env / 配置指纹 / 原生权限 env / 活连接读面）：
	// 换成端口实现时它一个都不需要——那些来源只为"A CP 子进程该拿到什么"服务。
	if connPool == nil {
		pool.SetProviderEnvSource(svc)
		pool.SetConfigDigestSource(planes.Resources)
		pool.SetNativeEnvSource(svc)
		plane.SetLiveClientSource(pool)
	}
	return svc, planes, plane, pool
}

// resourceStore 资源同步面的窄库口（未接库 → nil）。
func (as *assembly) resourceStore() apprun.ResourceStore {
	if as.st == nil {
		return nil
	}
	return as.st
}

// planeFacts 把具体平面集合折成执行面要的**按面端口**（阶段 0.2）。
//
// 这里是"具体平面 → 端口"的唯一折点：执行面因此不 import `drivers/registry`。
func (as *assembly) planeFacts() apprun.PlaneFacts { return PlaneFactsOf(as.planes) }

// planeStore 折出领域平面的接库动作（未接库时返回 nil）。
func (as *assembly) planeStore() apprun.PlaneStore {
	if as.st == nil || as.planes == nil {
		return nil
	}
	return planeStore{planes: as.planes, st: as.st}.EnableStore
}
