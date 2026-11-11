package sessionruntime

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-mate/internal/core/eventbus"
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

// TestOrchAskElicitationTimed_PendingThenLateAnswer 限时问答（askTimed）的端到端（编排层）：
//
//   - 发起一次 `Timed=true` 的外部提问 → 弹卡（阻塞）；
//   - 桥接**断开**（底座计时器到期中止 HTTP，模拟 askTimed 拿到 pending）→ 平台转 pending：
//     `session.elicitation_resolved` action=pending、挂起项保留（卡片仍可回答）；
//   - 用户**迟到作答**（late=true, accept）：走 ResolveLate → 作为**新一轮**注入（Prompt），
//     并发 accept/late 收口事件。
//
// 这条钉的是设计 §10.5「askTimed pending/continued 迟到回答」的二期接线：超时后仍可答、
// 回答作为新一轮进入，且不踩 D1（归属仍由 ScopeWire 显式给出）。
func TestOrchAskElicitationTimed_PendingThenLateAnswer(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	orch.svc.sessionPlane.RegisterSession(&session.SessionRec{ID: "ses_t", Cwd: "/tmp"})
	sub := bus.SubscribeAfter(nil)
	defer bus.Unsubscribe(sub)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan protocol.ElicitationOutcome, 1)
	go func() {
		out, _ := orch.AskElicitation(ctx, "ses_t", protocol.ElicitationRequest{
			Mode: "form", Message: "用哪个库？", Timed: true,
		})
		done <- out
	}()

	// 等弹卡，拿 elicitation_id，并校验 scope_source=wire（绕开 D1）。
	var elicID string
	deadline := time.After(3 * time.Second)
	for elicID == "" {
		select {
		case e := <-sub.C:
			if e.Type != session.BusElicitationRequest || e.AggregID != "ses_t" {
				continue
			}
			m, _ := e.Payload.(map[string]any)
			elicID, _ = m["elicitation_id"].(string)
			if m["scope_source"] != protocol.ScopeWire {
				t.Fatalf("scope_source 应 wire，实际 %v", m["scope_source"])
			}
		case <-deadline:
			t.Fatalf("未收到 session.elicitation_request")
		}
	}

	// 模拟桥接断开（底座计时器到期中止 HTTP）。
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("断开后 AskElicitation 未返回")
	}

	// 期待一条 pending 收口（卡片保留，不是 cancel）。
	waitResolvedAction(t, sub, "ses_t", elicID, "pending")
	// 挂起项必须还在（可回答）。
	if items := orch.PendingElicitations("ses_t"); len(items) != 1 {
		t.Fatalf("限时断开后挂起项应保留可回答，got %d 条", len(items))
	}

	// 迟到作答：走 late=true，把回答作为新一轮注入。
	_, _ = svc.ResolveElicitation("ses_t", elicID, "accept", map[string]any{"lib": "zap"}, true)
	// 期待一条 accept/late 收口（ResolveLate 已抹掉挂起项；Prompt 注入新一轮可能因无实时底座报错，
	// 但收口事件在 Prompt 之前发布，是「已作答」的事实）。
	waitResolvedAction(t, sub, "ses_t", elicID, "accept")
	if items := orch.PendingElicitations("ses_t"); len(items) != 0 {
		t.Fatalf("迟到作答后挂起项应被抹掉，got %d 条", len(items))
	}
}

// waitResolvedAction 在事件流里等一条指定 elicitation_id 的 session.elicitation_resolved 且
// action 匹配（超时 fail）。供限时问答的 pending / late 两处断言复用。
func waitResolvedAction(t *testing.T, sub *eventbus.Subscription, sessID, elicID, wantAction string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e := <-sub.C:
			if e.Type != session.BusElicitationResolved || e.AggregID != sessID {
				continue
			}
			m, _ := e.Payload.(map[string]any)
			if m["elicitation_id"] != elicID {
				continue
			}
			if m["action"] != wantAction {
				t.Fatalf("收口 action 应为 %q，实际 %v（payload=%+v）", wantAction, m["action"], m)
			}
			return
		case <-deadline:
			t.Fatalf("未等到 %s 的 %q 收口事件", elicID, wantAction)
		}
	}
}
