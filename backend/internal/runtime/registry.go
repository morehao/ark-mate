package runtime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/resource"
)

// Registry 是 runtime adapter 注册表（`map[string]Spec` = **声明表**，加**三张可执行端口表**：
// ① 清单同步 `syncers` ② 原生权限 `nativePermissions` ③ 模型值投影 `modelValues`）。
//
// **下文说「第 N 张表」一律指这三张可执行表**（不含 `Spec`）——它是这套 adapter 层唯一的接口面。
//
// **为什么可执行半场不进 `Spec`**（ADR-2）：`Spec` 是**声明式半场**——纯数据，会被打印、
// 被 `/meta` 与诊断读出、被测试断言。混入可执行对象会让「描述」与「行为」同源。
// 另外两条理由：注册时机不同（`Spec` 在 `AllBuiltinSpecs()` / `ProductRegistry()` 里静态构造，
// `Syncer` / `NativePermissionAdapter` 要按 runtime 实例与配置构造），以及**测试要能只注册 stub**
// （`TestSyncerContractEmptySet` 就靠这张表，否则每个用例都得伪造一份完整 `Spec`）。
//
// **每加一张表都要问一次"这张表能不能只有两三个键"**：表的形状与数量就是这套 adapter 层的
// 全部接口面，一份新底座要挂几张表、每张表的签名是什么，直接决定"接一个底座要动几处"。
type Registry struct {
	specs   map[string]Spec
	syncers map[string]resource.Syncer
	// nativePermissions 第二张可执行端口表：该底座的「原生权限投影」实现（见 native_permission.go）。
	nativePermissions map[string]NativePermissionAdapter
	// modelValues 第三张可执行端口表：该底座的「模型值生成」实现（见 model_value.go）。
	modelValues map[string]ModelValueAdapter
	// toolFailures 各 runtime 的**工具失败归类**（第四张可执行端口表，见 tool_failure.go）。
	toolFailures map[string]ToolFailureClassifier
	// identities 第五张可执行端口表：该底座的「产品身份交付」实现（见 identity.go）。
	//
	// 为什么单独一张表而不是塞进 `Syncer`：身份不是资源（不落库、没有清单、没有凭据），
	// 它的输入只有一个**部署级常量**；塞进同步端口会让 `SyncModels` 多一个与模型无关的职责，
	// 而"空清单必撤回"那条契约会莫名其妙地作用到身份上。
	identities map[string]IdentityAdapter
	// adapters 构造本注册表的那组 adapter（`NewAdapterRegistry` 记录）。
	//
	// 为什么注册表要记住它们：隔离 env 必须在**应用之后**再登记一次可执行半场（落点里的
	// `${VAR}` 这时才解析得出来），而重登记只能由 adapter 自己做。记在注册表里而不是全局变量，
	// 就不会出现"两个注册表共用一份 adapter 清单"的串台。
	adapters []Adapter
}

// NewRegistry 从一组 Spec 构建注册表（按 ID 去重，后注册覆盖）。
func NewRegistry(specs ...Spec) *Registry {
	r := &Registry{
		specs:             make(map[string]Spec, len(specs)),
		syncers:           map[string]resource.Syncer{},
		nativePermissions: map[string]NativePermissionAdapter{},
		modelValues:       map[string]ModelValueAdapter{},
		toolFailures:      map[string]ToolFailureClassifier{},
		identities:        map[string]IdentityAdapter{},
	}
	for _, s := range specs {
		r.specs[s.ID] = s
	}
	return r
}

// RegisterSyncer 登记某 runtime 的同步实现（第一张可执行端口表；同 ID 后注册覆盖）。
func (r *Registry) RegisterSyncer(id string, s resource.Syncer) {
	if r == nil {
		return
	}
	if r.syncers == nil {
		r.syncers = map[string]resource.Syncer{}
	}
	r.syncers[id] = s
}

// Syncer 取某 runtime 的同步实现（`ok=false` = 该 runtime 未声明交付面 → Outcome{unsupported}）。
func (r *Registry) Syncer(id string) (resource.Syncer, bool) {
	if r == nil || r.syncers == nil {
		return nil, false
	}
	s, ok := r.syncers[id]
	return s, ok && s != nil
}

// RegisterIdentity 登记某 runtime 的产品身份交付实现（第五张可执行端口表）。
func (r *Registry) RegisterIdentity(id string, a IdentityAdapter) {
	if r == nil {
		return
	}
	if r.identities == nil {
		r.identities = map[string]IdentityAdapter{}
	}
	r.identities[id] = a
}

// Identity 取某 runtime 的身份交付实现（`ok=false` = 该底座不承载产品身份）。
//
// 不可用的语义是**如实降级**：调用方据此只走首轮块（或干脆无身份），而不是假装交付过。
// 它不报错——"这个底座没有身份面"与"交付失败了"是两件事。
func (r *Registry) Identity(id string) (IdentityAdapter, bool) {
	if r == nil || r.identities == nil {
		return nil, false
	}
	a, ok := r.identities[id]
	return a, ok && a != nil
}

// DeliveryFor 返回**绑定到某 runtime** 的窄交付面（`resource.Delivery`）。
//
// 为什么要绑定：端口接口只有三个方法（`SetEnv(key,value)` / `SetArg(flag,value)` / `SetArgs(flag,values)`），
// 而注册表的方法要带 runtimeID。adapter 不该知道自己在注册表里的键是什么——那是父包的事，
// 也是「父子 import 环」这条约束的另一面（adapter 拿不到注册表，只拿到这个三方法的窗口）。
func (r *Registry) DeliveryFor(runtimeID string) resource.Delivery {
	return &runtimeDelivery{reg: r, id: runtimeID}
}

// runtimeDelivery 把 `resource.Delivery` 的两个方法落到指定 runtime 的 Spec 上。
type runtimeDelivery struct {
	reg *Registry
	id  string
}

// SetEnv 同键替换该项启动 env；value 为空 = 撤销（从 Launch.Env 里摘掉）。
func (d *runtimeDelivery) SetEnv(key, value string) error {
	if d == nil || d.reg == nil {
		return nil
	}
	if strings.TrimSpace(value) == "" {
		return d.reg.unsetLaunchEnv(d.id, key)
	}
	return d.reg.SetLaunchEnv(d.id, key, value)
}

// SetArg 覆盖该 runtime 的启动参数（`flag` 在 `Launch.Args` 里加上/替换；value 为空 = 摘掉）。
//
// 为什么是「按 flag 替换」而不是「整份覆盖」：adapter 只知道**自己那一个**参数（dsh 的
// `--patch`），整份覆盖会让 adapter 有权删掉平台别的启动参数——那不是它的知识范围。
func (d *runtimeDelivery) SetArg(flag, value string) error {
	if d == nil || d.reg == nil {
		return nil
	}
	values := []string(nil)
	if value != "" {
		values = []string{value}
	}
	return d.SetArgs(flag, values)
}

// SetArgs 覆盖**可重复**启动参数的全部取值（按 values 顺序；空 = 摘掉该 flag 的所有出现）。
//
// 与 `SetArg` 是同一条落点（`replaceArgs`），区别只在"这个 flag 的完整取值列表"由谁给：
// 由一处给（`SetArgs`）时，两条独立交付面可以共用同一个 flag 而不互相吃掉。
func (d *runtimeDelivery) SetArgs(flag string, values []string) error {
	if d == nil || d.reg == nil {
		return nil
	}
	spec, ok := d.reg.specs[d.id]
	if !ok {
		return fmt.Errorf("runtime %q 未注册", d.id)
	}
	next := replaceArgs(spec.Launch.Args, flag, values)
	return d.reg.SetLaunchArgs(d.id, next)
}

// matchesFlag 判断一个 token 是不是该 flag（两种形态：`--flag` 与 `--flag=value`）。
//
// 为什么不能只比 `==`：旧实现只写了"值也可能在 `=` 后面"的注释，却按 `==` 匹配——于是
// `--patch=/x` 永远不会被摘掉。那种参数在底座侧等同一条额外覆盖层，表现为"改了但像是没改"。
func matchesFlag(token, flag string) bool {
	return token == flag || strings.HasPrefix(token, flag+"=")
}

// replaceArgs 在 args 里按 flag 增删**一组**参数（values 为空 = 删掉该 flag 的所有出现）。
//
// 三条形态都认：`--flag <value>`（值紧随其后）与 `--flag=<value>`。摘除时**所有出现**都摘——
// 只摘第一条会让第二次同步留下一条重复参数，而重复的 `--patch` 在底座侧是"叠加两层"，
// 表现为"改了但像是没改"（旧层仍压在上面）。
func replaceArgs(args []string, flag string, values []string) []string {
	out := make([]string, 0, len(args)+2*len(values))
	for i := 0; i < len(args); i++ {
		if !matchesFlag(args[i], flag) {
			out = append(out, args[i])
			continue
		}
		// 值可能是紧随其后的一个 token（`--patch <path>`），也可能是 `--patch=<path>`（后者
		// 自带值，不需要多看一个 token）。
		if !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
		}
	}
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			continue
		}
		out = append(out, flag, v)
	}
	return out
}

// 注册表的**构造入口**不在本文件：构建期固定底座之后有两张语义不同的入口，
// 分别见 build.go 的 `ProductRegistry`（产品装配：只注册构建期选中的那一个）与
// `AllBuiltinRegistry`（全部内置：测试 / 诊断 / 能力矩阵守卫用）。
// adapter 子包只提供纯数据与端口实现，父包汇入统一 Spec（避免父子 import 环）；
// **两张可执行表的登记在 `registerBuiltinPorts`**（它引用各 adapter 的 `NewSyncer` 与原生权限实现）。

// Get 按 ID 取 Spec。
func (r *Registry) Get(id string) (Spec, bool) {
	s, ok := r.specs[id]
	return s, ok
}

// Register 追加/覆盖一个 runtime。
func (r *Registry) Register(s Spec) { r.specs[s.ID] = s }

// SetLaunchEnv 覆盖/追加某 runtime 的一个启动环境键（**同键替换**，不追加第二条）。
//
// 为什么必须"同键替换"而\不是直接 append：`mergeEnv` 的语义是"先出现者胜"
// （用户在 shell 里导出的键不能盖掉平台显式注入的值），所以把新值追加到末尾**不会生效**
// ——隔离表里那条**同名的空值**（`<变量>=`）会把我们后追加的内容吃掉。
//
// 谁用它：T5 的 `env_content` 通道（把生成的配置正文经子进程 env 交给底座，dsh 走的是
// 文件通道，见各 adapter 的凭据/清单落点声明）在物化时把生成的字节注入子进程 env。
// 只影响**此后新拉起的进程**（这正是 T5 的"下一个进程生效"语义）。
func (r *Registry) SetLaunchEnv(runtimeID, key, value string) error {
	if r == nil {
		return nil
	}
	spec, ok := r.specs[runtimeID]
	if !ok {
		return fmt.Errorf("runtime %q 未注册", runtimeID)
	}
	env := make([]string, 0, len(spec.Launch.Env)+1)
	replaced := false
	for _, kv := range spec.Launch.Env {
		name, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(strings.TrimSpace(name), key) {
			env = append(env, key+"="+value)
			replaced = true
			continue
		}
		env = append(env, kv)
	}
	if !replaced {
		env = append(env, key+"="+value)
	}
	spec.Launch.Env = env
	r.specs[runtimeID] = spec
	return nil
}

// unsetLaunchEnv 撤销某 runtime 的一个启动 env（键不在 → 幂等无操作）。
//
// 它是 `resource.Delivery.SetEnv(key, "")` 的落点，也是契约③「空集必撤回」在启动面上的实现：
// 只把它设成空串**不算撤回**（空串仍是一条 env 记录，语义上"显式给了空值"），必须真的摘掉。
func (r *Registry) unsetLaunchEnv(runtimeID, key string) error {
	if r == nil {
		return nil
	}
	spec, ok := r.specs[runtimeID]
	if !ok {
		return fmt.Errorf("runtime %q 未注册", runtimeID)
	}
	env := make([]string, 0, len(spec.Launch.Env))
	for _, kv := range spec.Launch.Env {
		name, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(strings.TrimSpace(name), key) {
			continue
		}
		env = append(env, kv)
	}
	spec.Launch.Env = env
	r.specs[runtimeID] = spec
	return nil
}

// SetLaunchArgs 覆盖某 runtime 的启动参数（T5 的 `cli_patch_arg` 通道用它按 overlay 的存在性
// 增删 `--patch <绝对路径>`）。
//
// 只影响**此后新拉起的进程**：这正是"下一个进程生效"的语义，也是"坏了就去掉参数"那条恢复路径的实现。
func (r *Registry) SetLaunchArgs(runtimeID string, args []string) error {
	if r == nil {
		return nil
	}
	spec, ok := r.specs[runtimeID]
	if !ok {
		return fmt.Errorf("runtime %q 未注册", runtimeID)
	}
	spec.Launch.Args = append([]string(nil), args...)
	r.specs[runtimeID] = spec
	return nil
}

// List 返回全部 Spec（按 ID 排序，保证稳定输出）。
func (r *Registry) List() []Spec {
	ids := make([]string, 0, len(r.specs))
	for id := range r.specs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Spec, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.specs[id])
	}
	return out
}

// 注意：`GET /runtimes` 已随「用户不能选 runtime」（2026-10-10 产品约束）删除，随它一起的
// `ListItems() []ListItem` 出口也在这里删掉了——**这是刻意的**：静态 `Caps`（尤其
// `Caps.Images`）唯一剩下的读法就是"把保守基线当能力门禁"，而那正是 ADR-14 判定为错误的那一侧
// （真值只有连接期 initialize 的 `promptCapabilities` 知道）。没有出口 = 没有误用路径，
// 由 archtest 的 `TestStaticCapsImagesHasNoConsumer` 钉住。

// IDs 已注册 runtime id（升序）。
//
// 与 `List()` 的区别只在"要不要 Spec"：调用方往往只要 id（交付矩阵要按 runtime 逐条展开、
// 医生要枚举全部内置底座），拿一整份 Spec 再取 id 会让人以为"没读 Spec 就是没判据"。
// **升序是刻意的**：它经 `/providers`、`/library` 这类读面外露，顺序漂了会让响应字节随 map
// 迭代顺序抖动。
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.specs))
	for id := range r.specs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// McpTransportsFor 该 runtime 声明承载的 MCP 传输。
//
// **未声明 → 仅 stdio**（ACP 要求所有 agent 至少支持 stdio）；未知 runtime → `ok=false`
// （调用方据此区分"这个底座不在册"与"它什么都不承载"——两者混一起会让不可达的 runtime 报成可达）。
//
// 为什么这条约定归注册表：它有两个消费面（Library 的逐 runtime 可达性、MCP 注入时的传输判据），
// 各写一遍必然漂移，而漂移的表现是"面板说可达、注入时被跳过"。它读的又只有 Spec。
//
// 用字面量 `"stdio"` 而不是 `capability.TransportStdio`：`pkg/runtime` **零依赖 `pkg/capability`**
// （P-1 定的包边界，见 projection.go 顶部），同包 `adapters.go` 的 Spec 声明也是这么写的。
func (r *Registry) McpTransportsFor(id string) ([]string, bool) {
	spec, ok := r.Get(id)
	if !ok {
		return nil, false
	}
	if len(spec.McpTransports) == 0 {
		return []string{"stdio"}, true
	}
	return spec.McpTransports, true
}

// Require 返回 Spec；未知 ID 返回可读错误（供上层做参数校验）。
func (r *Registry) Require(id string) (Spec, error) {
	s, ok := r.Get(id)
	if !ok {
		return Spec{}, fmt.Errorf("unknown runtime %q", id)
	}
	return s, nil
}
