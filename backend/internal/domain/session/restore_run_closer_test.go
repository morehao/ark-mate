package session

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/ark-mate/pkg/object/objid"
)

// 恢复路径的收口（v27 + S7 / ADR-8 的后半）：崩溃残轮在 `runs` 表里不再永久停在 `running`。
//
// 这条测试钉的是**行为**，而 `internal/archtest` 的两条守卫钉的是**形状**（恢复路径有铸造点、
// 实时路径没有伪终态、重建之后没有悬空行）。三处互补：形状判据挡不住"接了线但写错了状态"，
// 行为判据挡不住"下一个人把两处统一成一支"。
//
// 造的是真实的崩溃现场：会话 `in_progress`、`run.started` 已落库、`runs` 行 `last_seq = -1`、
// 没有任何终态事件。然后**关掉句柄再打开**（进程重启的等价物），交给 `Restore`。
func TestRestoreClosesInterruptedRuns(t *testing.T) {
	ctx := context.Background()
	path := testutil.TempDBPath(t)

	sessionID := objid.New(objid.Session)
	runID := objid.New(objid.Run)

	st := testutil.OpenStore(t, path)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("跑迁移: %v", err)
	}
	if err := st.UpsertSession(ctx, model.SessionEntity{
		ID: sessionID, Cwd: t.TempDir(), Status: SessionStatusInProgress,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("写会话行: %v", err)
	}
	firstSeq, err := st.AppendEvent(ctx, objid.New(objid.Event), sessionID, BusRunStarted, map[string]any{
		"session_id": sessionID, "run_id": runID, "text": "跑一下",
	})
	if err != nil {
		t.Fatalf("写 run.started: %v", err)
	}
	if err := st.CreateRun(ctx, model.RunEntity{
		ID: runID, SessionID: sessionID, UserText: "跑一下",
		Status: SessionStatusInProgress, FirstSeq: firstSeq, LastSeq: -1,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("写开轮行: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("关闭第一个句柄（模拟进程退出）: %v", err)
	}

	// —— 重启 ——
	st2 := testutil.OpenStore(t, path)
	t.Cleanup(func() { _ = st2.Close() })
	bus := eventbus.New()
	pl := NewPlane()
	pl.SetStore(st2)
	pl.SetBus(bus)
	// 恢复期的排队输入作废是另一个域的事（端口）；这里给一个空实现，免得把 nil 端口当成
	// "没有重启过"。
	pl.SetStaleInputsOnRestore(func(context.Context) {})

	sessions, injected, err := pl.Restore(ctx)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if sessions != 1 || injected < 1 {
		t.Fatalf("恢复计数异常：sessions=%d（want 1）events=%d（want ≥1）", sessions, injected)
	}
	if rec, ok := pl.Lookup(sessionID); !ok || rec.Status != SessionStatusInterrupted {
		t.Fatalf("恢复后会话状态 = %q（want %q）：残留 in_progress 必须被收敛成可操作态",
			rec.Status, SessionStatusInterrupted)
	}

	// ① `runs` 行被收口：不再是 running，且取值说得出「被重启带走」。
	runs, err := st2.ListRuns(ctx, sessionID)
	if err != nil {
		t.Fatalf("读 runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs 行数 = %d（want 1）", len(runs))
	}
	if runs[0].Status != SessionStatusInterrupted {
		t.Fatalf("该轮状态 = %q（want %q）：崩溃残轮必须在恢复路径收口，否则界面永远给出"+
			"「可点停止解除一个已死的轮次」", runs[0].Status, SessionStatusInterrupted)
	}
	if runs[0].LastSeq < firstSeq {
		t.Fatalf("收口后的 last_seq = %d（应 ≥ first_seq=%d）：窗口右端必须落在日志里真实存在的位置上",
			runs[0].LastSeq, firstSeq)
	}

	// ② 收口是**durable 事实**：事件流里有这一条，且 reason 说得出为什么。
	evs, _, err := st2.EventsAfter(ctx, sessionID, -1, 100)
	if err != nil {
		t.Fatalf("读事件: %v", err)
	}
	var closer map[string]any
	for _, e := range evs {
		if e.Type != BusRunStatus {
			continue
		}
		m, ok := DecodePayload(e.PayloadJSON).(map[string]any)
		if !ok {
			continue
		}
		if m["status"] == RunStatusInterrupted {
			closer = m
		}
	}
	if closer == nil {
		t.Fatalf("事件流里没有 `%s(%s)`：收口必须是**可回放的事实**（否则重建出来的读模型里"+
			"这一轮仍是 running），实际事件数 %d", BusRunStatus, RunStatusInterrupted, len(evs))
	}
	if closer["run_id"] != runID {
		t.Fatalf("收口事件的 run_id = %v（want %q）：收口必须指向**这一轮**的身份",
			closer["run_id"], runID)
	}
	if closer["reason"] != "restart" {
		t.Fatalf("收口事件的 reason = %v（want \"restart\"）：界面据此说「上一轮因进程重启中断，可重发」，"+
			"**不**说「失败」——那是底座的结论", closer["reason"])
	}

	// ③ 幂等：再恢复一次不该铸第二条收口（否则每次启动都往事件流里加一行噪声）。
	if err := st2.Close(); err != nil {
		t.Fatalf("关闭第二个句柄: %v", err)
	}
	st3 := testutil.OpenStore(t, path)
	t.Cleanup(func() { _ = st3.Close() })
	pl2 := NewPlane()
	pl2.SetStore(st3)
	pl2.SetBus(eventbus.New())
	pl2.SetStaleInputsOnRestore(func(context.Context) {})
	if _, _, err := pl2.Restore(ctx); err != nil {
		t.Fatalf("第二次 Restore: %v", err)
	}
	evs2, _, err := st3.EventsAfter(ctx, sessionID, -1, 100)
	if err != nil {
		t.Fatalf("第二次读事件: %v", err)
	}
	closers := 0
	for _, e := range evs2 {
		if e.Type != BusRunStatus {
			continue
		}
		if m, ok := DecodePayload(e.PayloadJSON).(map[string]any); ok && m["status"] == RunStatusInterrupted {
			closers++
		}
	}
	if closers != 1 {
		t.Fatalf("收口事件出现了 %d 条（want 1）：已收口的轮次不该被再次收口——"+
			"第二次启动时该行已不是 running，`RunningRuns` 不会再选中它", closers)
	}
}
