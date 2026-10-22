package acp

import acpsdk "github.com/coder/acp-go-sdk"

// capabilityFromAgent 把 ACP initialize 响应折叠为内部断言。
// authMethods 独立于 AgentCapabilities（位于 InitializeResponse 顶层）。
// 语义：sessionCapabilities.* 为指针且非 nil 视为支持；loadSession 为独立布尔。
func capabilityFromAgent(a acpsdk.AgentCapabilities, authMethods []acpsdk.AuthMethod) (Capabilities, acpsdk.AgentCapabilities) {
	sc := a.SessionCapabilities
	caps := Capabilities{
		SessionsPersistent:     a.LoadSession || sc.List != nil || sc.Resume != nil || sc.Fork != nil,
		MultiSessionConcurrent: sc.List != nil,
		Images:                 a.PromptCapabilities.Image,
		EmbeddedContext:        a.PromptCapabilities.EmbeddedContext,
		AuthMethods:            make([]string, 0, len(authMethods)),
		LoadSession:            a.LoadSession,
		SessionClose:           sc.Close != nil,
		SessionList:            sc.List != nil,
		SessionResume:          sc.Resume != nil,
		SessionDelete:          sc.Delete != nil,
		SessionFork:            sc.Fork != nil,
		AuthGuidance:           make([]AuthGuidance, 0, len(authMethods)),
	}
	for _, m := range authMethods {
		caps.AuthMethods = append(caps.AuthMethods, authMethodName(m))
		caps.AuthGuidance = append(caps.AuthGuidance, authGuidanceOf(m))
	}
	return caps, a
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

// authMethodID 取该鉴权方式在 authenticate 时使用的 methodId。
func authMethodID(m acpsdk.AuthMethod) string {
	switch {
	case m.EnvVar != nil:
		return m.EnvVar.Id
	case m.Terminal != nil:
		return m.Terminal.Id
	case m.Agent != nil:
		return m.Agent.Id
	}
	return ""
}

// authGuidanceOf 归一化一条鉴权方式为可展示信息。
// 终端登录命令有两个来源：terminal 变体（Args，命令由 agent 二进制补齐），
// 或 agent 变体 _meta["terminal-auth"]（客户端声明该能力后 agent 附带 command/args/label）。
func authGuidanceOf(m acpsdk.AuthMethod) AuthGuidance {
	g := AuthGuidance{MethodID: authMethodID(m)}
	switch {
	case m.EnvVar != nil:
		g.Name = m.EnvVar.Name
		g.Description = firstNonEmptyPtr(m.EnvVar.Description)
	case m.Terminal != nil:
		g.Name = m.Terminal.Name
		g.Description = firstNonEmptyPtr(m.Terminal.Description)
		g.Terminal = true
		if cmd, args := terminalAuthMeta(m.Terminal.Meta); cmd != "" {
			g.Command = append([]string{cmd}, args...)
		} else {
			g.Command = append([]string(nil), m.Terminal.Args...)
		}
	case m.Agent != nil:
		g.Name = m.Agent.Name
		g.Description = firstNonEmptyPtr(m.Agent.Description)
		if cmd, args := terminalAuthMeta(m.Agent.Meta); cmd != "" {
			g.Terminal = true
			g.Command = append([]string{cmd}, args...)
		}
	}
	if g.Name == "" {
		g.Name = authMethodName(m)
	}
	return g
}

// terminalAuthMeta 从 _meta["terminal-auth"] 解出 {command, args}（缺失/形状不符 → 空串）。
// 该扩展位为 unstable：解析失败一律降级为「无命令」，不猜测。
func terminalAuthMeta(meta map[string]any) (string, []string) {
	if len(meta) == 0 {
		return "", nil
	}
	raw, ok := meta["terminal-auth"]
	if !ok {
		return "", nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return "", nil
	}
	cmd, _ := obj["command"].(string)
	var args []string
	if list, ok := obj["args"].([]any); ok {
		for _, v := range list {
			if s, ok := v.(string); ok {
				args = append(args, s)
			}
		}
	}
	return cmd, args
}

// describeAuthMethods 人类可读地列出全部鉴权方式（报 access 错误时用）。
func describeAuthMethods(methods []acpsdk.AuthMethod) []string {
	out := make([]string, 0, len(methods))
	for _, m := range methods {
		out = append(out, authMethodName(m))
	}
	return out
}
