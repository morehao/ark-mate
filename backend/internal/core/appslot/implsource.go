package appslot

import (
	"fmt"
	"sort"
	"sync"
)

// ——— 运行期 impl 来源契约（WP-C2，设计 §17.1 契约继承 / §17.4 双来源并存）———
//
// 一期（A+Slot）里 impl 只有一个来源：编译期 `RegisterImpl` 登记的 KnownImpls（内置 app 的 Go 包
// 在 init 里登记）。二期 C 引入**运行期应用包**（apppkg 装载的第三方包，其 manifest 声明
// `provides.impls[]{impl_id,kind}`）——这些 impl 的实现代码在包里、要经 C3 的隔离 host 子进程执行。
//
// 本文件把「一个 impl_id 在册在哪」抽象成 `ImplSource`，让编译期来源与运行期来源**并列**：
// 白名单判定 = 「在任一来源在册即合法」（§17.4）。关键不变量**不放松**：impl_id 仍必须落在
// **某个已声明来源**里（编译期二进制表 ∪ 已装包清单），绝不退化成「数据给个字符串就能查到实现」
// 的服务定位图——`CombinedSource.Has` 只认注册表/清单里真有的 id，查不到就是查不到。
//
// 分工边界（本 WP 只做到这里）：
//   - 本文件只回答「这个 impl_id 在不在册、它属于哪个来源、来源里登记了什么」——**判定层**。
//   - 运行期来源的 `Impl()`（真正拿到可执行函数）在 C3 host 子进程就绪前**不承诺能执行**：
//     RuntimeSource 的 Impl 返回 `ok=false`（详见 runtime_source.go 注释）；真正把运行期 impl
//     接到 host RPC 执行是 C5（Reconcile 接线）。本 WP 不改 Reconcile。

// ImplSource 是一个「impl 白名单来源」：能按 impl_id 查实现、判定在册、枚举全部 id。
//
// 设计 §5 WP-C2 逐字钉的契约。两个实现：
//   - compiledSource：包装编译期 KnownImpls（appslot 注册表 + A+Slot 内置 app）；
//   - RuntimeSource：来自已装载 apppkg 的 manifest `provides.impls`（运行期第三方包）。
//
// 二者用 CombinedSource 组合，供 svcapp/Reconcile 的白名单判定与 WP-C5 使用。
type ImplSource interface {
	// Impl 按 impl_id 取可执行实现。不在本来源在册 → (nil, false)。
	// 注意：运行期来源在 C3 host 就绪前实现代码不可执行，会返回 (nil, false)——
	// 「在册」判定要靠 Has，不要用 Impl 的可执行性判断在册与否（见 Has 注释）。
	Impl(implID string) (Impl, bool)
	// Has 报告 impl_id 是否在本来源在册（**只查白名单，不看能否执行**）。
	Has(implID string) bool
	// IDs 返回本来源在册的全部 impl_id（升序，稳定；诊断/守卫/合并用）。
	IDs() []string
}

// ——— 编译期来源 ———

// compiledSource 是「编译期 KnownImpls」的 ImplSource 适配：把既有的包级注册表包装成来源。
//
// **不改 KnownImpls 语义**：RegisterImpl / KnownImpls / ImplIDs 行为一字不动，本类型只是它们在
// 新抽象下的一个视图。一期内置 app（A+Slot）照旧经注册表在册，走这个来源。
type compiledSource struct{}

// CompiledSource 返回编译期 impl 来源（包装当前注册表快照；调用时实时读取，不缓存）。
//
// 装配根/白名单判定用 `CombinedSource(CompiledSource(), runtime)` 把编译期与运行期并列。
func CompiledSource() ImplSource { return compiledSource{} }

func (compiledSource) Impl(implID string) (Impl, bool) {
	fn, ok := KnownImpls()[implID]
	return fn, ok
}

func (compiledSource) Has(implID string) bool {
	_, ok := KnownImpls()[implID]
	return ok
}

func (compiledSource) IDs() []string { return ImplIDs() }

// ——— 运行期来源（由 apppkg 侧构造，见 apppkg/runtime_source.go）———

// RuntimeImplSpec 是运行期包清单里一个 impl 声明的**来源无关视图**（appslot 侧不认识 apppkg）。
//
// apppkg 把 `Package.Manifest.Provides.Impls[]{impl_id,kind}` 映射成本结构喂给 NewRuntimeSource，
// 从而 appslot 不 import apppkg（保持 appslot 在最内层、无 cycle）。Kind 目前区分 "slot"/"entity"
// 等类别，留给 C5 的实体分派与来源路由；本 WP 只存不用。
type RuntimeImplSpec struct {
	// ImplID 清单声明的实现 id（词法已由 apppkg 在清单校验时确认，此处不重校验）。
	ImplID string
	// Kind 实现类别（"slot"/"entity"…）。
	Kind string
	// PackageID 声明该 impl 的包 id（= app 定义 id）。同一 impl_id 被多个包声明的冲突由上层
	// 装配根裁决；这里只记录「归属哪个包」，供来源路由与诊断。
	PackageID string
}

// runtimeSource 是「已装载 apppkg 清单」的 ImplSource 实现：白名单来自 manifest 的 provides.impls。
//
// **执行接 C3**：本类型只承载「在册」信息，`Impl()` 一律返回 (nil, false)——因为运行期包的实现
// 代码要经 C3 的隔离 host 子进程执行，host 就绪前没有任何可执行函数可给。真正把运行期 impl 接到
// host RPC 是 C5（Reconcile 换用 ImplSource 并调 applySlot RPC）。**这是有意的**：本 WP 只建抽象、
// 让「是否在册」判定能查 manifest，不假装能执行。
type runtimeSource struct {
	mu    sync.RWMutex
	specs map[string]RuntimeImplSpec // impl_id -> 声明
	ids   []string                   // 升序稳定 id 列表
}

// NewRuntimeSource 从一组运行期 impl 声明构造运行期来源。
//
// 冲突语义：**同一 impl_id 被重复声明即 panic**——与 RegisterImpl 的「同 id 两种实现是配置事故」
// 同一条纪律（运行期包同样不该出现「谁生效取决于装载顺序」的静默失效）。空 ImplID 也 panic。
// 传入空切片 → 合法的空来源（没有装任何提供 impl 的包）。
func NewRuntimeSource(specs []RuntimeImplSpec) ImplSource {
	s := &runtimeSource{specs: make(map[string]RuntimeImplSpec, len(specs))}
	for _, sp := range specs {
		if sp.ImplID == "" {
			panic("appslot: NewRuntimeSource 收到空 ImplID")
		}
		if prev, dup := s.specs[sp.ImplID]; dup {
			panic(fmt.Sprintf("appslot: 运行期 impl_id 重复声明: %s（包 %q 与 %q）", sp.ImplID, prev.PackageID, sp.PackageID))
		}
		s.specs[sp.ImplID] = sp
	}
	s.ids = make([]string, 0, len(s.specs))
	for id := range s.specs {
		s.ids = append(s.ids, id)
	}
	sort.Strings(s.ids)
	return s
}

// Impl 运行期 impl 的实现代码要经 C3 host 子进程执行；host 就绪前没有可执行函数——
// 一律 (nil, false)。「在册」请用 Has。接 C5：Reconcile 换 ImplSource 后，对运行期来源的
// impl 调 applySlot RPC，此时本方法或由 C5 的另一实现替代（本 WP 不接）。
func (r *runtimeSource) Impl(implID string) (Impl, bool) {
	return nil, false
}

func (r *runtimeSource) Has(implID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.specs[implID]
	return ok
}

func (r *runtimeSource) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.ids))
	copy(out, r.ids)
	return out
}

// Spec 返回某 impl_id 的运行期声明（来源路由/诊断用）。不在册 → (zero, false)。
func (r *runtimeSource) Spec(implID string) (RuntimeImplSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sp, ok := r.specs[implID]
	return sp, ok
}

// ——— 合并来源（编译期 ∪ 运行期）———

// combinedSource 把多个 ImplSource **并列**成一个来源：任一来源 Has 即在册（§17.4）。
//
// **判定语义**：`Has(id)` = 至少一个子来源在册；`Impl(id)` 按顺序（通常是 编译期→运行期）
// 返回第一个有可执行函数的来源（运行期来源当前恒 false，故实际只返回编译期实现——执行接 C5）。
// `IDs()` = 各子来源 id 的去重并集（升序）。
//
// 关键：Combined 只把**已声明来源**里的 id 视为合法。它不会因为 id 长得像个 impl 就编造在册——
// 白名单不退化这条底线由「每个子来源本身是封闭表」保证，Combined 只是并集，不打开新入口。
type combinedSource struct {
	sources []ImplSource
}

// CombinedSource 组合编译期与运行期来源，供白名单判定（svcapp/Reconcile）与 WP-C5 使用。
//
// 传参顺序即 `Impl()` 的查找优先级（建议 `CombinedSource(CompiledSource(), runtime)`：编译期优先，
// 因为内置 app 的实现已在二进制里可直接执行）。nil 来源被忽略；全 nil → 一个空来源（不 panic）。
func CombinedSource(sources ...ImplSource) ImplSource {
	kept := make([]ImplSource, 0, len(sources))
	for _, s := range sources {
		if s != nil {
			kept = append(kept, s)
		}
	}
	return &combinedSource{sources: kept}
}

func (c *combinedSource) Impl(implID string) (Impl, bool) {
	for _, s := range c.sources {
		if fn, ok := s.Impl(implID); ok {
			return fn, true
		}
	}
	return nil, false
}

func (c *combinedSource) Has(implID string) bool {
	for _, s := range c.sources {
		if s.Has(implID) {
			return true
		}
	}
	return false
}

func (c *combinedSource) IDs() []string {
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, s := range c.sources {
		for _, id := range s.IDs() {
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// ——— 来源路由（T-C2.2）———

// SourceKind 标识一个 impl 的来源类别（决定执行路径：编译期直接调用 / 运行期走 host RPC）。
type SourceKind string

const (
	// SourceCompiled 编译期来源：内置 app 的 impl，实现已在二进制里，可直接执行。
	SourceCompiled SourceKind = "compiled"
	// SourceRuntime 运行期来源：第三方 apppkg 的 impl，实现要经 C3 host 子进程执行。
	SourceRuntime SourceKind = "runtime"
)

// RouteSource 报告某 impl_id 属于哪个来源——即 T-C2.2 的「来源路由」。
//
// 判定顺序：编译期优先（内置 app 的实现已在二进制里可直接执行）；编译期不在册再看运行期。
// 都不在册 → 返回 ("", false)：**未在册**（调用方据此拒绝，绝不编造来源）。
//
// 说明「一个 app 的 slot impl_id 属哪一类由 app 定义来源决定」（§17.4）：
//   - 内置 app（无 manifest、走 App 定义）：其 impl 由 A+Slot 的 Go 包 RegisterImpl 登记 → compiled；
//   - 从 apppkg 目录装载、带 manifest 的第三方 app：其 impl 来自包的 provides.impls → runtime。
//
// 本函数不区分「这个 id 属于哪个 app」，只据**在册情况**路由：同一 impl_id 若同时存在编译期与
// 运行期声明，编译期优先（编译期已能执行，无需 host）。按 app 归属的精确路由（装配根知道我正
// 在展开哪个 app 的 slot）由装配根在展开 Slot 时结合「该 app 是否来自包目录」判定——本函数提供
// 的是契约层可复用的「id → 来源」判定，二者一致时以本函数为准。
func (c *combinedSource) RouteSource(implID string) (SourceKind, bool) {
	for _, s := range c.sources {
		if s.Has(implID) {
			return kindOf(s), true
		}
	}
	return "", false
}

// kindOf 把一个具体 ImplSource 归到 SourceKind。未知实现按运行期保守处理（要经 host）——
// fail-closed 方向正确：宁可多走隔离 host，也不把陌生来源当编译期直接在内核进程执行。
func kindOf(s ImplSource) SourceKind {
	switch s.(type) {
	case compiledSource:
		return SourceCompiled
	case *runtimeSource:
		return SourceRuntime
	case *combinedSource:
		// 合并来源本身不作为叶子被 RouteSource 查询（RouteSource 在 combined 上逐个查子来源），
		// 若某组合把另一个 combined 当子来源，这里保守按运行期处理。
		return SourceRuntime
	default:
		return SourceRuntime
	}
}

// RouteByApps 是**按 app 来源**路由的辅助：给定「来自 apppkg 目录的 app id 集合」，报告某 impl
// 属于运行期包还是编译期（T-C2.2 的装配期判定形态）。
//
// 语义：implID 若来自某个包目录 app 的声明（该 impl 的 PackageID ∈ loadedAppIDs），归运行期；
// 否则归编译期。这与 RouteSource 的「id 在册在哪」方向一致，只是**以 app 归属为第一判据**（一个
// app 带 manifest → 它的 impl 属运行期，即便同 id 碰巧编译期也有——精确按归属而非按 id）。
//
// 实现要点：需要运行期来源的 Spec 拿到 PackageID。传 nil 运行期来源或 impl 不在运行期 → 看编译期。
// 都不在册 → ("", false)。
func RouteByApps(compiled ImplSource, runtime ImplSource, loadedAppIDs map[string]bool, implID string) (SourceKind, bool) {
	// 先看运行期：若该 impl 由某个已装载包 app 声明，且该包 id 在 loadedAppIDs 里 → 运行期。
	if rs, ok := runtime.(*runtimeSource); ok {
		if sp, ok := rs.Spec(implID); ok && loadedAppIDs[sp.PackageID] {
			return SourceRuntime, true
		}
	}
	// 再看编译期在册。
	if compiled != nil && compiled.Has(implID) {
		return SourceCompiled, true
	}
	// 运行期在册但该包 app 未在 loadedAppIDs（或非 runtimeSource 实现）→ 仍算运行期（宽松在册）。
	if runtime != nil && runtime.Has(implID) {
		return SourceRuntime, true
	}
	return "", false
}
