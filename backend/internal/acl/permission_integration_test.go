package acl

import (
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
	if _, err := sess.Prompt(context.Background(), "hi"); err != nil {
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
	if _, err := sess.Prompt(context.Background(), "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := <-agentRes; got != "cancelled" {
		t.Fatalf("agent 决策不是 Cancelled: %q", got)
	}
}

func ptrString(s string) *string { return &s }

func toolKindPtr(k acpsdk.ToolKind) *acpsdk.ToolKind { return &k }
