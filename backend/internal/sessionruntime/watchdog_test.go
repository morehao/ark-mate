package sessionruntime

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"
)

// TestRunTimeoutWatchdog 看门狗超时自动取消挂起 run：run.cancelled + 状态 cancelled + busy 释放。
func TestRunTimeoutWatchdog(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	orch.SetRunTimeout(80 * time.Millisecond)
	as := session.NewActiveSession(session.HandleParams{ID: "s1", Runtime: "r", Session: &stubSession{cancelOnCtx: true}})
	orch.handles.Put(as)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// 三条：开轮（v22 起 `Prompt` 先发 run.started）→ 用户消息 → 看门狗取消。
	evts := feed(bus, "s1", 3)
	if len(evts) < 3 || evts[0].Type != session.BusRunStarted || evts[1].Type != session.BusUserMessage ||
		evts[len(evts)-1].Type != session.BusRunCancelled {
		t.Fatalf("期望 run.started + user_message + run.cancelled（看门狗取消），got %+v", evts)
	}
	// 终态载荷必须带本轮身份：`runs` 收口靠它找开轮行（找不到只能退化成"该会话最近的 running"）。
	if got, _ := evts[len(evts)-1].Payload.(map[string]any)["run_id"].(string); got == "" {
		t.Fatalf("run.cancelled 载荷缺 run_id: %+v", evts[len(evts)-1].Payload)
	}
	rec, ok := svc.sessionPlane.Lookup("s1")
	if !ok || rec.Status != session.SessionStatusCancelled {
		t.Fatalf("会话状态应 cancelled: %+v", rec)
	}
	// busy 必须已释放（否则后续 prompt 仍报 run_in_progress）。
	deadline := time.Now().Add(2 * time.Second)
	for {
		busy := as.Busy()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("busy 未在看门狗取消后释放")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunTimeoutOff 看门狗关闭时挂起 run 不自动取消（保持 busy，等外部 cancel）。
func TestRunTimeoutOff(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	as := session.NewActiveSession(session.HandleParams{ID: "s1", Runtime: "r", Session: &stubSession{cancelOnCtx: true}})
	orch.handles.Put(as)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// 观察一个超过潜在超时窗口的时间片：不得出现**新**的终态自动取消事件。
	// live-only：回放历史会把订阅之前的事件立刻投进来，而这里要看的正是订阅之后的这个时间片。
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)
	until := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(until) {
		select {
		case e := <-sub.C:
			if e.AggregID == "s1" && e.Type == session.BusRunCancelled {
				t.Fatalf("未开看门狗不应自动取消，got %s", e.Type)
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
	busy := as.Busy()
	if !busy {
		t.Fatalf("看门狗关闭时挂起 run 应保持 busy")
	}
	// 手动取消仍可用。
	if err := orch.CancelRun(context.Background(), "s1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
}
