package interaction

import (
	"testing"

	"github.com/morehao/ark-mate/pkg/acl"
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
