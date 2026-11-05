package sessionruntime

import (
	"context"
	"errors"
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

// TestBuildSelection 白名单映射：nil 子项 = 该维度全量（空指针）；非 nil 复制。
func TestBuildSelection(t *testing.T) {
	sel := buildSelection([]string{"s1", "s2"}, nil)
	if sel.Skills == nil || len(*sel.Skills) != 2 || (*sel.Skills)[0] != "s1" {
		t.Fatalf("Skills 未正确设置: %+v", sel.Skills)
	}
	if sel.Mcps != nil {
		t.Fatalf("mcps=nil 应保持 nil（全量），得到 %+v", sel.Mcps)
	}
	// 只有 mcps
	sel2 := buildSelection(nil, []string{"m1"})
	if sel2.Skills != nil || sel2.Mcps == nil || (*sel2.Mcps)[0] != "m1" {
		t.Fatalf("仅 mcps 场景不符: %+v", sel2)
	}
}

// TestTokensOfEvent tokensOfEvent 解析载荷（多形状容错）。
func TestTokensOfEvent(t *testing.T) {
	if got := tokensOfEvent(map[string]any{"total_tokens": float64(42)}); got != 42 {
		t.Fatalf("tokensOfEvent 不符: %d", got)
	}
	if got := tokensOfEvent(map[string]any{"used": int64(7)}); got != 7 {
		t.Fatalf("tokensOfEvent(used) 不符: %d", got)
	}
	if got := tokensOfEvent("not-a-map"); got != 0 {
		t.Fatalf("非 map 应返回 0: %d", got)
	}
}

// TestStatefulUnboundedRejected PurposeAgentTurn 且无步数/超时 → 默认拒绝。
func TestStatefulUnboundedRejected(t *testing.T) {
	e := NewStatefulExecutor(&Orchestrator{}, "", "")
	_, err := e.Call(context.Background(), corellm.Request{
		Purpose:  corellm.PurposeAgentTurn,
		Messages: []corellm.Message{{Role: corellm.RoleUser, Text: "x"}},
	})
	if !errors.Is(err, corellm.ErrUnboundedAgentTurn) {
		t.Fatalf("应拒绝无护栏 agent 轮次，得到 %v", err)
	}
}

// TestGuardrailErrors 错误类型 Error() 可用且可判别。
func TestGuardrailErrors(t *testing.T) {
	var e error = &corellm.StepExceededError{Limit: 3, Actual: 5}
	if !strings.Contains(e.Error(), "MaxSteps=3") {
		t.Fatalf("StepExceededError 文案不符: %s", e.Error())
	}
	e2 := &corellm.BudgetExceededError{Kind: "token", Limit: 100, Actual: 200}
	if !strings.Contains(e2.Error(), "token") {
		t.Fatalf("BudgetExceededError 文案不符: %s", e2.Error())
	}
}
