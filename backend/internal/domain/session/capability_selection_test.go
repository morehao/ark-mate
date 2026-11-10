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

// ——— WP5：Apps 维度（app 业务工具独立注入通道的唯一来源）———
//
// Apps 三态与 Skills/Mcps 同构，但语义更硬：nil=未声明→无 app 工具、`[]`=明确不要、非空=启用这些 app。
// 它进 `capability_json` 列（落库）与代际指纹——三态在序列化往返后必须逐字保持（否则"明确不要"会被
// 悄悄抹成"未声明"，反过来"未声明"会被写成空集），这是"默认零注入"能不能守住的前提。

// TestAppsPatchTriState 补丁对 Apps 三态（不改 / 回未声明 / 空集 / 白名单）。
func TestAppsPatchTriState(t *testing.T) {
	current := &CapabilitySelection{Apps: ids("b", "a")}

	// 空补丁不动 Apps（但保留并规范化）。
	same := ApplyCapabilityPatch(current, CapabilityPatch{})
	if same.Apps == nil || len(*same.Apps) != 2 {
		t.Fatalf("空补丁应保留 apps，got %+v", same.Apps)
	}

	// null → 回未声明。
	cleared := ApplyCapabilityPatch(current, CapabilityPatch{Apps: OptionalIDs{Set: true, Value: nil}})
	if cleared.Apps != nil {
		t.Fatalf("apps null 应回未声明，got %+v", cleared.Apps)
	}

	// [] → 明确不要（非 nil 空集）。
	empty := ApplyCapabilityPatch(current, CapabilityPatch{Apps: OptionalIDs{Set: true, Value: ids()}})
	if empty.Apps == nil || len(*empty.Apps) != 0 {
		t.Fatalf("apps [] 应保留成非 nil 空集，got %+v", empty.Apps)
	}

	// 白名单规范化（去空/去重/升序）。
	list := ApplyCapabilityPatch(current, CapabilityPatch{Apps: OptionalIDs{Set: true, Value: ids("b", " ", "a", "b")}})
	if got := *list.Apps; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("apps 白名单应规范化，got %v", got)
	}
}

// TestAppsMarshalParseRoundTrip 三态经 capability_json 往返逐字保持。
func TestAppsMarshalParseRoundTrip(t *testing.T) {
	// 未声明（nil）：整体未声明 → 空串。
	if got := MarshalCapabilitySelection(&CapabilitySelection{}); got != "{}" {
		t.Fatalf("无 apps 的声明选定应为 {}，got %q", got)
	}

	// 非空白名单：往返后 Apps 与顺序（规范化升序）保持。
	raw := MarshalCapabilitySelection(&CapabilitySelection{Apps: ids("gamma", "alpha")})
	back := ParseCapabilitySelection(raw)
	if back == nil || back.Apps == nil {
		t.Fatalf("apps 白名单往返丢失，raw=%q got=%+v", raw, back)
	}
	if got := *back.Apps; len(got) != 2 || got[0] != "alpha" || got[1] != "gamma" {
		t.Fatalf("apps 白名单应规范化升序往返，got %v", got)
	}

	// 空集（[]）：必须往返成**非 nil 空集**，不能塌成 nil（"明确不要" ⇄ "未声明" 是两个代际）。
	rawEmpty := MarshalCapabilitySelection(&CapabilitySelection{Apps: ids()})
	backEmpty := ParseCapabilitySelection(rawEmpty)
	if backEmpty == nil || backEmpty.Apps == nil || len(*backEmpty.Apps) != 0 {
		t.Fatalf("apps [] 往返后应仍是非 nil 空集，raw=%q got=%+v", rawEmpty, backEmpty)
	}
}

// TestAppsEmptyAndAppIDs Empty 把 Apps 计入；AppIDs 三态 nil-safe。
func TestAppsEmptyAndAppIDs(t *testing.T) {
	if (&CapabilitySelection{Apps: ids()}).Empty() {
		t.Fatal("声明了 apps（哪怕空集）就不算 Empty")
	}
	if (&CapabilitySelection{Apps: ids("x")}).Empty() {
		t.Fatal("带 apps 白名单不算 Empty")
	}
	if (&CapabilitySelection{}).Empty() != true {
		t.Fatal("四 kind 全未声明才算 Empty")
	}

	if got := (&CapabilitySelection{}).AppIDs(); got != nil {
		t.Fatalf("apps nil → AppIDs 应返回 nil，got %v", got)
	}
	if got := (&CapabilitySelection{Apps: ids()}).AppIDs(); got == nil || len(got) != 0 {
		t.Fatalf("apps [] → AppIDs 应返回非 nil 空切片，got %v", got)
	}
	if got := (&CapabilitySelection{Apps: ids("a", "b")}).AppIDs(); len(got) != 2 {
		t.Fatalf("apps 白名单 → AppIDs 应返回两项，got %v", got)
	}
}

// TestIDGettersNilReceiverSafe 回归：`c` 本身为 nil 时 AppIDs/McpIDs/SkillIDs 不得 panic，返回 nil。
//
// 真起 serve + 真模型建「不带 capabilities」的会话暴露的 bug：会话没声明 capabilities 时
// opt.Selection 是 nil，注入链拿 nil 调 sel.AppIDs() → nil deref panic（session/new 变 500）。
// Empty() 一直是 receiver-safe 的，但三个 ID 取值器此前只判字段 nil、没判 receiver nil。
func TestIDGettersNilReceiverSafe(t *testing.T) {
	var c *CapabilitySelection
	// 直接调用必须不 panic 且返回 nil。
	if got := c.AppIDs(); got != nil {
		t.Fatalf("nil receiver AppIDs 应返回 nil，got %v", got)
	}
	if got := c.McpIDs(); got != nil {
		t.Fatalf("nil receiver McpIDs 应返回 nil，got %v", got)
	}
	if got := c.SkillIDs(); got != nil {
		t.Fatalf("nil receiver SkillIDs 应返回 nil，got %v", got)
	}
}
