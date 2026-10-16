package manager

import (
	"github.com/morehao/ark-mate/internal/domain/session"
	"testing"
	"time"

	"github.com/morehao/ark-mate/pkg/runtime"
)

// TestReapIdleOnceBasic 空闲超阈值且无会话/无 busy → 回收并置 stopped。
func TestReapIdleOnceBasic(t *testing.T) {
	o, svc := testReapHarness(t)
	now := time.Now()
	o.mu.Lock()
	o.conns["stub"] = &runtimeConn{spec: runtime.Spec{ID: "stub"}, key: "stub", started: now.Add(-10 * time.Minute)}
	o.mu.Unlock()

	n := o.reapIdleOnce(now, 5*time.Minute)
	if n != 1 {
		t.Fatalf("应回收 1 个，实际 %d", n)
	}
	o.mu.Lock()
	_, ok := o.conns["stub"]
	o.mu.Unlock()
	if ok {
		t.Fatal("连接应已摘除")
	}
	if st := svc.runtimePlane.Snapshot().State; st != session.AgentStateStopped {
		t.Fatalf("回收后状态应 stopped: %s", st)
	}
}

// TestReapIdleOnceSkips 未到阈值 / 有 busy / 有会话引用 → 不回收。
func TestReapIdleOnceSkips(t *testing.T) {
	o, svc := testReapHarness(t)
	now := time.Now()
	old := func() *runtimeConn {
		return &runtimeConn{spec: runtime.Spec{ID: "stub"}, key: "stub", started: now.Add(-10 * time.Minute)}
	}
	fresh := func() *runtimeConn {
		return &runtimeConn{spec: runtime.Spec{ID: "stub"}, key: "stub", started: now.Add(-time.Minute)}
	}
	busy := old()
	withSession := old()
	// 四种连接分四个 runtime 槽（避免互相干扰）。
	o.mu.Lock()
	o.conns["stub"] = fresh()
	o.conns["rtx"] = busy
	o.inflight.Mark(busy.client, "s1")
	o.conns["dsh"] = withSession
	o.sessions["s-x"] = &ActiveSession{client: withSession.client}
	o.mu.Unlock()

	if n := o.reapIdleOnce(now, 5*time.Minute); n != 0 {
		t.Fatalf("四种情形都不应回收，实际 %d", n)
	}
	o.mu.Lock()
	_, a := o.conns["stub"]
	_, b := o.conns["rtx"]
	_, c := o.conns["dsh"]
	o.mu.Unlock()
	if !a || !b || !c {
		t.Fatalf("连接不应被摘除: stub=%v rtx=%v dsh=%v", a, b, c)
	}
	_ = svc
}

// TestReapThenReacquireState 回收后立刻按需重拨不把新连接状态误置 stopped：
// 模拟新 rc 已入 map（ownerBefore=false 分支）后旧连接 dropConn 收尾不再回落。
//
// v19：状态只有**一份**（底座进程），不再按 agent 分桶。
func TestReapThenReacquireState(t *testing.T) {
	o, svc := testReapHarness(t)
	now := time.Now()
	old := &runtimeConn{spec: runtime.Spec{ID: "stub"}, key: "stub", started: now.Add(-10 * time.Minute)}
	newer := &runtimeConn{spec: runtime.Spec{ID: "stub"}, key: "stub", started: now}
	o.mu.Lock()
	o.conns["stub"] = newer
	o.mu.Unlock()
	svc.runtimePlane.SetState(session.AgentStateRunning)
	// 旧连接收尾 dropConn：ownerBefore=false → 状态不回落（仍 running）。
	o.dropConn(old)
	if st := svc.runtimePlane.Snapshot().State; st == session.AgentStateStopped {
		t.Fatal("旧连接收尾不应把新连接 running 置 stopped")
	}
}
