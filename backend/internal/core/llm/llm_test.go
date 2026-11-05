package llm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeExecutor 记录被调用次数，返回固定结果/错误。
type fakeExecutor struct {
	kind  ExecutorKind
	calls int
	res   Result
	err   error
}

func (f *fakeExecutor) Call(_ context.Context, _ Request) (Result, error) {
	f.calls++
	return f.res, f.err
}

// fakeRecorder 收集流水。
type fakeRecorder struct {
	calls []LLMCall
	err   error
}

func (f *fakeRecorder) Record(_ context.Context, c LLMCall) error {
	f.calls = append(f.calls, c)
	return f.err
}

// fixedClock 返回可控时刻序列（每次调用推进 10ms，供 LatencyMS 断言）。
type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time { c.t = c.t.Add(10 * time.Millisecond); return c.t }

// TestGatewayRoutesByPurpose 用途 → 默认执行器：agent_turn/summary 走 Stateful，其余走 Stateless。
func TestGatewayRoutesByPurpose(t *testing.T) {
	stateful := &fakeExecutor{kind: ExecutorStateful, res: Result{Text: "s"}}
	stateless := &fakeExecutor{kind: ExecutorStateless, res: Result{Text: "t"}}
	rec := &fakeRecorder{}
	g := NewGateway(GatewayConfig{
		Executors: map[ExecutorKind]Executor{
			ExecutorStateful:  stateful,
			ExecutorStateless: stateless,
		},
		Recorder: rec,
	})

	for _, tc := range []struct {
		purpose Purpose
		want    *fakeExecutor
	}{
		{PurposeAgentTurn, stateful},
		{PurposeSummary, stateful},
		{PurposeJudge, stateless},
		{PurposeEmbed, stateless},
		{PurposeTitle, stateless},
		{PurposeExtract, stateless},
		{PurposeRewrite, stateless},
	} {
		stateful.calls, stateless.calls = 0, 0
		if _, err := g.Call(context.Background(), Request{Purpose: tc.purpose}); err != nil {
			t.Fatalf("%s: Call err: %v", tc.purpose, err)
		}
		if tc.want.calls != 1 {
			t.Fatalf("%s 应路由到 %s（被调 %d 次）", tc.purpose, tc.want.kind, tc.want.calls)
		}
	}
}

// TestGatewayRecordsEveryCall 成败都记一条流水；成功记 OK、失败记 error。
func TestGatewayRecordsEveryCall(t *testing.T) {
	ok := &fakeExecutor{res: Result{Text: "x", Usage: TokenUsage{InputTokens: 3, OutputTokens: 4}}}
	rec := &fakeRecorder{}
	g := NewGateway(GatewayConfig{
		Executors: map[ExecutorKind]Executor{ExecutorStateful: ok},
		Recorder:  rec,
		Policy:    func(Purpose) ExecutorKind { return ExecutorStateful },
	})

	if _, err := g.Call(context.Background(), Request{Purpose: PurposeAgentTurn, Provider: "p", Model: "m"}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	boom := errors.New("boom")
	fail := &fakeExecutor{err: boom}
	g2 := NewGateway(GatewayConfig{
		Executors: map[ExecutorKind]Executor{ExecutorStateful: fail},
		Recorder:  rec,
		Policy:    func(Purpose) ExecutorKind { return ExecutorStateful },
	})
	if _, err := g2.Call(context.Background(), Request{Purpose: PurposeAgentTurn}); !errors.Is(err, boom) {
		t.Fatalf("应透传执行器错误，得到 %v", err)
	}

	if len(rec.calls) != 2 {
		t.Fatalf("成败各记一条，应 2 条，得到 %d", len(rec.calls))
	}
	if rec.calls[0].Status != StatusOK || rec.calls[1].Status != StatusError {
		t.Fatalf("状态不符: %q / %q", rec.calls[0].Status, rec.calls[1].Status)
	}
	if rec.calls[0].Provider != "p" || rec.calls[0].Model != "m" {
		t.Fatalf("provider/model 未带入流水: %+v", rec.calls[0])
	}
	if rec.calls[0].Usage.InputTokens != 3 {
		t.Fatalf("usage 未带入流水: %+v", rec.calls[0])
	}
}

// TestGatewayPreferOverridesPolicy Prefer 显式指定优先于 policy。
func TestGatewayPreferOverridesPolicy(t *testing.T) {
	stateful := &fakeExecutor{}
	stateless := &fakeExecutor{}
	g := NewGateway(GatewayConfig{
		Executors: map[ExecutorKind]Executor{
			ExecutorStateful:  stateful,
			ExecutorStateless: stateless,
		},
	})
	// agent_turn 默认 Stateful，但显式 Prefer Stateless。
	if _, err := g.Call(context.Background(), Request{Purpose: PurposeAgentTurn, Prefer: ExecutorStateless}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if stateless.calls != 1 || stateful.calls != 0 {
		t.Fatalf("Prefer 未生效: stateful=%d stateless=%d", stateful.calls, stateless.calls)
	}
}

// TestGatewayMissingExecutor 执行器未装配 → 明确错误 + 仍记一条 error 流水。
func TestGatewayMissingExecutor(t *testing.T) {
	rec := &fakeRecorder{}
	g := NewGateway(GatewayConfig{Executors: map[ExecutorKind]Executor{}, Recorder: rec})
	if _, err := g.Call(context.Background(), Request{Purpose: PurposeJudge}); err == nil {
		t.Fatal("应返回执行器缺失错误")
	}
	if len(rec.calls) != 1 || rec.calls[0].Status != StatusError {
		t.Fatalf("缺失执行器也应记一条 error 流水: %+v", rec.calls)
	}
}

// TestGatewayLatencyAndClock LatencyMS 用注入时钟计算，ID 用注入铸号。
func TestGatewayLatencyAndClock(t *testing.T) {
	clk := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	rec := &fakeRecorder{}
	g := NewGateway(GatewayConfig{
		Executors: map[ExecutorKind]Executor{ExecutorStateful: &fakeExecutor{}},
		Recorder:  rec,
		Policy:    func(Purpose) ExecutorKind { return ExecutorStateful },
		Now:       clk.now,
		NewID:     func() string { return "call_fixed" },
	})
	if _, err := g.Call(context.Background(), Request{Purpose: PurposeAgentTurn}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	c := rec.calls[0]
	if c.ID != "call_fixed" {
		t.Fatalf("ID 应来自 NewID: %q", c.ID)
	}
	// 两次 now（start + record）各推进 10ms → LatencyMS = 10。
	if c.LatencyMS != 10 {
		t.Fatalf("LatencyMS 应为 10，得到 %d", c.LatencyMS)
	}
}
