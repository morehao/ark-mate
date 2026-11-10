package apphost

import (
	"github.com/morehao/ark-mate/internal/core/appslot"
)

// ——— 运行期 impl 的「可执行封装」（T-C3.4：把 RuntimeSource.Impl 执行接到 host RPC）———
//
// C2 的 `appslot.RuntimeSource.Impl()` 只回答「在不在册」，**执行代码要经隔离 host 子进程**——
// 这层封装把「一个运行期 impl_id」变成一个可被 Reconcile 调用的 `appslot.Impl`（`func(Ctx) error`），
// 其内部走 `Host.ApplySlot` 的 JSON-lines RPC（apply 在子进程里执行、可失败隔离）。
//
// 为什么这层放在 apphost 而不是 appslot：执行要依赖 apphost.Host（上层），而 appslot 必须在最内层、
// **不能 import apphost**（否则成环）。故 appslot 只留「来源/在册」抽象（C2），执行封装与 host 调用
// 都归 apphost（C3）。**Reconcile 的接线**（把本封装塞进 Reconcile 的执行路径）归 C5——本层只提供
// 「可被调用的客户端封装」，不自己接 Reconcile（C5 才改装配根）。
//
// contextSnapshotFor：把一个 appslot.Ctx 压成可跨进程的字符串快照（本期只用 service 名清单，
// 真正的跨进程服务注入属 [增强项]——陌生代码拿到内核对象引用本身就是要极力避免的）。C3 里快照
// 只承载「这个 slot 能看到哪些 service 名」，不承载任何内核对象。Ctx 接口没有「枚举 provided」的
// 方法，故这里保守给空快照（apply 需要的服务解析留给 C5 用装配根显式传入的静态快照）。

// ImplRunner 把一个运行期 impl_id 变成可执行的 appslot.Impl（内部走 Host.ApplySlot RPC）。
//
// 用法（C5 装配根持有）：对从 apppkg 装载的运行期 impl，用 `runner.ImplFor(implID)` 得到 appslot.Impl
// 塞进 Reconcile；Reconcile 调它时，impl 的 apply 经 host 子进程执行。同一 impl_id 的 apply 是幂等的
// 从调用方视角看（host 侧负责真实效果）。
type ImplRunner struct {
	host *Host
}

// NewImplRunner 用某个包的 Host 构造 runner。
func NewImplRunner(host *Host) *ImplRunner {
	return &ImplRunner{host: host}
}

// Impl 返回 implID 对应的 appslot.Impl（内部 apply 走 host RPC）。返回 nil = 无法封装（host 不可用）。
//
// 返回的 Impl 语义：调用时向 host 发 applySlot RPC；失败即返回错误（Reconcile 会记该 slot FAILED，
// 失败隔离）。注意它**不**在 Ctx 上登记 OnDispose——撤销由 Reconcile 的 disposer 统一调 Idle，
// 或由 C5 显式调 DisposeSlot；本期 Impl 只做 apply。dispose 的 host 调用点见 Runner.DisposeImpl。
func (r *ImplRunner) Impl(implID string) appslot.Impl {
	if r == nil || r.host == nil || implID == "" {
		return nil
	}
	return func(ctx appslot.Ctx) error {
		// Ctx 的 service 名快照：Ctx 接口不暴露枚举，这里给空快照（见文件头说明）。
		return r.host.ApplySlot(implID, nil)
	}
}

// DisposeImpl 对某 implID 发 disposeSlot RPC（供 C5 撤销路径调用）。
func (r *ImplRunner) DisposeImpl(implID string) error {
	if r == nil || r.host == nil || implID == "" {
		return nil
	}
	return r.host.DisposeSlot(implID)
}

// ListImpls 透传 host 的 listImpls（C4/C5 用它核对清单与进程自述是否一致）。
func (r *ImplRunner) ListImpls() ([]string, error) {
	if r == nil || r.host == nil {
		return nil, nil
	}
	return r.host.ListImpls()
}

// ListTools 透传 host 的 listTools（供 C4 的工具域装载使用）。
func (r *ImplRunner) ListTools() ([]ToolSpec, error) {
	if r == nil || r.host == nil {
		return nil, nil
	}
	return r.host.ListTools()
}

// InvokeTool 透传 host 的 invokeTool（供 C4 聚合的工具面最终调用）。
func (r *ImplRunner) InvokeTool(name string, args []byte) ([]byte, bool, error) {
	if r == nil || r.host == nil {
		return nil, false, nil
	}
	return r.host.InvokeTool(name, args)
}
