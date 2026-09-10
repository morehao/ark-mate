package acl

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morehao/ark-forge/pkg/runtime/echo"
)

// TestEchoAgentElicitationSingleAccept 端到端验证 elicitation 单选表单通道：
// echo 在 Prompt 中途发起 elicitation/create（form：string+oneOf）→ host 侧收到完整请求 →
// 决策 accept（content）→ agent 回显提交摘要并正常收尾。
// 注（Rev2）：acl 层不再注入会话归属（SDK 不建模 scope；SessionID 由宿主 orchestrator 注入），
// 故断言 SessionID 为空，归属正确性由 manager/orchestrator 的 busyConn 单测覆盖。
func TestEchoAgentElicitationSingleAccept(t *testing.T) {
	captured := make(chan ElicitationRequest, 1)
	events := make(chan Event, 64)

	tp := newPipeTransport()
	echo.Serve(tp.a2cW, tp.c2aR)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
		OnEvent: func(e Event) { events <- e },
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: true, OptionID: "allow-once"}
		},
		CreateElicitation: func(_ context.Context, req ElicitationRequest) ElicitationOutcome {
			captured <- req
			return ElicitationOutcome{Action: "accept", Content: map[string]any{"env": "staging"}}
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
	stop, err := sess.Prompt(context.Background(), "帮我做个单选：选个部署环境")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop 不符: %q", stop)
	}

	var req ElicitationRequest
	select {
	case req = <-captured:
	default:
		t.Fatal("CreateElicitation 未被 echo agent 调用")
	}
	if req.SessionID != "" {
		t.Fatalf("acl 层不应注入会话归属（归属由宿主注入）: %+v", req)
	}
	if req.Mode != "form" || req.Message == "" {
		t.Fatalf("mode/message 不符: %+v", req)
	}
	if req.Schema == nil || req.Schema.Properties == nil {
		t.Fatalf("schema 缺失: %+v", req)
	}
	if _, ok := req.Schema.Properties["env"]; !ok {
		t.Fatalf("缺 env 字段: %+v", req.Schema)
	}

	// 事件流：思考 + 引导消息 + 提交摘要回显（含 content 键值）。
	var sawSummary bool
	for _, e := range drainEvents(events, 8) {
		if d, ok := e.Payload.(MessageDelta); ok && strings.Contains(d.Text, "env = staging") {
			sawSummary = true
		}
	}
	if !sawSummary {
		t.Fatal("未收到提交摘要回显（env = staging）")
	}
}

// TestEchoAgentElicitationMultiDecline 多选表单被用户拒绝 → agent 回显「用户拒绝」并收尾。
func TestEchoAgentElicitationMultiDecline(t *testing.T) {
	events := make(chan Event, 64)
	tp := newPipeTransport()
	echo.Serve(tp.a2cW, tp.c2aR)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
		OnEvent: func(e Event) { events <- e },
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: true, OptionID: "allow-once"}
		},
		CreateElicitation: func(_ context.Context, req ElicitationRequest) ElicitationOutcome {
			if req.Mode != "form" {
				t.Errorf("期望 form 模式: %+v", req)
			}
			return ElicitationOutcome{Action: "decline"}
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
	stop, err := sess.Prompt(context.Background(), "帮我做个多选：选任务")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop 不符: %q", stop)
	}

	var sawDeclined bool
	for _, e := range drainEvents(events, 8) {
		if d, ok := e.Payload.(MessageDelta); ok && strings.Contains(d.Text, "用户拒绝") {
			sawDeclined = true
		}
	}
	if !sawDeclined {
		t.Fatal("未收到拒绝回显消息")
	}
}

// TestEchoAgentElicitationURL 引导链接模式：host 决策 accept（无内容）→ agent 回显提交。
func TestEchoAgentElicitationURL(t *testing.T) {
	events := make(chan Event, 64)
	tp := newPipeTransport()
	echo.Serve(tp.a2cW, tp.c2aR)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
		OnEvent: func(e Event) { events <- e },
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: true, OptionID: "allow-once"}
		},
		CreateElicitation: func(_ context.Context, req ElicitationRequest) ElicitationOutcome {
			if req.Mode != "url" || req.URL == "" || req.URLID == "" {
				t.Errorf("URL 载荷不符: %+v", req)
			}
			return ElicitationOutcome{Action: "accept"}
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
	stop, err := sess.Prompt(context.Background(), "发个链接给我完成授权")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop 不符: %q", stop)
	}

	var sawURL bool
	for _, e := range drainEvents(events, 8) {
		if d, ok := e.Payload.(MessageDelta); ok && strings.Contains(d.Text, "URL") && strings.Contains(d.Text, "未提供内容") {
			sawURL = true
		}
	}
	if !sawURL {
		t.Fatal("未收到 URL 模式提交回显（未提供内容）")
	}
}

// TestEchoAgentElicitationUnsupportedHost 宿主未实现 elicitation（无 CreateElicitation 钩子）：
// 默认按取消回应，agent 优雅降级告知并正常收尾（不把整轮打成错误）。
func TestEchoAgentElicitationUnsupportedHost(t *testing.T) {
	events := make(chan Event, 64)
	tp := newPipeTransport()
	echo.Serve(tp.a2cW, tp.c2aR)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
		OnEvent: func(e Event) { events <- e },
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: true, OptionID: "allow-once"}
		},
		// 无 CreateElicitation → 宿主按取消处理
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
	stop, err := sess.Prompt(context.Background(), "来个单选吧")
	if err != nil {
		t.Fatalf("无 elicitation 宿主下不应报错: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop 不符: %q", stop)
	}
	var sawCancel bool
	for _, e := range drainEvents(events, 8) {
		if d, ok := e.Payload.(MessageDelta); ok && strings.Contains(d.Text, "取消") {
			sawCancel = true
		}
	}
	if !sawCancel {
		t.Fatal("未收到取消回显消息")
	}
}
