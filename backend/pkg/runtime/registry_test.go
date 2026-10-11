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
	reg := AllBuiltinRegistry()
	items := reg.List()

	// 内置 runtime 收敛为两个：opencode / dsh（echo 演示底座已于 M0 删除）。
	if len(items) != 2 {
		t.Fatalf("期望 2 个 runtime，got %d: %+v", len(items), items)
	}
	// List 按 ID 排序：dsh < opencode。
	if items[0].ID != "dsh" || items[1].ID != "opencode" {
		t.Fatalf("排序不符: %+v", items)
	}
	// echo 必须彻底消失：既不在清单里，也不能靠别名复活。
	if _, ok := reg.Get("echo"); ok {
		t.Fatal("echo 演示底座已删除，不应仍在注册表里")
	}

	oc, ok := reg.Get("opencode")
	if !ok {
		t.Fatal("opencode 未注册")
	}
	if oc.Name != "opencode" || oc.Version != "opencode acp" {
		t.Fatalf("opencode spec 不符: %+v", oc)
	}
	if !reflect.DeepEqual(oc.Launch.Args, []string{"acp"}) || oc.Launch.Command != "opencode" {
		t.Fatalf("opencode launch 不符: %+v", oc.Launch)
	}
	// opencode 基线：会话能力完整 + image/embeddedContext。
	if !oc.Caps.SessionsPersistent || !oc.Caps.MultiSessionConcurrent || !oc.Caps.Images || !oc.Caps.EmbeddedContext {
		t.Fatalf("opencode 能力基线不符: %+v", oc.Caps)
	}
	// 与 adapter 声明一致（adapters.go 汇入不丢失字段）。
	if oc.Caps.SessionsPersistent != true {
		t.Fatal("opencode SessionsPersistent 应为 true")
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
	if !strings.Contains(dh.Notes, "dsh --profile acp") {
		t.Fatalf("harness Notes 应说明启动形态: %s", dh.Notes)
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
