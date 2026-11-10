package appslot

import (
	"fmt"

	"github.com/morehao/ark-mate/internal/core/capability"
)

// ——— Reconcile 接运行期来源（WP-C5，T-C5）———
//
// 一期（WP6）的 Reconcile 只认识编译期 KnownImpls：按 slots[].impl_id 查表、依赖排序、失败隔离、
// OnDispose 逆序撤销。二期 C 引入运行期应用包（apppkg 装载的第三方包，impl 来自 manifest，执行要经
// C3 的隔离 host 子进程）后，Reconcile 的白名单要扩成「编译期 ∪ 运行期」两个来源并列（§17.4）。
//
// 本文件是**接线层**：把「impl_id → 可执行 appslot.Impl」的解析从「写死 KnownImpls」改成「查
// ImplSource（执行函数由调用方经 ImplResolver 提供）」，从而对运行期包 app 的 slot，impl 取自
// RuntimeSource（执行经 C3 ImplRunner→Host.ApplySlot RPC）。
//
// **语义完全不变（§17.1 契约继承）**：依赖排序（topoOrder）、失败隔离（applyOne 的 recover/记
// FAILED/其它照跑）、OnDispose 逆序撤销（runtimeCtx.disposeAll）—— 一字不改，只是「取实现」这一步
// 多了一个来源。禁止把 Reconcile 改成「运行期来源也走同一张编译期表」——那会让第三方 impl 假装
// 在二进制里、既不能执行也不该在册。
//
// **undo 精确撤销**：运行期 impl 的撤销不是调内核可执行函数，而是经 ImplResolver.Dispose 发
// disposeSlot RPC 让子进程精确撤销它注册的东西（非重启整个子进程）。本层把「撤销一个来源 impl」
// 也抽成 ImplResolver.Dispose，编译期来源的 Dispose 是空操作（编译期 impl 的撤销走 Ctx.OnDispose，
// 由 runtimeCtx 统一逆序执行，语义与一期一致）。

// ImplResolver 在**不 import apphost** 的前提下，为一个 impl_id 提供「可执行函数 + 精确撤销」。
//
// 为什么需要这层而非直接吃 ImplSource：ImplSource.Impl() 只对编译期来源返回可执行函数（运行期来源
// 恒 (nil,false)，执行代码在 C3 的 host 子进程里）。appslot 在最内层不能 import apphost，故由装配根
// 用 C3 的 ImplRunner 构造一个 ImplResolver 传进来：
//   - 编译期 impl_id：Exec 返回 KnownImpls 里的真函数，Dispose 空操作（撤销靠 Ctx.OnDispose）；
//   - 运行期 impl_id：Exec 返回「调用 Host.ApplySlot RPC」的封装函数，Dispose 调 Host.DisposeSlot RPC。
//
// 于是 Reconcile 只认 ImplResolver 这一个端口，不关心 impl 到底在二进制还是子进程——**在内核直接
// 调用运行期包代码这条路在类型上就不存在**（守卫 TestRuntimeImplOnlyViaHost 钉这一点）。
type ImplResolver interface {
	// Resolve 取 implID 的可执行函数。ok=false = 该 impl 在其来源里不可执行（unknown / 不可达）。
	Resolve(implID string) (Impl, bool)
	// Dispose 精确撤销一个已 apply 的 impl（运行期来源走 disposeSlot RPC；编译期来源空操作）。
	// 返回 error 仅供诊断；撤销失败不应阻断其它 slot 的撤销（调用方 recover + 继续）。
	Dispose(implID string) error
}

// SourceImplResolver 是把「ImplSource（在册判定）+ 装配根提供的执行函数表」适配成 ImplResolver 的
// 通用实现。它把「哪些 id 在册」（source.Has）与「怎么执行/撤销」（execs/disposers 由装配根按来源
// 路由后塞入）分开——appslot 不认识 apphost，执行函数由装配根用 C3 ImplRunner 构造后传进来。
//
// 语义：
//   - Resolve(id)：若 source 里在册且 execs 有 id → 返回该函数；否则按在册情况区分：
//     在册但无执行函数（运行期来源在 host 不可用时）→ (nil,false)，Reconcile 记 FAILED（失败隔离）。
//   - Dispose(id)：若 disposers 有 id → 调它；否则空操作（编译期 impl 撤销走 Ctx.OnDispose）。
//
// 关键不变量：Resolve 只对**source 在册**的 id 返回函数——不在册的 id 永远 (nil,false)，白名单退化成
// 服务定位图这条底线在接线层也不放松。
type SourceImplResolver struct {
	source    ImplSource
	execs     map[string]Impl
	disposers map[string]func() error
}

// NewSourceImplResolver 构造一个 resolver。source 为在册判定来源（通常是 CombinedSource(编译期,运行期)）；
// execs 是「impl_id → 可执行函数」（只应放**在这个来源里合法**的 id → 函数映射，装配根按来源路由填）；
// disposers 是「impl_id → 精确撤销函数」（运行期 impl 的 disposeSlot RPC 封装；编译期 impl 可不填，空操作）。
func NewSourceImplResolver(source ImplSource, execs map[string]Impl, disposers map[string]func() error) *SourceImplResolver {
	if execs == nil {
		execs = map[string]Impl{}
	}
	if disposers == nil {
		disposers = map[string]func() error{}
	}
	return &SourceImplResolver{source: source, execs: execs, disposers: disposers}
}

// Resolve 取可执行函数：只有在 source 在册、且有显式执行函数时才返回。这是「不退化服务定位图」在
// 接线层的落点——不在册的 id 即便 execs 里塞了函数也不返回（defense in depth）。
func (r *SourceImplResolver) Resolve(implID string) (Impl, bool) {
	if r == nil || r.source == nil || !r.source.Has(implID) {
		return nil, false
	}
	fn, ok := r.execs[implID]
	if !ok || fn == nil {
		return nil, false
	}
	return fn, true
}

// Has 报告 implID 是否在 source 在册（供只判在册的场景；等价的 source.Has）。
func (r *SourceImplResolver) Has(implID string) bool {
	if r == nil || r.source == nil {
		return false
	}
	return r.source.Has(implID)
}

// Dispose 精确撤销：有 disposer 则调用，否则空操作（编译期 impl 走 Ctx.OnDispose）。
func (r *SourceImplResolver) Dispose(implID string) error {
	if r == nil {
		return nil
	}
	if fn, ok := r.disposers[implID]; ok && fn != nil {
		return fn()
	}
	return nil
}

// ReconcileWithResolver 是 ReconcileSlots 的**来源感知**完整形态：用 ImplResolver 解析实现与撤销。
//
// 与 ReconcileSlots 的差异仅在「取实现/撤销」两步：排序、失败隔离、逆序撤销栈全复用同一套 runtimeCtx
// 与 topoOrder，语义一字不改（§17.1）。运行期来源的 slot：Resolve 返回「走 Host.ApplySlot RPC」的函数，
// 调它时 apply 在子进程执行；撤销时 disposer 调 Host.DisposeSlot RPC 精确撤销。
//
// undo 顺序：runtimeCtx.disposeAll 逆序执行撤销栈。运行期 impl 也在 apply 成功时**把它的 Dispose 登记
// 进同一个撤销栈**（见 applyOneWithResolver），从而运行期与编译期 impl 的撤销在同一逆序里协奏，
// 「后 apply 的先撤」对两种来源一致成立（G6 关得干净）。
func ReconcileWithResolver(slots []Slot, resolver ImplResolver) (results []SlotResult, disposer func()) {
	ordered, cycleIDs := topoOrder(slots)

	ctx := newRuntimeCtx(nil) // known map 不再使用；解析走 resolver（保留 newRuntimeCtx 签名兼容）

	results = make([]SlotResult, 0, len(slots))
	for _, id := range cycleIDs {
		results = append(results, SlotResult{
			Slot:   Slot{ImplID: id},
			Status: StatusFailed,
			Err:    fmt.Errorf("appslot: impl %q 依赖成环，无法确定 apply 顺序", id),
		})
	}

	for _, s := range ordered {
		results = append(results, applyOneWithResolver(ctx, resolver, s))
	}

	disposer = func() { ctx.disposeAll() }
	return results, disposer
}

// applyOneWithResolver 与 applyOne 同构，只是「取实现」走 resolver，并在 apply 成功时把来源侧的
// 精确撤销（resolver.Dispose）也登记进撤销栈——使运行期 impl 的 disposeSlot RPC 与编译期 impl 的
// Ctx.OnDispose 在**同一个逆序栈**里撤销（后 apply 的先撤）。
//
// 失败隔离语义与 applyOne 完全一致：未知 impl / apply 返错 / apply panic → 只记该 slot FAILED，继续。
func applyOneWithResolver(ctx *runtimeCtx, resolver ImplResolver, s Slot) (res SlotResult) {
	if resolver == nil {
		return SlotResult{Slot: s, Status: StatusFailed,
			Err: fmt.Errorf("appslot: impl %q 无 resolver（装配根未接来源）", s.ImplID)}
	}
	fn, ok := resolver.Resolve(s.ImplID)
	if !ok {
		// 未在册或来源不可执行：数据引用了不存在/不可达的实现。记 FAILED，绝不凭空造（白名单底线）。
		return SlotResult{Slot: s, Status: StatusFailed,
			Err: fmt.Errorf("appslot: impl_id %q 未在册或来源不可执行", s.ImplID)}
	}
	// apply panic 不能让整批 Reconcile 崩掉：recover 成该 slot FAILED，其它照跑。
	panicked := true
	defer func() {
		if panicked {
			r := recover()
			res = SlotResult{Slot: s, Status: StatusFailed,
				Err: fmt.Errorf("appslot: impl %q apply panic: %v", s.ImplID, r)}
		}
	}()
	if err := fn(ctx); err != nil {
		return SlotResult{Slot: s, Status: StatusFailed,
			Err: fmt.Errorf("appslot: impl %q apply 失败: %w", s.ImplID, err)}
	}
	// apply 成功 → 把来源侧精确撤销登记进同一个逆序栈（运行期走 disposeSlot RPC，编译期空操作）。
	// 用局部 id 捕获，避免闭包引用循环变量。
	implID := s.ImplID
	ctx.OnDispose(func() { _ = resolver.Dispose(implID) })
	panicked = false
	return SlotResult{Slot: s, Status: StatusApplied}
}

// ReconcileAppsWithResolver 是「按 app 定义 + 来源路由」的便捷入口：把 enabled apps 的 slots 展开，
// 交给 ReconcileWithResolver。当前与 Reconcile(apps) 等价（AppSlot 不带 Requires），一期入口
// （Reconcile/ReconcileSlots）不动。
func ReconcileAppsWithResolver(enabledApps []capability.App, resolver ImplResolver) (results []SlotResult, disposer func()) {
	return ReconcileWithResolver(collectSlots(enabledApps), resolver)
}
