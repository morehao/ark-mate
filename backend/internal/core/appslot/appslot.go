// Package appslot 定义「App Slot」扩展点契约——应用携带的**后端行为**入口（编译期内置插件）。
//
// 定位（设计 .dsh/docs/specs/2026-11-09-app-system-design.md §5.3/§5.4）：App Slot 类比 magpie L1
// （内置插件 + 数据驱动开关），**不是运行期装载陌生代码**。各 app 的 Go 包在 `init` 里用
// `RegisterImpl` 登记自己贡献的实现、用 `RegisterEntity` 登记自己拥有的实体种类；app 定义里的
// `slots[].impl_id` / `entities[].kind` 只能**引用**这些登记过的 id/kind——数据只选「开关哪些已编入
// 的实现」，不能凭空指定一个实现。这条「数据只选 impl、不提供 impl」是本包存在的理由，也是
// `TestAppSlotImplWhitelisted` 要守住的底线（防 Slot 退化成数据驱动的服务定位图）。
//
// 本包只做**契约与登记白名单**（WP2）。运行时（Reconcile 依赖排序 / 失败隔离 / OnDispose 逆序撤销）
// 属 WP6，另在本包追加运行时文件——**不在这里提前写**。
package appslot

import (
	"sort"
	"sync"
)

// Impl 是一个已编入的后端行为实现：装配期由 Reconcile 调用（WP6），失败隔离按 slot 类别定。
//
// 签名取设计 §5.4 的 `Impl func(Ctx) error`：实现通过 Ctx 提供/取用服务、登记撤销钩子。
type Impl func(Ctx) error

// Ctx 是 Impl 执行时拿到的上下文（设计 §5.4）：提供服务给其它 slot、取依赖、登记撤销栈。
//
// 只有接口、无具体实现——具体 Ctx 由 WP6 的运行时提供（它承担依赖排序与撤销栈）。本包只钉语义。
type Ctx interface {
	// Provide 把一个服务以 name 提供给其它 slot（供其 Requires 取用）。
	Provide(name string, v any)
	// Service 取一个依赖服务（应是 Core 服务或别的 slot 已 Provide 的）。
	Service(name string) any
	// OnDispose 登记一个撤销函数；停用 app 时**逆序**执行（「关得干净」G6）。
	OnDispose(f func())
}

// EntitySpec 是实体型应用在实体注册表里登记的一条归属声明（设计 §5.2.1）。
//
// 由各实体型 app 的包在 init 里 `RegisterEntity(kind, spec)` 登记；app 定义的 `entities[].kind`
// 必须在册。字段含义：
//
//	Table        该实体落哪张物理表（如 "tickets"）
//	IDPrefix     实体 id 前缀（如 "tk_"），用于 id 生成（WP3 落库用）
//	OwnerColumn  反向查归属的列名（如 "app_id"）
//	OnAppDelete  删 app 时对实体数据的处置："archive"（级联归档）/ "keep"（保守保留，默认）
type EntitySpec struct {
	Table       string
	IDPrefix    string
	OwnerColumn string
	OnAppDelete string
}

// OnAppDeleteKeep 缺省的删 app 处置：保留实体数据（保守，避免误删用户数据，设计 §5.2.1）。
const OnAppDeleteKeep = "keep"

var (
	mu       sync.RWMutex
	impls    = map[string]Impl{}
	entities = map[string]EntitySpec{}
)

// RegisterImpl 登记一个已编入的实现（编译期白名单的唯一来源）。由各 app 包在 init 里调用。
//
// implID 全局唯一；**重复登记 panic**（同 id 两种实现是配置事故，静默覆盖会让「谁生效」取决于
// init 顺序——这类静默失效宁可启动即炸）。空 implID 或 nil fn 同样 panic。
func RegisterImpl(implID string, fn Impl) {
	if implID == "" {
		panic("appslot: RegisterImpl 收到空 implID")
	}
	if fn == nil {
		panic("appslot: RegisterImpl(" + implID + ") 收到 nil Impl")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := impls[implID]; dup {
		panic("appslot: impl_id 重复登记: " + implID)
	}
	impls[implID] = fn
}

// RegisterEntity 登记一个实体种类（编译期白名单）。由各实体型 app 的包在 init 里调用。
//
// kind 全局唯一；**重复登记 panic**（同上：同 kind 两个表定义是事故）。空 kind 同样 panic。
// OnAppDelete 为空时归一为 "keep"（保守缺省）。
func RegisterEntity(kind string, spec EntitySpec) {
	if kind == "" {
		panic("appslot: RegisterEntity 收到空 kind")
	}
	if spec.OnAppDelete == "" {
		spec.OnAppDelete = OnAppDeleteKeep
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := entities[kind]; dup {
		panic("appslot: entity kind 重复登记: " + kind)
	}
	entities[kind] = spec
}

// KnownImpls 返回当前登记的全部实现（**只读快照**：返回新 map，调用方改动不影响注册表）。
//
// 装配期按 app 定义的 slots[].impl_id 到这张表查实现（WP6）。返回快照而非共享 map，是为了
// 防止调用方顺手往返回值里塞一个实现——那会让白名单退化成"谁都能加"的开放表。
func KnownImpls() map[string]Impl {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]Impl, len(impls))
	for k, v := range impls {
		out[k] = v
	}
	return out
}

// KnownEntities 返回当前登记的全部实体种类（**只读快照**，理由同 KnownImpls）。
func KnownEntities() map[string]EntitySpec {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]EntitySpec, len(entities))
	for k, v := range entities {
		out[k] = v
	}
	return out
}

// ImplIDs 返回已登记 impl_id 的升序列表（诊断/守卫用：一份稳定的白名单视图）。
func ImplIDs() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(impls))
	for k := range impls {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EntityKinds 返回已登记实体 kind 的升序列表（诊断/守卫用）。
func EntityKinds() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(entities))
	for k := range entities {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
