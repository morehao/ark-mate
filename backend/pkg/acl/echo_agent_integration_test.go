package acl

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-forge/pkg/runtime/echo"
)

// TestEchoAgentPermissionRoundTrip 端到端验证 echo 演示底座（权限真实链路演示）：
// agent 在 Prompt 中途发起 session/request_permission（allow/reject × once/always 四选项）→
// host 决策（允许一次）→ agent 收到 Selected → 推送授权消息 + 工具卡；Prompt 正常收尾 end_turn。
// 该 agent 即 Web 面板里无需模型 key 即可点选权限卡的底座。
func TestEchoAgentPermissionRoundTrip(t *testing.T) {
	captured := make(chan PermissionRequest, 1)
	events := make(chan Event, 64)

	tp := newPipeTransport()
	echo.Serve(tp.a2cW, tp.c2aR)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
		OnEvent: func(e Event) { events <- e },
		RequestPermission: func(_ context.Context, req PermissionRequest) PermissionDecision {
			captured <- req
			return PermissionDecision{Allowed: true, OptionID: "allow-once"}
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() {
		_ = cl.Close()
		tp.Close()
	}()

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	stop, err := sess.Prompt(context.Background(), "帮我跑个演示")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop 不符: %q", stop)
	}

	// host 侧收到的完整权限请求（四选项 + 工具信息）。
	var req PermissionRequest
	select {
	case req = <-captured:
	default:
		t.Fatal("RequestPermission 未被 echo agent 调用")
	}
	if req.ToolCall.Title != "运行演示命令 echo hello" || req.ToolCall.Kind != "execute" {
		t.Fatalf("tool 信息不符: %+v", req.ToolCall)
	}
	if len(req.Options) != 4 {
		t.Fatalf("期望四选项，got %d: %+v", len(req.Options), req.Options)
	}
	kinds := map[string]bool{}
	for _, o := range req.Options {
		kinds[o.Kind] = true
	}
	for _, k := range []string{"allow_once", "allow_always", "reject_once", "reject_always"} {
		if !kinds[k] {
			t.Fatalf("缺选项 kind %s: %+v", k, req.Options)
		}
	}

	// 事件流：思考 + 输入回显 + 授权消息 + 工具卡。
	got := drainEvents(events, 8)
	var sawGranted, sawToolStart, sawToolDone bool
	for _, e := range got {
		if e.Kind == KindMessage {
			if d, ok := e.Payload.(MessageDelta); ok && strings.Contains(d.Text, "已授权（允许一次）") {
				sawGranted = true
			}
		}
		if e.Kind == KindToolCall {
			if d, ok := e.Payload.(ToolCallInfo); ok && d.Title == "运行演示命令 echo hello" {
				sawToolStart = true
			}
		}
		if e.Kind == KindToolUpdate {
			if d, ok := e.Payload.(ToolCallInfo); ok && d.Output == "hello" {
				sawToolDone = true
			}
		}
	}
	if !sawGranted {
		t.Fatalf("未收到授权回显消息: %+v", summarizeEvents(got))
	}
	if !sawToolStart || !sawToolDone {
		t.Fatalf("工具卡不完整 start=%v done=%v: %+v", sawToolStart, sawToolDone, summarizeEvents(got))
	}
}

// TestEchoAgentPermissionReject 拒绝分支：host 拒绝 → agent 推送「已拒绝」消息，不出现工具卡。
func TestEchoAgentPermissionReject(t *testing.T) {
	events := make(chan Event, 64)
	tp := newPipeTransport()
	echo.Serve(tp.a2cW, tp.c2aR)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
		OnEvent: func(e Event) { events <- e },
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: false, OptionID: "reject-once"}
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() {
		_ = cl.Close()
		tp.Close()
	}()

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := sess.Prompt(context.Background(), "跑个演示"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	var sawRejected, sawTool bool
	for _, e := range drainEvents(events, 8) {
		if d, ok := e.Payload.(MessageDelta); ok && strings.Contains(d.Text, "未获授权") {
			sawRejected = true
		}
		if e.Kind == KindToolCall || e.Kind == KindToolUpdate {
			sawTool = true
		}
	}
	if !sawRejected {
		t.Fatal("未收到拒绝回显消息")
	}
	if sawTool {
		t.Fatal("拒绝后不应出现工具卡")
	}
}

// TestEchoAgentSimulatedFailure boom 模拟失败：首次含 boom 的轮次返回 error，
// 同会话再次发同一文本恢复正常（end_turn）——为「错误 turn → 一键重试」提供确定性演示。
func TestEchoAgentSimulatedFailure(t *testing.T) {
	tp := newPipeTransport()
	echo.Serve(tp.a2cW, tp.c2aR)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
		OnEvent: func(Event) {},
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: true, OptionID: "allow-once"}
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() {
		_ = cl.Close()
		tp.Close()
	}()

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// 首次：失败。
	if _, err := sess.Prompt(context.Background(), "boom 跑一下"); err == nil {
		t.Fatal("首次 boom 轮应返回错误")
	}
	// 同会话重试同一文本：成功。
	stop, err := sess.Prompt(context.Background(), "boom 跑一下")
	if err != nil {
		t.Fatalf("重试应成功: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("重试 stop 不符: %q", stop)
	}
}

func summarizeEvents(evs []Event) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		s := string(e.Kind)
		if d, ok := e.Payload.(MessageDelta); ok {
			s += ": " + d.Text
		}
		out = append(out, s)
	}
	return out
}
