package acp

import (
	"github.com/morehao/ark-mate/internal/core/protocol"
	"context"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

// TestRequestPermissionRoundTrip 端到端验证 ACP 权限确认通道：
// agent 在 Prompt 中途发起 session/request_permission（选项 allow_once/allow_always）→
// hostClient.RequestPermission 收到完整请求并阻塞等待宿主决策 → 决策（允许，选 option_id）
// → agent 收到 Selected；若拒绝则收到 Cancelled。同时验证不会卡死读循环（Prompt 仍能返回）。
func TestRequestPermissionRoundTrip(t *testing.T) {
	type result struct {
		optionID string
		outcome  string
	}
	agentRes := make(chan result, 1)
	captured := make(chan PermissionRequest, 1)

	agent := &fakeAgent{
		init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, false), nil),
	}
	agent.promptHook = func(ctx context.Context, req acpsdk.PromptRequest) {
		resp, err := agent.conn.RequestPermission(ctx, acpsdk.RequestPermissionRequest{
			SessionId: req.SessionId,
			ToolCall: acpsdk.ToolCallUpdate{
				ToolCallId: "tc-perm",
				Title:      ptrString("运行单元测试"),
				Kind:       toolKindPtr(acpsdk.ToolKindExecute),
			},
			Options: []acpsdk.PermissionOption{
				{OptionId: "allow-once", Name: "允许一次", Kind: acpsdk.PermissionOptionKindAllowOnce},
				{OptionId: "allow-all", Name: "始终允许", Kind: acpsdk.PermissionOptionKindAllowAlways},
			},
		})
		if err != nil {
			t.Errorf("agent RequestPermission: %v", err)
			return
		}
		if resp.Outcome.Selected != nil {
			agentRes <- result{optionID: string(resp.Outcome.Selected.OptionId), outcome: "selected"}
		} else if resp.Outcome.Cancelled != nil {
			agentRes <- result{outcome: "cancelled"}
		} else {
			agentRes <- result{outcome: "unknown"}
		}
	}

	cl := dialFake(t, agent, ClientOptions{
		RequestPermission: func(_ context.Context, req PermissionRequest) PermissionDecision {
			captured <- req
			// 宿主决策：允许并选中「允许一次」。
			return PermissionDecision{Allowed: true, OptionID: "allow-once"}
		},
	})

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.Prompt(context.Background(), protocol.TextParts("hi")); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// hostClient 收到的完整请求（含 options 折叠）。
	var req PermissionRequest
	select {
	case req = <-captured:
	default:
		t.Fatal("RequestPermission 未被调用")
	}
	if req.SessionID != "sess-fake-1" {
		t.Fatalf("session 不符: %q", req.SessionID)
	}
	if req.ToolCall.Title != "运行单元测试" || req.ToolCall.Kind != "execute" {
		t.Fatalf("tool 信息不符: %+v", req.ToolCall)
	}
	if len(req.Options) != 2 || req.Options[0].OptionID != "allow-once" || req.Options[0].Kind != "allow_once" {
		t.Fatalf("options 折叠不符: %+v", req.Options)
	}

	select {
	case r := <-agentRes:
		if r.outcome != "selected" || r.optionID != "allow-once" {
			t.Fatalf("agent 收到的决策不符: %+v", r)
		}
	default:
		t.Fatal("agent 未收到决策")
	}
}

// TestRequestPermissionReject 决策为拒绝时 agent 收到 Cancelled。
func TestRequestPermissionReject(t *testing.T) {
	agentRes := make(chan string, 1)
	agent := &fakeAgent{
		init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, false), nil),
	}
	agent.promptHook = func(ctx context.Context, req acpsdk.PromptRequest) {
		resp, err := agent.conn.RequestPermission(ctx, acpsdk.RequestPermissionRequest{
			SessionId: req.SessionId,
			ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: "tc2", Title: ptrString("删除文件")},
			Options: []acpsdk.PermissionOption{
				{OptionId: "allow-once", Kind: acpsdk.PermissionOptionKindAllowOnce},
			},
		})
		if err != nil {
			t.Errorf("agent RequestPermission: %v", err)
			return
		}
		if resp.Outcome.Cancelled != nil {
			agentRes <- "cancelled"
		} else {
			agentRes <- "not-cancelled"
		}
	}
	cl := dialFake(t, agent, ClientOptions{
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{} // 拒绝（零值）
		},
	})
	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.Prompt(context.Background(), protocol.TextParts("hi")); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := <-agentRes; got != "cancelled" {
		t.Fatalf("agent 决策不是 Cancelled: %q", got)
	}
}

// TestRequestPermissionRejectSelected 显式拒绝且选了 deny 选项 → agent 收到 Selected{reject 选项}
// （与取消/超时的 Cancelled 可区分，决策 v2 语义）。
func TestRequestPermissionRejectSelected(t *testing.T) {
	agentRes := make(chan string, 1)
	agent := &fakeAgent{
		init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, false), nil),
	}
	agent.promptHook = func(ctx context.Context, req acpsdk.PromptRequest) {
		resp, err := agent.conn.RequestPermission(ctx, acpsdk.RequestPermissionRequest{
			SessionId: req.SessionId,
			ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: "tc3", Title: ptrString("删除文件")},
			Options: []acpsdk.PermissionOption{
				{OptionId: "allow-once", Kind: acpsdk.PermissionOptionKindAllowOnce},
				{OptionId: "reject-once", Name: "拒绝一次", Kind: acpsdk.PermissionOptionKindRejectOnce},
			},
		})
		if err != nil {
			t.Errorf("agent RequestPermission: %v", err)
			return
		}
		if resp.Outcome.Selected != nil {
			agentRes <- "selected:" + string(resp.Outcome.Selected.OptionId)
		} else if resp.Outcome.Cancelled != nil {
			agentRes <- "cancelled"
		} else {
			agentRes <- "unknown"
		}
	}
	cl := dialFake(t, agent, ClientOptions{
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			// 宿主决策：显式拒绝并选中「拒绝一次」（reject-once）。
			return PermissionDecision{Rejected: true, OptionID: "reject-once"}
		},
	})
	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.Prompt(context.Background(), protocol.TextParts("hi")); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := <-agentRes; got != "selected:reject-once" {
		t.Fatalf("agent 收到的拒绝决策不符: %q（期望 Selected{reject-once}）", got)
	}
}

// TestRequestPermissionRejectWithoutOption 显式拒绝但所选 deny 选项不在 agent 提供的选项内
// → 兜底 Cancelled（无法 Selected 时不得伪造选项）。
func TestRequestPermissionRejectWithoutOption(t *testing.T) {
	agentRes := make(chan string, 1)
	agent := &fakeAgent{
		init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, false), nil),
	}
	agent.promptHook = func(ctx context.Context, req acpsdk.PromptRequest) {
		resp, err := agent.conn.RequestPermission(ctx, acpsdk.RequestPermissionRequest{
			SessionId: req.SessionId,
			ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: "tc4", Title: ptrString("写文件")},
			Options: []acpsdk.PermissionOption{
				{OptionId: "allow-once", Kind: acpsdk.PermissionOptionKindAllowOnce},
			},
		})
		if err != nil {
			t.Errorf("agent RequestPermission: %v", err)
			return
		}
		if resp.Outcome.Cancelled != nil {
			agentRes <- "cancelled"
		} else {
			agentRes <- "not-cancelled"
		}
	}
	cl := dialFake(t, agent, ClientOptions{
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			// 宿主想拒绝并选了 deny 选项，但 agent 没提供该选项 → 兜底 Cancelled。
			return PermissionDecision{Rejected: true, OptionID: "reject-once"}
		},
	})
	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.Prompt(context.Background(), protocol.TextParts("hi")); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := <-agentRes; got != "cancelled" {
		t.Fatalf("无 deny 选项时拒绝应兜底 Cancelled: %q", got)
	}
}

func ptrString(s string) *string { return &s }

func toolKindPtr(k acpsdk.ToolKind) *acpsdk.ToolKind { return &k }

// TestEffectivePermissionOutcome 决策 → ACP 回执形态的唯一事实源（permissionResponseFromDecision
// 与 REST 回执共用）：覆盖放行兜底、无选项、显式拒绝回执、拒绝无 deny 选项降级。
func TestEffectivePermissionOutcome(t *testing.T) {
	opts := []PermissionOption{
		{OptionID: "allow_once", Name: "允许一次", Kind: "allow_once"},
		{OptionID: "allow_always", Name: "始终允许", Kind: "allow_always"},
		{OptionID: "reject_once", Name: "拒绝一次", Kind: "reject_once"},
	}
	cases := []struct {
		name   string
		d      PermissionDecision
		opts   []PermissionOption
		kind   string
		option string
	}{
		{"放行+指定有效选项", PermissionDecision{Allowed: true, OptionID: "allow_always"}, opts, OutcomeSelected, "allow_always"},
		{"放行+未指定→第一个选项", PermissionDecision{Allowed: true}, opts, OutcomeSelected, "allow_once"},
		{"放行+无效选项→第一个选项", PermissionDecision{Allowed: true, OptionID: "nope"}, opts, OutcomeSelected, "allow_once"},
		{"放行+无选项→取消", PermissionDecision{Allowed: true}, nil, OutcomeCancelled, ""},
		{"拒绝+deny 选项→Selected（agent 可区分）", PermissionDecision{Rejected: true, OptionID: "reject_once"}, opts, OutcomeSelected, "reject_once"},
		{"拒绝+无 deny 选项→Cancelled", PermissionDecision{Rejected: true}, opts, OutcomeCancelled, ""},
		{"拒绝+无效选项→Cancelled", PermissionDecision{Rejected: true, OptionID: "nope"}, opts, OutcomeCancelled, ""},
		{"取消/超时→Cancelled", PermissionDecision{}, opts, OutcomeCancelled, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, oid := EffectivePermissionOutcome(tc.d, tc.opts)
			if kind != tc.kind || oid != tc.option {
				t.Fatalf("期望 (%s,%q)，得到 (%s,%q)", tc.kind, tc.option, kind, oid)
			}
		})
	}
}
