package appslot

import (
	"reflect"
	"testing"
)

// ——— WP-C2：双来源并存 / 运行期未在册仍拒绝 / 来源路由 ———

// TestImplSourceDualSource 双来源并存：编译期与运行期各在册一个 impl，
// CombinedSource.Has 对两者都为真；都不在册的 id 为假。
func TestImplSourceDualSource(t *testing.T) {
	const compiledID = "c2test.compiled.only"
	RegisterImpl(compiledID, func(Ctx) error { return nil })

	runtime := NewRuntimeSource([]RuntimeImplSpec{
		{ImplID: "c2test.runtime.only", Kind: "slot", PackageID: "vendor-app"},
	})
	combined := CombinedSource(CompiledSource(), runtime)

	if !combined.Has(compiledID) {
		t.Errorf("编译期在册 impl 应 Has=true")
	}
	if !combined.Has("c2test.runtime.only") {
		t.Errorf("运行期在册 impl 应 Has=true（双来源并存）")
	}
	if combined.Has("c2test.not.anywhere") {
		t.Errorf("都不在册的 impl 必须 Has=false（白名单底线）")
	}
}

// TestRuntimeImplNotExecutableYet 运行期来源 Impl() 在 C3 host 就绪前一律 (nil,false)——
// 本 WP 只建抽象，不假装能执行；在册靠 Has。
func TestRuntimeImplNotExecutableYet(t *testing.T) {
	runtime := NewRuntimeSource([]RuntimeImplSpec{{ImplID: "c2test.rt.exec", Kind: "slot", PackageID: "p"}})
	if fn, ok := runtime.Impl("c2test.rt.exec"); ok || fn != nil {
		t.Fatalf("运行期 impl 在 C3 前不应可执行，got ok=%v fn=%v", ok, fn)
	}
	if !runtime.Has("c2test.rt.exec") {
		t.Fatalf("但运行期 impl 应 Has=true（在册≠可执行）")
	}
}

// TestCombinedImplPrefersExecutable combined.Impl 返回第一个有可执行函数的来源——编译期优先，
// 运行期来源恒无函数（因此 combined.Impl 对运行期 id 返回 false，但对编译期 id 返回真函数）。
func TestCombinedImplPrefersExecutable(t *testing.T) {
	const id = "c2test.combined.exec"
	ran := false
	RegisterImpl(id, func(Ctx) error { ran = true; return nil })

	runtime := NewRuntimeSource([]RuntimeImplSpec{{ImplID: id, Kind: "slot", PackageID: "p"}})
	combined := CombinedSource(CompiledSource(), runtime)

	fn, ok := combined.Impl(id)
	if !ok || fn == nil {
		t.Fatalf("编译期在册且可执行，combined.Impl 应返回真函数")
	}
	_ = fn(nil)
	if !ran {
		t.Fatalf("取到的应是编译期实现且可真跑")
	}
	// 纯运行期 id：Has 真但 Impl 假（执行接 C5）。
	if _, ok := combined.Impl("c2test.runtime.only2"); ok {
		t.Fatalf("未在册的运行期 id 不应有函数")
	}
}

// TestRuntimeSourceDupPanics 同一 impl_id 被两个包声明 → 装载期 panic（配置事故，静默覆盖不可接受）。
func TestRuntimeSourceDupPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("重复 impl_id 应 panic")
		}
	}()
	NewRuntimeSource([]RuntimeImplSpec{
		{ImplID: "c2test.dup", Kind: "slot", PackageID: "a"},
		{ImplID: "c2test.dup", Kind: "slot", PackageID: "b"},
	})
}

// TestCombinedIDsUnion 合并来源 IDs 是各来源去重并集、升序。
func TestCombinedIDsUnion(t *testing.T) {
	RegisterImpl("c2test.ids.c", func(Ctx) error { return nil })
	runtime := NewRuntimeSource([]RuntimeImplSpec{
		{ImplID: "c2test.ids.a", Kind: "slot", PackageID: "p"},
		{ImplID: "c2test.ids.b", Kind: "slot", PackageID: "p"},
	})
	combined := CombinedSource(CompiledSource(), runtime)
	ids := combined.IDs()
	want := []string{"c2test.ids.a", "c2test.ids.b", "c2test.ids.c"}
	// 只断言我们关心的三个按相对顺序出现（注册表可能含其它测试登记的 id）。
	got := filterIDs(ids, "c2test.ids.")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("并集错，got %v want %v（全集 %v）", got, want, ids)
	}
}

// TestRouteSource 来源路由：编译期在册 → compiled；仅运行期在册 → runtime；都不在册 → false。
func TestRouteSource(t *testing.T) {
	const compiledID = "c2test.route.compiled"
	RegisterImpl(compiledID, func(Ctx) error { return nil })
	runtime := NewRuntimeSource([]RuntimeImplSpec{{ImplID: "c2test.route.runtime", Kind: "slot", PackageID: "p"}})
	combined := CombinedSource(CompiledSource(), runtime).(*combinedSource)

	if k, ok := combined.RouteSource(compiledID); !ok || k != SourceCompiled {
		t.Errorf("编译期 impl 应路由到 compiled，got %v ok=%v", k, ok)
	}
	if k, ok := combined.RouteSource("c2test.route.runtime"); !ok || k != SourceRuntime {
		t.Errorf("运行期 impl 应路由到 runtime，got %v ok=%v", k, ok)
	}
	if k, ok := combined.RouteSource("c2test.route.nowhere"); ok {
		t.Errorf("都不在册应 false，got %v ok=%v", k, ok)
	}
}

// TestRouteByApps 按 app 归属路由：impl 的 PackageID 在 loadedAppIDs → 运行期；否则编译期。
func TestRouteByApps(t *testing.T) {
	const compiledID = "c2test.byapps.compiled"
	RegisterImpl(compiledID, func(Ctx) error { return nil })
	compiled := CompiledSource()
	runtime := NewRuntimeSource([]RuntimeImplSpec{
		{ImplID: "c2test.byapps.vendor", Kind: "slot", PackageID: "vendor-app"},
	})
	loaded := map[string]bool{"vendor-app": true}

	if k, ok := RouteByApps(compiled, runtime, loaded, "c2test.byapps.vendor"); !ok || k != SourceRuntime {
		t.Errorf("包目录 app 的 impl 应路由到 runtime，got %v ok=%v", k, ok)
	}
	if k, ok := RouteByApps(compiled, runtime, loaded, compiledID); !ok || k != SourceCompiled {
		t.Errorf("内置 app 的 impl 应路由到 compiled，got %v ok=%v", k, ok)
	}
	if _, ok := RouteByApps(compiled, runtime, loaded, "c2test.byapps.unknown"); ok {
		t.Errorf("都不在册应 false")
	}
}

// filterIDs 返回 ids 里带 prefix 的子集（保持原顺序），便于在共享注册表下断言相对顺序。
func filterIDs(ids []string, prefix string) []string {
	out := make([]string, 0)
	for _, id := range ids {
		if len(id) >= len(prefix) && id[:len(prefix)] == prefix {
			out = append(out, id)
		}
	}
	return out
}
