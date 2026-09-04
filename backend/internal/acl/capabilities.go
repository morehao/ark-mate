package acl

import acpsdk "github.com/coder/acp-go-sdk"

// Capabilities 是 initialize 阶段一次性冻结的能力断言（供上层裁剪接口，不随会话变化）。
type Capabilities struct {
	// SessionsPersistent：支持 load/list/resume/fork/close 等持久会话能力（harness=false）。
	SessionsPersistent bool
	// MultiSessionConcurrent：同连接可同时存在多个会话（近似以 sessionCapabilities.list 表达）。
	MultiSessionConcurrent bool
	// Images：prompt 可携带图片。
	Images bool
	// EmbeddedContext：prompt 可内嵌 Resource 上下文。
	EmbeddedContext bool
	// AuthMethods：agent 声明的鉴权方式（非空则需走 Authenticate 流程）。
	AuthMethods []string
	// Raw 保留原始 AgentCapabilities（adapter 探测/降级时细看）。
	Raw acpsdk.AgentCapabilities
}

// capabilityFromAgent 把 ACP initialize 响应折叠为内部断言。
// authMethods 独立于 AgentCapabilities（位于 InitializeResponse 顶层）。
// 语义：sessionCapabilities.* 为指针且非 nil 视为支持；loadSession 为独立布尔。
func capabilityFromAgent(a acpsdk.AgentCapabilities, authMethods []acpsdk.AuthMethod) Capabilities {
	sc := a.SessionCapabilities
	caps := Capabilities{
		SessionsPersistent:     a.LoadSession || sc.List != nil || sc.Resume != nil || sc.Fork != nil,
		MultiSessionConcurrent: sc.List != nil,
		Images:                 a.PromptCapabilities.Image,
		EmbeddedContext:        a.PromptCapabilities.EmbeddedContext,
		AuthMethods:            make([]string, 0, len(authMethods)),
		Raw:                    a,
	}
	for _, m := range authMethods {
		caps.AuthMethods = append(caps.AuthMethods, authMethodName(m))
	}
	return caps
}

// authMethodName 返回鉴权方式的可读标签：env_var/terminal 取种类名，
// agent 自托管取 Agent.Id（Authenticate 时需回传的 methodId）。
func authMethodName(m acpsdk.AuthMethod) string {
	switch {
	case m.EnvVar != nil:
		return "env_var"
	case m.Terminal != nil:
		return "terminal"
	case m.Agent != nil:
		if m.Agent.Id != "" {
			return m.Agent.Id
		}
		return "agent"
	}
	return "unknown"
}

// describeAuthMethods 人类可读地列出全部鉴权方式（报 access 错误时用）。
func describeAuthMethods(methods []acpsdk.AuthMethod) []string {
	out := make([]string, 0, len(methods))
	for _, m := range methods {
		out = append(out, authMethodName(m))
	}
	return out
}
