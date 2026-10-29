package session

import (
	"context"
	"strings"
	"testing"
)

// ——— P1 专家角色注入（A13–A15 的会话域判据）———
//
// 复用 identity_injection_test.go 的 captureACP / newIdentityRunner（同一包）。

func TestExpertRoleInjectedFirstTurnOnly(t *testing.T) {
	block := BuildExpertRoleBlock("data-analyst", "数据分析师", "先确认口径再给结论")
	if block == "" {
		t.Fatal("非空 instructions 应构造出角色块")
	}
	agent := newCaptureACP()
	as := NewActiveSession(HandleParams{
		ID: "s1", Runtime: "r", ExpertRole: block, Session: agent,
	})
	r, _ := newIdentityRunner(t, as)

	if _, err := r.Prompt(context.Background(), "s1", "第一问"); err != nil {
		t.Fatalf("首轮: %v", err)
	}
	waitPrompt(t, agent, 1)
	waitIdle(t, as)
	first := agent.PromptAt(0)
	if !strings.Contains(first, `data-role="`+ExpertRoleAttr+`"`) {
		t.Fatalf("首轮应含专家角色标记，got %q", first)
	}
	if !strings.Contains(first, "先确认口径再给结论") || !strings.HasSuffix(first, "第一问") {
		t.Fatalf("首轮应以用户原文结尾且含 instructions，got %q", first)
	}

	if _, err := r.Prompt(context.Background(), "s1", "第二问"); err != nil {
		t.Fatalf("次轮: %v", err)
	}
	waitPrompt(t, agent, 2)
	if got := agent.PromptAt(1); got != "第二问" {
		t.Fatalf("次轮应逐字等于用户原文（不重复注入），got %q", got)
	}
}

func TestExpertRoleNotInUserMessageEvent(t *testing.T) {
	block := BuildExpertRoleBlock("e", "专家", "秘密角色设定")
	agent := newCaptureACP()
	as := NewActiveSession(HandleParams{ID: "s1", Runtime: "r", ExpertRole: block, Session: agent})
	r, bus := newIdentityRunner(t, as)

	if _, err := r.Prompt(context.Background(), "s1", "用户原文"); err != nil {
		t.Fatalf("首轮: %v", err)
	}
	waitPrompt(t, agent, 1)
	var found bool
	for _, e := range bus.EventsAll() {
		if e.Type != BusUserMessage {
			continue
		}
		found = true
		p, _ := e.Payload.(map[string]any)
		if text, _ := p["text"].(string); text != "用户原文" {
			t.Fatalf("session.user_message 必须是用户原文（角色块不落事件），got %q", text)
		}
	}
	if !found {
		t.Fatal("没有发布 session.user_message 事件")
	}
}

func TestNoExpertRoleChangesNothing(t *testing.T) {
	agent := newCaptureACP()
	as := NewActiveSession(HandleParams{ID: "s1", Runtime: "r", Session: agent})
	r, _ := newIdentityRunner(t, as)
	if _, err := r.Prompt(context.Background(), "s1", "你好"); err != nil {
		t.Fatalf("首轮: %v", err)
	}
	waitPrompt(t, agent, 1)
	if got := agent.PromptAt(0); got != "你好" {
		t.Fatalf("无专家人格时不得改变 prompt 字节，got %q", got)
	}
}

func TestExpertRoleEmptyInstructions(t *testing.T) {
	if got := BuildExpertRoleBlock("e", "专家", "   "); got != "" {
		t.Fatalf("空 instructions 不应构造角色块，got %q", got)
	}
}

func TestRunPrefixComposeOrderWithExpertRole(t *testing.T) {
	p := RunPrefix{Identity: "ID", ExpertRole: "ROLE", Memo: "MEMO"}
	if got, want := p.Compose("U"), "ID\n\nROLE\n\nMEMO\n\nU"; got != want {
		t.Fatalf("顺序应为 identity → expertRole → memo → 原文：got=%q want=%q", got, want)
	}
	if !(RunPrefix{}).Empty() || (RunPrefix{ExpertRole: "x"}).Empty() {
		t.Fatal("Empty 判据应包含 ExpertRole")
	}
}

func TestStripExpertRolePrefix(t *testing.T) {
	block := BuildExpertRoleBlock("e", "专家", "设定")
	text := block + "\n\n用户原话"
	if got, ok := StripExpertRolePrefix(text, block); !ok || got != "用户原话" {
		t.Fatalf("应剥离精确前缀，got=%q ok=%v", got, ok)
	}
	// 不匹配时原样返回（不静默吞掉用户内容）。
	if got, ok := StripExpertRolePrefix("别的文本", block); ok || got != "别的文本" {
		t.Fatalf("不匹配时应原样返回，got=%q ok=%v", got, ok)
	}
}

// TestExpertRoleProviderEvaluatedAtFirstRun 惰性 provider 在首轮才求值且只取一次：
// 使「创建后、首轮前改专家定义」能按发送时最新 instructions 注入（设计沉默清单 #6）。
func TestExpertRoleProviderEvaluatedAtFirstRun(t *testing.T) {
	agent := newCaptureACP()
	calls := 0
	as := NewActiveSession(HandleParams{
		ID: "s1", Runtime: "r", Session: agent,
		ExpertPersona: func() string {
			calls++
			return BuildExpertRoleBlock("e", "专家", "发送时最新设定")
		},
	})
	r, _ := newIdentityRunner(t, as)

	if _, err := r.Prompt(context.Background(), "s1", "第一问"); err != nil {
		t.Fatalf("首轮: %v", err)
	}
	waitPrompt(t, agent, 1)
	waitIdle(t, as)
	if got := agent.PromptAt(0); !strings.Contains(got, "发送时最新设定") {
		t.Fatalf("首轮应含 provider 给出的角色块，got %q", got)
	}
	if calls != 1 {
		t.Fatalf("provider 应只在首轮求值一次，got %d", calls)
	}

	if _, err := r.Prompt(context.Background(), "s1", "第二问"); err != nil {
		t.Fatalf("次轮: %v", err)
	}
	waitPrompt(t, agent, 2)
	if calls != 1 {
		t.Fatalf("次轮不应再调用 provider，got %d", calls)
	}
	if got := agent.PromptAt(1); got != "第二问" {
		t.Fatalf("次轮应逐字等于原文，got %q", got)
	}
}
