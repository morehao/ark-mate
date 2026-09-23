package runtime

import (
	"fmt"
	"sort"
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
