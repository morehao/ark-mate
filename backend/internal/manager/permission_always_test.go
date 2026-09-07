package manager

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
)

// TestPermissionAlwaysMemory 会话级「始终允许」记忆的可见与撤销面：
// Remember → AlwaysAllowed 列出（含授予时刻、稳定排序）→ AutoDecision 命中 →
// RevokeAlways 单条/整会话撤销后不再命中。
func TestPermissionAlwaysMemory(t *testing.T) {
	pm := NewPermissionManager(DefaultPermissionTimeout)

	cmd := acl.ToolCallInfo{Title: "运行演示命令 echo hello", Kind: "execute"}
	other := acl.ToolCallInfo{Title: "写入文件", Kind: "write"}
	req := func(t acl.ToolCallInfo) acl.PermissionRequest { return acl.PermissionRequest{ToolCall: t} }

	// 未记录前不命中。
	if d, ok := pm.AutoDecision("s1", req(cmd)); ok {
		t.Fatalf("未记录不应命中: %+v", d)
	}

	pm.Remember("s1", cmd)
	pm.Remember("s1", other)

	list := pm.AlwaysAllowed("s1")
	if len(list) != 2 {
		t.Fatalf("期望 2 条记忆，got %d: %+v", len(list), list)
	}
	if list[0].Title != "运行演示命令 echo hello" || list[0].Kind != "execute" || list[0].Since.IsZero() {
		t.Fatalf("记忆条目字段不符: %+v", list[0])
	}

	// 命中本会话、不命中他会话。
	if _, ok := pm.AutoDecision("s1", req(cmd)); !ok {
		t.Fatal("s1 应命中始终允许记忆")
	}
	if _, ok := pm.AutoDecision("s2", req(cmd)); ok {
		t.Fatal("s2 不应命中 s1 的记忆")
	}

	// 撤销单条：目标消失，其余保留。
	if !pm.RevokeAlways("s1", "运行演示命令 echo hello", "execute") {
		t.Fatal("撤销应返回存在")
	}
	if pm.RevokeAlways("s1", "不存在", "execute") {
		t.Fatal("撤销不存在条目应返回 false")
	}
	if _, ok := pm.AutoDecision("s1", req(cmd)); ok {
		t.Fatal("撤销后不应再命中")
	}
	if _, ok := pm.AutoDecision("s1", req(other)); !ok {
		t.Fatal("撤销单条不应影响其它记忆")
	}
	if got := len(pm.AlwaysAllowed("s1")); got != 1 {
		t.Fatalf("撤销后应剩 1 条，got %d", got)
	}

	// 整会话撤销（一键）清空。
	pm.ForgetSession("s1")
	if got := len(pm.AlwaysAllowed("s1")); got != 0 {
		t.Fatalf("ForgetSession 后应为空，got %d", got)
	}
	if _, ok := pm.AutoDecision("s1", req(other)); ok {
		t.Fatal("ForgetSession 后不应命中")
	}
}

// TestRegisterAndWaitRemembersAlways allow_always 决策 → RegisterAndWait 内部记录会话级记忆。
func TestRegisterAndWaitRemembersAlways(t *testing.T) {
	pm := NewPermissionManager(DefaultPermissionTimeout)
	done := make(chan acl.PermissionDecision, 1)
	go func() {
		d := pm.RegisterAndWait(context.Background(), "s1", acl.PermissionRequest{
			ToolCall: acl.ToolCallInfo{Title: "执行脚本", Kind: "execute"},
		}, nil)
		done <- d
	}()
	// 轮询等挂起注册完成（RegisterAndWait 在 goroutine 内同步注册）。
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
	list := pm.AlwaysAllowed("s1")
	if len(list) != 1 || list[0].Title != "执行脚本" {
		t.Fatalf("应记录始终允许记忆: %+v", list)
	}
}
