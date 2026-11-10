package appslot

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/capability"
)

// freshSlots 返回一批测试用 app：每个 app 带一个 slot（供 Reconcile 展开）。
func appsWithImpls(impls ...string) []capability.App {
	out := make([]capability.App, 0, len(impls))
	for _, id := range impls {
		out = append(out, capability.App{
			ID:    "app-" + id,
			Slots: []capability.AppSlot{{ImplID: id}},
		})
	}
	return out
}

// applyOrder 从 results 里按 apply 顺序取成功/全部 impl_id。

// 依赖排序：requires 决定 apply 顺序，被依赖者先 apply。
func TestReconcileOrdersByRequires(t *testing.T) {
	// 三实现：b 依赖 a，c 依赖 b。期望 apply 顺序 a → b → c（无论定义顺序如何）。
	var order []string
	RegisterImpl("t6.dep.a", func(Ctx) error { order = append(order, "a"); return nil })
	RegisterImpl("t6.dep.b", func(c Ctx) error { order = append(order, "b"); return nil })
	RegisterImpl("t6.dep.c", func(c Ctx) error { order = append(order, "c"); return nil })

	apps := appsWithImpls("t6.dep.c", "t6.dep.a", "t6.dep.b") // 故意乱序
	// Requires 只在 Slot 上，不在 app 定义里——走拓扑排序需显式构造 Slot 列表。
	slots := []Slot{
		{ImplID: "t6.dep.a"},
		{ImplID: "t6.dep.b", Requires: []string{"t6.dep.a"}},
		{ImplID: "t6.dep.c", Requires: []string{"t6.dep.b"}},
	}
	ordered, cycle := topoOrder(slots)
	if len(cycle) != 0 {
		t.Fatalf("不应成环，got cycle=%v", cycle)
	}
	ids := make([]string, 0, len(ordered))
	for _, s := range ordered {
		ids = append(ids, s.ImplID)
	}
	want := []string{"t6.dep.a", "t6.dep.b", "t6.dep.c"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("依赖排序错：want %v got %v", want, ids)
	}

	// 真跑一遍 Reconcile：app 定义（AppSlot）只带 ImplID、不带 Requires，故 Reconcile 里各 slot
	// 无依赖边，apply 顺序按 impl_id 稳定升序（a,b,c）——这验证 Reconcile 的**确定性**，不是
	// Requires 排序（Requires 排序见上面 topoOrder 与 TestTopoOrderCycleAndUnknownRequires）。
	order = nil
	_ = apps
	results, dispose := Reconcile(apps)
	defer dispose()
	_ = results
	if !reflect.DeepEqual(order, []string{"a", "b", "c"}) {
		t.Fatalf("Reconcile 的 apply 顺序应确定（按 id 升序 a,b,c），got %v", order)
	}

	// ReconcileSlots 走**带 Requires** 的完整形态：apply 顺序真正由依赖决定。
	// 定义顺序故意 c 在前，靠 Requires c→b→a 拉成 a,b,c。
	order = nil
	_, dispose2 := ReconcileSlots([]Slot{
		{ImplID: "t6.dep.c", Requires: []string{"t6.dep.b"}},
		{ImplID: "t6.dep.a"},
		{ImplID: "t6.dep.b", Requires: []string{"t6.dep.a"}},
	})
	dispose2()
	if !reflect.DeepEqual(order, []string{"a", "b", "c"}) {
		t.Fatalf("ReconcileSlots 应按 Requires 排序 apply（a,b,c），got %v", order)
	}
}

// topoOrder 的自依赖/未知 requires 不误判成环；成环如实返回。
func TestTopoOrderCycleAndUnknownRequires(t *testing.T) {
	// 未知 requires（Core 服务）不影响排序。
	ordered, cycle := topoOrder([]Slot{
		{ImplID: "t6.topo.x", Requires: []string{"core.someService"}},
	})
	if len(cycle) != 0 || len(ordered) != 1 || ordered[0].ImplID != "t6.topo.x" {
		t.Fatalf("未知 requires 应视为 Core 服务，不参与排序也不成环：ordered=%v cycle=%v", ordered, cycle)
	}

	// 成环：y→z, z→y。
	_, cycle2 := topoOrder([]Slot{
		{ImplID: "t6.topo.y", Requires: []string{"t6.topo.z"}},
		{ImplID: "t6.topo.z", Requires: []string{"t6.topo.y"}},
	})
	got := append([]string(nil), cycle2...)
	if len(got) != 2 {
		t.Fatalf("成环应返回两个环上 id，got %v", got)
	}
}

// 失败隔离：一个 impl panic、一个返回错误、一个未知 impl_id，其它 impl 照跑（status applied）。
func TestReconcileIsolatesFailures(t *testing.T) {
	var ran []string
	RegisterImpl("t6.iso.ok1", func(Ctx) error { ran = append(ran, "ok1"); return nil })
	RegisterImpl("t6.iso.panic", func(Ctx) error { panic("boom") })
	RegisterImpl("t6.iso.err", func(Ctx) error { return errors.New("bad") })
	RegisterImpl("t6.iso.ok2", func(Ctx) error { ran = append(ran, "ok2"); return nil })

	apps := appsWithImpls("t6.iso.ok1", "t6.iso.panic", "t6.iso.err", "t6.iso.unknown_impl", "t6.iso.ok2")
	results, dispose := Reconcile(apps)
	defer dispose()

	byID := map[string]SlotResult{}
	for _, r := range results {
		byID[r.Slot.ImplID] = r
	}
	if byID["t6.iso.ok1"].Status != StatusApplied || byID["t6.iso.ok2"].Status != StatusApplied {
		t.Fatalf("正常 impl 应 applied，got ok1=%v ok2=%v", byID["t6.iso.ok1"].Status, byID["t6.iso.ok2"].Status)
	}
	if byID["t6.iso.panic"].Status != StatusFailed {
		t.Fatalf("panic impl 应 failed（隔离），got %v", byID["t6.iso.panic"].Status)
	}
	if byID["t6.iso.err"].Status != StatusFailed {
		t.Fatalf("返错 impl 应 failed，got %v", byID["t6.iso.err"].Status)
	}
	if byID["t6.iso.unknown_impl"].Status != StatusFailed {
		t.Fatalf("未知 impl 应 failed，got %v", byID["t6.iso.unknown_impl"].Status)
	}
	if !strings.Contains(byID["t6.iso.unknown_impl"].Err.Error(), "未知 impl_id") {
		t.Fatalf("未知 impl 错误应指明原因，got %v", byID["t6.iso.unknown_impl"].Err)
	}
	// 失败隔离的核心：坏 slot 不影响正常 slot 真跑过。
	if !reflect.DeepEqual(ran, []string{"ok1", "ok2"}) {
		t.Fatalf("失败隔离：正常 impl 应都跑到，got ran=%v", ran)
	}
}

// 逆序撤销：多个 impl 各自 OnDispose，disposer 时按 apply 的**逆序**执行。
func TestReconcileDisposesInReverseOrder(t *testing.T) {
	var disposed []string
	RegisterImpl("t6.dis.a", func(c Ctx) error { c.OnDispose(func() { disposed = append(disposed, "a") }); return nil })
	RegisterImpl("t6.dis.b", func(c Ctx) error { c.OnDispose(func() { disposed = append(disposed, "b") }); return nil })
	RegisterImpl("t6.dis.c", func(c Ctx) error { c.OnDispose(func() { disposed = append(disposed, "c") }); return nil })

	// 依赖链 a←b←c：apply 顺序 a,b,c；撤销应 c,b,a（后 apply 的先撤）。
	slots := []Slot{
		{ImplID: "t6.dis.b", Requires: []string{"t6.dis.a"}},
		{ImplID: "t6.dis.c", Requires: []string{"t6.dis.b"}},
		{ImplID: "t6.dis.a"},
	}
	ordered, _ := topoOrder(slots)
	// 用 ordered 真跑：需要把 ordered 转成 apps 会丢 Requires，故直接驱动 runtimeCtx。
	ctx := newRuntimeCtx(KnownImpls())
	for _, s := range ordered {
		if r := applyOne(ctx, KnownImpls(), s); r.Status != StatusApplied {
			t.Fatalf("apply %s 失败：%v", s.ImplID, r.Err)
		}
	}
	ctx.disposeAll()
	if !reflect.DeepEqual(disposed, []string{"c", "b", "a"}) {
		t.Fatalf("撤销应逆序 c,b,a，got %v", disposed)
	}

	// 幂等：再撤一次不重复。
	ctx.disposeAll()
	if !reflect.DeepEqual(disposed, []string{"c", "b", "a"}) {
		t.Fatalf("disposeAll 应幂等，二次调用后 got %v", disposed)
	}
}

// 停用（G6 语义）：Reconcile 得到的 disposer 一调，之前 Provide 的服务在撤销钩子里可见，
// 且撤销后再无残留钩子。这里用「Provide 一个值 + OnDispose 读取它」验证撤销栈与 provide 表共存。
func TestReconcileDisposerRunsOnDisable(t *testing.T) {
	var got string
	RegisterImpl("t6.off.svc", func(c Ctx) error {
		c.Provide("foo", "bar")
		c.OnDispose(func() { got = c.Service("foo").(string) })
		return nil
	})
	results, dispose := Reconcile(appsWithImpls("t6.off.svc"))
	if len(results) != 1 || results[0].Status != StatusApplied {
		t.Fatalf("应 applied，got %v", results)
	}
	if got != "" {
		t.Fatalf("未停用前不应撤销，got %q", got)
	}
	dispose() // app 停用
	if got != "bar" {
		t.Fatalf("停用后 OnDispose 应执行且能取到已 Provide 的服务，got %q", got)
	}
}
