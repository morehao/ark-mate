package runtime

import (
	"testing"

)

// TestBuiltinPortsAreRegistered 内置 adapter 的**可执行半场**必须真的被登记进注册表。
//
// 为什么值得一条独立判据：`Spec` 与执行表是**两张分开的表**（ADR-2），于是"adapter 写了实现"
// 与"装配时把它登记上了"可以各自成立；漏了后者的表现是运行期那句
// 「该 runtime 无原生权限开关（仅平台拦截面）」——它**看起来像一条正常结论**，
// 而不是故障。这张表是"今天有哪些底座声明了原生权限面"的事实清单：新增 adapter 时必须在此登记
// （写清它有还是没有），否则本判据红。
func TestBuiltinPortsAreRegistered(t *testing.T) {
	// 这张表是"今天有哪些内置底座声明了原生权限面"的事实清单：**没有原生权限面的底座也要登记**
	// （值为 false），否则"adapter 实现了但没人登记"与"这个底座本来就没有"在运行期长得一模一样。
	wantNative := map[string]bool{
		DefaultRuntimeID: true,
	}
	all := fixtureRegistry()
	specs := fixtureSpecs()
	if len(specs) == 0 {
		t.Fatal("内置 adapter 一个都没扫到：判据已失效（fail-closed）")
	}
	for _, spec := range specs {
		if _, ok := all.NativePermission(spec.ID); ok != wantNative[spec.ID] {
			t.Errorf("AllBuiltinRegistry 的 %s 原生权限端口：登记=%v，期望=%v（新增 adapter 请更新本表）",
				spec.ID, ok, wantNative[spec.ID])
		}
	}
	// 反向：表里列了、但内置清单里没有的 id 说明表已过期（判据会静默空转）。
	for id := range wantNative {
		if _, ok := all.Get(id); !ok {
			t.Errorf("本表列了 %s，但它不在 fixtureSpecs() 里——表已过期", id)
		}
	}
}

// TestProductRegistryCarriesPorts 产品装配（只注册构建期选中的那一个）同样要带上执行半场，
// 且**只带选中的那个**：未选中的底座在注册表里不存在 → 内核如实报"无原生权限开关"。
func TestProductRegistryCarriesPorts(t *testing.T) {
	saved := BuildRuntime
	t.Cleanup(func() { BuildRuntime = saved })

	for _, tc := range []struct {
		injected string
		selected string
	}{
		{"", DefaultRuntimeID},
		{DefaultRuntimeID, DefaultRuntimeID},
	} {
		BuildRuntime = tc.injected
		reg, err := ProductRegistry([]Adapter{stubAdapter{spec: fixtureSpec()}}, nil)
		if err != nil {
			t.Fatalf("注入 %q 时产品装配失败: %v", tc.injected, err)
		}
		if got := SelectedRuntime(); got != tc.selected {
			t.Fatalf("注入 %q 选中 %q，期望 %q", tc.injected, got, tc.selected)
		}
		if _, ok := reg.NativePermission(tc.selected); !ok {
			t.Errorf("选中 %s，但它的原生权限端口没被登记（产品装配漏了一条执行表）", tc.selected)
		}
		for _, other := range []string{DefaultRuntimeID} {
			if other == tc.selected {
				continue
			}
			if _, ok := reg.Get(other); ok {
				t.Errorf("选中 %s 时不该注册 %s", tc.selected, other)
			}
			if _, ok := reg.NativePermission(other); ok {
				t.Errorf("选中 %s 时不该登记 %s 的原生权限端口", tc.selected, other)
			}
		}
	}
}
