package session

import (
	"testing"
)

// ——— M3-8：会话内改能力的三态补丁（域层判据）———
//
// 这一组钉的是**三态**本身，不是某个接口的形状：键缺失（不改）、`null`（回未声明）、
// `[]`（明确一个都不要）在合并后必须还是三件不同的事。混掉任何一格，用户就永远表达不出
// 其中一种意图——而它的表现是"改了没反应"或"没改却被清空"，两者都不会报错。

func ids(v ...string) *[]string {
	out := make([]string, len(v))
	copy(out, v)
	return &out
}

func str(v string) *string { return &v }

// TestApplyCapabilityPatchTriState 三个 kind 各自的「不改 / 回未声明 / 空集 / 白名单」。
func TestApplyCapabilityPatchTriState(t *testing.T) {
	current := &CapabilitySelection{
		Skills: ids("b", "a"), Mcps: ids("m1"), Expert: str("exp-a"),
	}

	// ① 空补丁 = 什么都不改（且规范化：升序、去重）。
	same := ApplyCapabilityPatch(current, CapabilityPatch{})
	if same == current {
		t.Fatal("应返回新对象，不原地改入参")
	}
	if got := *same.Skills; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("空补丁应保留并规范化 skills，got %v", got)
	}
	if same.Expert == nil || *same.Expert != "exp-a" {
		t.Fatalf("空补丁不应动 expert，got %+v", same.Expert)
	}

	// ② `null` = 回未声明（三个 kind 独立）。
	cleared := ApplyCapabilityPatch(current, CapabilityPatch{
		Skills: OptionalIDs{Set: true, Value: nil},
	})
	if cleared.Skills != nil {
		t.Fatalf("null 应把 skills 置回未声明，got %+v", cleared.Skills)
	}
	if cleared.Mcps == nil || cleared.Expert == nil {
		t.Fatal("只声明了 skills 的补丁不应动另外两个 kind")
	}

	// ③ `[]` = 明确一个都不要（**不是**回未声明：两者在服务端是两个代际）。
	empty := ApplyCapabilityPatch(current, CapabilityPatch{
		Mcps: OptionalIDs{Set: true, Value: ids()},
	})
	if empty.Mcps == nil || len(*empty.Mcps) != 0 {
		t.Fatalf("[] 应保留成非 nil 的空集，got %+v", empty.Mcps)
	}

	// ④ 白名单：去空串/去重/升序（否则换个勾选顺序就白换一个进程）。
	list := ApplyCapabilityPatch(current, CapabilityPatch{
		Skills: OptionalIDs{Set: true, Value: ids("b", " ", "a", "b")},
	})
	if got := *list.Skills; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("白名单应规范化，got %v", got)
	}

	// ⑤ expert：`""` = 明确不用（非 nil），`null` = 未声明（nil）。
	noExpert := ApplyCapabilityPatch(current, CapabilityPatch{Expert: OptionalString{Set: true, Value: str("")}})
	if noExpert.Expert == nil || *noExpert.Expert != "" {
		t.Fatalf(`expert "" 应保留成"明确不用"，got %+v`, noExpert.Expert)
	}
	unsetExpert := ApplyCapabilityPatch(current, CapabilityPatch{Expert: OptionalString{Set: true, Value: nil}})
	if unsetExpert.Expert != nil {
		t.Fatalf("expert null 应回未声明，got %+v", unsetExpert.Expert)
	}
}

// TestApplyCapabilityPatchFromUndeclared 起点是「未声明」（nil）时，补丁只带某一格。
func TestApplyCapabilityPatchFromUndeclared(t *testing.T) {
	out := ApplyCapabilityPatch(nil, CapabilityPatch{Skills: OptionalIDs{Set: true, Value: ids("a")}})
	if out == nil || out.Skills == nil || len(*out.Skills) != 1 {
		t.Fatalf("应从 nil 起点建出带 skills 的选定，got %+v", out)
	}
	if out.Mcps != nil || out.Expert != nil {
		t.Fatal("没在补丁里出现的 kind 应保持未声明")
	}
	if out.Empty() {
		t.Fatal("带 skills 的选定不算 Empty")
	}
}

// TestEquivalentCapabilitySelection 只用于「这次编辑要不要写库」的等价判据。
//
// 两条边界刻意写死：nil 与 `{}` 等价（对用户都是"回落默认"）；但 `{}` 与 `{skills:[]}`
// **不**等价（后者是"一个都不要"）。
func TestEquivalentCapabilitySelection(t *testing.T) {
	cases := []struct {
		name string
		a, b *CapabilitySelection
		want bool
	}{
		{"都是 nil", nil, nil, true},
		{"nil 与 {}", nil, &CapabilitySelection{}, true},
		{"{} 与 {}", &CapabilitySelection{}, &CapabilitySelection{}, true},
		{"顺序不同", &CapabilitySelection{Skills: ids("b", "a")}, &CapabilitySelection{Skills: ids("a", "b")}, true},
		{"{} 与空集", &CapabilitySelection{}, &CapabilitySelection{Skills: ids()}, false},
		{"空集与 nil", &CapabilitySelection{Skills: ids()}, nil, false},
		{"专家不同", &CapabilitySelection{Expert: str("a")}, &CapabilitySelection{Expert: str("b")}, false},
		{"专家空串与 nil", &CapabilitySelection{Expert: str("")}, nil, false},
	}
	for _, c := range cases {
		if got := EquivalentCapabilitySelection(c.a, c.b); got != c.want {
			t.Errorf("%s：got %v want %v", c.name, got, c.want)
		}
	}
}

// TestMarshalCapabilitySelectionKeepsDeclaredEmpty 空选定序列化成 `{}` 而不是空串。
//
// 这条是「会话内清空能力」能不能生效的**唯一**支点：空串是"未声明"，而代际迁移对未声明会把
// 目标指纹回落成句柄当前值 → 判"没变化" → 清空永远不生效（见 `planGenerationSwitch`）。
func TestMarshalCapabilitySelectionKeepsDeclaredEmpty(t *testing.T) {
	if got := MarshalCapabilitySelection(&CapabilitySelection{}); got != "{}" {
		t.Fatalf("空选定应序列化成 {}，got %q", got)
	}
	if got := MarshalCapabilitySelection(nil); got != "" {
		t.Fatalf("nil 应序列化成空串，got %q", got)
	}
	parsed := ParseCapabilitySelection("{}")
	if parsed == nil || !parsed.Empty() {
		t.Fatalf("{} 读回应是「声明了但不限制」，got %+v", parsed)
	}
}
