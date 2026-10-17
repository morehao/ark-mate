package sessionruntime

import (
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/runtimeconn"
	"testing"
	"time"

	"github.com/morehao/ark-mate/pkg/runtime"
)

// 本文件的三个用例验的是**空闲回收的判据**：它随 P4a 正文第五十四刀进了执行面
// （`runtimeconn.Pool.ReapIdle`），而"这条连接还有人用吗"仍由编排侧回答
// （`DropSink.InUseOn`）——所以夹具必须两边都接上（池 + 真 Orchestrator 当 sink）。

// stubConn 造一条只用于回收判据的连接（无句柄：本组用例不拨子进程）。
func stubConn(key string, started time.Time) *runtimeconn.Conn {
	return &runtimeconn.Conn{Spec: runtime.Spec{ID: key}, Key: key, Started: started}
}

// TestReapIdleOnceBasic 空闲超阈值且无会话/无 busy → 回收并置 stopped。
func TestReapIdleOnceBasic(t *testing.T) {
	o, svc := testReapHarness(t)
	now := time.Now()
	o.pool.Add(stubConn("stub", now.Add(-10*time.Minute)))

	n := o.pool.ReapIdle(now, 5*time.Minute)
	if n != 1 {
		t.Fatalf("应回收 1 个，实际 %d", n)
	}
	if o.pool.Get("stub") != nil {
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
	old := func(key string) *runtimeconn.Conn {
		return stubConn(key, now.Add(-10*time.Minute))
	}
	// 三条连接分下不同 key（池的槽位就是 key，避免互相覆盖）。
	o.pool.Add(stubConn("stub", now.Add(-time.Minute))) // 未到阈值
	busy := old("rtx")
	o.pool.Add(busy)
	o.inflight.Mark(busy.Client, "s1")
	withSession := old("dsh")
	o.pool.Add(withSession)
	o.handles.Put(session.NewActiveSession(session.HandleParams{Client: withSession.Client}))

	if n := o.pool.ReapIdle(now, 5*time.Minute); n != 0 {
		t.Fatalf("三种情形都不应回收，实际 %d", n)
	}
	for _, k := range []string{"stub", "rtx", "dsh"} {
		if o.pool.Get(k) == nil {
			t.Fatalf("连接 %q 不应被摘除", k)
		}
	}
	_ = svc
}

// TestReapThenReacquireState 回收后立刻按需重拨不把新连接状态误置 stopped：
// 模拟新连接已入池（`wasCurrent=false` 分支）后旧连接收尾不再回落。
//
// v19：状态只有**一份**（底座进程），不再按 agent 分桶。
func TestReapThenReacquireState(t *testing.T) {
	o, svc := testReapHarness(t)
	now := time.Now()
	old := stubConn("stub", now.Add(-10*time.Minute))
	newer := stubConn("stub", now)
	o.pool.Add(newer)
	svc.runtimePlane.SetState(session.AgentStateRunning)
	// 旧连接收尾：wasCurrent=false → 状态不回落（仍 running）。
	o.pool.Drop(old)
	if st := svc.runtimePlane.Snapshot().State; st == session.AgentStateStopped {
		t.Fatal("旧连接收尾不应把新连接 running 置 stopped")
	}
}
