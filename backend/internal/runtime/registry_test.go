package runtime

import (
	"reflect"
	"strings"
	"testing"
)

// TestDefaultRegistry 内置 runtime 的注册与静态能力基线（M0 DoD：
// `/agents` 返回真实数据的前提）。
//
// 注意它查的是 `List()`（全部 Spec），不是已删除的 `/runtimes` 出口：静态 `Caps` 只剩
// "声明基线"这一个用途，**不得当作能力门禁**（ADR-14；由 archtest 的
// `TestStaticCapsImagesHasNoConsumer` 钉住）。
func TestDefaultRegistry(t *testing.T) {
	reg := fixtureRegistry()
	items := reg.List()

	// 内置 runtime 目前只有一个：dsh（演示底座 echo 与第二个一等适配 opencode 均已下线；
	// **下线一个底座不该让这条判据变形**——它断言的是"注册表里就是 fixtureSpecs() 那些"，
	// 而不是"恰好有两个"）。
	if len(items) != len(fixtureSpecs()) {
		t.Fatalf("注册表条目应与内置清单一致（%d），got %d: %+v", len(fixtureSpecs()), len(items), items)
	}
	// List 按 ID 排序（稳定输出；`/agents` 与诊断面依赖它）。
	for i := 1; i < len(items); i++ {
		if items[i-1].ID >= items[i].ID {
			t.Fatalf("List 未按 ID 升序: %+v", items)
		}
	}
	// 已下线的 id 必须彻底消失：既不在清单里，也不能靠别名复活。
	for _, gone := range []string{"echo", "opencode"} {
		if _, ok := reg.Get(gone); ok {
			t.Fatalf("%s 已下线，不应仍在注册表里", gone)
		}
	}

	dh, ok := reg.Get("dsh")
	if !ok {
		t.Fatal("dsh 未注册")
	}
	if dh.Name != "dsh" || dh.Version != "dsh --profile acp" {
		t.Fatalf("harness spec 不符: %+v", dh)
	}
	if dh.Launch.Command != "dsh" || !reflect.DeepEqual(dh.Launch.Args, []string{"--profile", "acp"}) {
		t.Fatalf("harness launch 不符: %+v", dh.Launch)
	}
	// harness 基线（source 核实）：持久会话真（list/resume/close），无 image/embeddedContext。
	if !dh.Caps.SessionsPersistent || !dh.Caps.MultiSessionConcurrent {
		t.Fatalf("harness 能力基线不符: %+v", dh.Caps)
	}
	if dh.Caps.Images || dh.Caps.EmbeddedContext {
		t.Fatalf("harness 不应声明 image/embedded: %+v", dh.Caps)
	}
	if dh.ID != "dsh" {
		t.Fatalf("夹具的 id 应与内置 adapter 一致: %q", dh.ID)
	}

}

// TestRegistryCRUD 注册表 Get/Register（覆盖）/Require/List 语义。
func TestRegistryCRUD(t *testing.T) {
	s1 := Spec{ID: "a", Name: "A", Version: "v1"}
	s2 := Spec{ID: "b", Name: "B", Version: "v1"}
	reg := NewRegistry(s1, s2)

	// List 按 ID 排序。
	ids := make([]string, 0, 2)
	for _, s := range reg.List() {
		ids = append(ids, s.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("List 排序不符: %v", ids)
	}

	// Register 覆盖同名 ID（后注册覆盖），长度不变。
	reg.Register(Spec{ID: "a", Name: "A2", Version: "v2"})
	if len(reg.List()) != 2 {
		t.Fatalf("覆盖不应增项: %d", len(reg.List()))
	}
	if s, _ := reg.Get("a"); s.Version != "v2" || s.Name != "A2" {
		t.Fatalf("Register 覆盖失败: %+v", s)
	}

	// Require 未知 → 错误。
	if _, err := reg.Require("ghost"); err == nil || !strings.Contains(err.Error(), "unknown runtime") {
		t.Fatalf("Require 未知应报错: %v", err)
	}
	if s, err := reg.Require("b"); err != nil || s.ID != "b" {
		t.Fatalf("Require 已知应成功: %v %+v", err, s)
	}

	// 空注册表。
	empty := NewRegistry()
	if len(empty.List()) != 0 {
		t.Fatal("空注册表应无条目")
	}
	if _, ok := empty.Get("x"); ok {
		t.Fatal("空注册表 Get 应为 false")
	}
}
