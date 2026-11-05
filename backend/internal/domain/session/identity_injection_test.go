package session

import (
	"context"

	"github.com/morehao/ark-mate/internal/core/object/objusage"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"sync"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/eventbus"
)

// ——— 首轮身份注入（P2 的确定性判据） ———
//
// A1（"问你是谁 3/3 答 ark-mate"）靠的是两件事：系统提示（每轮）与首轮 user-context 块
// （每个执行上下文一次）。这里钉的是后者在**会话域**的四条行为：
//
//	① 首个 prompt 的正文以身份块开头（identity → memo → 用户原文）；
//	② `session.user_message` 事件仍是**用户原文**（前缀不进事件、不进 transcript）；
//	③ 第二轮不再重复注入（「每个执行上下文一次」，不是每轮一次）；
//	④ 没有身份时不改变任何字节（`off` 模式的回归判据）。

// captureACP 记下发给底座的 prompt 正文（测试桩：不拨号、不执行任何东西）。
type captureACP struct {
	mu      sync.Mutex
	prompts []string
	ch      chan struct{}
}

func newCaptureACP() *captureACP { return &captureACP{ch: make(chan struct{}, 8)} }

func (c *captureACP) Prompt(_ context.Context, parts []protocol.PromptPart) (string, objusage.TokenUsage, error) {
	var text string
	for _, p := range parts {
		if p.Kind == protocol.PromptPartText || p.Kind == "" {
			text += p.Text
		}
	}
	c.mu.Lock()
	c.prompts = append(c.prompts, text)
	c.mu.Unlock()
	c.ch <- struct{}{}
	return "end_turn", objusage.TokenUsage{}, nil
}

func (c *captureACP) Cancel(context.Context) error           { return nil }
func (c *captureACP) ConfigOptions() []protocol.ConfigOption { return nil }

func (c *captureACP) PromptAt(i int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.prompts[i]
}

func (c *captureACP) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.prompts)
}

// waitPrompt 等第 n 条 prompt 落地（executeRun 是异步的）。
func waitPrompt(t *testing.T, c *captureACP, n int) {
	t.Helper()
	select {
	case <-c.ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("等第 %d 条 prompt 超时（已收到 %d 条）", n, c.Count())
	}
}

// waitIdle 等这一轮收尾（executeRun 把 busy 清掉）。
func waitIdle(t *testing.T, as *ActiveSession) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for as.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("等会话空闲超时")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// newIdentityRunner 造一个只有会话域在跑的最小装置（无库、无 orch、无连接）。
func newIdentityRunner(t *testing.T, as *ActiveSession) (*Runner, *eventbus.Bus) {
	t.Helper()
	bus := eventbus.New()
	pl := NewPlane()
	pl.SetBus(bus)
	handles := NewHandles()
	handles.Put(as)
	r := NewRunner(RunnerDeps{
		Plane:   pl,
		Handles: handles,
		Acquire: func(context.Context, string) (*ActiveSession, error) { return as, nil },
		Busy:    NewInFlightTracker(),
	})
	return r, bus
}

// TestFirstPromptStartsWithIdentity 首轮顺序与事件原文（①②）。
func TestFirstPromptStartsWithIdentity(t *testing.T) {
	agent := newCaptureACP()
	as := NewActiveSession(HandleParams{
		ID: "s1", Runtime: "r", Identity: "<product_identity>身份</product_identity>",
		Memo: "摘要", Session: agent,
	})
	r, bus := newIdentityRunner(t, as)

	if _, err := r.Prompt(context.Background(), "s1", "你是谁"); err != nil {
		t.Fatalf("发首轮: %v", err)
	}
	waitPrompt(t, agent, 1)

	want := "<product_identity>身份</product_identity>\n\n摘要\n\n你是谁"
	if got := agent.PromptAt(0); got != want {
		t.Fatalf("首轮 prompt 顺序应为 identity → memo → 用户原文：\n got=%q\nwant=%q", got, want)
	}
	// 事件仍是用户原文：前缀是**平台注入**，不是用户说的话。
	var found bool
	for _, e := range bus.EventsAll() {
		if e.Type != BusUserMessage {
			continue
		}
		found = true
		p, _ := e.Payload.(map[string]any)
		if text, _ := p["text"].(string); text != "你是谁" {
			t.Fatalf("session.user_message 必须是用户原文，得到 %q", text)
		}
	}
	if !found {
		t.Fatal("没有发布 session.user_message 事件")
	}
}

// TestIdentityInjectedOnlyOnce 第三条：每个执行上下文一次，不是每轮一次。
func TestIdentityInjectedOnlyOnce(t *testing.T) {
	agent := newCaptureACP()
	as := NewActiveSession(HandleParams{
		ID: "s1", Runtime: "r", Identity: "<product_identity>身份</product_identity>", Session: agent,
	})
	r, _ := newIdentityRunner(t, as)

	if _, err := r.Prompt(context.Background(), "s1", "第一句"); err != nil {
		t.Fatalf("首轮: %v", err)
	}
	waitPrompt(t, agent, 1)
	waitIdle(t, as)

	if _, err := r.Prompt(context.Background(), "s1", "第二句"); err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	waitPrompt(t, agent, 2)

	if got := agent.PromptAt(1); got != "第二句" {
		t.Fatalf("身份块只应注入首个执行上下文，第二轮应逐字等于用户原文，得到 %q", got)
	}
}

// TestNoIdentityChangesNothing 第四条：`off` 模式（身份为空）下 prompt 逐字等于原文。
func TestNoIdentityChangesNothing(t *testing.T) {
	agent := newCaptureACP()
	as := NewActiveSession(HandleParams{ID: "s1", Runtime: "r", Session: agent})
	r, _ := newIdentityRunner(t, as)

	if _, err := r.Prompt(context.Background(), "s1", "你好"); err != nil {
		t.Fatalf("发首轮: %v", err)
	}
	waitPrompt(t, agent, 1)
	if got := agent.PromptAt(0); got != "你好" {
		t.Fatalf("没有身份时不应改变 prompt 字节，得到 %q", got)
	}
}

// TestRunPrefixComposeOrder 顺序判据本身（identity 在 memo 之前）。
//
// 单独钉它是因为"顺序写死在调用点"正是下一次改动最容易对调的东西，而对调的后果不是报错，
// 是历史摘要覆盖产品身份（旧自述回流）。
func TestRunPrefixComposeOrder(t *testing.T) {
	p := RunPrefix{Identity: "ID", Memo: "MEMO"}
	if got, want := p.Compose("U"), "ID\n\nMEMO\n\nU"; got != want {
		t.Fatalf("拼接顺序不符：got=%q want=%q", got, want)
	}
	if (RunPrefix{}).Compose("U") != "U" {
		t.Fatal("空前缀必须原样返回")
	}
	if !(RunPrefix{}).Empty() || (RunPrefix{Identity: "x"}).Empty() {
		t.Fatal("Empty 判据不符")
	}
}
