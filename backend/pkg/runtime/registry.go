package runtime

import (
	"fmt"
	"sort"
	"strings"
)

// Registry 是 runtime adapter 注册表（map[string]Spec）。
type Registry struct {
	specs map[string]Spec
}

// NewRegistry 从一组 Spec 构建注册表（按 ID 去重，后注册覆盖）。
func NewRegistry(specs ...Spec) *Registry {
	r := &Registry{specs: make(map[string]Spec, len(specs))}
	for _, s := range specs {
		r.specs[s.ID] = s
	}
	return r
}

// DefaultRegistry 返回内置 runtime 的注册表（opencode / deepseekharness）。
// adapter 子包只提供纯数据，父包在此汇入统一 Spec（避免父子 import 环）。
func DefaultRegistry() *Registry {
	oc := opencodeSpec()
	dh := deepseekharnessSpec()
	return NewRegistry(oc, dh)
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
