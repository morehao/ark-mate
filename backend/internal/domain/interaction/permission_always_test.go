package interaction

import (
	"context"
	"github.com/morehao/ark-mate/internal/domain/session/event"
	"strings"
	"testing"
	"time"
)

// TestPermissionMemoryBothSides 会话级权限记忆（allow/reject 双侧对称）：
// Remember → List 列出（含 decision 侧与授予时刻）→ AutoDecision 命中对应决策 →
// Revoke 单条/整会话撤销后不再命中。
func TestPermissionMemoryBothSides(t *testing.T) {
	pm := NewPermissionManager(DefaultPermissionTimeout)

	cmd := event.ToolCall{Title: "运行演示命令 echo hello", Kind: "execute"}
	other := event.ToolCall{Title: "写入文件", Kind: "write"}
	req := func(t event.ToolCall) PermissionRequest { return PermissionRequest{ToolCall: t} }

	// 未记录前不命中。
	if d, ok := pm.AutoDecision("s1", req(cmd)); ok {
		t.Fatalf("未记录不应命中: %+v", d)
	}

	pm.Remember("s1", cmd, memDecisionAllow, "allow-all")
	pm.Remember("s1", other, memDecisionReject, "reject-all")

	list := pm.List("s1")
	if len(list) != 2 {
		t.Fatalf("期望 2 条记忆，got %d: %+v", len(list), list)
	}
	if list[0].Title != "运行演示命令 echo hello" || list[0].Kind != "execute" || list[0].Decision != memDecisionAllow || list[0].Since.IsZero() {
		t.Fatalf("记忆条目字段不符: %+v", list[0])
	}
	if list[1].Decision != memDecisionReject {
		t.Fatalf("reject 侧记忆缺失: %+v", list)
	}

	// allow 记忆命中放行、reject 记忆命中拒绝（optionID 沿用当初所选）。
	if d, ok := pm.AutoDecision("s1", req(cmd)); !ok || !d.Allowed || d.OptionID != "allow-all" {
		t.Fatalf("allow 记忆应直接放行: ok=%v d=%+v", ok, d)
	}
	if d, ok := pm.AutoDecision("s1", req(other)); !ok || !d.Rejected || d.OptionID != "reject-all" {
		t.Fatalf("reject 记忆应直接拒绝: ok=%v d=%+v", ok, d)
	}
	// 命中本会话、不命中他会话。
	if _, ok := pm.AutoDecision("s2", req(cmd)); ok {
		t.Fatal("s2 不应命中 s1 的记忆")
	}

	// 同键互斥覆盖：先 allow 再 reject 同一工具 → 仅剩 reject（reject 侧覆盖）。
	pm.Remember("s1", cmd, memDecisionReject, "reject-all")
	if d, ok := pm.AutoDecision("s1", req(cmd)); !ok || !d.Rejected {
		t.Fatalf("同键覆盖后应命中 reject: ok=%v d=%+v", ok, d)
	}
	if got := len(pm.List("s1")); got != 2 {
		t.Fatalf("同键覆盖不应新增条目，got %d", got)
	}

	// 撤销单条：目标消失，其余保留。
	if !pm.Revoke("s1", "运行演示命令 echo hello", "execute") {
		t.Fatal("撤销应返回存在")
	}
	if pm.Revoke("s1", "不存在", "execute") {
		t.Fatal("撤销不存在条目应返回 false")
	}
	if _, ok := pm.AutoDecision("s1", req(cmd)); ok {
		t.Fatal("撤销后不应再命中")
	}
	if _, ok := pm.AutoDecision("s1", req(other)); !ok {
		t.Fatal("撤销单条不应影响其它记忆")
	}
	if got := len(pm.List("s1")); got != 1 {
		t.Fatalf("撤销后应剩 1 条，got %d", got)
	}

	// 整会话撤销（一键）清空。
	pm.ForgetSession("s1")
	if got := len(pm.List("s1")); got != 0 {
		t.Fatalf("ForgetSession 后应为空，got %d", got)
	}
	if _, ok := pm.AutoDecision("s1", req(other)); ok {
		t.Fatal("ForgetSession 后不应命中")
	}
}

// TestRegisterAndWaitRemembersAlways allow_always 决策 → RegisterAndWait 内部记录 allow 侧会话级记忆。
func TestRegisterAndWaitRemembersAlways(t *testing.T) {
	pm := NewPermissionManager(DefaultPermissionTimeout)
	done := make(chan PermissionDecision, 1)
	go func() {
		d := pm.RegisterAndWait(context.Background(), "s1", PermissionRequest{
			ToolCall: event.ToolCall{Title: "执行脚本", Kind: "execute"},
		}, nil)
		done <- d
	}()
	deadline := time.Now().Add(2 * time.Second)
	var callID string
	for time.Now().Before(deadline) {
		calls := pm.pendingForSession("s1")
		if len(calls) == 1 {
			callID = calls[0].callID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if callID == "" {
		t.Fatal("挂起权限未在限期内注册")
	}
	if !pm.ResolveAllow(callID, "allow-all", true) {
		t.Fatal("ResolveAllow 失败")
	}
	if d := <-done; !d.Allowed || !d.Always {
		t.Fatalf("决策不符: %+v", d)
	}
	list := pm.List("s1")
	if len(list) != 1 || list[0].Title != "执行脚本" || list[0].Decision != memDecisionAllow {
		t.Fatalf("应记录始终允许记忆: %+v", list)
	}
}

// TestRegisterAndWaitRemembersReject reject_always 决策 → RegisterAndWait 内部记录 reject 侧会话级记忆，
// 二次请求经 AutoDecision 直接拒绝（带当初所选 deny 选项）。
func TestRegisterAndWaitRemembersReject(t *testing.T) {
	pm := NewPermissionManager(DefaultPermissionTimeout)
	tool := event.ToolCall{Title: "删除文件", Kind: "execute"}
	done := make(chan PermissionDecision, 1)
	go func() {
		d := pm.RegisterAndWait(context.Background(), "s1", PermissionRequest{ToolCall: tool}, nil)
		done <- d
	}()
	deadline := time.Now().Add(2 * time.Second)
	var callID string
	for time.Now().Before(deadline) {
		calls := pm.pendingForSession("s1")
		if len(calls) == 1 {
			callID = calls[0].callID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if callID == "" {
		t.Fatal("挂起权限未在限期内注册")
	}
	if !pm.ResolveReject(callID, "reject-all", true) {
		t.Fatal("ResolveReject(always) 失败")
	}
	if d := <-done; !d.Rejected || !d.Always || d.OptionID != "reject-all" {
		t.Fatalf("决策不符（rejected/always/option 应透传）: %+v", d)
	}
	list := pm.List("s1")
	if len(list) != 1 || list[0].Title != "删除文件" || list[0].Decision != memDecisionReject {
		t.Fatalf("应记录始终拒绝记忆: %+v", list)
	}
	// 二次请求命中始终拒绝记忆 → 直接拒绝。
	if d, ok := pm.AutoDecision("s1", PermissionRequest{ToolCall: tool}); !ok || !d.Rejected || d.OptionID != "reject-all" {
		t.Fatalf("始终拒绝记忆未命中: ok=%v d=%+v", ok, d)
	}
}

// TestResolveRejectOnce 拒绝一次不落记忆：显式拒绝（带 deny 选项）后 AutoDecision 仍不命中。
func TestResolveRejectOnce(t *testing.T) {
	pm := NewPermissionManager(DefaultPermissionTimeout)
	tool := event.ToolCall{Title: "清理缓存", Kind: "execute"}
	done := make(chan PermissionDecision, 1)
	go func() {
		d := pm.RegisterAndWait(context.Background(), "s1", PermissionRequest{ToolCall: tool}, nil)
		done <- d
	}()
	deadline := time.Now().Add(2 * time.Second)
	var callID string
	for time.Now().Before(deadline) {
		calls := pm.pendingForSession("s1")
		if len(calls) == 1 {
			callID = calls[0].callID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if callID == "" {
		t.Fatal("挂起权限未在限期内注册")
	}
	if !pm.ResolveReject(callID, "reject-once", false) {
		t.Fatal("ResolveReject 失败")
	}
	if d := <-done; !d.Rejected || d.Always || d.OptionID != "reject-once" {
		t.Fatalf("决策不符: %+v", d)
	}
	if got := len(pm.List("s1")); got != 0 {
		t.Fatalf("拒绝一次不应落记忆，got %d", got)
	}
	if _, ok := pm.AutoDecision("s1", PermissionRequest{ToolCall: tool}); ok {
		t.Fatal("拒绝一次后不应命中记忆")
	}
}

// TestPermissionMemoryListOrderIsDeterministic 同一授予时刻的记忆，顺序按写入先后（不是 map 迭代序）。
//
// 为什么值得单测：这条曾经是**偶发失败**的来源——`List` 只按 since 排序，而 `time.Now()` 的分辨率
// 在并行负载下会让两条记忆拿到同一时间戳，此时 `sort.Slice`（不稳定）退化成 map 顺序，
// 断言首条的用例（本文件的 BothSides）就会随机换序。这里用显式 since 复现"同一时刻"这个边界。
func TestPermissionMemoryListOrderIsDeterministic(t *testing.T) {
	pm := NewPermissionManager(DefaultPermissionTimeout)
	same := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	pm.LoadMemories([]MemoryItem{
		{sessionID: "s1", Title: "先写的", Kind: "execute", Decision: memDecisionAllow, Since: same},
		{sessionID: "s1", Title: "后写的", Kind: "write", Decision: memDecisionReject, Since: same},
	})

	for i := 0; i < 50; i++ { // 重复取：顺序一旦依赖 map 迭代，这里会随机失败
		list := pm.List("s1")
		if len(list) != 2 {
			t.Fatalf("期望 2 条，got %d", len(list))
		}
		if list[0].Title != "先写的" || list[1].Title != "后写的" {
			t.Fatalf("同一时刻应按写入先后，得到 %q, %q", list[0].Title, list[1].Title)
		}
	}
}

// TestAutoDecisionSkipsOnceOnlyRequests 升权请求**不得**被会话级记忆自动放行（A1 / v28）。
//
// 底座发来的**升权请求**只给 `once` 档（探针实测：options 恒 allow-once / reject-once）。
// 会话级记忆记的是"这个会话里这类工具**始终**允许"——若让记忆替升权请求裁决，等于把用户当时
// 那句"这一类都行"自动扩张成一个底座**刻意只肯给一次**的动作。那是本仓最不该发生的一类静默授权。
//
// 反过来的方向是可恢复的：给了 `always` 档却仍走审批，只是多问一次。
func TestAutoDecisionSkipsOnceOnlyRequests(t *testing.T) {
	pm := NewPermissionManager(time.Minute)
	cmd := event.ToolCall{ID: "tc1", Title: "bash: rm -rf build", Kind: "execute"}

	// 先记一条"始终允许"。
	pm.Remember("s1", cmd, memDecisionAllow, "allow-always")
	// 常态请求（选项里有 always 档）→ 记忆命中。
	withAlways := PermissionRequest{
		SessionID: "s1", ToolCall: cmd,
		Options: []PermissionOption{
			{OptionID: "a1", Kind: "allow_once"}, {OptionID: "a2", Kind: "allow_always"},
		},
	}
	if d, ok := pm.AutoDecision("s1", withAlways); !ok || !d.Allowed {
		t.Fatalf("带 always 档的请求应被记忆命中，ok=%v d=%+v", ok, d)
	}
	// 升权请求（**只有 once 档**）→ 不自动裁决，走人工审批。
	onceOnly := withAlways
	onceOnly.Options = []PermissionOption{
		{OptionID: "a1", Kind: "allow_once"}, {OptionID: "r1", Kind: "reject_once"},
	}
	if d, ok := pm.AutoDecision("s1", onceOnly); ok {
		t.Fatalf("只给 once 档的请求**不得**被记忆自动放行（那是升权请求），got %+v", d)
	}
}

// TestRememberCarriesGrantShape 授权形态随记忆一起落库（P2-a / v28）。
//
// 三件事：`scope` 恒 `session`（"一次"不落库，所以它没有第二个取值可写）、入参**只进摘要**
// （不存原文）、同一 (title,kind) 被**重新授权**时摘要跟着刷新（否则库里记的是上一次那条调用，
// 审计会指到一次与当前授权无关的调用上）。
func TestRememberCarriesGrantShape(t *testing.T) {
	pm := NewPermissionManager(time.Minute)
	var got []MemoryItem
	pm.SetMemoryHooks(func(it MemoryItem) { got = append(got, it) },
		func(_, _, _ string) {}, func(_ string) {})

	first := event.ToolCall{ID: "tc1", Title: "bash", Kind: "execute",
		Input: map[string]any{"command": "rm -rf build"}}
	pm.Remember("s1", first, memDecisionAllow, "allow-always")
	if len(got) != 1 {
		t.Fatalf("应落一条记忆，got %d", len(got))
	}
	if got[0].Scope != MemoryScopeSession {
		t.Fatalf("scope 应恒 %q（「一次」不落库所以没有第二档），got %q", MemoryScopeSession, got[0].Scope)
	}
	if got[0].GrantInputDigest == "" {
		t.Fatal("入参非空时摘要不该为空")
	}
	if strings.Contains(got[0].GrantInputDigest, "rm -rf") {
		t.Fatalf("摘要里出现了入参原文 %q：入参可能含路径/token/命令里的密钥，**只进摘要**",
			got[0].GrantInputDigest)
	}

	// 同一 (title,kind) 换一条入参重新授权 → 摘要必须刷新。
	second := first
	second.Input = map[string]any{"command": "ls"}
	pm.Remember("s1", second, memDecisionAllow, "allow-always")
	if len(got) != 2 {
		t.Fatalf("重新授权应再落一次（同键覆盖），got %d 条", len(got))
	}
	if got[1].GrantInputDigest == got[0].GrantInputDigest {
		t.Fatal("入参变了摘要必须跟着变：否则库里记的是上一次那条调用，审计会指到一次与当前授权无关的调用上")
	}
	if got[1].Title != "bash" || got[1].Kind != "execute" {
		t.Fatalf("同键覆盖不该改 title/kind，got %q/%q", got[1].Title, got[1].Kind)
	}
	// 空入参 → 空摘要（不是"空对象的摘要"）。
	pm.Remember("s1", event.ToolCall{Title: "write", Kind: "edit"}, memDecisionReject, "reject-always")
	if last := got[len(got)-1]; last.GrantInputDigest != "" {
		t.Fatalf("空入参的摘要应为空串，got %q", last.GrantInputDigest)
	}
}
