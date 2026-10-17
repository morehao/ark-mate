package sessionruntime

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/acl"
)

// TestPermissionMemoriesPersistAcrossRestart 验证 P1 权限记忆持久化不变量：
//  1. Remember（allow/reject 双侧）经 hooks 落 permission_memories 表；
//  2. Revoke / ForgetSession 同步删库；
//  3. 重启恢复：新 Orchestrator.RestoreMemoriesFromStore 从表装载，AutoDecision 双侧语义不丢
//     （reject 记忆 → 直接 Rejected，allow 记忆 → 直接 Allowed），跨重启可追溯。
func TestPermissionMemoriesPersistAcrossRestart(t *testing.T) {
	dbPath := testutil.TempDBPath(t)
	ctx := context.Background()
	reg := testutil.StubRegistry()

	// —— 进程一：sqlite 模式记录双侧记忆 ——
	st1 := testutil.OpenStore(t, dbPath)
	svc1 := NewService(reg, nil)
	svc1.EnableStore(st1)
	orch1 := NewOrchestrator(svc1, reg)
	svc1.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})

	toolRun := acl.ToolCallInfo{Title: "跑脚本", Kind: "execute"}
	orch1.perm.Remember("s1", toolRun, "reject", "option-reject-always")
	// 同键异侧覆盖：allow 覆盖 reject（同键互斥），DB 应只有一条且为 allow。
	orch1.perm.Remember("s1", toolRun, "allow", "option-allow-always")
	toolWrite := acl.ToolCallInfo{Title: "写文件", Kind: "write"}
	orch1.perm.Remember("s1", toolWrite, "reject", "option-reject-always")

	rows, err := st1.ListMemories(ctx, "s1")
	if err != nil || len(rows) != 2 {
		t.Fatalf("DB 应有 2 条记忆: n=%d err=%v %+v", len(rows), err, rows)
	}
	decisionOf := map[string]string{}
	for _, r := range rows {
		decisionOf[r.Title] = r.Decision
	}
	if decisionOf["跑脚本"] != "allow" || decisionOf["写文件"] != "reject" {
		t.Fatalf("同键覆盖/双侧不符: %+v", decisionOf)
	}

	// Revoke（写文件）→ DB 同步删；ForgetSession 其余 → 整会话删。
	if !orch1.perm.Revoke("s1", "写文件", "write") {
		t.Fatal("Revoke 应命中")
	}
	if n, _ := st1.ListMemories(ctx, "s1"); len(n) != 1 {
		t.Fatalf("Revoke 后 DB 应 1 条: %+v", n)
	}
	orch1.perm.ForgetSession("s1")
	if n, _ := st1.ListMemories(ctx, "s1"); len(n) != 0 {
		t.Fatalf("ForgetSession 后 DB 应为空: %+v", n)
	}
	_ = st1.Close()

	// —— 进程二：重启恢复 ——
	st2 := testutil.OpenStore(t, dbPath)
	defer st2.Close()
	svc2 := NewService(reg, nil)
	svc2.EnableStore(st2)
	orch2 := NewOrchestrator(svc2, reg)
	svc2.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})
	svc2.sessionPlane.RegisterSession(&session.SessionRec{ID: "s2"})

	// 恢复前先记录（模拟重启前的历史记忆），恢复后二者叠加。
	orch2.perm.Remember("s1", toolRun, "reject", "option-reject-always")
	orch2.perm.Remember("s2", toolWrite, "allow", "option-allow-always")
	if err := orch2.RestoreMemoriesFromStore(ctx); err != nil {
		t.Fatalf("RestoreMemoriesFromStore: %v", err)
	}

	// AutoDecision 双侧语义不丢（reject → Rejected；allow → Allowed；已撤销 → 无记忆）。
	req := acl.PermissionRequest{ToolCall: toolRun, Options: []acl.PermissionOption{{OptionID: "x"}}}
	d, ok := orch2.perm.AutoDecision("s1", req)
	if !ok || !d.Rejected {
		t.Fatalf("s1 跑脚本应为 reject 记忆: %+v ok=%v", d, ok)
	}
	reqWrite := acl.PermissionRequest{ToolCall: toolWrite, Options: []acl.PermissionOption{{OptionID: "x"}}}
	d2, ok2 := orch2.perm.AutoDecision("s2", reqWrite)
	if !ok2 || !d2.Allowed {
		t.Fatalf("s2 写文件应为 allow 记忆: %+v ok=%v", d2, ok2)
	}
	if _, ok3 := orch2.perm.AutoDecision("s1", reqWrite); ok3 {
		t.Fatal("s1 写文件已被撤销，不应有记忆")
	}
	// 同键覆盖保留 since（reject→allow 异侧覆盖后才更新）。
	items := orch2.perm.List("s1")
	if len(items) != 1 || items[0].Decision != "reject" {
		t.Fatalf("s1 记忆应为 1 条 reject: %+v", items)
	}
	if items[0].Since.IsZero() || items[0].Since.After(time.Now().Add(time.Minute)) {
		t.Fatalf("since 异常: %+v", items[0].Since)
	}
}
