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

// stubConversation 代替 acl.Session（Prompt/Cancel 可按脚本返回）。
type stubConversation struct {
	stop string
	err  error
	// cancelOnCtx：ctx 被取消时返回 ctx.Err（模拟 agent 在取消后断开等待）
	cancelOnCtx bool
	cancelled   chan struct{}
}

func (s *stubConversation) Prompt(ctx context.Context, _ string) (string, error) {
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

func (s *stubConversation) Cancel(context.Context) error { return nil }

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
	as := &ActiveConversation{ID: "s1", AgentID: "agent-opencode", Runtime: "opencode", sess: &stubConversation{stop: "end_turn"}}
	orch.conversations["s1"] = as
	svc.registerConversation(&ConversationRec{ID: "s1", AgentID: "agent-opencode"})

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
	rec, ok := svc.conversations["s1"]
	if !ok || rec.Status != ConversationStatusCompleted {
		t.Fatalf("会话状态应 completed: %+v", rec)
	}
}

// TestPromptBusy 忙时第二轮返回 turn_in_progress。
func TestPromptBusy(t *testing.T) {
	svc, orch, _ := newTestRig(t)
	as := &ActiveConversation{ID: "s1", AgentID: "a", Runtime: "r", sess: &stubConversation{cancelOnCtx: true}}
	orch.conversations["s1"] = as
	svc.registerConversation(&ConversationRec{ID: "s1", AgentID: "a"})

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
	as := &ActiveConversation{ID: "s1", AgentID: "a", Runtime: "r", sess: &stubConversation{cancelOnCtx: true}}
	orch.conversations["s1"] = as
	svc.registerConversation(&ConversationRec{ID: "s1", AgentID: "a"})

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
	if svc.conversations["s1"].Status != ConversationStatusCancelled {
		t.Fatalf("状态应 cancelled: %v", svc.conversations["s1"].Status)
	}
}

// TestServiceRESTErrors 会话动作错误映射为对外错误码（不存在 agent/conversation → 40401）。
func TestServiceRESTErrors(t *testing.T) {
	svc, _, _ := newTestRig(t)
	_, err := svc.CreateConversation(context.Background(), handler.ConversationCreateReq{AgentID: "nope", Prompt: "hi"})
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

// stuckCancel 的 Cancel 仅在 ctx 到期（agentCancelTimeout 兜底）时返回：
// 模拟 runtime 对 conversation/cancel 无响应（如模型限流重试挂起）。
type stuckCancel struct {
	*stubConversation
}

func (s *stuckCancel) Cancel(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestCancelTurnAgentUnresponsive agent 无响应时 CancelTurn 有界返回并本地解除：
// 不依赖 conversation/cancel RPC，Prompt 总是被 ctx 取消打断 → run.cancelled。
func TestCancelTurnAgentUnresponsive(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	as := &ActiveConversation{
		ID: "s1", AgentID: "a", Runtime: "r",
		sess: &stuckCancel{stubConversation: &stubConversation{cancelOnCtx: true}},
	}
	orch.conversations["s1"] = as
	svc.registerConversation(&ConversationRec{ID: "s1", AgentID: "a"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	start := time.Now()
	if err := orch.CancelTurn(context.Background(), "s1"); err != nil {
		t.Fatalf("CancelTurn: %v", err)
	}
	if el := time.Since(start); el > agentCancelTimeout+2*time.Second {
		t.Fatalf("CancelTurn 被无响应 agent 拖住: %v（agentCancelTimeout=%v）", el, agentCancelTimeout)
	}
	evts := feed(bus, "s1", 2)
	if len(evts) < 2 || evts[len(evts)-1].Type != BusRunCancelled {
		t.Fatalf("期望 run.cancelled，got %+v", evts)
	}
	if svc.conversations["s1"].Status != ConversationStatusCancelled {
		t.Fatalf("状态应 cancelled: %v", svc.conversations["s1"].Status)
	}
}

// TestBusyAttributionHelpers 验证在途归属表（§17 E1：inFlightTracker 语义同 §1.5 修订）：
// 登记/撤销/唯一性判定/nil 安全（经 Orchestrator 委托方法覆盖）。
func TestBusyAttributionHelpers(t *testing.T) {
	orch := &Orchestrator{inflight: newInFlightTracker()}
	c1 := &acl.Client{}
	c2 := &acl.Client{}

	// 未登记 / nil client → 空
	if got := orch.busyConversationForClient(c1); got != "" {
		t.Fatalf("未登记应返回空: %q", got)
	}
	if got := orch.busyConversationForClient(nil); got != "" {
		t.Fatalf("nil client 应返回空: %q", got)
	}
	orch.markBusy(nil, "s1") // no-op 不 panic

	orch.markBusy(c1, "s1")
	if got := orch.busyConversationForClient(c1); got != "s1" {
		t.Fatalf("唯一在途应为 s1: %q", got)
	}
	orch.markBusy(c1, "s2")
	if got := orch.busyConversationForClient(c1); got != "" {
		t.Fatalf("两会话并发在途不应猜测: %q", got)
	}
	orch.unmarkBusy(c1, "s2")
	if got := orch.busyConversationForClient(c1); got != "s1" {
		t.Fatalf("撤销 s2 后应回到 s1: %q", got)
	}
	orch.unmarkBusy(c1, "s1")
	if got := orch.busyConversationForClient(c1); got != "" {
		t.Fatalf("全部撤销后应为空: %q", got)
	}

	// 不同连接互不影响
	orch.markBusy(c2, "s3")
	if got := orch.busyConversationForClient(c1); got != "" {
		t.Fatalf("c1 不应受 c2 影响: %q", got)
	}
	if got := orch.busyConversationForClient(c2); got != "s3" {
		t.Fatalf("c2 唯一在途应为 s3: %q", got)
	}
	orch.unmarkBusy(c2, "s3")
}

// TestBusyAttributionLifecycle Prompt 到收尾的登记/撤销生命周期：
// turn 进行中能唯一归属会话，取消收尾后归属清除。
func TestBusyAttributionLifecycle(t *testing.T) {
	svc, orch, _ := newTestRig(t)
	c1 := &acl.Client{}
	as := &ActiveConversation{
		ID: "s1", AgentID: "a", Runtime: "r", client: c1,
		sess: &stubConversation{cancelOnCtx: true},
	}
	orch.conversations["s1"] = as
	svc.registerConversation(&ConversationRec{ID: "s1", AgentID: "a"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := orch.busyConversationForClient(c1); got != "s1" {
		t.Fatalf("turn 进行中应归属 s1: %q", got)
	}
	if err := orch.CancelTurn(context.Background(), "s1"); err != nil {
		t.Fatalf("CancelTurn: %v", err)
	}
	// runTurn 收尾后在途登记应清空（轮询等待，2s 防御）
	deadline := time.Now().Add(2 * time.Second)
	for orch.busyConversationForClient(c1) != "" {
		if time.Now().After(deadline) {
			t.Fatal("turn 收尾后在途登记未清除")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
