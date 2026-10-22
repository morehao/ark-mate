package acp

import (
	"reflect"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

// capsResponse 便捷构造 AgentCapabilities（仅设本层关心的字段）。
func capsResponse(load bool, list, resume, fork, close bool, image, embedded bool) acpsdk.AgentCapabilities {
	sc := acpsdk.SessionCapabilities{}
	if list {
		sc.List = &acpsdk.SessionListCapabilities{}
	}
	if resume {
		sc.Resume = &acpsdk.SessionResumeCapabilities{}
	}
	if fork {
		sc.Fork = &acpsdk.SessionForkCapabilities{}
	}
	if close {
		sc.Close = &acpsdk.SessionCloseCapabilities{}
	}
	return acpsdk.AgentCapabilities{
		LoadSession:         load,
		SessionCapabilities: sc,
		PromptCapabilities: acpsdk.PromptCapabilities{
			Image:           image,
			EmbeddedContext: embedded,
		},
	}
}

// TestCapabilityFromAgent 能力折叠语义：
//   - SessionsPersistent = load/list/resume/fork 任一
//   - MultiSessionConcurrent ≈ list
//   - 指针型 sessionCapabilities 为空结构体即视为支持
//   - AuthMethods 独立收集为可读标签
func TestCapabilityFromAgent(t *testing.T) {
	// 形参样本取自历史实测的全能力底座（该底座已下线）：load/list/resume/fork/close 与
	// 图/嵌入全为真。子测试名保留当初的样本名（测试内字符串一律不改名），它只描述能力
	// 形态、不指向任何在册适配。
	t.Run("opencode_like_full", func(t *testing.T) {
		auth := []acpsdk.AuthMethod{
			{Agent: &acpsdk.AuthMethodAgent{Id: "google.com"}},
		}
		c, raw := capabilityFromAgent(capsResponse(true, true, true, true, true, true, true), auth)
		if !raw.LoadSession {
			t.Fatalf("原始 SDK 载荷未回填（rawCaps 现在是 Client 的内部状态）")
		}
		if !c.SessionsPersistent || !c.MultiSessionConcurrent || !c.Images || !c.EmbeddedContext {
			t.Fatalf("全能力折叠不符: %+v", c)
		}
		if !reflect.DeepEqual(c.AuthMethods, []string{"google.com"}) {
			t.Fatalf("AuthMethods 不符: %v", c.AuthMethods)
		}
	})

	t.Run("harness_like_no_load", func(t *testing.T) {
		// harness：load=false、无 fork；list/resume/close 有。
		c, _ := capabilityFromAgent(capsResponse(false, true, true, false, true, false, false), nil)
		if !c.SessionsPersistent { // list/resume 存在 → 仍判持久
			t.Fatalf("list/resume 应判 SessionsPersistent: %+v", c)
		}
		if c.Images || c.EmbeddedContext {
			t.Fatalf("harness 不应声明 image/embedded: %+v", c)
		}
	})

	t.Run("minimal_no_session_caps", func(t *testing.T) {
		c, _ := capabilityFromAgent(capsResponse(false, false, false, false, false, false, false), nil)
		if c.SessionsPersistent || c.MultiSessionConcurrent {
			t.Fatalf("无任何会话能力不应判持久/多会话: %+v", c)
		}
	})
}

// TestCapabilityForkOnlyPersistent 仅 fork 也构成持久会话能力。
func TestCapabilityForkOnlyPersistent(t *testing.T) {
	c, _ := capabilityFromAgent(capsResponse(false, false, false, true, false, false, false), nil)
	if !c.SessionsPersistent {
		t.Fatalf("仅 fork 也应判持久: %+v", c)
	}
}

// TestAuthMethodName 鉴权方式可读标签。
func TestAuthMethodName(t *testing.T) {
	cases := []struct {
		name string
		m    acpsdk.AuthMethod
		want string
	}{
		{"env_var", acpsdk.AuthMethod{EnvVar: &acpsdk.AuthMethodEnvVarInline{}}, "env_var"},
		{"terminal", acpsdk.AuthMethod{Terminal: &acpsdk.AuthMethodTerminalInline{}}, "terminal"},
		{"agent_with_id", acpsdk.AuthMethod{Agent: &acpsdk.AuthMethodAgent{Id: "github.com"}}, "github.com"},
		{"agent_no_id", acpsdk.AuthMethod{Agent: &acpsdk.AuthMethodAgent{}}, "agent"},
		{"empty", acpsdk.AuthMethod{}, "unknown"},
	}
	for _, tc := range cases {
		if got := authMethodName(tc.m); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// TestDescribeAuthMethods 报错文案的鉴权列表。
func TestDescribeAuthMethods(t *testing.T) {
	methods := []acpsdk.AuthMethod{
		{EnvVar: &acpsdk.AuthMethodEnvVarInline{}},
		{Agent: &acpsdk.AuthMethodAgent{Id: "id1"}},
	}
	got := describeAuthMethods(methods)
	if !reflect.DeepEqual(got, []string{"env_var", "id1"}) {
		t.Fatalf("got %v", got)
	}
}
