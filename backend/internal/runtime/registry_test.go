package runtime

import (
	"reflect"
	"strings"
	"testing"
)

// TestDefaultRegistry 内置 runtime 的注册与静态能力基线（M0 DoD：
// GET /runtimes、/agents 返回真实数据的前提）。
func TestDefaultRegistry(t *testing.T) {
	reg := DefaultRegistry()
	items := reg.ListItems()

	// opencode / deepseekharness / echo 演示。
	if len(items) != 3 {
		t.Fatalf("期望 3 个 runtime，got %d: %+v", len(items), items)
	}
	// List/ListItems 按 ID 排序：deepseekharness < echo < opencode。
	if items[0].ID != "deepseekharness" || items[1].ID != "echo" || items[2].ID != "opencode" {
		t.Fatalf("排序不符: %+v", items)
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

	dh, ok := reg.Get("deepseekharness")
	if !ok {
		t.Fatal("deepseekharness 未注册")
	}
	if dh.Name != "deepseek-harness" || dh.Version != "dsh --profile acp" {
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

	ec, ok := reg.Get("echo")
	if !ok {
		t.Fatal("echo 未注册")
	}
	if !strings.HasSuffix(ec.Version, "echo acp-demo") && !strings.Contains(ec.Version, "echo acp-demo") {
		t.Fatalf("echo version 不符: %+v", ec)
	}
	if !reflect.DeepEqual(ec.Launch.Args, []string{"acp-demo"}) || ec.Launch.Command == "" {
		t.Fatalf("echo launch 不符（应自派生 forge acp-demo）: %+v", ec.Launch)
	}
	if !strings.Contains(ec.Notes, "request_permission") && !strings.Contains(ec.Notes, "权限确认") {
		t.Fatalf("echo Notes 应说明权限演示用途: %s", ec.Notes)
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
	if len(empty.List()) != 0 || len(empty.ListItems()) != 0 {
		t.Fatal("空注册表应无条目")
	}
	if _, ok := empty.Get("x"); ok {
		t.Fatal("空注册表 Get 应为 false")
	}
}

// TestRegistryListItemsMapsSpec ListItem 正确映射 Spec（/runtimes 载荷）。
func TestRegistryListItemsMapsSpec(t *testing.T) {
	reg := NewRegistry(Spec{
		ID:      "r1",
		Name:    "Runtime 1",
		Version: "v9",
		Launch:  LaunchSpec{Command: "cmd"},
		Caps:    Capabilities{SessionsPersistent: true},
		Notes:   "note",
	})
	items := reg.ListItems()
	if len(items) != 1 {
		t.Fatalf("items: %+v", items)
	}
	it := items[0]
	if it.ID != "r1" || it.Name != "Runtime 1" || it.Version != "v9" || !it.Caps.SessionsPersistent {
		t.Fatalf("ListItem 映射不符: %+v", it)
	}
	if it.Caps != reg.List()[0].Caps {
		t.Fatal("ListItem.Caps 应与 Spec.Caps 相等（值拷贝）")
	}
}

// TestListItemsStableOrder 多次调用输出稳定（前端列表渲染确定性）。
func TestListItemsStableOrder(t *testing.T) {
	reg := DefaultRegistry()
	a := reg.ListItems()
	b := reg.ListItems()
	if !reflect.DeepEqual(a, b) {
		t.Fatal("ListItems 两次输出不一致")
	}
}
