package runtime

// Adapter 是一个 runtime 底座的**装配契约**（v3.1 §3.2.1 规则 5）。
//
// 为什么是"两方法接口 + 显式构造"而不是 Driver 全局注册表：
//   - **Spec()** 给出的声明式半场是纯数据（会被 `/meta`、诊断与守卫读出）；
//   - **RegisterPorts** 把可执行半场登记进注册表的五张端口表（syncer / 原生权限 / 模型值 /
//     工具失败归类 / 产品身份）。落点解析之类"只有 adapter 知道"的知识留在 adapter 里。
//
// 装配根显式 `dsh.New()` → `NewAdapterRegistry(...)`：**只有 `internal/bootstrap/assembly` 认识具体
// adapter**，`internal/runtime` 不认识任何实现（`TestRuntimeDoesNotImportRuntimeAdaptersV31`
// 钉住这条）。第二个底座出现时在装配根加一个 `switch`，不引入 init、不写匿名 import。
type Adapter interface {
	// Spec 该底座的静态描述（ID 决定注册表键）。
	Spec() Spec
	// RegisterPorts 把可执行半场登记进 r（同 ID 覆盖，幂等）。
	RegisterPorts(r *Registry)
}

// NewAdapterRegistry 用一组 adapter 构造注册表：先按 Spec 注册声明，再让它登记端口。
//
// 顺序是语义的：`RegisterPorts` 只登记**已在册**的 id（见各 adapter 实现），所以 Spec 必须先行。
func NewAdapterRegistry(adapters ...Adapter) *Registry {
	r := NewRegistry()
	for _, a := range adapters {
		if a == nil {
			continue
		}
		r.adapters = append(r.adapters, a)
		if a == nil {
			continue
		}
		spec := a.Spec()
		r.Register(spec)
		a.RegisterPorts(r)
	}
	return r
}

// SpecsOf 取一组 adapter 的声明（诊断/守卫与装配共用同一份构造，避免各抄一遍字段）。
func SpecsOf(adapters ...Adapter) []Spec {
	out := make([]Spec, 0, len(adapters))
	for _, a := range adapters {
		if a == nil {
			continue
		}
		out = append(out, a.Spec())
	}
	return out
}
