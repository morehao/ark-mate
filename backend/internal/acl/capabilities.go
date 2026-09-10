package acl

import acpsdk "github.com/coder/acp-go-sdk"

// Capabilities 是 initialize 阶段一次性冻结的能力断言（供上层裁剪接口，不随会话变化）。
// 一律以协议自报为准：不按 runtime 名字硬编码行为差异（例如 opencode 支持 session/load、
// deepseek-harness 不支持，这里各自体现在 LoadSession 上）。
type Capabilities struct {
	// SessionsPersistent：支持 load/list/resume/fork 等持久会话能力（harness=false）。
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

	// —— sessionCapabilities.* 展开：供上层决定「能不能 load/resume/list/close/fork」——
	//
	// 关键区别（实测两家）：dsh 广告 {close,list,resume}、opencode 广告 {close,fork,list,resume}，
	// **两家都不广告 delete** —— 因此不得把「建会话→close」当探针（close 不删持久记录）。
	LoadSession   bool
	SessionClose  bool
	SessionList   bool
	SessionResume bool
	SessionDelete bool
	SessionFork   bool

	// AuthGuidance 鉴权方式的展示信息（空 = agent 未声明鉴权要求）。
	// 用途：运行时没有可用模型（未配凭证）时给用户一条确切的补救路径。
	AuthGuidance []AuthGuidance
}

// AuthGuidance 一条鉴权方式的展示信息。
type AuthGuidance struct {
	// MethodID 传给 ACP authenticate 的 methodId。
	MethodID string
	Name     string
	// Description agent 给出的说明（如「Run `opencode auth login` in the terminal」）。
	Description string
	// Terminal 是否需要在终端交互完成登录。
	Terminal bool
	// Command 可执行命令行；为空且 Terminal=true 时由调用方用 runtime 启动命令补齐
	// （ACP 的 terminal 变体只带 Args，约定用 agent 自身的二进制）。
	Command []string
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
