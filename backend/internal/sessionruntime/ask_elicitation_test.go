package sessionruntime

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/domain/session"
)

// TestOrchAskElicitation_HappyPath 外部 source 提问的端到端（编排层）：
//   - 发起 AskElicitation（answerer 桥接）→ 阻塞；
//   - 发布一条 session.elicitation_request（前端据此弹卡），scope_source 必须是 wire（显式归属，绕开 D1）；
//   - 用户经 ResolveElicitation(accept + content) 决策 → AskElicitation 返回到那份内容。
//
// 这条钉的是设计文档 §8.4「answerer 显式携带 session → ScopeWire」的契约落地。
func TestOrchAskElicitation_HappyPath(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	orch.svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "ses_a", Cwd: "/tmp"})

	sub := bus.SubscribeAfter(nil)
	defer bus.Unsubscribe(sub)

	// 后台发起提问（阻塞），把结果经 channel 交回主测试协程。
	type askResult struct {
		out protocol.ElicitationOutcome
		err error
	}
	done := make(chan askResult, 1)
	go func() {
		out, err := orch.AskElicitation(context.Background(), "ses_a", protocol.ElicitationRequest{
			Mode: "form", Message: "请选择环境",
			Schema: &protocol.ElicitationSchema{
				Properties: map[string]any{"env": map[string]any{"type": "string"}},
			},
		})
		done <- askResult{out: out, err: err}
	}()

	// 等 session.elicitation_request：拿到 elicitation_id + 校验 scope_source=wire。
	var elicID string
	deadline := time.After(3 * time.Second)
	for elicID == "" {
		select {
		case e := <-sub.C:
			if e.Type != session.BusElicitationRequest || e.AggregID != "ses_a" {
				continue
			}
			m, _ := e.Payload.(map[string]any)
			elicID, _ = m["elicitation_id"].(string)
			if m["scope_source"] != protocol.ScopeWire {
				t.Fatalf("scope_source 应 wire（显式归属），实际 %v", m["scope_source"])
			}
			if m["mode"] != "form" || m["message"] != "请选择环境" {
				t.Fatalf("请求载荷不符: %+v", m)
			}
		case <-deadline:
			t.Fatalf("未收到 session.elicitation_request")
		}
	}

	// 用户决策：accept + content。
	if _, err := svc.ResolveElicitation("ses_a", elicID, "accept", map[string]any{"env": "prod"}, false); err != nil {
		// svc 的 ResolveElicitation 适配器返回 (bool, error)；这里实为 orch 层，改用 orch。
		t.Fatalf("ResolveElicitation: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("AskElicitation 返回错误: %v", r.err)
		}
		if r.out.Action != "accept" || r.out.Content["env"] != "prod" {
			t.Fatalf("AskElicitation 结果不符: %+v", r.out)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("AskElicitation 未在决策后返回")
	}
}

// TestOrchAskElicitation_EmptySession 空归属必须拒绝（不猜测）：外部提问无确定归属 → 报错，
// 而不是挂到一个错误/不存在的会话上（与 D1「不猜测」同一立场）。
func TestOrchAskElicitation_EmptySession(t *testing.T) {
	_, orch, _ := newTestRig(t)
	if _, err := orch.AskElicitation(context.Background(), "   ", protocol.ElicitationRequest{
		Mode: "form", Message: "q",
	}); err == nil {
		t.Fatalf("空 session 应拒绝")
	}
}

// TestOrchAskElicitation_Decline 用户拒绝：AskElicitation 返回 decline（无 content）。
func TestOrchAskElicitation_Decline(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	orch.svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "ses_d", Cwd: "/tmp"})
	sub := bus.SubscribeAfter(nil)
	defer bus.Unsubscribe(sub)

	done := make(chan protocol.ElicitationOutcome, 1)
	go func() {
		out, _ := orch.AskElicitation(context.Background(), "ses_d", protocol.ElicitationRequest{Mode: "form", Message: "q"})
		done <- out
	}()

	var elicID string
	deadline := time.After(3 * time.Second)
	for elicID == "" {
		select {
		case e := <-sub.C:
			if e.Type != session.BusElicitationRequest || e.AggregID != "ses_d" {
				continue
			}
			m, _ := e.Payload.(map[string]any)
			elicID, _ = m["elicitation_id"].(string)
		case <-deadline:
			t.Fatalf("未收到 session.elicitation_request")
		}
	}
	if _, err := svc.ResolveElicitation("ses_d", elicID, "decline", nil, false); err != nil {
		t.Fatalf("ResolveElicitation decline: %v", err)
	}
	select {
	case out := <-done:
		if out.Action != "decline" {
			t.Fatalf("应返回 decline，实际 %+v", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("decline 后 AskElicitation 未返回")
	}
}

// TestServiceAskElicitationResolvesNativeID 投递口把**底座原生 id**（不带 rt_ 壳）反查成平台会话
// （answerer 插件在 dsh 内只有原生 id）。`platformSessionIDForProvider` 补壳后匹配 runtime_session_id。
func TestServiceAskElicitationResolvesNativeID(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	orch.svc.sessionPlane.RegisterSession(&session.SessionRec{
		ID: "ses_native", RuntimeSessionID: "rt_native-x", Cwd: "/tmp",
	})
	sub := bus.SubscribeAfter(nil)
	defer bus.Unsubscribe(sub)

	done := make(chan protocol.ElicitationOutcome, 1)
	go func() {
		out, err := svc.AskElicitation(context.Background(), "", "native-x", protocol.ElicitationRequest{Mode: "form", Message: "q"})
		if err != nil {
			done <- protocol.ElicitationOutcome{}
			return
		}
		done <- out
	}()

	var elicID string
	deadline := time.After(3 * time.Second)
	for elicID == "" {
		select {
		case e := <-sub.C:
			if e.Type != session.BusElicitationRequest || e.AggregID != "ses_native" {
				continue
			}
			m, _ := e.Payload.(map[string]any)
			elicID, _ = m["elicitation_id"].(string)
		case <-deadline:
			t.Fatalf("原生 id 未反查到平台会话 ses_native")
		}
	}
	if _, err := svc.ResolveElicitation("ses_native", elicID, "decline", nil, false); err != nil {
		t.Fatalf("ResolveElicitation: %v", err)
	}
	select {
	case out := <-done:
		if out.Action != "decline" {
			t.Fatalf("应返回 decline，实际 %+v", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("未返回")
	}
}

// TestServiceAskElicitationUnknownNative 无法归属的原生 id → 报 elicitation_scope_required（不猜）。
func TestServiceAskElicitationUnknownNative(t *testing.T) {
	svc, _, _ := newTestRig(t)
	if _, err := svc.AskElicitation(context.Background(), "", "no-such-native", protocol.ElicitationRequest{Mode: "form", Message: "q"}); err == nil {
		t.Fatalf("未知原生 id 应报错（不猜测归属）")
	}
}
