package acp

import (
	"context"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/morehao/ark-mate/internal/core/object/objusage"
	"github.com/morehao/ark-mate/internal/core/protocol"
)

// TestTokenUsageOf 钉住 ACP PromptResponse.Usage → objusage.TokenUsage 的映射：
//   - nil → 零值（底座未上报，不臆造）；
//   - 指针字段（cached/thought）正确解引用；
//   - 全零 → Reported()==false（视为未上报）。
func TestTokenUsageOf(t *testing.T) {
	i := func(v int) *int { return &v }

	t.Run("nil", func(t *testing.T) {
		if u := tokenUsageOf(nil); u.Reported() {
			t.Fatalf("nil usage 应映射为零值且 Reported=false，得到 %+v", u)
		}
	})

	t.Run("full", func(t *testing.T) {
		got := tokenUsageOf(&acpsdk.Usage{
			InputTokens: 100, OutputTokens: 50, TotalTokens: 180,
			CachedReadTokens: i(20), CachedWriteTokens: i(5), ThoughtTokens: i(5),
		})
		want := objusage.TokenUsage{
			InputTokens: 100, OutputTokens: 50, TotalTokens: 180,
			CachedReadTokens: 20, CachedWriteTokens: 5, ThoughtTokens: 5,
		}
		if got != want {
			t.Fatalf("映射不符：got %+v want %+v", got, want)
		}
		if !got.Reported() {
			t.Fatal("有数据时应 Reported=true")
		}
	})

	t.Run("nil_optionals", func(t *testing.T) {
		got := tokenUsageOf(&acpsdk.Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7})
		if got.CachedReadTokens != 0 || got.CachedWriteTokens != 0 || got.ThoughtTokens != 0 {
			t.Fatalf("可选字段为 nil 时应为 0，得到 %+v", got)
		}
	})
}

// TestSessionPromptReturnsUsage 端到端断言：此前被丢弃的 PromptResponse.Usage，现在经
// Session.Prompt 的第三个返回值接住（走真实 Dial + fakeAgent 路径）。
func TestSessionPromptReturnsUsage(t *testing.T) {
	i := func(v int) *int { return &v }
	agent := &fakeAgent{
		init:  fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, false), nil),
		usage: &acpsdk.Usage{InputTokens: 11, OutputTokens: 22, TotalTokens: 33, ThoughtTokens: i(4)},
	}
	cl := dialFake(t, agent, ClientOptions{})
	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	stop, usage, err := sess.Prompt(context.Background(), protocol.TextParts("hi"))
	if err != nil {
		t.Fatalf("Prompt err: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop=%q", stop)
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 22 || usage.TotalTokens != 33 || usage.ThoughtTokens != 4 {
		t.Fatalf("usage 未接住: %+v", usage)
	}
}
