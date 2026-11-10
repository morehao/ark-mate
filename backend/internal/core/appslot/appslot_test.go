package appslot

import (
	"testing"
)

// mustPanic 断言 fn 会 panic（用于登记冲突）。用 recover 包住，避免测试进程被炸掉。
func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("%s 应当 panic 但没有", what)
		}
	}()
	fn()
}

// 登记后能从 KnownImpls 查到；重复登记 panic；空 id / nil fn panic。
func TestRegisterImpl(t *testing.T) {
	const id = "test.unique.impl.a"
	fn := func(Ctx) error { return nil }
	RegisterImpl(id, fn)

	got, ok := KnownImpls()[id]
	if !ok || got == nil {
		t.Fatalf("登记后 KnownImpls 应查得到 %q", id)
	}

	mustPanic(t, "重复登记同一 impl_id", func() { RegisterImpl(id, fn) })
	mustPanic(t, "空 impl_id", func() { RegisterImpl("", fn) })
	mustPanic(t, "nil Impl", func() { RegisterImpl("test.unique.impl.nilfn", nil) })
}

// KnownImpls 返回只读快照：改返回值不影响注册表（防白名单退化成开放表）。
func TestKnownImplsIsSnapshot(t *testing.T) {
	const id = "test.unique.impl.snap"
	RegisterImpl(id, func(Ctx) error { return nil })

	snap := KnownImpls()
	// 往快照里塞一个新实现、删掉已有实现。
	snap["injected.not.real"] = func(Ctx) error { return nil }
	delete(snap, id)

	fresh := KnownImpls()
	if _, ok := fresh["injected.not.real"]; ok {
		t.Fatal("往 KnownImpls 快照里新增的实现不应进入注册表（快照必须是副本）")
	}
	if _, ok := fresh[id]; !ok {
		t.Fatal("从 KnownImpls 快照里删除不应影响注册表（快照必须是副本）")
	}
}

// 登记实体：OnAppDelete 缺省归一为 keep；重复 kind panic；空 kind panic。
func TestRegisterEntity(t *testing.T) {
	const kind = "test_unique_entity_kind"
	RegisterEntity(kind, EntitySpec{Table: "t", IDPrefix: "t_", OwnerColumn: "app_id"})

	got, ok := KnownEntities()[kind]
	if !ok {
		t.Fatalf("登记后 KnownEntities 应查得到 %q", kind)
	}
	if got.OnAppDelete != OnAppDeleteKeep {
		t.Fatalf("OnAppDelete 缺省应归一为 %q，得到 %q", OnAppDeleteKeep, got.OnAppDelete)
	}
	if got.Table != "t" || got.IDPrefix != "t_" || got.OwnerColumn != "app_id" {
		t.Fatalf("EntitySpec 字段登记不一致：%+v", got)
	}

	mustPanic(t, "重复登记同一 entity kind", func() {
		RegisterEntity(kind, EntitySpec{Table: "t2"})
	})
	mustPanic(t, "空 entity kind", func() { RegisterEntity("", EntitySpec{}) })
}

// KnownEntities 返回只读快照。
func TestKnownEntitiesIsSnapshot(t *testing.T) {
	const kind = "test_unique_entity_snap"
	RegisterEntity(kind, EntitySpec{Table: "t"})

	snap := KnownEntities()
	snap["injected_not_real"] = EntitySpec{}
	delete(snap, kind)

	fresh := KnownEntities()
	if _, ok := fresh["injected_not_real"]; ok {
		t.Fatal("往 KnownEntities 快照里新增不应进入注册表")
	}
	if _, ok := fresh[kind]; !ok {
		t.Fatal("从 KnownEntities 快照里删除不应影响注册表")
	}
}

// ImplIDs / EntityKinds 返回升序列表，且包含已登记项。
func TestSortedViews(t *testing.T) {
	RegisterImpl("test.sorted.zzz", func(Ctx) error { return nil })
	RegisterImpl("test.sorted.aaa", func(Ctx) error { return nil })
	ids := ImplIDs()
	idxA, idxZ := -1, -1
	for i, v := range ids {
		if v == "test.sorted.aaa" {
			idxA = i
		}
		if v == "test.sorted.zzz" {
			idxZ = i
		}
	}
	if idxA < 0 || idxZ < 0 || idxA > idxZ {
		t.Fatalf("ImplIDs 应升序且包含已登记项，得到 %v", ids)
	}
}
