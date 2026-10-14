package manager

import (
	"context"
	"testing"
	"time"
)

// TestRunTimeoutWatchdog 看门狗超时自动取消挂起 run：run.cancelled + 状态 cancelled + busy 释放。
func TestRunTimeoutWatchdog(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	orch.SetRunTimeout(80 * time.Millisecond)
	as := &ActiveSession{ID: "s1", Runtime: "r", sess: &stubSession{cancelOnCtx: true}}
	orch.sessions["s1"] = as
	svc.registerSession(&SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	evts := feed(bus, "s1", 2)
	if len(evts) < 2 || evts[0].Type != BusUserMessage || evts[len(evts)-1].Type != BusRunCancelled {
		t.Fatalf("期望 user_message + run.cancelled（看门狗取消），got %+v", evts)
	}
	rec, ok := svc.sessions["s1"]
	if !ok || rec.Status != SessionStatusCancelled {
		t.Fatalf("会话状态应 cancelled: %+v", rec)
	}
	// busy 必须已释放（否则后续 prompt 仍报 run_in_progress）。
	deadline := time.Now().Add(2 * time.Second)
	for {
		as.mu.Lock()
		busy := as.busy
		as.mu.Unlock()
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
	as := &ActiveSession{ID: "s1", Runtime: "r", sess: &stubSession{cancelOnCtx: true}}
	orch.sessions["s1"] = as
	svc.registerSession(&SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// 观察一个超过潜在超时窗口的时间片：不得出现终态自动取消事件。
	sub := bus.Subscribe(0)
	defer bus.Unsubscribe(sub)
	until := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(until) {
		select {
		case e := <-sub.C:
			if e.AggregID == "s1" && e.Type == BusRunCancelled {
				t.Fatalf("未开看门狗不应自动取消，got %s", e.Type)
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
	as.mu.Lock()
	busy := as.busy
	as.mu.Unlock()
	if !busy {
		t.Fatalf("看门狗关闭时挂起 run 应保持 busy")
	}
	// 手动取消仍可用。
	if err := orch.CancelRun(context.Background(), "s1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
}
