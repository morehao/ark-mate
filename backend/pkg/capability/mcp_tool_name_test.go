package capability

import (
	"strings"
	"testing"
)

// TestGatewayToolName 命名形态与解析（唯一实现：注入端、网关端、探测面共用）。
func TestGatewayToolName(t *testing.T) {
	if got := GatewayToolName("gh", "search"); got != "gh__search" {
		t.Fatalf("拼名不符: %q", got)
	}
	ids := []string{"gh", "gh__x"}
	// **最长 id 前缀**：`gh__x__t` 同时匹配 `gh`（工具名 `x__t`）与 `gh__x`（工具名 `t`），
	// 取更长的那台——它才是有意写出的那个 id。
	if sid, tool, ok := SplitGatewayToolName("gh__x__t", ids); !ok || sid != "gh__x" || tool != "t" {
		t.Fatalf("最长前缀解析不符: %q / %q / %v", sid, tool, ok)
	}
	if sid, tool, ok := SplitGatewayToolName("gh__t", ids); !ok || sid != "gh" || tool != "t" {
		t.Fatalf("短前缀解析不符: %q / %q / %v", sid, tool, ok)
	}
	// 无匹配（未知 server）/ 只有前缀没有工具名 → 都不算命中（调用方按"未知工具"处理）。
	if _, _, ok := SplitGatewayToolName("nope__t", ids); ok {
		t.Fatal("未知 server 不该命中")
	}
	if _, _, ok := SplitGatewayToolName("gh__", ids); ok {
		t.Fatal("空工具名不该命中")
	}
	if _, _, ok := SplitGatewayToolName("gh", ids); ok {
		t.Fatal("无分隔符不该命中")
	}
}

// TestNamespaceTools 批量命名 + **fail-closed** 冲突裁决。
//
// 冲突是怎么来的：server id 与工具名都允许下划线，`"a" + "__" + "b__c"` 与
// `"a__b" + "__" + "c"` 会撞成同一个暴露名。此时按名字反查来源是歧义的——网关若"保留第一个"，
// 调用就会静默落到一台**不是调用方以为的** server 上。
func TestNamespaceTools(t *testing.T) {
	items := []NamespacedTool{
		{ServerID: "a", Raw: "search"},
		{ServerID: "b", Raw: "search"}, // 与上一行同名但前缀不同 → 不冲突
		{ServerID: "a", Raw: "b__x"},   // ← 与下一行撞名
		{ServerID: "a__b", Raw: "x"},
		{ServerID: "", Raw: "orphan"}, // 无来源 → 不入面
		{ServerID: "c", Raw: ""},      // 无名 → 不入面
	}
	kept, conflicts := NamespaceTools(items)
	names := make([]string, 0, len(kept))
	for _, i := range kept {
		names = append(names, items[i].Name)
	}
	if strings.Join(names, ",") != "a__search,b__search" {
		t.Fatalf("保留集不符: %v", names)
	}
	if len(conflicts) != 1 || conflicts[0].Name != "a__b__x" {
		t.Fatalf("应报出一条冲突 a__b__x: %+v", conflicts)
	}
	// 冲突**整组丢弃**：两边的来源都要能从冲突明细里读出来（调用方要能响亮留痕）。
	owners := make([]string, 0, len(conflicts[0].Claims))
	for _, c := range conflicts[0].Claims {
		owners = append(owners, c.ServerID+"→"+c.Raw)
	}
	if strings.Join(owners, ",") != "a→b__x,a__b→x" {
		t.Fatalf("冲突明细应含双方来源: %v", owners)
	}
	// 顺序稳定（按输入序），便于日志与断言。
	again, _ := NamespaceTools(items)
	if len(again) != len(kept) || again[0] != kept[0] || again[1] != kept[1] {
		t.Fatalf("下标不稳定: %v vs %v", again, kept)
	}
}
