package sessionruntime

import (
	"strings"
	"testing"

	corellm "github.com/morehao/ark-mate/internal/core/llm"
)

// TestComposeInternalPrompt system 前置 + messages 逐条 `角色: 内容`。
func TestComposeInternalPrompt(t *testing.T) {
	got := composeInternalPrompt(corellm.Request{
		System: "你是助手",
		Messages: []corellm.Message{
			{Role: corellm.RoleUser, Text: "你好"},
			{Role: corellm.RoleAssistant, Text: "在"},
		},
	})
	want := "你是助手\n\nuser: 你好\nassistant: 在"
	if got != want {
		t.Fatalf("composeInternalPrompt 不符:\n got %q\n want %q", got, want)
	}
	// 空 role 按 user 处理。
	if got := composeInternalPrompt(corellm.Request{Messages: []corellm.Message{{Text: "hi"}}}); got != "user: hi" {
		t.Fatalf("空 role 应按 user，得到 %q", got)
	}
	// 空请求 → 空串（Call 会据空串报错，不静默发空 prompt）。
	if got := composeInternalPrompt(corellm.Request{}); strings.TrimSpace(got) != "" {
		t.Fatalf("空请求应得到空串，得到 %q", got)
	}
}

// TestStatefulExecutorImplementsExecutor 编译期/运行期确认实现 corellm.Executor。
func TestStatefulExecutorImplementsExecutor(t *testing.T) {
	var e corellm.Executor = NewStatefulExecutor(nil, "", "")
	if e == nil {
		t.Fatal("NewStatefulExecutor 返回 nil")
	}
	// 未接 Orchestrator 时 Call 应 fail-closed（不 panic）。
	if _, err := e.Call(nil, corellm.Request{Messages: []corellm.Message{{Text: "x"}}}); err == nil {
		t.Fatal("未接 Orchestrator 应报错")
	}
}
