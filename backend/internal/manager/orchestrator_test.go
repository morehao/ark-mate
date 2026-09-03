package manager

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
	"github.com/morehao/ark-forge/internal/runtime"
	"github.com/morehao/ark-forge/internal/server/eventbus"
	"github.com/morehao/ark-forge/internal/server/handler"
	"github.com/morehao/golib/gerror"
)

// stubSession 代替 acl.Session（Prompt/Cancel 可按脚本返回）。
type stubSession struct {
	stop string
	err  error
	// cancelOnCtx：ctx 被取消时返回 ctx.Err（模拟 agent 在取消后断开等待）
	cancelOnCtx bool
	cancelled   chan struct{}
}

func (s *stubSession) Prompt(ctx context.Context, _ string) (string, error) {
	if s.cancelOnCtx {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
			return "end_turn", nil
		}
	}
	if s.cancelled != nil {
		defer close(s.cancelled)
	}
	return s.stop, s.err
}

func (s *stubSession) Cancel(context.Context) error { return nil }

// newTestRig 组装 svc+orch+bus（不拨真实连接）。
func newTestRig(t *testing.T) (*Service, *Orchestrator, *eventbus.Bus) {
	t.Helper()
	reg := runtime.NewRegistry()
	bus := eventbus.New()
	svc := NewService(reg, bus)
	orch := NewOrchestrator(svc, reg)
	t.Cleanup(orch.Close)
	return svc, orch, bus
}

// feed 收集某聚合最多 want 个 bus 事件（超时 2s 防御）。
func feed(bus *eventbus.Bus, aggID string, want int) []eventbus.Event {
	sub := bus.Subscribe(0)
	defer bus.Unsubscribe(sub)
	var out []eventbus.Event
	timeout := time.After(2 * time.Second)
	for len(out) < want {
		select {
		case e := <-sub.C:
			if e.AggregID == aggID {
				out = append(out, e)
			}
		case <-timeout:
			return out
		}
	}
	return out
}

// TestPromptTurnEnded 正常 end_turn → turn.ended + completed。
func TestPromptTurnEnded(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	as := &ActiveSession{ID: "s1", AgentID: "agent-opencode", Runtime: "opencode", sess: &stubSession{stop: "end_turn"}}
	orch.sessions["s1"] = as
	svc.registerSession(&SessionRec{ID: "s1", AgentID: "agent-opencode", Runtime: "opencode"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	evts := feed(bus, "s1", 2)
	if len(evts) < 2 {
		t.Fatalf("期望 user+turn.ended，got %d 事件", len(evts))
	}
	if evts[0].Type != BusUserMessage || evts[len(evts)-1].Type != BusTurnEnded {
		t.Fatalf("事件序列不符: %+v", evts)
	}
	rec, ok := svc.sessions["s1"]
	if !ok || rec.Status != SessionStatusCompleted {
		t.Fatalf("会话状态应 completed: %+v", rec)
	}
}

// TestPromptBusy 忙时第二轮返回 turn_in_progress。
func TestPromptBusy(t *testing.T) {
	svc, orch, _ := newTestRig(t)
	as := &ActiveSession{ID: "s1", AgentID: "a", Runtime: "r", sess: &stubSession{cancelOnCtx: true}}
	orch.sessions["s1"] = as
	svc.registerSession(&SessionRec{ID: "s1", AgentID: "a", Runtime: "r"})

	if _, err := orch.Prompt(context.Background(), "s1", "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	_, err := orch.Prompt(context.Background(), "s1", "second")
	var ae *acl.Error
	if !errors.As(err, &ae) || ae.Code != "turn_in_progress" {
		t.Fatalf("期望 turn_in_progress，got %v", err)
	}
	// 结束第一个 turn（cancel 它）以免泄漏 goroutine
	_ = orch.CancelTurn(context.Background(), "s1")
}

// TestCancelTurn 取消 → run.cancelled + cancelled 状态。
func TestCancelTurn(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	as := &ActiveSession{ID: "s1", AgentID: "a", Runtime: "r", sess: &stubSession{cancelOnCtx: true}}
	orch.sessions["s1"] = as
	svc.registerSession(&SessionRec{ID: "s1", AgentID: "a", Runtime: "r"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := orch.CancelTurn(context.Background(), "s1"); err != nil {
		t.Fatalf("CancelTurn: %v", err)
	}
	evts := feed(bus, "s1", 2)
	if evts[len(evts)-1].Type != BusRunCancelled {
		t.Fatalf("期望 run.cancelled，got %+v", evts)
	}
	if svc.sessions["s1"].Status != SessionStatusCancelled {
		t.Fatalf("状态应 cancelled: %v", svc.sessions["s1"].Status)
	}
}

// TestServiceRESTErrors 会话动作错误映射为对外错误码（不存在 agent/session → 40401）。
func TestServiceRESTErrors(t *testing.T) {
	svc, _, _ := newTestRig(t)
	_, err := svc.CreateSession(context.Background(), handler.SessionCreateReq{AgentID: "nope", Prompt: "hi"})
	var ge gerror.Error
	if !errors.As(err, &ge) || ge.Code != 40401 {
		t.Fatalf("期望 40401，got %v", err)
	}
	if err := svc.CancelTurn(context.Background(), "ghost"); err == nil || !strings.Contains(err.Error(), "40401") {
		t.Fatalf("CancelTurn 期望 40401 错误，got %v", err)
	}
}

// TestErrLooksCancelled 取消类错误归一判定。
func TestErrLooksCancelled(t *testing.T) {
	if !errLooksCancelled(context.Canceled) {
		t.Fatal("ctx.Canceled 应为取消")
	}
	if !errLooksCancelled(acl.NewError(acl.ClassUnknown, "jsonrpc_error", "Request cancelled", nil)) {
		t.Fatal("Request cancelled 应为取消")
	}
	if errLooksCancelled(acl.NewError(acl.ClassService, "service_error", "boom", nil)) {
		t.Fatal("普通错误不应判为取消")
	}
}
