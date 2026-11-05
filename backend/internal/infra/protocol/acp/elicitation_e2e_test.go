package acp

import (
	"github.com/morehao/ark-mate/internal/core/protocol"
	"context"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// 本文件是 M0 删除 echo 演示底座后**重建**的 elicitation 端到端覆盖。
//
// 原先这四条链路（单选 accept / 多选 decline / URL / 宿主不支持）靠 `echo.Serve` 当真实
// ACP 对端。echo 已删（见 .dsh/docs/notes/2026-09-22-echo-removal-coverage-ledger.md 的 G1），
// 这里改用 `fakeAgent` + `wireFake`（真 SDK 的 AgentSideConnection，走内存管道）重建：
// 被测的是**客户端侧 elicitation 往返**——agent 发起 → hostClient 折叠为 ElicitationRequest
// → 宿主决策 → 折叠回 ACP 响应 → agent 收到。协议编解码由 SDK 保证，不是自造协议。
//
// 仍存在的缺口（G2）：**stdio 形态**（真实子进程）无自动覆盖；本组测试走的是内存管道。

// newE2EAgent 造一个「可拨号」的 fakeAgent：Init 必须声明协议版本，否则 Dial 在握手期就
// 以 protocol_mismatch 失败（fakeAgent 零值的 init 是 v0）。
func newE2EAgent() *fakeAgent {
	return &fakeAgent{init: fakeInit(int(acpsdk.ProtocolVersionNumber), dialCaps(true, true, true), nil)}
}

// elicitationAgent 造一个在 Prompt 中途发起 elicitation 的 fakeAgent。
// issue 拿到 AgentSideConnection 后自行发请求，并把结果写进 out。
func elicitationAgent(t *testing.T, issue func(ctx context.Context, conn *acpsdk.AgentSideConnection) (acpsdk.UnstableCreateElicitationResponse, error)) (*fakeAgent, *pipeTransport, chan acpsdk.UnstableCreateElicitationResponse) {
	t.Helper()
	out := make(chan acpsdk.UnstableCreateElicitationResponse, 1)
	agent := newE2EAgent()
	agent.promptHook = func(ctx context.Context, _ acpsdk.PromptRequest) {
		resp, err := issue(ctx, agent.conn)
		if err != nil {
			t.Errorf("agent 发起 elicitation 失败: %v", err)
			return
		}
		out <- resp
	}
	tp := wireFake(agent)
	return agent, tp, out
}

// formSchema 单选表单 schema（string + enum），与 ACP `requestedSchema` 同形。
func formSchema() acpsdk.UnstableElicitationSchema {
	return acpsdk.UnstableElicitationSchema{
		Title: ptrString("部署参数"),
		Properties: map[string]any{
			"env": map[string]any{
				"type": "string",
				"enum": []string{"staging", "prod"},
			},
		},
		Required: []string{"env"},
	}
}

func issueForm(message string, meta map[string]any) func(context.Context, *acpsdk.AgentSideConnection) (acpsdk.UnstableCreateElicitationResponse, error) {
	return func(ctx context.Context, conn *acpsdk.AgentSideConnection) (acpsdk.UnstableCreateElicitationResponse, error) {
		return conn.UnstableCreateElicitation(ctx, acpsdk.UnstableCreateElicitationRequest{
			Form: &acpsdk.UnstableCreateElicitationForm{
				Meta:            meta,
				Message:         message,
				Mode:            "form",
				RequestedSchema: formSchema(),
			},
		})
	}
}

// TestElicitationFormAcceptEndToEnd 单选表单 accept：宿主收到完整请求（mode/message/schema）
// → 决策 accept（带 content）→ agent 收到同 content 的 accept 响应 → 整轮正常收尾。
func TestElicitationFormAcceptEndToEnd(t *testing.T) {
	captured := make(chan ElicitationRequest, 1)
	_, tp, out := elicitationAgent(t, issueForm("选个部署环境", nil))
	defer tp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
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
	defer cl.Close()

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	stop, _, err := sess.Prompt(context.Background(), protocol.TextParts("帮我做个单选：选个部署环境"))
	if err != nil || stop != "end_turn" {
		t.Fatalf("Prompt: stop=%q err=%v", stop, err)
	}

	var req ElicitationRequest
	select {
	case req = <-captured:
	default:
		t.Fatal("CreateElicitation 未被调用")
	}
	if req.SessionID != "" {
		t.Fatalf("acl 层不应注入会话归属（归属由宿主注入）: %+v", req)
	}
	if req.Mode != "form" || req.Message != "选个部署环境" {
		t.Fatalf("mode/message 不符: %+v", req)
	}
	if req.Schema == nil || req.Schema.Properties == nil {
		t.Fatalf("schema 缺失: %+v", req)
	}
	if _, ok := req.Schema.Properties["env"]; !ok {
		t.Fatalf("缺 env 字段: %+v", req.Schema)
	}
	if req.Schema.Title != "部署参数" || len(req.Schema.Required) != 1 {
		t.Fatalf("schema title/required 不符: %+v", req.Schema)
	}

	select {
	case resp := <-out:
		if resp.Accept == nil || resp.Accept.Content["env"] != "staging" {
			t.Fatalf("agent 未收到 accept+content: %+v", resp)
		}
	default:
		t.Fatal("agent 未收到 elicitation 响应")
	}
}

// TestElicitationFormDeclineEndToEnd 用户拒绝 → agent 收到 decline（而非 cancel，两者可区分）。
func TestElicitationFormDeclineEndToEnd(t *testing.T) {
	_, tp, out := elicitationAgent(t, issueForm("选任务", nil))
	defer tp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
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
	defer cl.Close()

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	stop, _, err := sess.Prompt(context.Background(), protocol.TextParts("帮我做个多选：选任务"))
	if err != nil || stop != "end_turn" {
		t.Fatalf("Prompt: stop=%q err=%v", stop, err)
	}
	select {
	case resp := <-out:
		if resp.Decline == nil || resp.Accept != nil || resp.Cancel != nil {
			t.Fatalf("agent 应收到 decline: %+v", resp)
		}
	default:
		t.Fatal("agent 未收到 elicitation 响应")
	}
}

// TestElicitationURLEndToEnd 引导链接模式：url + elicitationId 透传，accept 无内容。
func TestElicitationURLEndToEnd(t *testing.T) {
	captured := make(chan ElicitationRequest, 1)
	_, tp, out := elicitationAgent(t, func(ctx context.Context, conn *acpsdk.AgentSideConnection) (acpsdk.UnstableCreateElicitationResponse, error) {
		return conn.UnstableCreateElicitation(ctx, acpsdk.UnstableCreateElicitationRequest{
			Url: &acpsdk.UnstableCreateElicitationUrl{
				ElicitationId: "acp-el-1",
				Message:       "授权链接",
				Mode:          "url",
				Url:           "https://example.test/authorize",
			},
		})
	})
	defer tp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: true, OptionID: "allow-once"}
		},
		CreateElicitation: func(_ context.Context, req ElicitationRequest) ElicitationOutcome {
			captured <- req
			return ElicitationOutcome{Action: "accept"}
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
	if stop, _, err := sess.Prompt(context.Background(), protocol.TextParts("发个链接给我完成授权")); err != nil || stop != "end_turn" {
		t.Fatalf("Prompt: stop=%q err=%v", stop, err)
	}

	select {
	case req := <-captured:
		if req.Mode != "url" || req.URL != "https://example.test/authorize" || req.URLID != "acp-el-1" {
			t.Fatalf("URL 载荷不符: %+v", req)
		}
	default:
		t.Fatal("CreateElicitation 未被调用")
	}
	select {
	case resp := <-out:
		if resp.Accept == nil || resp.Decline != nil || resp.Cancel != nil {
			t.Fatalf("agent 应收到 accept: %+v", resp)
		}
	default:
		t.Fatal("agent 未收到 elicitation 响应")
	}
}

// TestElicitationUnsupportedHostEndToEnd 宿主未实现 elicitation（无 CreateElicitation 钩子）：
// 一律按 cancel 回应，agent 优雅降级，整轮不被打成错误。
func TestElicitationUnsupportedHostEndToEnd(t *testing.T) {
	_, tp, out := elicitationAgent(t, issueForm("来个单选吧", nil))
	defer tp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: true, OptionID: "allow-once"}
		},
		// 无 CreateElicitation → 宿主按取消处理
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cl.Close()

	sess, err := cl.NewSession(context.Background(), SessionOptions{Cwd: "/work"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	stop, _, err := sess.Prompt(context.Background(), protocol.TextParts("来个单选吧"))
	if err != nil {
		t.Fatalf("无 elicitation 宿主下不应报错: %v", err)
	}
	if stop != "end_turn" {
		t.Fatalf("stop 不符: %q", stop)
	}
	select {
	case resp := <-out:
		if resp.Cancel == nil {
			t.Fatalf("宿主未实现 elicitation 时应回 cancel: %+v", resp)
		}
	default:
		t.Fatal("agent 未收到 elicitation 响应")
	}
}

// TestElicitationWireScopeFromMeta 归属经 wire 的 `_meta` 传递（SDK 顶层 sessionId 被丢弃，
// 仅变体 `_meta` 保留）：这是「会话归属」在无 fork 前提下的唯一 wire 来源。
func TestElicitationWireScopeFromMeta(t *testing.T) {
	captured := make(chan ElicitationRequest, 1)
	_, tp, _ := elicitationAgent(t, issueForm("归属测试", map[string]any{"sessionId": "conv-wire-1"}))
	defer tp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cl, err := Dial(ctx, tp, ClientOptions{
		RequestPermission: func(_ context.Context, _ PermissionRequest) PermissionDecision {
			return PermissionDecision{Allowed: true, OptionID: "allow-once"}
		},
		CreateElicitation: func(_ context.Context, req ElicitationRequest) ElicitationOutcome {
			captured <- req
			return ElicitationOutcome{Action: "accept"}
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
	if _, _, err := sess.Prompt(context.Background(), protocol.TextParts("归属测试")); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	select {
	case req := <-captured:
		if req.SessionID != "conv-wire-1" || req.ScopeSource != ScopeWire {
			t.Fatalf("wire 归属未生效: %+v", req)
		}
	default:
		t.Fatal("CreateElicitation 未被调用")
	}
}
