package acl

import (
	"context"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// 本文件是 M0 删除 echo 底座后**重建**的权限 reject / 失败轮端到端覆盖
// （见 .dsh/docs/notes/2026-09-22-echo-removal-coverage-ledger.md 的 G3/G4）。
//
// 原先这两条靠 `echo.Serve` 当真实对端；这里改用 `fakeAgent` + `wireFake`（真 SDK 连接，
// 内存管道）重建——被测的是「宿主决策 → ACP 回执形态」与「底座失败/拒绝 → 客户端如实透传」。

// permissionOptions 与 agent 侧提供的选项一致（allow-once / allow-all / reject-once）。
func permissionOptions() []acpsdk.PermissionOption {
	return []acpsdk.PermissionOption{
		{OptionId: "allow-once", Name: "允许一次", Kind: acpsdk.PermissionOptionKindAllowOnce},
		{OptionId: "allow-all", Name: "始终允许", Kind: acpsdk.PermissionOptionKindAllowAlways},
		{OptionId: "reject-once", Name: "拒绝一次", Kind: acpsdk.PermissionOptionKindRejectOnce},
	}
}

// permissionAgent 造一个在 Prompt 中途发起权限请求的 fakeAgent，回执形态写进 out。
func permissionAgent(t *testing.T) (*fakeAgent, *pipeTransport, chan string) {
	t.Helper()
	out := make(chan string, 1)
	agent := newE2EAgent()
	agent.promptHook = func(ctx context.Context, req acpsdk.PromptRequest) {
		resp, err := agent.conn.RequestPermission(ctx, acpsdk.RequestPermissionRequest{
			SessionId: req.SessionId,
			ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: "tc-e2e", Title: ptrString("跑个命令")},
			Options:   permissionOptions(),
		})
		if err != nil {
			t.Errorf("agent 发起权限请求失败: %v", err)
			return
		}
		switch {
		case resp.Outcome.Selected != nil:
			out <- "selected:" + string(resp.Outcome.Selected.OptionId)
		case resp.Outcome.Cancelled != nil:
			out <- "cancelled"
		default:
			out <- "unknown"
		}
	}
	tp := wireFake(agent)
	return agent, tp, out
}

// TestPermissionRejectEndToEnd 显式拒绝：宿主选 agent 提供的 deny 选项 → agent 收到
// `selected:reject-once`（**可与取消区分**），整轮仍正常收尾。
//
// 这是 echo 删除后补回的关键覆盖：reject 不等于 cancel，agent 需要能分辨。
func TestPermissionRejectEndToEnd(t *testing.T) {
	_, tp, out := permissionAgent(t)
	defer tp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Rejected: true, OptionID: "reject-once"}
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cl.Close()

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if stop, err := sess.Prompt(context.Background(), "帮我跑个命令"); err != nil || stop != "end_turn" {
		t.Fatalf("Prompt: stop=%q err=%v", stop, err)
	}
	select {
	case got := <-out:
		if got != "selected:reject-once" {
			t.Fatalf("拒绝经 deny 选项投递时应为 selected:reject-once，得到 %q", got)
		}
	default:
		t.Fatal("agent 未收到权限回执")
	}
}

// TestPermissionCancelledEndToEnd 宿主取消（零值决策）：agent 收到 cancelled。
func TestPermissionCancelledEndToEnd(t *testing.T) {
	_, tp, out := permissionAgent(t)
	defer tp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{} // 取消/超时/默认拒绝
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cl.Close()

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.Prompt(context.Background(), "帮我跑个命令"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	select {
	case got := <-out:
		if got != "cancelled" {
			t.Fatalf("零值决策应投递 cancelled，得到 %q", got)
		}
	default:
		t.Fatal("agent 未收到权限回执")
	}
}

// TestPromptRefusalEndToEnd 底座以非 end_turn 的 stop reason 收尾：客户端**如实透传**，
// 不臆造成成功（对应被删的 `TestEchoAgentSimulatedFailure` 的失败轮语义）。
func TestPromptRefusalEndToEnd(t *testing.T) {
	agent := newE2EAgent()
	agent.stopReason = acpsdk.StopReasonRefusal
	tp := wireFake(agent)
	defer tp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cl.Close()

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	stop, err := sess.Prompt(context.Background(), "做点不该做的事")
	if err != nil {
		t.Fatalf("拒绝收尾不是传输错误: %v", err)
	}
	if stop != string(acpsdk.StopReasonRefusal) {
		t.Fatalf("stop reason 应如实透传，得到 %q", stop)
	}
}

// TestPromptAgentErrorEndToEnd 底座在 Prompt 上直接返回错误：客户端归类上抛，
// **不静默当成空成功**（失败轮的另一半语义）。
func TestPromptAgentErrorEndToEnd(t *testing.T) {
	agent := newE2EAgent()
	agent.promptErr = context.DeadlineExceeded
	tp := wireFake(agent)
	defer tp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cl.Close()

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.Prompt(context.Background(), "boom"); err == nil {
		t.Fatal("底座报错应上抛，不得静默成功")
	}
}
