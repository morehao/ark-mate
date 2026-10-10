package runtime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/pkg/resource"
)

// Registry 是 runtime adapter 注册表（map[string]Spec + 第二张表 syncer）。
//
// **为什么 syncer 不进 `Spec`**（ADR-2）：`Spec` 是**声明式半场**——纯数据，会被打印、
// 被 `/meta` 与诊断读出、被测试断言。混入可执行对象会让「描述」与「行为」同源。
// 另外两条理由：注册时机不同（`Spec` 在 `DefaultRegistry()` 里静态构造，`Syncer` 要按 runtime
// 实例与配置构造），以及**测试要能只注册 stub**（`TestSyncerContractEmptySet` 就靠这张表，
// 否则每个用例都得伪造一份完整 `Spec`）。
type Registry struct {
	specs   map[string]Spec
	syncers map[string]resource.Syncer
}

// NewRegistry 从一组 Spec 构建注册表（按 ID 去重，后注册覆盖）。
func NewRegistry(specs ...Spec) *Registry {
	r := &Registry{specs: make(map[string]Spec, len(specs)), syncers: map[string]resource.Syncer{}}
	for _, s := range specs {
		r.specs[s.ID] = s
	}
	return r
}

// RegisterSyncer 登记某 runtime 的同步实现（第二张表；同 ID 后注册覆盖）。
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

// DeliveryFor 返回**绑定到某 runtime** 的窄交付面（`resource.Delivery`）。
//
// 为什么要绑定：端口接口只有两个方法（`SetEnv(key,value)` / `SetArg(flag,value)`），
// 而注册表的方法要带 runtimeID。adapter 不该知道自己在注册表里的键是什么——那是父包的事，
// 也是「父子 import 环」这条约束的另一面（adapter 拿不到注册表，只拿到这个两方法的窗口）。
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
	spec, ok := d.reg.specs[d.id]
	if !ok {
		return fmt.Errorf("runtime %q 未注册", d.id)
	}
	next := replaceArg(spec.Launch.Args, flag, value)
	return d.reg.SetLaunchArgs(d.id, next)
}

// replaceArg 在 args 里按 flag 增删一个参数（value 为空 = 删掉 flag 与它的值）。
func replaceArg(args []string, flag, value string) []string {
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		if args[i] != flag {
			out = append(out, args[i])
			continue
		}
		// 值可能是紧随其后的一个 token（`--patch <path>`），也可能是 `--patch=<path>`。
		if !strings.Contains(args[i], "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
		}
	}
	if value != "" {
		out = append(out, flag, value)
	}
	return out
}

// DefaultRegistry 返回内置 runtime 的注册表（opencode / deepseekharness）。
// adapter 子包只提供纯数据与端口实现，父包在此汇入统一 Spec（避免父子 import 环）。
//
// **Syncer 的登记在 `registerBuiltinSyncers`**（M2/M3 落地）：它引用各 adapter 的
// `NewSyncer`，而那条装配边要等生成器从 `internal/manager` 下沉到位才存在。
func DefaultRegistry() *Registry {
	oc := opencodeSpec()
	dh := deepseekharnessSpec()
	r := NewRegistry(oc, dh)
	registerBuiltinSyncers(r)
	return r
}

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
// ——隔离表里那条空的 `OPENCODE_CONFIG_CONTENT=` 会把我们的内容吃掉。
//
// 谁用它：T5 的 `env_content` 通道（opencode）在物化时把生成的 JSON 注入子进程 env。
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

// ListItems 返回 /runtimes 列表项（控制面用）。
func (r *Registry) ListItems() []ListItem {
	specs := r.List()
	out := make([]ListItem, 0, len(specs))
	for _, s := range specs {
		out = append(out, ListItem{
			ID:      s.ID,
			Name:    s.Name,
			Version: s.Version,
			Caps:    s.Caps,
		})
	}
	return out
}

// Require 返回 Spec；未知 ID 返回可读错误（供上层做参数校验）。
func (r *Registry) Require(id string) (Spec, error) {
	s, ok := r.Get(id)
	if !ok {
		return Spec{}, fmt.Errorf("unknown runtime %q", id)
	}
	return s, nil
}
