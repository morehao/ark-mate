package acl

import (
	"context"
	"errors"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/morehao/ark-forge/pkg/object/objusage"
)

// collect 收集 normalizeUpdate 输出的事件（按 Kind+关键文本）。
func collect(n acpsdk.SessionNotification) []Event {
	var out []Event
	for _, ev := range normalizeUpdate(n) {
		out = append(out, ev)
	}
	return out
}

// TestNormalizeUpdateMessageKinds 文本类 chunk → message/thought/user_message 事件。
func TestNormalizeUpdateMessageKinds(t *testing.T) {
	sid := acpsdk.SessionId("s1")

	t.Run("agent_message", func(t *testing.T) {
		evs := collect(acpsdk.SessionNotification{SessionId: sid, Update: acpsdk.UpdateAgentMessageText("hello")})
		if len(evs) != 1 || evs[0].Kind != KindMessage || evs[0].Payload.(MessageDelta).Text != "hello" {
			t.Fatalf("agent message 归一不符: %+v", evs)
		}
	})
	t.Run("agent_thought", func(t *testing.T) {
		evs := collect(acpsdk.SessionNotification{SessionId: sid, Update: acpsdk.UpdateAgentThoughtText("reasoning")})
		if len(evs) != 1 || evs[0].Kind != KindThought || evs[0].Payload.(ThoughtDelta).Text != "reasoning" {
			t.Fatalf("thought 归一不符: %+v", evs)
		}
	})
	t.Run("user_message", func(t *testing.T) {
		evs := collect(acpsdk.SessionNotification{SessionId: sid, Update: acpsdk.UpdateUserMessageText("echo")})
		if len(evs) != 1 || evs[0].Kind != KindUserMessage {
			t.Fatalf("user message 归一不符: %+v", evs)
		}
	})
	t.Run("empty_text_skipped", func(t *testing.T) {
		// 非 text content block（M0 忽略）→ 0 事件。
		evs := collect(acpsdk.SessionNotification{SessionId: sid, Update: acpsdk.UpdateAgentMessage(acpsdk.AudioBlock("x", "audio/mp3"))})
		if len(evs) != 0 {
			t.Fatalf("非文本块应被忽略: %+v", evs)
		}
	})
	t.Run("session_id_carried", func(t *testing.T) {
		evs := collect(acpsdk.SessionNotification{SessionId: "sid-abc", Update: acpsdk.UpdateAgentMessageText("x")})
		if evs[0].SessionID != "sid-abc" {
			t.Fatalf("SessionID 未透传: %+v", evs[0])
		}
	})
	t.Run("message_id_passthrough", func(t *testing.T) {
		// ACP unstable messageId（决策 v5①）：同 MessageID 的 chunk 归同一条消息。
		mid := "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d"
		upd := acpsdk.SessionUpdate{AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
			Content: acpsdk.TextBlock("seg"), MessageId: &mid,
		}}
		evs := collect(acpsdk.SessionNotification{SessionId: sid, Update: upd})
		if len(evs) != 1 || evs[0].Kind != KindMessage {
			t.Fatalf("message 归一不符: %+v", evs)
		}
		if d := evs[0].Payload.(MessageDelta); d.Text != "seg" || d.MessageID != mid {
			t.Fatalf("message_id 未透传: %+v", d)
		}
	})
	t.Run("message_id_absent", func(t *testing.T) {
		// agent 未实现 messageId 时为可选空串（向后兼容，前端回退 tail 拼接）。
		evs := collect(acpsdk.SessionNotification{SessionId: sid, Update: acpsdk.UpdateAgentMessageText("no-id")})
		if d := evs[0].Payload.(MessageDelta); d.MessageID != "" {
			t.Fatalf("空 messageId 应为空串: %+v", d)
		}
	})
}

// TestNormalizeUpdateToolKinds 工具调用发起/更新/用量事件。
func TestNormalizeUpdateToolKinds(t *testing.T) {
	sid := acpsdk.SessionId("s1")

	t.Run("tool_call", func(t *testing.T) {
		n := acpsdk.SessionNotification{SessionId: sid, Update: acpsdk.StartToolCall(
			acpsdk.ToolCallId("call_1"),
			"Modifying config",
			acpsdk.WithStartKind(acpsdk.ToolKindEdit),
			acpsdk.WithStartStatus(acpsdk.ToolCallStatusPending),
			acpsdk.WithStartRawInput(map[string]any{"path": "/x"}),
		)}
		evs := collect(n)
		if len(evs) != 1 || evs[0].Kind != KindToolCall {
			t.Fatalf("tool_call 归一不符: %+v", evs)
		}
		tc := evs[0].Payload.(ToolCallInfo)
		if tc.ID != "call_1" || tc.Title != "Modifying config" || tc.Status != string(acpsdk.ToolCallStatusPending) {
			t.Fatalf("tool call 载荷不符: %+v", tc)
		}
	})

	t.Run("tool_call_update", func(t *testing.T) {
		status := acpsdk.ToolCallStatusCompleted
		title := "New title"
		n := acpsdk.SessionNotification{SessionId: sid, Update: acpsdk.SessionUpdate{
			ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
				ToolCallId: acpsdk.ToolCallId("call_2"),
				Title:      &title,
				Status:     &status,
			},
		}}
		evs := collect(n)
		if len(evs) != 1 || evs[0].Kind != KindToolUpdate {
			t.Fatalf("tool_update 归一不符: %+v", evs)
		}
		tc := evs[0].Payload.(ToolCallInfo)
		if tc.ID != "call_2" || tc.Title != "New title" || tc.Status != "completed" {
			t.Fatalf("tool update 载荷不符: %+v", tc)
		}
	})

	t.Run("usage", func(t *testing.T) {
		n := acpsdk.SessionNotification{SessionId: sid, Update: acpsdk.SessionUpdate{
			UsageUpdate: &acpsdk.SessionUsageUpdate{Used: 1, Size: 100},
		}}
		evs := collect(n)
		if len(evs) != 1 || evs[0].Kind != KindUsage {
			t.Fatalf("usage 归一不符: %+v", evs)
		}
		// M3：用量明细透传（used/size 必填；cost 可选）。
		u, ok := evs[0].Payload.(objusage.UsageInfo)
		if !ok || u.Used != 1 || u.Size != 100 || u.Reported() != true {
			t.Fatalf("usage 载荷不符: %+v ok=%v", evs[0].Payload, ok)
		}
	})

	t.Run("usage_with_cost", func(t *testing.T) {
		n := acpsdk.SessionNotification{SessionId: sid, Update: acpsdk.SessionUpdate{
			UsageUpdate: &acpsdk.SessionUsageUpdate{Used: 10, Size: 200,
				Cost: &acpsdk.Cost{Amount: 0.42, Currency: "USD"}},
		}}
		evs := collect(n)
		u, ok := evs[0].Payload.(objusage.UsageInfo)
		if !ok || u.CostAmount != 0.42 || u.CostCurrency != "USD" {
			t.Fatalf("cost 未透传: %+v", evs[0].Payload)
		}
	})

	t.Run("unmodeled_plan_ignored", func(t *testing.T) {
		n := acpsdk.SessionNotification{SessionId: sid, Update: acpsdk.UpdatePlan(acpsdk.PlanEntry{})}
		if evs := collect(n); len(evs) != 0 {
			t.Fatalf("plan 类更新 M0 应忽略: %+v", evs)
		}
	})
}

// TestHostClientPermission 权限决策：允许→选中首选项；拒绝→cancelled；
// 无选项允许也回落 cancelled；事件始终上抛（留痕）。
func TestHostClientPermission(t *testing.T) {
	req := func(options int) acpsdk.RequestPermissionRequest {
		var opts []acpsdk.PermissionOption
		for i := 0; i < options; i++ {
			opts = append(opts, acpsdk.PermissionOption{
				OptionId: acpsdk.PermissionOptionId(string(rune('a' + i))),
				Name:     "opt",
			})
		}
		title := "run tool"
		return acpsdk.RequestPermissionRequest{
			SessionId: acpsdk.SessionId("s1"),
			ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: acpsdk.ToolCallId("c1"), Title: &title},
			Options:   opts,
		}
	}

	t.Run("allow_picks_first", func(t *testing.T) {
		var got []Event
		h := &hostClient{opts: ClientOptions{OnEvent: func(e Event) { got = append(got, e) },
			AllowPermission: func(ctx context.Context, sid, title string, options int) bool { return true }}}
		resp, err := h.RequestPermission(context.Background(), req(2))
		if err != nil {
			t.Fatalf("RequestPermission: %v", err)
		}
		if resp.Outcome.Selected == nil || string(resp.Outcome.Selected.OptionId) != "a" {
			t.Fatalf("应选中首选项 a: %+v", resp.Outcome)
		}
		if len(got) != 1 || got[0].Kind != KindPermission {
			t.Fatalf("应上抛 permission 事件: %+v", got)
		}
	})

	t.Run("deny_cancels", func(t *testing.T) {
		h := &hostClient{opts: ClientOptions{AllowPermission: func(ctx context.Context, sid, title string, options int) bool { return false }}}
		resp, err := h.RequestPermission(context.Background(), req(2))
		if err != nil {
			t.Fatalf("RequestPermission: %v", err)
		}
		if resp.Outcome.Cancelled == nil {
			t.Fatalf("拒绝应回落 cancelled: %+v", resp.Outcome)
		}
	})

	t.Run("allow_no_options_cancels", func(t *testing.T) {
		h := &hostClient{opts: ClientOptions{AllowPermission: func(ctx context.Context, sid, title string, options int) bool { return true }}}
		resp, _ := h.RequestPermission(context.Background(), req(0))
		if resp.Outcome.Cancelled == nil {
			t.Fatalf("无选项可授应 cancelled: %+v", resp.Outcome)
		}
	})

	t.Run("nil_policy_denies", func(t *testing.T) {
		h := &hostClient{} // AllowPermission nil → 默认拒绝
		resp, err := h.RequestPermission(context.Background(), req(2))
		if err != nil {
			t.Fatalf("RequestPermission: %v", err)
		}
		if resp.Outcome.Cancelled == nil {
			t.Fatalf("默认策略应拒绝: %+v", resp.Outcome)
		}
	})
}

// TestHostClientUnopenedMethods M0 未开放的 fs/terminal 能力返回错误。
func TestHostClientUnopenedMethods(t *testing.T) {
	h := &hostClient{}
	if _, err := h.ReadTextFile(context.Background(), acpsdk.ReadTextFileRequest{}); err == nil {
		t.Fatal("ReadTextFile 应报错")
	}
	if _, err := h.CreateTerminal(context.Background(), acpsdk.CreateTerminalRequest{}); err == nil {
		t.Fatal("CreateTerminal 应报错")
	}
	if _, err := h.WaitForTerminalExit(context.Background(), acpsdk.WaitForTerminalExitRequest{}); err == nil {
		t.Fatal("WaitForTerminalExit 应报错")
	}
}

// TestPermissionSessionUpdateNilOnEvent nil 回调不 panic。
func TestPermissionSessionUpdateNilOnEvent(t *testing.T) {
	h := &hostClient{}
	if err := h.SessionUpdate(context.Background(), acpsdk.SessionNotification{SessionId: "s1", Update: acpsdk.UpdateAgentMessageText("x")}); err != nil {
		t.Fatalf("nil OnEvent 应静默成功: %v", err)
	}
}

// TestHelpers 内部文本工具。
func TestHelpers(t *testing.T) {
	if firstNonEmpty("", "b") != "b" || firstNonEmpty("a", "b") != "a" {
		t.Fatal("firstNonEmpty 不符")
	}
	s := "x"
	if firstNonEmptyPtr(&s) != "x" || firstNonEmptyPtr(nil) != "" {
		t.Fatal("firstNonEmptyPtr 不符")
	}
	if !errors.Is(NewError(ClassConnection, "e", "d", context.Canceled), context.Canceled) {
		t.Fatal("NewError cause 应可解包")
	}
}
