package manager

import (
	"context"
	"testing"
	"time"

	"github.com/morehao/ark-forge/internal/acl"
)

// TestPermissionPolicyDecide 匹配优先级：工具 title > ACP kind > default；大小写不敏感。
func TestPermissionPolicyDecide(t *testing.T) {
	p := &PermissionPolicy{
		Default: "deny",
		Tools:   map[string]string{"Bash": "allow", "execute": "ask", "write": "deny"},
	}
	cases := []struct {
		title, kind, want string
	}{
		{"bash", "execute", PermissionAllow},  // title 命中（大小写不敏感）优先于 kind
		{"unknown", "execute", PermissionAsk}, // kind 命中（显式 ask）
		{"write", "", PermissionDeny},         // title 命中
		{"unknown", "read", PermissionDeny},   // 未命中 → default
		{"", "", PermissionDeny},              // 无信息 → default
	}
	for _, tc := range cases {
		if got := p.Decide(tc.title, tc.kind); got != tc.want {
			t.Fatalf("Decide(%q,%q) = %q，want %q", tc.title, tc.kind, got, tc.want)
		}
	}
	// 未声明策略 / 空策略 → 恒 ask（历史行为不回归）。
	var nilPolicy *PermissionPolicy
	if nilPolicy.Decide("bash", "execute") != PermissionAsk {
		t.Fatal("nil 策略应 ask")
	}
	if (&PermissionPolicy{}).Decide("bash", "execute") != PermissionAsk {
		t.Fatal("空策略应 ask")
	}
	// default 只接受 allow/deny（未知值 → ask）。
	if (&PermissionPolicy{Default: "ALLOW"}).Decide("x", "y") != PermissionAllow {
		t.Fatal("default 应大小写不敏感")
	}
	if (&PermissionPolicy{Default: "bogus"}).Decide("x", "y") != PermissionAsk {
		t.Fatal("default 非法值应回落 ask")
	}
}

// TestPermissionPolicyValidate 未知动作值必须报错（避免静默失效）。
func TestPermissionPolicyValidate(t *testing.T) {
	if err := (&PermissionPolicy{}).Validate(); err != nil {
		t.Fatalf("空策略应合法: %v", err)
	}
	if err := (&PermissionPolicy{Default: "allow", Tools: map[string]string{"bash": "DENY"}}).Validate(); err != nil {
		t.Fatalf("合法策略不应报错: %v", err)
	}
	if err := (&PermissionPolicy{Default: "yes"}).Validate(); err == nil {
		t.Fatal("default=yes 应报错")
	}
	if err := (&PermissionPolicy{Tools: map[string]string{"bash": "maybe"}}).Validate(); err == nil {
		t.Fatal("tools 非法值应报错")
	}
	if err := (&PermissionPolicy{Tools: map[string]string{" ": "allow"}}).Validate(); err == nil {
		t.Fatal("空键应报错")
	}
}

// TestAutoPermissionDecision 动作 → ACP 选项选择（优先 once；无匹配选项不臆造）。
func TestAutoPermissionDecision(t *testing.T) {
	opts := []acl.PermissionOption{
		{OptionID: "a-always", Kind: permKindAllowAlways},
		{OptionID: "a-once", Kind: permKindAllowOnce},
		{OptionID: "r-once", Kind: permKindRejectOnce},
	}
	d, ok := autoPermissionDecision(PermissionAllow, opts)
	if !ok || !d.Allowed || d.OptionID != "a-once" || d.Always {
		t.Fatalf("allow 应选 allow_once 且不写记忆: %+v ok=%v", d, ok)
	}
	d, ok = autoPermissionDecision(PermissionDeny, opts)
	if !ok || !d.Rejected || d.OptionID != "r-once" {
		t.Fatalf("deny 应选 reject_once: %+v ok=%v", d, ok)
	}
	// 只有 allow_always 时回落。
	d, ok = autoPermissionDecision(PermissionAllow, []acl.PermissionOption{{OptionID: "x", Kind: permKindAllowAlways}})
	if !ok || d.OptionID != "x" {
		t.Fatalf("应回落 allow_always: %+v ok=%v", d, ok)
	}
	// 无对应选项 → 不决策（调用方征询用户）。
	if _, ok := autoPermissionDecision(PermissionDeny, nil); ok {
		t.Fatal("无选项时不应自动决策")
	}
	if _, ok := autoPermissionDecision(PermissionAsk, opts); ok {
		t.Fatal("ask 不应自动决策")
	}
}

// TestOrchestratorPermissionPolicyAutoDecide per-agent 策略在编排层生效：
// allow → 直接放行（不挂起、不写记忆）；deny → 直接拒绝；ask → 回落挂起征询。
func TestOrchestratorPermissionPolicyAutoDecide(t *testing.T) {
	svc, orch, bus := newTestRig(t)
	svc.agents["a-allow"] = &AgentRec{ID: "a-allow", Runtimes: []string{"echo"},
		PermissionPolicy: &PermissionPolicy{Tools: map[string]string{"bash": "allow"}}}
	svc.agents["a-deny"] = &AgentRec{ID: "a-deny", Runtimes: []string{"echo"},
		PermissionPolicy: &PermissionPolicy{Default: "deny"}}
	svc.agents["a-ask"] = &AgentRec{ID: "a-ask", Runtimes: []string{"echo"},
		PermissionPolicy: &PermissionPolicy{Default: "ask"}}

	opts := []acl.PermissionOption{
		{OptionID: "o-allow", Kind: permKindAllowOnce},
		{OptionID: "o-reject", Kind: permKindRejectOnce},
	}
	// 会话 id 直接作为原生 session id（无 byProvider 映射时 sid 回落原生 = 平台 id）。
	reqFor := func(sid string) acl.PermissionRequest {
		return acl.PermissionRequest{SessionID: sid,
			ToolCall: acl.ToolCallInfo{ID: "tc1", Title: "bash", Kind: "execute"}, Options: opts}
	}

	// allow：直接放行 + 审计事件。
	svc.registerConversation(&ConversationRec{ID: "c-allow", AgentID: "a-allow"})
	sub := bus.Subscribe(0)
	defer bus.Unsubscribe(sub)
	d := orch.requestPermission(context.Background(), reqFor("c-allow"))
	if !d.Allowed || d.OptionID != "o-allow" || d.Always {
		t.Fatalf("策略 allow 应直接放行: %+v", d)
	}
	if orch.perm.pendingCount("c-allow") != 0 {
		t.Fatal("策略决策不应产生挂起")
	}
	select {
	case e := <-sub.C:
		if e.Type != BusPermissionPolicy || e.AggregID != "c-allow" {
			t.Fatalf("期望 %s 审计事件，got %s", BusPermissionPolicy, e.Type)
		}
		p := e.Payload.(map[string]any)
		if p["action"] != PermissionAllow || p["agent_id"] != "a-allow" || p["title"] != "bash" {
			t.Fatalf("审计载荷不符: %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到策略审计事件")
	}
	if len(orch.perm.List("c-allow")) != 0 {
		t.Fatal("策略决策不应写会话级记忆")
	}

	// deny：直接拒绝。
	svc.registerConversation(&ConversationRec{ID: "c-deny", AgentID: "a-deny"})
	d = orch.requestPermission(context.Background(), reqFor("c-deny"))
	if !d.Rejected || d.OptionID != "o-reject" {
		t.Fatalf("策略 deny 应直接拒绝: %+v", d)
	}

	// ask：回落挂起（短超时 → 自动取消，决策为空；且发布了挂起请求事件）。
	orch.perm.SetTimeout(30 * time.Millisecond)
	svc.registerConversation(&ConversationRec{ID: "c-ask", AgentID: "a-ask"})
	subAsk := bus.Subscribe(0)
	defer bus.Unsubscribe(subAsk)
	d = orch.requestPermission(context.Background(), reqFor("c-ask"))
	if d.Allowed || d.Rejected {
		t.Fatalf("ask 应回落征询（超时取消）: %+v", d)
	}
	// 订阅会重放历史事件 → 扫描到目标类型（挂起请求）。
	deadline := time.After(time.Second)
	for pending := true; pending; {
		select {
		case e := <-subAsk.C:
			if e.Type == BusPermissionRequest && e.AggregID == "c-ask" {
				pending = false
			}
		case <-deadline:
			t.Fatal("未收到挂起权限请求事件")
		}
	}

	// 无策略 agent（nil）→ 行为不变（挂起）。
	svc.agents["a-none"] = &AgentRec{ID: "a-none", Runtimes: []string{"echo"}}
	svc.registerConversation(&ConversationRec{ID: "c-none", AgentID: "a-none"})
	if d = orch.requestPermission(context.Background(), reqFor("c-none")); d.Allowed || d.Rejected {
		t.Fatalf("无策略应回落征询: %+v", d)
	}
}
