package appslot

import (
	"errors"
	"reflect"
	"testing"

	"github.com/morehao/ark-mate/internal/core/capability"
)

// ——— WP-C5：Reconcile 走来源（ImplResolver）——依赖排序/失败隔离/逆序撤销语义不变 ———

// TestReconcileWithResolverCompiled 编译期 impl 走 resolver 的 exec 真函数；apply 顺序按依赖排序，
// 撤销按逆序（编译期 impl 无 disposer，撤销靠 Ctx.OnDispose）。
func TestReconcileWithResolverCompiled(t *testing.T) {
	var order []string
	execs := map[string]Impl{
		"c5.compiled.a": func(c Ctx) error {
			order = append(order, "a")
			c.OnDispose(func() { order = append(order, "dispose-a") })
			return nil
		},
		"c5.compiled.b": func(c Ctx) error {
			order = append(order, "b")
			c.OnDispose(func() { order = append(order, "dispose-b") })
			return nil
		},
	}
	RegisterImpl("c5.compiled.a", execs["c5.compiled.a"])
	RegisterImpl("c5.compiled.b", execs["c5.compiled.b"])
	resolver := NewSourceImplResolver(CombinedSource(CompiledSource()), execs, nil)

	slots := []Slot{
		{ImplID: "c5.compiled.a", AppID: "app"},
		{ImplID: "c5.compiled.b", AppID: "app"},
	}
	results, dispose := ReconcileWithResolver(slots, resolver)
	for _, r := range results {
		if r.Status != StatusApplied {
			t.Fatalf("应全部 applied，got %s(%v)", r.Slot.ImplID, r.Err)
		}
	}
	if !reflect.DeepEqual(order, []string{"a", "b"}) {
		t.Fatalf("apply 顺序错（按 id 稳定排序 a<b），got %v", order)
	}
	dispose()
	// 撤销逆序：b 的 OnDispose 先于 a。
	if !reflect.DeepEqual(order, []string{"a", "b", "dispose-b", "dispose-a"}) {
		t.Fatalf("撤销应逆序（dispose-b 先于 dispose-a），got %v", order)
	}
}

// TestReconcileWithResolverRuntimeViaDispose 运行期 impl：exec 是「假 host apply」函数，disposer 是
// 「假 disposeSlot RPC」——验证运行期 impl 的 apply 走 exec、撤销走 resolver.Dispose，且与编译期
// impl 在同一逆序栈里协奏（后 apply 的先撤）。
func TestReconcileWithResolverRuntimeViaDispose(t *testing.T) {
	var log []string

	// 编译期 impl（真函数，撤销走 OnDispose）。
	execs := map[string]Impl{
		"c5.mix.compiled": func(c Ctx) error {
			log = append(log, "apply-compiled")
			c.OnDispose(func() { log = append(log, "dispose-compiled") })
			return nil
		},
	}
	RegisterImpl("c5.mix.compiled", execs["c5.mix.compiled"])

	// 运行期 impl：exec 走「假 Host.ApplySlot」，disposer 走「假 DisposeSlot」。
	execs["c5.mix.runtime"] = func(Ctx) error { log = append(log, "apply-runtime"); return nil }
	disposers := map[string]func() error{
		"c5.mix.runtime": func() error { log = append(log, "disposeSlot-runtime"); return nil },
	}

	runtime := NewRuntimeSource([]RuntimeImplSpec{
		{ImplID: "c5.mix.runtime", Kind: "slot", PackageID: "vendor"},
	})
	combined := CombinedSource(CompiledSource(), runtime)
	resolver := NewSourceImplResolver(combined, execs, disposers)

	// b(runtime) < c(compiled) id 排序：apply 顺序 c5.mix.compiled 先、c5.mix.runtime 后。
	slots := []Slot{
		{ImplID: "c5.mix.compiled", AppID: "a"},
		{ImplID: "c5.mix.runtime", AppID: "vendor"},
	}
	results, dispose := ReconcileWithResolver(slots, resolver)
	for _, r := range results {
		if r.Status != StatusApplied {
			t.Fatalf("应全部 applied，got %s(%v)", r.Slot.ImplID, r.Err)
		}
	}
	wantApply := []string{"apply-compiled", "apply-runtime"}
	if !reflect.DeepEqual(log, wantApply) {
		t.Fatalf("apply 顺序错，got %v want %v", log, wantApply)
	}
	dispose()
	// 逆序撤销：runtime 后 apply → 先撤；compiled 后撤。runtime 撤销走 disposeSlot RPC。
	wantDispose := []string{"apply-compiled", "apply-runtime", "disposeSlot-runtime", "dispose-compiled"}
	if !reflect.DeepEqual(log, wantDispose) {
		t.Fatalf("运行期+编译期撤销应同一逆序栈协奏，got %v want %v", log, wantDispose)
	}
}

// TestReconcileWithResolverUnknownFailed 未在册 impl → FAILED（不凭空造）；在册但无 exec 的运行期
// impl 也 FAILED（来源不可执行），其余照跑（失败隔离）。
func TestReconcileWithResolverUnknownFailed(t *testing.T) {
	execs := map[string]Impl{
		"c5.iso.ok": func(Ctx) error { return nil },
	}
	RegisterImpl("c5.iso.ok", execs["c5.iso.ok"])
	runtime := NewRuntimeSource([]RuntimeImplSpec{
		{ImplID: "c5.iso.runtime.noexec", Kind: "slot", PackageID: "v"},
	})
	combined := CombinedSource(CompiledSource(), runtime)
	resolver := NewSourceImplResolver(combined, execs, nil)

	slots := []Slot{
		{ImplID: "c5.iso.ok", AppID: "a"},
		{ImplID: "c5.iso.not.registered", AppID: "a"}, // 完全不在册
		{ImplID: "c5.iso.runtime.noexec", AppID: "v"}, // 在册但无 exec
	}
	results, dispose := ReconcileWithResolver(slots, resolver)
	defer dispose()
	byID := map[string]SlotResult{}
	for _, r := range results {
		byID[r.Slot.ImplID] = r
	}
	if byID["c5.iso.ok"].Status != StatusApplied {
		t.Errorf("在册且有 exec 应 applied")
	}
	if byID["c5.iso.not.registered"].Status != StatusFailed {
		t.Errorf("不在册必须 FAILED（白名单底线）")
	}
	if byID["c5.iso.runtime.noexec"].Status != StatusFailed {
		t.Errorf("在册但无 exec 的运行期 impl 必须 FAILED（要经 host）")
	}
}

// TestReconcileWithResolverIsolatesPanic apply panic 只让该 slot FAILED，其它照跑（失败隔离不变）。
func TestReconcileWithResolverIsolatesPanic(t *testing.T) {
	execs := map[string]Impl{
		"c5.pan.panic": func(Ctx) error { panic("boom") },
		"c5.pan.err":   func(Ctx) error { return errors.New("bad") },
		"c5.pan.ok":    func(Ctx) error { return nil },
	}
	for id, fn := range execs {
		RegisterImpl(id, fn)
	}
	resolver := NewSourceImplResolver(CombinedSource(CompiledSource()), execs, nil)
	slots := []Slot{
		{ImplID: "c5.pan.panic", AppID: "a"},
		{ImplID: "c5.pan.err", AppID: "a"},
		{ImplID: "c5.pan.ok", AppID: "a"},
	}
	results, dispose := ReconcileWithResolver(slots, resolver)
	defer dispose()
	byID := map[string]SlotResult{}
	for _, r := range results {
		byID[r.Slot.ImplID] = r
	}
	if byID["c5.pan.panic"].Status != StatusFailed || byID["c5.pan.err"].Status != StatusFailed {
		t.Errorf("panic/err 应 FAILED（失败隔离）")
	}
	if byID["c5.pan.ok"].Status != StatusApplied {
		t.Errorf("ok 应 applied（隔离后续照跑）")
	}
}

// TestSourceImplResolverRefusesUnregisteredResolver 只在 execs 有函数、但来源不在册 → Resolve 返回 false
// （defense in depth：即便 execs 被塞了函数，不在册的 id 也不返回）。
func TestSourceImplResolverRefusesUnregisteredResolver(t *testing.T) {
	execs := map[string]Impl{
		"c5.depth.ghost": func(Ctx) error { return nil }, // 塞了函数，但来源不在册
	}
	resolver := NewSourceImplResolver(CombinedSource(CompiledSource()), execs, nil)
	if _, ok := resolver.Resolve("c5.depth.ghost"); ok {
		t.Fatalf("不在册的 id 即便 execs 里有函数也必须 Resolve=false（防退化服务定位图）")
	}
}

// 用一下 capability 包避免「声明未用」指针（保留语义：ReconcileAppsWithResolver 收 capability.App）。
var _ = ReconcileAppsWithResolver
var _ = capability.App{}
