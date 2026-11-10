package appslot

import (
	"fmt"
	"sort"
	"sync"

	"github.com/morehao/ark-mate/internal/core/capability"
)

// ——— App Slot 运行时（WP6）———
//
// 契约层（appslot.go，WP2）只钉「Impl / Ctx / 登记白名单」的**语义**；本文件落**运行时**：
// 把「一组启用的 app」变成「一批按依赖顺序 apply 的 impl + 一棵撤销栈」，并在 app 停用时按
// **逆序**撤销（设计 §5.4 / §6 G6「关得干净」）。
//
// 三条不变量（缺一即静默失效，故每条都有 reconcile_test.go 的对照）：
//
//	① **依赖排序**：Impl 的 Requires 决定 apply 顺序——被依赖的先用。顺序错了的表现是
//	  「某个 slot apply 时 Service(name) 拿到 nil」，而它只在那一条 app 上偶发失败，别处全绿。
//	② **失败隔离**：一个 impl apply 时 panic / 返错，只把它自己记 FAILED，**其它照跑**。
//	  若让一个坏 slot 把整批 Reconcile 拖垮，等于「装了一个坏 app 就全站起不来」——这正是
//	  magpie「单点失败隔离」要挡的。
//	③ **逆序撤销**：OnDispose 的撤销栈按 apply 的**反向**执行（后 apply 的先撤，依赖它的先退），
//	  保证「关得干净」——正序撤会把「被依赖的」先摘掉，依赖它的那个 dispose 时就撞空。
//
// **数据只选 impl，不提供 impl**：Reconcile 只按 app 定义里的 slots[].impl_id 到 KnownImpls
// 查实现；查不到 = 该 slot 记 FAILED（「未知 impl」），绝不凭空造一个。这是白名单不退化成
// 服务定位图（TestAppSlotImplWhitelisted）在运行期的另一半。

// Slot 是一次 Reconcile 要 apply 的一个执行点（设计 §5.4 的 `Slot { ImplID; Requires }`）。
//
// 它由装配根从 enabled app 的 `App.Slots` 展开得到：`ImplID` 取自 `app.Slots[].impl_id`；
// `Requires` 是「这个 impl 依赖哪些已 Provide 的 service 名」，决定 apply 顺序（本包不解析、
// 只排序）。
type Slot struct {
	// ImplID 必须是已 RegisterImpl 的 id（未在册 = 该 slot FAILED）。
	ImplID string
	// Requires 依赖的 service 名（Core 服务或别的 slot 已 Provide 的），决定 apply 顺序。
	Requires []string
	// AppID 归属的 app id（诊断/日志用；空则由装配根填）。
	AppID string
}

// Status 是一个 slot 在一次 Reconcile 后的终态。
type Status string

const (
	// StatusApplied 已成功 apply（其 OnDispose 已进撤销栈）。
	StatusApplied Status = "applied"
	// StatusFailed 未 apply（未知 impl / apply 返回错误 / apply panic）。失败隔离：其它照跑。
	StatusFailed Status = "failed"
)

// SlotResult 是一个 slot 的结算结果（诊断/测试可读）。
type SlotResult struct {
	Slot   Slot
	Status Status
	// Err 失败原因（Status==StatusFailed 时非空；apply panic 时含 recover 到的值）。
	Err error
}

// Reconcile 把一组启用的 app 的 slots 按依赖排序后逐个 apply，返回每个 slot 的结算结果。
//
// 入参 enabledApps 是**已启用**的 app 定义（调用方负责过滤 Enabled；本函数不关心启停判断，
// 只关心「这些 app 的 slots 现在要生效」）。返回值：
//   - results：每个 slot 的 Status/Err（顺序与 apply 顺序一致，便于诊断）；
//   - disposer：把本次 apply 成功的所有 OnDispose 按**逆序**执行的撤销函数。调用方在 app
//     停用 / 进程关闭时调一次 disposer（幂等：重复调用不重复撤销）。
//
// 失败隔离：单个 slot 未知 impl / apply 返错 / apply panic，只记该 slot FAILED，继续 apply 后续。
// 依赖排序：按 Slot.Requires 拓扑排序（被依赖者先 apply）；**成环**时不静默——把环上的 slot
// 记 FAILED（按原位次稳定顺序处理其余），因为「顺序无法确定」不能假装成功。
func Reconcile(enabledApps []capability.App) (results []SlotResult, disposer func()) {
	return ReconcileSlots(collectSlots(enabledApps))
}

// ReconcileSlots 是 Reconcile 的**完整形态**：直接吃一组已展开的 Slot（含 Requires）。
//
// 装配根在需要「显式依赖排序」时用它：app 定义里的 `AppSlot` 只带 ImplID、不带 Requires
// （capability.AppSlot 的既有形状），Requires 来自 impl 自身声明的依赖（Core / 别的 slot
// Provide 的 service 名），由装配根在展开 Slot 时补齐。`Reconcile(apps)` 是它的简化入口
// （app 定义不带 Requires，等价于全部无依赖、按 id 稳定排序）。
func ReconcileSlots(slots []Slot) (results []SlotResult, disposer func()) {
	ordered, cycleIDs := topoOrder(slots)

	known := KnownImpls()
	ctx := newRuntimeCtx(known)

	results = make([]SlotResult, 0, len(slots))
	// 环上的 slot：依赖顺序无法判定，一律 FAILED（不静默假装成功）。
	for _, id := range cycleIDs {
		results = append(results, SlotResult{
			Slot:   Slot{ImplID: id},
			Status: StatusFailed,
			Err:    fmt.Errorf("appslot: impl %q 依赖成环，无法确定 apply 顺序", id),
		})
	}

	for _, s := range ordered {
		results = append(results, applyOne(ctx, known, s))
	}

	// disposer 只撤本次真正 apply 成功的（ctx 的撤销栈已按 apply 顺序压入；逆序执行）。
	disposer = func() { ctx.disposeAll() }
	return results, disposer
}

// applyOne apply 单个 slot，带 panic 恢复（失败隔离）。
func applyOne(ctx *runtimeCtx, known map[string]Impl, s Slot) (res SlotResult) {
	fn, ok := known[s.ImplID]
	if !ok {
		// 未知 impl：数据引用了不存在的实现。记 FAILED，绝不凭空造一个（白名单底线）。
		return SlotResult{Slot: s, Status: StatusFailed,
			Err: fmt.Errorf("appslot: 未知 impl_id %q（未在编译期 RegisterImpl 登记）", s.ImplID)}
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
	panicked = false
	return SlotResult{Slot: s, Status: StatusApplied}
}

// collectSlots 把 app 的 slots 展开成扁平 Slot 列表（每个 app.Slots[].impl_id 一个）。
// Requires 在 app 定义里不声明（AppSlot 只有 ImplID），由装配根需要时另填；这里只搬 ImplID/AppID。
func collectSlots(apps []capability.App) []Slot {
	out := make([]Slot, 0)
	for _, a := range apps {
		for _, s := range a.Slots {
			out = append(out, Slot{ImplID: s.ImplID, AppID: a.ID})
		}
	}
	return out
}

// topoOrder 按 Requires 做拓扑排序，返回 apply 顺序（被依赖者先）与成环的 impl_id 列表。
//
// 稳定：同一「就绪层」内按 impl_id 升序，保证同一批输入产出确定顺序（便于测试与复现）。
// 依赖只按 Requires 里出现的 service 名匹配**同批 slots 的 ImplID**——匹配不到的是 Core 服务，
// 不影响排序（Core 服务在 Reconcile 前已 Provide）。
func topoOrder(slots []Slot) (ordered []Slot, cycleIDs []string) {
	// 去重（同一 impl_id 只 apply 一次；重复定义以首个为准，稳定）。
	uniq := make(map[string]Slot, len(slots))
	ids := make([]string, 0, len(slots))
	for _, s := range slots {
		if _, ok := uniq[s.ImplID]; ok {
			continue
		}
		uniq[s.ImplID] = s
		ids = append(ids, s.ImplID)
	}
	sort.Strings(ids)

	// 邻接：dep → 依赖它的（dep 必须先 apply）。
	// 只把 Requires 里**恰好是另一个 slot 的 ImplID** 的边算进来；其余视为 Core 服务。
	knownSlot := func(id string) bool { _, ok := uniq[id]; return ok }
	dependents := make(map[string][]string, len(ids)) // dep -> [depender...]
	indeg := make(map[string]int, len(ids))
	for _, id := range ids {
		indeg[id] = 0
	}
	for _, id := range ids {
		for _, req := range uniq[id].Requires {
			if !knownSlot(req) {
				continue // Core 服务，非本批 slot
			}
			if req == id {
				continue // 自依赖忽略（不与自身成环，登记层已去重）
			}
			dependents[req] = append(dependents[req], id)
			indeg[id]++
		}
	}

	// Kahn：每轮取 indeg==0 的、按 id 升序（稳定）。
	ready := make([]string, 0)
	for _, id := range ids {
		if indeg[id] == 0 {
			ready = append(ready, id)
		}
	}
	ordered = make([]Slot, 0, len(ids))
	done := make(map[string]bool, len(ids))
	for len(ready) > 0 {
		sort.Strings(ready)
		id := ready[0]
		ready = ready[1:]
		if done[id] {
			continue
		}
		done[id] = true
		ordered = append(ordered, uniq[id])
		for _, dep := range dependents[id] {
			indeg[dep]--
			if indeg[dep] == 0 {
				ready = append(ready, dep)
			}
		}
	}

	// 剩下的（indeg>0）即环上的：按 id 升序返回，调用方记 FAILED。
	if len(done) != len(ids) {
		for _, id := range ids {
			if !done[id] {
				cycleIDs = append(cycleIDs, id)
			}
		}
	}
	return ordered, cycleIDs
}

// ——— runtimeCtx：Ctx 接口的运行期实现（承担 Provide/Service 与撤销栈）———
//
// provide 表跨 slot 共享（后 apply 的能取到先 apply 的 Provide）；撤销栈按 apply 顺序压入，
// disposeAll 逆序弹出执行（后 apply 的先撤 = 依赖它的先退，G6）。
type runtimeCtx struct {
	mu       sync.Mutex
	provided map[string]any
	dispose  []func()
	known    map[string]Impl
}

func newRuntimeCtx(known map[string]Impl) *runtimeCtx {
	return &runtimeCtx{provided: make(map[string]any), known: known}
}

// Provide 把一个服务以 name 提供给其它 slot。重名覆盖（后 provide 覆盖先）——同一 name 两个
// 提供者是配置事故，但运行期没有「登记白名单」可拒，这里以「后到者为准」并对调用方留白
// （Diagnostics 可读 provided 键集）。真正的重名防护在写面（app 定义校验）。
func (c *runtimeCtx) Provide(name string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.provided[name] = v
}

// Service 取一个依赖服务（Core 服务或别的 slot 已 Provide 的）；取不到返回 nil。
func (c *runtimeCtx) Service(name string) any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.provided[name]
}

// OnDispose 登记一个撤销函数；停用时逆序执行（后登记的先撤）。
func (c *runtimeCtx) OnDispose(f func()) {
	if f == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dispose = append(c.dispose, f)
}

// disposeAll 逆序执行撤销栈（幂等：执行后清空，重复调用不再执行）。
//
// 单个 dispose panic 不能拖垮整批撤销：recover 记日志语义（这里无 logger，忽略值），继续撤下一个。
func (c *runtimeCtx) disposeAll() {
	c.mu.Lock()
	stack := c.dispose
	c.dispose = nil
	c.mu.Unlock()
	for i := len(stack) - 1; i >= 0; i-- {
		func() {
			defer func() { _ = recover() }()
			stack[i]()
		}()
	}
}
