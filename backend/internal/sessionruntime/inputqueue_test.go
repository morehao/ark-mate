package sessionruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/domain/session"

	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/platform/eventbus"
	"github.com/morehao/ark-mate/internal/contract/dtosession"
	"github.com/morehao/ark-mate/internal/runtime"
	"github.com/morehao/ark-mate/internal/testutil"
	"github.com/morehao/golib/gerror"
)

// ——— §6 busy input 队列的测试夹具 ———

// gatedSession 一轮一轮放行的会话桩：Prompt 阻塞到收到 release（或 ctx 取消）。
// 用它把「会话正忙」这一状态握在测试手里——echo runtime 的轮次太快，来不及排队。
type gatedSession struct {
	started chan string   // 每轮 prompt 文本送达（测试据此等「某一轮真的开始了」）
	release chan struct{} // 每收到一个信号放行当前轮
	mu      sync.Mutex
	all     []string // 全部轮次的文本（顺序即投递顺序）
}

func newGatedSession() *gatedSession {
	return &gatedSession{started: make(chan string, 16), release: make(chan struct{}, 16)}
}

func (s *gatedSession) Prompt(ctx context.Context, text string) (string, error) {
	s.mu.Lock()
	s.all = append(s.all, text)
	s.mu.Unlock()
	s.started <- text
	select {
	case <-s.release:
		return "end_turn", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *gatedSession) Cancel(context.Context) error          { return nil }
func (s *gatedSession) ConfigOptions() []runtime.ConfigOption { return nil }
func (s *gatedSession) ID() string                            { return "" }
func (s *gatedSession) Capabilities() runtime.CapabilityFacts { return runtime.CapabilityFacts{} }
func (s *gatedSession) Close(context.Context) error           { return nil }
func (s *gatedSession) SetConfigOption(context.Context, string, string) ([]runtime.ConfigOption, error) {
	return nil, nil
}

func (s *gatedSession) prompts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.all...)
}

// newQueueRig 队列测试架子：echo 注册表 + 总线 +（可选）临时库 + 一条**已登记
// 且有活句柄**的会话 conv-1（句柄的 sess 由调用方给定，用于掌控轮次节奏）。
func newQueueRig(t *testing.T, sess session.AgentSession, withStore bool) (*Service, *Orchestrator, *gatedSession, *session.ActiveSession) {
	t.Helper()
	r := newRig(t, func(c *rigConfig) {
		c.stub, c.bus, c.store = true, true, withStore
	})
	g := sess.(*gatedSession)
	as := session.NewActiveSession(session.HandleParams{ID: "conv-1", Runtime: "stub", Session: sess})
	r.Orch.handles.Put(as)
	r.Svc.sessionPlane.RegisterSession(&session.SessionRec{
		ID: as.ID, RuntimeID: "stub", Status: session.SessionStatusInProgress, Generation: 1,
	})
	t.Cleanup(func() {
		// 放行所有阻塞中的轮次，**等到在途轮次真正收尾**，最后才摘句柄——否则清库时还有 executeRun 在写。
		//
		// 判据**不能**是 `as.busy == false`：`executeRun` 的收尾顺序是
		// 「先置 `as.busy = false` / `as.run = nil` → 再发布终态事件、落库（events/runs/transcript）
		// → 最后 `close(t.done)`」。用 busy 判据会在"还没写完库"时就返回，紧随其后的关库与
		// `t.TempDir()` 清理便与那次写入赛跑：SQLite 在失败**之前**会先建出 `-journal`，
		// 于是 `RemoveAll` 的 `unlinkat` 撞上这个瞬态文件 → 偶发
		// `TempDir RemoveAll cleanup: directory not empty`（本仓实测基线约 3%）。
		//
		// 这里**只等、绝不写**（不 flush、不关合并器）：任何在清理期额外触发的写都只会把
		// `-journal` 的瞬态窗口撑得更大——实测把 `coalescer.Close()` 挂进清理链会把概率从
		// 约 3% 抬到约 40%。等 `t.done`（收尾写完才关闭）才是与 `executeRun` 同步的正确信号。
		for i := 0; i < 8; i++ {
			select {
			case g.release <- struct{}{}:
			default:
			}
		}
		drainRun := func() bool { // true = 已无在途轮次
			t := as.ActiveRun()
			if t == nil {
				return true
			}
			select {
			case <-t.Done():
				return false // 这一轮收尾完了；可能又被投递了下一轮，外层再判一次
			case <-time.After(time.Second):
				return true
			}
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if !drainRun() {
				continue
			}
			// `as.run = nil` 早于 `close(t.done)`，所以"看到 nil"仍有可能是收尾窗口：
			// 给一个静默窗口再确认一次确实没有新轮次，才摘句柄。
			time.Sleep(5 * time.Millisecond)
			still := as.ActiveRun()
			if still == nil {
				break
			}
		}
		r.Orch.runner.Evict(as)
	})
	return r.Svc, r.Orch, g, as
}

// busEvents 取当前总线上已发布的事件（含历史；断言用）。
func busEvents(svc *Service) []eventbus.Event { return svc.bus.EventsAll() }

// payloadOf 取事件的 map 载荷（域事件载荷恒为 map；非 map 时返回空 map）。
func payloadOf(ev eventbus.Event) map[string]any {
	if m, ok := ev.Payload.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// waitQueueDrain 等队列排空（投递发生在 executeRun 的 goroutine 里，断言前必须等它落地）。
func waitQueueDrain(t *testing.T, svc *Service, convID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(svc.sessionPlane.ListQueuedInputsOf(context.Background(), convID)) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待队列排空超时: %+v", svc.sessionPlane.ListQueuedInputsOf(context.Background(), convID))
}

// readInputs 直读 inputs 表的状态列（台账断言：作废/已投递的行必须还在）。
func readInputs(t *testing.T, svc *Service) ([]string, error) {
	t.Helper()
	if planesOf(svc).Resources.Store() == nil {
		t.Fatal("本用例需要 sqlite 库")
	}
	rows, err := planesOf(svc).Resources.Store().DB().Raw("SELECT status FROM inputs ORDER BY created_at, id").Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var st string
		if err := rows.Scan(&st); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// awaitPrompt 等下一轮真的开始（超时 3s 防御），返回该轮文本。
func awaitPrompt(t *testing.T, g *gatedSession) string {
	t.Helper()
	select {
	case text := <-g.started:
		return text
	case <-time.After(3 * time.Second):
		t.Fatal("等待轮次开始超时")
		return ""
	}
}

// startFirstRun 起一轮并等它真的进入执行（此后会话处于 busy）。
func startFirstRun(t *testing.T, orch *Orchestrator, g *gatedSession, text string) {
	t.Helper()
	if _, err := orch.Prompt(context.Background(), "conv-1", text); err != nil {
		t.Fatalf("首轮 prompt 失败: %v", err)
	}
	if got := awaitPrompt(t, g); got != text {
		t.Fatalf("首轮文本不符: %q", got)
	}
}

// ——— 用例 ———

// TestPromptAfterRunQueuesWhenBusy 会话忙 + after_run = 入队（不报错、不发 user_message），
// 响应带 queued/input_id，事件发 session.input_queued。
func TestPromptAfterRunQueuesWhenBusy(t *testing.T) {
	svc, orch, g, _ := newQueueRig(t, newGatedSession(), true)
	startFirstRun(t, orch, g, "第一轮")

	resp, err := sessionOf(svc).PromptSession(context.Background(), "conv-1",
		dtosession.PromptReq{Prompt: "排队的话", Delivery: session.DeliveryAfterRun})
	if err != nil {
		t.Fatalf("after_run 入队失败: %v", err)
	}
	if !resp.Queued || resp.InputID == "" {
		t.Fatalf("响应应带 queued+input_id: %+v", resp)
	}
	// 队列里确实有它，且**没有**被当成新轮次执行。
	queued := svc.sessionPlane.ListQueuedInputsOf(context.Background(), "conv-1")
	if len(queued) != 1 || queued[0].Text != "排队的话" || queued[0].Delivery != session.DeliveryAfterRun {
		t.Fatalf("队列内容不符: %+v", queued)
	}
	if got := g.prompts(); len(got) != 1 {
		t.Fatalf("排队不应立刻消费: %v", got)
	}
	if queued[0].TargetRunID == "" {
		t.Fatal("应记下入队时正在执行的那一轮")
	}
	// 事件：input_queued（含文本与投递模式——前端队列靠它建项）。
	var found bool
	for _, ev := range busEvents(svc) {
		if ev.Type != session.BusInputQueued {
			continue
		}
		found = true
		if p := payloadOf(ev); p["input_id"] != resp.InputID || p["text"] != "排队的话" ||
			p["delivery"] != session.DeliveryAfterRun {
			t.Fatalf("input_queued 载荷不符: %+v", ev.Payload)
		}
	}
	if !found {
		t.Fatal("应发布 session.input_queued")
	}
}

// TestQueuedInputDeliveredAfterRunEnds 轮次正常收尾 → 队首自动投递为新一轮（FIFO），
// 并发布 input_delivered + 该轮的 user_message。
func TestQueuedInputDeliveredAfterRunEnds(t *testing.T) {
	svc, orch, g, _ := newQueueRig(t, newGatedSession(), true)
	startFirstRun(t, orch, g, "第一轮")
	if _, err := sessionOf(svc).PromptSession(context.Background(), "conv-1",
		dtosession.PromptReq{Prompt: "排队一", Delivery: session.DeliveryAfterRun}); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionOf(svc).PromptSession(context.Background(), "conv-1",
		dtosession.PromptReq{Prompt: "排队二", Delivery: session.DeliveryAfterRun}); err != nil {
		t.Fatal(err)
	}

	g.release <- struct{}{} // 放行第一轮 → executeRun 收尾 → 投递「排队一」
	if got := awaitPrompt(t, g); got != "排队一" {
		t.Fatalf("应投递队首「排队一」，得到 %q", got)
	}
	if left := svc.sessionPlane.ListQueuedInputsOf(context.Background(), "conv-1"); len(left) != 1 || left[0].Text != "排队二" {
		t.Fatalf("第二个应仍在队列: %+v", left)
	}

	g.release <- struct{}{} // 放行第二轮 → 投递「排队二」
	if got := awaitPrompt(t, g); got != "排队二" {
		t.Fatalf("应投递「排队二」，得到 %q", got)
	}
	g.release <- struct{}{} // 收尾，队列排空
	waitQueueDrain(t, svc, "conv-1")

	if got := g.prompts(); strings.Join(got, "|") != "第一轮|排队一|排队二" {
		t.Fatalf("投递顺序不符: %v", got)
	}
	// 事件面：每条排队输入恰好一次 delivered，且 delivered 之前有对应的 user_message。
	var delivered, cancelled int
	for _, ev := range busEvents(svc) {
		switch ev.Type {
		case session.BusInputDelivered:
			delivered++
			if payloadOf(ev)["reason"] != session.ReasonRunFinished {
				t.Fatalf("delivered 应带 reason=run_finished: %+v", ev.Payload)
			}
		case session.BusInputCancelled:
			cancelled++
		}
	}
	if delivered != 2 || cancelled != 0 {
		t.Fatalf("delivered/cancelled 计数不符: %d/%d", delivered, cancelled)
	}
}

// TestQueuedInputStaledWhenGenerationChanged 入队后执行上下文被重建（代际 +1）→ 该项作废、
// 不投递（旧上下文里的排队不该灌进新会话），且轮次照常收尾。
func TestQueuedInputStaledWhenGenerationChanged(t *testing.T) {
	svc, orch, g, as := newQueueRig(t, newGatedSession(), true)
	startFirstRun(t, orch, g, "第一轮")
	if _, err := sessionOf(svc).PromptSession(context.Background(), "conv-1",
		dtosession.PromptReq{Prompt: "为旧上下文排的队", Delivery: session.DeliveryAfterRun}); err != nil {
		t.Fatal(err)
	}
	// 模拟连接断开重连：执行代际自增（真实路径见 ensureSession 里的 bumpGeneration）。
	svc.sessionPlane.BumpGeneration(as.ID)

	g.release <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(svc.sessionPlane.ListQueuedInputsOf(context.Background(), as.ID)) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := g.prompts(); len(got) != 1 {
		t.Fatalf("作废项不应被投递: %v", got)
	}
	if left := svc.sessionPlane.ListQueuedInputsOf(context.Background(), as.ID); len(left) != 0 {
		t.Fatalf("作废项应离开待投递集合: %+v", left)
	}
	// 台账行保留（stale），不是被删掉。
	rows, err := readInputs(t, svc)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0] != "stale" {
		t.Fatalf("作废应留台账行: %v", rows)
	}
}

// TestQueuedInputInterruptThenPrompt 忙 + interrupt_then_prompt = 取消当前轮后投递本项。
func TestQueuedInputInterruptThenPrompt(t *testing.T) {
	svc, orch, g, _ := newQueueRig(t, newGatedSession(), true)
	startFirstRun(t, orch, g, "被打断的一轮")

	resp, err := sessionOf(svc).PromptSession(context.Background(), "conv-1",
		dtosession.PromptReq{Prompt: "插话", Delivery: session.DeliveryInterruptThenPrompt})
	if err != nil {
		t.Fatalf("interrupt_then_prompt 失败: %v", err)
	}
	if !resp.Queued {
		t.Fatalf("应先入队再取消: %+v", resp)
	}
	// 取消生效（stub 的 ctx 被取消 → 第一轮返回取消）；随后收尾路径投递插话项。
	if got := awaitPrompt(t, g); got != "插话" {
		t.Fatalf("应投递插话项，得到 %q", got)
	}
	g.release <- struct{}{}
	waitQueueDrain(t, svc, "conv-1")
	if got := g.prompts(); len(got) != 2 {
		t.Fatalf("应恰好两轮: %v", got)
	}
	var sawCancelled bool
	for _, ev := range busEvents(svc) {
		if ev.Type == session.BusRunCancelled {
			sawCancelled = true
		}
	}
	if !sawCancelled {
		t.Fatal("应发布 run.cancelled（用户插话取消了这一轮）")
	}
}

// TestDeleteQueuedInput 撤下待投递项 → 出队 + 发 input_cancelled；重复撤下/不存在的 id 幂等 false；
// 已投递的项不可撤（台账行不可删）。
func TestDeleteQueuedInput(t *testing.T) {
	svc, orch, g, _ := newQueueRig(t, newGatedSession(), true)
	startFirstRun(t, orch, g, "第一轮")
	resp, err := sessionOf(svc).PromptSession(context.Background(), "conv-1",
		dtosession.PromptReq{Prompt: "要撤下的", Delivery: session.DeliveryAfterRun})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := queueSvc(svc).DeleteQueuedInput(context.Background(), "conv-1", resp.InputID)
	if err != nil || !ok {
		t.Fatalf("撤下失败: ok=%v err=%v", ok, err)
	}
	if left := svc.sessionPlane.ListQueuedInputsOf(context.Background(), "conv-1"); len(left) != 0 {
		t.Fatalf("撤下后队列应为空: %+v", left)
	}
	if again, err := queueSvc(svc).DeleteQueuedInput(context.Background(), "conv-1", resp.InputID); err != nil || again {
		t.Fatalf("重复撤下应幂等 false: %v/%v", again, err)
	}
	// 跨会话撤下：换个会话 id 撤不掉（归属由 SQL 双条件钉住）。
	if other, err := queueSvc(svc).DeleteQueuedInput(context.Background(), "conv-other", resp.InputID); err != nil || other {
		t.Fatalf("跨会话撤下必须失败: %v/%v", other, err)
	}
	var cancelled bool
	for _, ev := range busEvents(svc) {
		if ev.Type == session.BusInputCancelled && payloadOf(ev)["reason"] == session.ReasonUserCancel {
			cancelled = true
		}
	}
	if !cancelled {
		t.Fatal("应发布 session.input_cancelled(user_cancel)")
	}
	// 轮次收尾后队列已空 → 不该有 delivered。
	g.release <- struct{}{}
	waitQueueDrain(t, svc, "conv-1")
	for _, ev := range busEvents(svc) {
		if ev.Type == session.BusInputDelivered {
			t.Fatalf("撤下的项不该被投递: %+v", ev.Payload)
		}
	}
}

// TestPromptDeliveryValidation 非法 delivery 显式报错（不静默回落 reject）；reject 保持历史行为。
func TestPromptDeliveryValidation(t *testing.T) {
	svc, orch, g, _ := newQueueRig(t, newGatedSession(), true)
	startFirstRun(t, orch, g, "第一轮")

	// 领域层：稳定 code 在平台错误上（`delivery_*` 这类码由 RuntimeErrorOf 读出，不进文案）。
	if _, err := session.NormalizeDelivery("whenever"); err == nil {
		t.Fatal("非法 delivery 应报错")
	} else {
		if re, ok := code.RuntimeErrorOf(err); !ok || re.Code != "invalid_delivery" {
			t.Fatalf("应报 invalid_delivery: %v", err)
		}
	}
	if got, err := session.NormalizeDelivery(""); err != nil || got != session.DeliveryReject {
		t.Fatalf("空值应归一为 reject: %q/%v", got, err)
	}
	// 服务出口：信封码是平台分类的投影（ClassRequest → 40001，ClassLimit → 40901）。
	if _, err := sessionOf(svc).PromptSession(context.Background(), "conv-1",
		dtosession.PromptReq{Prompt: "x", Delivery: "whenever"}); err == nil {
		t.Fatal("非法 delivery 应报错")
	} else {
		var ge gerror.Error
		if !errors.As(err, &ge) || ge.Code != code.InvalidRequest {
			t.Fatalf("应报 %d: %v", code.InvalidRequest, err)
		}
	}
	if _, err := sessionOf(svc).PromptSession(context.Background(), "conv-1",
		dtosession.PromptReq{Prompt: "x", Delivery: session.DeliveryReject}); err == nil {
		t.Fatal("忙 + reject 应报错（历史行为）")
	} else {
		var ge gerror.Error
		if !errors.As(err, &ge) || ge.Code != code.Conflict {
			t.Fatalf("忙 + reject 应报 %d: %v", code.Conflict, err)
		}
	}
	if left := svc.sessionPlane.ListQueuedInputsOf(context.Background(), "conv-1"); len(left) != 0 {
		t.Fatalf("两次失败都不该留下队列项: %+v", left)
	}
}

// TestPromptAfterRunWhenIdleRunsImmediately 会话空闲时 after_run 与 reject 同路径：
// 直接执行，不留队列行（没有「等的那一轮」）。
func TestPromptAfterRunWhenIdleRunsImmediately(t *testing.T) {
	svc, _, g, _ := newQueueRig(t, newGatedSession(), true)
	resp, err := sessionOf(svc).PromptSession(context.Background(), "conv-1",
		dtosession.PromptReq{Prompt: "空闲时提交", Delivery: session.DeliveryAfterRun})
	if err != nil {
		t.Fatalf("空闲投递失败: %v", err)
	}
	if resp.Queued || resp.InputID != "" {
		t.Fatalf("空闲时不该入队: %+v", resp)
	}
	if got := awaitPrompt(t, g); got != "空闲时提交" {
		t.Fatalf("应直接执行: %q", got)
	}
	if left := svc.sessionPlane.ListQueuedInputsOf(context.Background(), "conv-1"); len(left) != 0 {
		t.Fatalf("不该留下队列行: %+v", left)
	}
	g.release <- struct{}{}
	waitQueueDrain(t, svc, "conv-1")
}

// TestQueuedInputsStaledOnRestore 重启（RestoreFromStore）把库里的待投递项统一置作废：
// 不自动重放（等待的那一轮已随进程消失）。
func TestQueuedInputsStaledOnRestore(t *testing.T) {
	st := testutil.OpenStore(t, testutil.TempDBPath(t))
	t.Cleanup(func() { _ = st.Close() })
	svc1 := newServiceForTest(runtime.NewRegistry(), nil)
	enableStore(svc1, st)
	svc1.sessionPlane.RegisterSession(&session.SessionRec{ID: "c1", Generation: 1})
	queued, err := svc1.sessionPlane.EnqueueInput(context.Background(), "c1", "重启前的排队", session.DeliveryAfterRun, "run-x", 1)
	if err != nil {
		t.Fatal(err)
	}

	svc2 := newServiceForTest(runtime.NewRegistry(), nil)
	enableStore(svc2, st)
	if _, _, err := svc2.SessionPlane().Restore(context.Background()); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if left := svc2.sessionPlane.ListQueuedInputsOf(context.Background(), "c1"); len(left) != 0 {
		t.Fatalf("重启后不该有可投递项: %+v", left)
	}
	rows, err := readInputs(t, svc2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0] != "stale" {
		t.Fatalf("应留一条 stale 台账行: %v", rows)
	}

	// 作废必须发 input_cancelled(reason=stale)：前端队列只认事件，只改台账行会让重启后的
	// 页面留下一条永远投不出、也撤不掉的幻影队列项。
	evs, _, err := st.EventsAfter(context.Background(), "c1", -1, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		if e.Type != session.BusInputCancelled {
			continue
		}
		p, _ := session.DecodePayload(e.PayloadJSON).(map[string]any)
		if p["input_id"] != queued.ID || p["reason"] != session.ReasonStale || p["delivery"] != session.DeliveryAfterRun {
			t.Fatalf("作废事件载荷不符: %+v", p)
		}
		found = true
	}
	if !found {
		t.Fatalf("应在事件表里留下 input_cancelled（重放历史才能收敛队列），events=%d", len(evs))
	}
}

// TestInputQueueMemoryMode memory 模式（无库）：入队/撤下/投递全在进程内，进程结束即消失。
func TestInputQueueMemoryMode(t *testing.T) {
	svc, orch, g, _ := newQueueRig(t, newGatedSession(), false)
	startFirstRun(t, orch, g, "第一轮")
	resp, err := sessionOf(svc).PromptSession(context.Background(), "conv-1",
		dtosession.PromptReq{Prompt: "内存里的排队", Delivery: session.DeliveryAfterRun})
	if err != nil || !resp.Queued {
		t.Fatalf("内存模式应能入队: %v/%+v", err, resp)
	}
	g.release <- struct{}{}
	if got := awaitPrompt(t, g); got != "内存里的排队" {
		t.Fatalf("内存模式应投递: %q", got)
	}
	g.release <- struct{}{}
	waitQueueDrain(t, svc, "conv-1")
}

// TestQueuedInputThreeActions ADR-10 的三件动作：重排 / 立即发送 / 取出编辑。
//
// 三件都挂在「用户看得见后果」这条判据上（这正是本仓只补这三件、不抄参照产品的 17 个方法的理由）：
//
//	重排     「我看到的顺序」就是投递顺序
//	立即发送 不想等了（跳过 FIFO 成为下一轮）
//	取出编辑 发错了（取回草稿并作废，**且要说话**）
//
// 最要紧的一条是「取出编辑必须发 `input_cancelled{reason:user_edit}`」：这条事件是那一行从
// 界面队列消失的**唯一**依据——漏发的话 REST 照样回成功、库里那行也没了，而队列里那一行
// 会一直挂着，直到刷新页面。那种"看起来成功了"的静默，是这一批里最容易漏掉的一份。
func TestQueuedInputThreeActions(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, func(c *rigConfig) { c.store, c.bus = true, true })
	svc := r.Svc
	id := "s-q3"
	svc.sessionPlane.RegisterSession(&session.SessionRec{ID: id})
	q := queueSvc(svc)

	// 三条排队输入（store 分配会话内位次）。
	var ids []string
	for _, text := range []string{"一", "二", "三"} {
		in, err := svc.sessionPlane.EnqueueInput(ctx, id, text, session.DeliveryAfterRun, "", 0)
		if err != nil {
			t.Fatalf("EnqueueInput: %v", err)
		}
		ids = append(ids, in.ID)
	}
	orderOf := func() []string {
		out := make([]string, 0, 3)
		for _, it := range svc.sessionPlane.ListQueuedInputsOf(ctx, id) {
			out = append(out, it.ID)
		}
		return out
	}
	if got := orderOf(); len(got) != 3 || got[0] != ids[0] {
		t.Fatalf("初始顺序应为入队顺序，got %v", got)
	}

	// ① 重排：把第三条插到第一条之前（DOM insertBefore 语义）。
	if err := q.MoveQueuedInput(ctx, id, ids[2], ids[0]); err != nil {
		t.Fatalf("MoveQueuedInput: %v", err)
	}
	if got := orderOf(); got[0] != ids[2] || got[1] != ids[0] || got[2] != ids[1] {
		t.Fatalf("重排后顺序不符：got %v（want 三/一/二）", got)
	}
	// 追加到末尾（before 为空）。
	if err := q.MoveQueuedInput(ctx, id, ids[2], ""); err != nil {
		t.Fatalf("MoveQueuedInput(末尾): %v", err)
	}
	if got := orderOf(); got[2] != ids[2] {
		t.Fatalf("追加到末尾失败：got %v", got)
	}
	// 不存在的 id 不是幂等：它是"你以为移动成功"的假象。
	if err := q.MoveQueuedInput(ctx, id, "inp_nope", ""); numericCode(err) != 40401 {
		t.Fatalf("移动不存在的队项应 40401: %v", err)
	}

	// ② 立即发送：排到队首（会话忙/无句柄时端口那半是 no-op，顺序仍然落定）。
	if err := q.SendNow(ctx, id, ids[1]); err != nil {
		t.Fatalf("SendNow: %v", err)
	}
	if got := orderOf(); got[0] != ids[1] {
		t.Fatalf("立即发送应把它排到队首：got %v", got)
	}

	// ③ 取出编辑：回正文 + 离队 + **发一条 user_edit 的 cancelled**。
	sub := svc.bus.SubscribeAfter(nil)
	defer svc.bus.Unsubscribe(sub)
	text, err := q.PopForEdit(ctx, id, ids[1])
	if err != nil {
		t.Fatalf("PopForEdit: %v", err)
	}
	if text != "二" {
		t.Fatalf("取回的正文应为 %q，got %q", "二", text)
	}
	if got := orderOf(); len(got) != 2 {
		t.Fatalf("取出后队列应剩 2 条：got %v", got)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-sub.C:
			if e.Type != session.BusInputCancelled {
				continue
			}
			m, _ := e.Payload.(map[string]any)
			if m["input_id"] != ids[1] {
				continue
			}
			if m["reason"] != session.ReasonUserEdit {
				t.Fatalf("取出编辑的 reason 应为 %q（与 user_cancel 分开是刻意的：界面含义不同），got %v",
					session.ReasonUserEdit, m["reason"])
			}
			return
		case <-deadline:
			t.Fatalf("取出编辑必须发一条 session.input_cancelled：不发的话 REST 说成功了、库里也没了，" +
				"而前端的队列只认事件——那一行会永远挂着")
		}
	}
}
