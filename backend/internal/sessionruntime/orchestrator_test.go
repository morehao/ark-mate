package sessionruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"

	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/core/failure"
	"github.com/morehao/ark-mate/internal/core/object/objusage"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/dto/dtosession"
)

// stubSession 代替 acp.Session（Prompt/Cancel 可按脚本返回）。
type stubSession struct {
	stop string
	err  error
	// options 会话配置项桩（nil = 运行时未提供任何选项）。
	options []protocol.ConfigOption
	// cancelOnCtx：ctx 被取消时返回 ctx.Err（模拟 agent 在取消后断开等待）
	cancelOnCtx bool
	cancelled   chan struct{}
}

// ConfigOptions 桩：默认无配置项（模型目录为空 → 目录来源走缓存/resume/空）。
func (s *stubSession) ConfigOptions() []protocol.ConfigOption { return s.options }

func (s *stubSession) Prompt(ctx context.Context, _ []protocol.PromptPart) (string, objusage.TokenUsage, error) {
	if s.cancelOnCtx {
		select {
		case <-ctx.Done():
			return "", objusage.TokenUsage{}, ctx.Err()
		case <-time.After(3 * time.Second):
			return "end_turn", objusage.TokenUsage{}, nil
		}
	}
	if s.cancelled != nil {
		defer close(s.cancelled)
	}
	return s.stop, objusage.TokenUsage{}, s.err
}

func (s *stubSession) Cancel(context.Context) error { return nil }

// feed 收集某聚合最多 want 个 bus 事件（超时 2s 防御）。
//
// **跳过 `run.status`**（P0-2 起每轮会插若干条过程状态）：本组用例的意图是
// 「开轮 → 用户消息 → 终态」这三段，过程状态的取值、顺序与去重由 `runstatus_test.go`
// 专门钉住。把它们算进计数，会让每加一类阶段都要改一堆与阶段无关的用例——而那类改动
// 一旦变成「顺手把断言放宽」，真正的回归就从眼前溜过去了。
func feed(bus *eventbus.Bus, aggID string, want int) []eventbus.Event {
	sub := bus.SubscribeAfter(nil) // 回放 + live：feed 收集该聚合此后可见的全部事件
	defer bus.Unsubscribe(sub)
	var out []eventbus.Event
	timeout := time.After(2 * time.Second)
	for len(out) < want {
		select {
		case e := <-sub.C:
			if e.AggregID == aggID && e.Type != session.BusRunStatus {
				out = append(out, e)
			}
		case <-timeout:
			return out
		}
	}
	return out
}

// feedAll 同 `feed`，但**不**跳过 `run.status`（过程状态的专属用例用）。
func feedAll(bus *eventbus.Bus, aggID string, want int) []eventbus.Event {
	sub := bus.SubscribeAfter(nil)
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

// TestPromptRunEnded 正常 end_turn → run.ended + completed。
func TestPromptRunEnded(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	as := session.NewActiveSession(session.HandleParams{ID: "s1", Runtime: "rtx", Session: &stubSession{stop: "end_turn"}})
	orch.handles.Put(as)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// v22 起 `Prompt` 的第一条事件是 `run.started`（开轮落行的起点），用户消息紧随其后。
	evts := feed(bus, "s1", 3)
	if len(evts) < 3 {
		t.Fatalf("期望 run.started+user+run.ended，got %d 事件", len(evts))
	}
	if evts[0].Type != session.BusRunStarted || evts[1].Type != session.BusUserMessage || evts[len(evts)-1].Type != session.BusRunEnded {
		t.Fatalf("事件序列不符: %+v", evts)
	}
	// 开轮与终态必须报**同一支** run_id：它们指的是同一轮（runs 行的身份在开轮时就定了）。
	startedID, _ := evts[0].Payload.(map[string]any)["run_id"].(string)
	endedID, _ := evts[len(evts)-1].Payload.(map[string]any)["run_id"].(string)
	if startedID == "" || startedID != endedID {
		t.Fatalf("run.started/run.ended 的 run_id 应同源: started=%q ended=%q", startedID, endedID)
	}
	rec, ok := svc.sessionPlane.Lookup("s1")
	if !ok || rec.Status != session.SessionStatusCompleted {
		t.Fatalf("会话状态应 completed: %+v", rec)
	}
}

// TestPromptBusy 忙时第二轮返回 run_in_progress。
func TestPromptBusy(t *testing.T) {
	svc, orch, _ := newTestRig(t)
	as := session.NewActiveSession(session.HandleParams{ID: "s1", Runtime: "r", Session: &stubSession{cancelOnCtx: true}})
	orch.handles.Put(as)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	_, err := orch.Prompt(context.Background(), "s1", "second")
	var ae *failure.Error
	if !errors.As(err, &ae) || ae.Code != "run_in_progress" {
		t.Fatalf("期望 run_in_progress，got %v", err)
	}
	// 结束第一个 run（cancel 它）以免泄漏 goroutine
	_ = orch.CancelRun(context.Background(), "s1")
}

// TestCancelRun 取消 → run.cancelled + cancelled 状态。
func TestCancelRun(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	as := session.NewActiveSession(session.HandleParams{ID: "s1", Runtime: "r", Session: &stubSession{cancelOnCtx: true}})
	orch.handles.Put(as)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := orch.CancelRun(context.Background(), "s1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	evts := feed(bus, "s1", 3)
	if evts[len(evts)-1].Type != session.BusRunCancelled {
		t.Fatalf("期望 run.cancelled，got %+v", evts)
	}
	if st, _ := svc.sessionPlane.Lookup("s1"); st.Status != session.SessionStatusCancelled {
		t.Fatalf("状态应 cancelled: %v", func() string { s, _ := svc.sessionPlane.Lookup("s1"); return s.Status }())
	}
}

// gatedPromptSession 的 Prompt 一直等到 release（模拟 0.2.1 的「取消后仍要 drain 才返回」）。
type gatedPromptSession struct {
	stubSession
	started  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (s *gatedPromptSession) Prompt(ctx context.Context, _ []protocol.PromptPart) (string, objusage.TokenUsage, error) {
	close(s.started)
	defer close(s.returned)
	select {
	case <-s.release:
		return "cancelled", objusage.TokenUsage{}, nil
	case <-ctx.Done():
		return "", objusage.TokenUsage{}, ctx.Err()
	}
}

// TestCancelWaitsForPromptSettlement 取消后**不立刻**清 busy：等 prompt 真正返回再收口。
//
// 0.2.1 的 session/cancel 会等根活动与已排队输出 drain 完才让 prompt 返回；旧口径「发完通知
// 就 t.Cancel()」会把迟到 update 甩到下一轮（ACP 没有 run id）。这条钉住新的有界 settle：
// 窗口内保持 in_progress，prompt 返回后才 run.cancelled。
func TestCancelWaitsForPromptSettlement(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	orch.SetSettleTimeout(500 * time.Millisecond)
	gate := &gatedPromptSession{started: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	as := session.NewActiveSession(session.HandleParams{ID: "s1", Runtime: "r", Session: gate})
	orch.handles.Put(as)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	<-gate.started
	if err := orch.CancelRun(context.Background(), "s1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case <-gate.returned:
		t.Fatal("取消后 prompt 不应立刻返回（要等底座 drain）")
	default:
	}
	if !svc.HostSessionRunning("s1") {
		t.Fatal("prompt 尚未结算时应保持 in_progress")
	}
	close(gate.release)
	evts := feed(bus, "s1", 3)
	if len(evts) < 3 || evts[len(evts)-1].Type != session.BusRunCancelled {
		t.Fatalf("结算后应收口为 run.cancelled，got %+v", evts)
	}
	if st, _ := svc.sessionPlane.Lookup("s1"); st.Status != session.SessionStatusCancelled {
		t.Fatalf("状态应 cancelled，got %s", st.Status)
	}
}

// TestServiceRESTErrors 会话动作错误映射为对外错误码：**参数不对是 40001、东西不在是 40401**。
//
// v19：建会话不再需要解析 agent（agent 面已撤销），但它**必须**落在一个工作空间里——没有 cwd
// 又没有默认工作空间是"参数不对"（40001），不是"找不到那个东西"（40401）。这条分界就是本用例
// 的判据：把它错报成 40401 会让用户去查一个并不存在的资源。
func TestServiceRESTErrors(t *testing.T) {
	svc, _, _ := newTestRig(t)
	_, err := svc.CreateSession(context.Background(), dtosession.SessionCreateReq{Prompt: "hi"})
	if numericCode(err) != 40001 {
		t.Fatalf("缺工作空间应 40001，got %d（%v）", numericCode(err), err)
	}
	if err := svc.CancelRun(context.Background(), "ghost"); err == nil || !strings.Contains(err.Error(), "40401") {
		t.Fatalf("CancelRun 期望 40401 错误，got %v", err)
	}
}

// stuckCancel 的 Cancel 仅在 ctx 到期（session.AgentCancelSendTimeout 兜底）时返回：
// 模拟 runtime 对 session/cancel 无响应（如模型限流重试挂起）。
type stuckCancel struct {
	*stubSession
}

func (s *stuckCancel) Cancel(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestCancelRunAgentUnresponsive agent 无响应时 CancelRun 有界返回并本地解除：
// 不依赖 session/cancel RPC，Prompt 总是被 ctx 取消打断 → run.cancelled。
func TestCancelRunAgentUnresponsive(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	as := session.NewActiveSession(session.HandleParams{
		ID: "s1", Runtime: "r",
		Session: &stuckCancel{stubSession: &stubSession{cancelOnCtx: true}},
	})
	orch.handles.Put(as)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	start := time.Now()
	if err := orch.CancelRun(context.Background(), "s1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if el := time.Since(start); el > session.AgentCancelSendTimeout+2*time.Second {
		t.Fatalf("CancelRun 被无响应 agent 拖住: %v（session.AgentCancelSendTimeout=%v）", el, session.AgentCancelSendTimeout)
	}
	evts := feed(bus, "s1", 3)
	if len(evts) < 3 || evts[len(evts)-1].Type != session.BusRunCancelled {
		t.Fatalf("期望 run.cancelled，got %+v", evts)
	}
	if st, _ := svc.sessionPlane.Lookup("s1"); st.Status != session.SessionStatusCancelled {
		t.Fatalf("状态应 cancelled: %v", func() string { s, _ := svc.sessionPlane.Lookup("s1"); return s.Status }())
	}
}

// TestBusyAttributionHelpers 验证在途归属表（§17 E1：session.InFlightTracker 语义同 §1.5 修订）：
// 登记/撤销/唯一性判定/nil 安全（经 Orchestrator 委托方法覆盖）。
func TestBusyAttributionHelpers(t *testing.T) {
	orch := &Orchestrator{inflight: session.NewInFlightTracker()}
	c1 := fakeConnOf(1)
	c2 := fakeConnOf(2)

	// 未登记 / nil client → 空
	if got := orch.busySessionForClient(c1); got != "" {
		t.Fatalf("未登记应返回空: %q", got)
	}
	if got := orch.busySessionForClient(nil); got != "" {
		t.Fatalf("nil client 应返回空: %q", got)
	}
	orch.inflight.Mark(nil, "s1") // no-op 不 panic

	orch.inflight.Mark(c1, "s1")
	if got := orch.busySessionForClient(c1); got != "s1" {
		t.Fatalf("唯一在途应为 s1: %q", got)
	}
	orch.inflight.Mark(c1, "s2")
	if got := orch.busySessionForClient(c1); got != "" {
		t.Fatalf("两会话并发在途不应猜测: %q", got)
	}
	orch.inflight.Unmark(c1, "s2")
	if got := orch.busySessionForClient(c1); got != "s1" {
		t.Fatalf("撤销 s2 后应回到 s1: %q", got)
	}
	orch.inflight.Unmark(c1, "s1")
	if got := orch.busySessionForClient(c1); got != "" {
		t.Fatalf("全部撤销后应为空: %q", got)
	}

	// 不同连接互不影响
	orch.inflight.Mark(c2, "s3")
	if got := orch.busySessionForClient(c1); got != "" {
		t.Fatalf("c1 不应受 c2 影响: %q", got)
	}
	if got := orch.busySessionForClient(c2); got != "s3" {
		t.Fatalf("c2 唯一在途应为 s3: %q", got)
	}
	orch.inflight.Unmark(c2, "s3")
}

// TestBusyAttributionLifecycle Prompt 到收尾的登记/撤销生命周期：
// run 进行中能唯一归属会话，取消收尾后归属清除。
func TestBusyAttributionLifecycle(t *testing.T) {
	svc, orch, _ := newTestRig(t)
	c1 := fakeConnOf(1)
	as := session.NewActiveSession(session.HandleParams{
		ID: "s1", Runtime: "r", Client: c1,
		Session: &stubSession{cancelOnCtx: true},
	})
	orch.handles.Put(as)
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "s1"})

	if _, err := orch.Prompt(context.Background(), "s1", "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := orch.busySessionForClient(c1); got != "s1" {
		t.Fatalf("run 进行中应归属 s1: %q", got)
	}
	if err := orch.CancelRun(context.Background(), "s1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	// executeRun 收尾后在途登记应清空（轮询等待，2s 防御）
	deadline := time.Now().Add(2 * time.Second)
	for orch.busySessionForClient(c1) != "" {
		if time.Now().After(deadline) {
			t.Fatal("run 收尾后在途登记未清除")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestEnsureSessionResumeGating 按需重建的门控（ADR-4：执行可丢弃、会话平台拥有）：
// 可续聊性只看会话是否存在 —— 上一轮终态（completed 等）不是作废判据；真正的不可用才报错
// （未知会话 / 构建期底座未装配）。这里不拨子进程：终态会话应一路走到 runtime 解析，
// 以 runtime_not_found 收尾即证明它没有被状态门拦下。
//
// v19 去掉了"目标 agent 已被删"那一支：底座由构建期固定，**"目标不存在"只剩一种形态**
// ——构建期选定的那个不在注册表里（`runtime_not_found`）。所以两段会话（有 / 无 RuntimeSessionID、
// interrupted / completed）现在都收敛到同一个错误；它们的差别在重建路径，不在门控。
func TestEnsureSessionResumeGating(t *testing.T) {
	svc, orch, _ := newTestRig(t) // 空 runtime 注册表
	ctx := context.Background()
	svc.sessionPlane.RegisterSession(&session.SessionRec{
		ID: "c-stub", RuntimeID: "stub", Status: session.SessionStatusInterrupted, // 无 RuntimeSessionID → memo 兜底候选
	})
	svc.sessionPlane.RegisterSession(&session.SessionRec{
		ID: "c-term", RuntimeID: "no-such-runtime", Status: session.SessionStatusCompleted,
		RuntimeSessionID: "x-1",
	})

	if _, err := orch.Prompt(ctx, "c-stub", "hi"); err == nil {
		t.Fatal("可续聊但构建期底座未装配应 runtime_not_found")
	} else if ae, ok := failure.As(err); !ok || ae.Code != "runtime_not_found" {
		t.Fatalf("期望 runtime_not_found，got %v", err)
	}
	// 终态会话（completed）不再按未找到处理：进入 runtime 解析 → 未注册 → runtime_not_found。
	if _, err := orch.Prompt(ctx, "c-term", "hi"); err == nil {
		t.Fatal("终态会话也应可续聊（应进到 runtime 解析）")
	} else if ae, ok := failure.As(err); !ok || ae.Code != "runtime_not_found" {
		t.Fatalf("期望 runtime_not_found（终态不再拦截），got %v", err)
	}
	if _, err := orch.Prompt(ctx, "no-such", "hi"); err == nil {
		t.Fatal("未知会话应 session_not_found")
	} else if ae, ok := failure.As(err); !ok || ae.Code != "session_not_found" {
		t.Fatalf("期望 session_not_found，got %v", err)
	}
}

// TestHandoffMemoBudget 已随 handoff 面搬进 `domain/session`（P3b-2b）：它现在直接验域的
// `HandoffMemo`，不再经 Service 转发（判据一字未改，见该包的 `TestHandoffMemoBudget`）。

// TestConnKeyModelScoping 连接缓存键：provider 密钥 env 与进程级模型 env 都影响共享键
// （B5 修复 + Phase B 模型作用域）。会话级模型（dsh，模型经 set_config_option）不进键。
func TestConnKeyModelScoping(t *testing.T) {
	native := []string{"DSH_PERMISSION_MODE=default"}
	keyE := []string{"OPENAI_API_KEY=sk-x"}
	modelE := []string{"RTX_MODEL=gpt-5"}

	base := runtimeconn.ConnKey("rtx", native)
	if base == runtimeconn.ConnKey("rtx", native, keyE) {
		t.Fatalf("provider 密钥 env 应并入连接键（B5 修复）")
	}
	if runtimeconn.ConnKey("rtx", native, keyE) != runtimeconn.ConnKey("rtx", native, keyE) {
		t.Fatalf("同 env 组应得同键")
	}
	if runtimeconn.ConnKey("rtx", native, keyE) == runtimeconn.ConnKey("rtx", native, keyE, modelE) {
		t.Fatalf("rtx（进程级模型）模型 env 应并入连接键（改模型 = 换连接）")
	}
	if runtimeconn.ConnKey("dsh", native, keyE) != runtimeconn.ConnKey("dsh", native, keyE, nil) {
		t.Fatalf("dsh（会话级模型）不应因不含模型 env 而改变键")
	}
	if runtimeconn.ConnKey("stub") != "stub" {
		t.Fatalf("无 env 应仅返回 runtimeID")
	}
}
