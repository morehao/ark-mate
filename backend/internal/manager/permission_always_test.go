package manager

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
)

// TestPermissionMemoryBothSides 会话级权限记忆（allow/reject 双侧对称）：
// Remember → List 列出（含 decision 侧与授予时刻）→ AutoDecision 命中对应决策 →
// Revoke 单条/整会话撤销后不再命中。
func TestPermissionMemoryBothSides(t *testing.T) {
	pm := NewPermissionManager(DefaultPermissionTimeout)

	cmd := acl.ToolCallInfo{Title: "运行演示命令 echo hello", Kind: "execute"}
	other := acl.ToolCallInfo{Title: "写入文件", Kind: "write"}
	req := func(t acl.ToolCallInfo) acl.PermissionRequest { return acl.PermissionRequest{ToolCall: t} }

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
	done := make(chan acl.PermissionDecision, 1)
	go func() {
		d := pm.RegisterAndWait(context.Background(), "s1", acl.PermissionRequest{
			ToolCall: acl.ToolCallInfo{Title: "执行脚本", Kind: "execute"},
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
	tool := acl.ToolCallInfo{Title: "删除文件", Kind: "execute"}
	done := make(chan acl.PermissionDecision, 1)
	go func() {
		d := pm.RegisterAndWait(context.Background(), "s1", acl.PermissionRequest{ToolCall: tool}, nil)
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
	if d, ok := pm.AutoDecision("s1", acl.PermissionRequest{ToolCall: tool}); !ok || !d.Rejected || d.OptionID != "reject-all" {
		t.Fatalf("始终拒绝记忆未命中: ok=%v d=%+v", ok, d)
	}
}

// TestResolveRejectOnce 拒绝一次不落记忆：显式拒绝（带 deny 选项）后 AutoDecision 仍不命中。
func TestResolveRejectOnce(t *testing.T) {
	pm := NewPermissionManager(DefaultPermissionTimeout)
	tool := acl.ToolCallInfo{Title: "清理缓存", Kind: "execute"}
	done := make(chan acl.PermissionDecision, 1)
	go func() {
		d := pm.RegisterAndWait(context.Background(), "s1", acl.PermissionRequest{ToolCall: tool}, nil)
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
	if _, ok := pm.AutoDecision("s1", acl.PermissionRequest{ToolCall: tool}); ok {
		t.Fatal("拒绝一次后不应命中记忆")
	}
}
