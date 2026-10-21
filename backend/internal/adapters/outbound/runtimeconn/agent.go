package runtimeconn

import (
	"context"

	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/adapters/outbound/acp"
	"github.com/morehao/ark-mate/internal/runtime"
)

// ——— 连接与原生会话的**协议适配体**（C2 收尾：连接与生命周期）———
//
// `session.AgentConn` / `session.AgentSession` 是平台的按用例端口；本文件是它们在生产路径上的
// 唯一实现：把 `*acp.Client` / `*acp.Session` 包一层，并在**这一处**做平台值 ⇄ 协议值的转换。
//
// 为什么要包一层而不是让 acp 类型直接满足接口：域侧接口的签名必须是平台值（`SessionOptions` /
// `runtime.ConfigOption` / `runtime.CapabilityFacts`），而协议类型的方法签名是协议值——Go 的接口
// 是结构性的，签名不同就满足不了。转换写两遍（读一遍、写一遍）就会出现"读的时候搬了这个字段、
// 写的时候漏了"的分叉，所以两边都收在这里。

// ——— 错误出口（B4）：协议错误 → 平台视图 ———
//
// 本文件与 `runtime.Connect` 是**把协议错误交给上层**的两条出口：会话/轮次调用返回的
// `*acp.Error`（以及 SDK/进程层错误）在这里折成 `code.RuntimeError`。
// 漏掉一处不会让编译失败，只会让本该 40401/50201 的错误在外层退化成 50000——
// 所以每个"把 acp 错误 return 出去"的分支都显式过 `runtime.FoldError`。

// Agent 实现 `session.Conn`：这条连接的能力面（协议形状不越过这一层）。
//
// 未拨号/测试桩（client == nil）→ nil：调用方据此判"没有可用连接"。nil 接口而不是
// "非 nil 但每个方法都返回零值"，是因为后者会让 NewSession 在 nil 客户端上 panic。
func (c *Conn) Agent() session.AgentConn {
	if c == nil || c.client == nil {
		return nil
	}
	return agentConn{cli: c.client}
}

// agentConn 一条 ACP 客户端的平台视图。
type agentConn struct{ cli *acp.Client }

var _ session.AgentConn = agentConn{}

func (a agentConn) Capabilities() runtime.CapabilityFacts {
	if a.cli == nil {
		return runtime.CapabilityFacts{}
	}
	return runtime.CapabilityFactsOfACP(a.cli.Capabilities())
}

func (a agentConn) StderrTail() []string {
	if a.cli == nil {
		return nil
	}
	return acp.TailLines(a.cli.StderrTail(), session.MaxDiagStderrLines)
}

func (a agentConn) NewSession(ctx context.Context, opt session.SessionOptions) (session.AgentSession, error) {
	sess, err := a.cli.NewSession(ctx, acpSessionOptions(opt))
	if err != nil {
		return nil, runtime.FoldError(err)
	}
	return agentSession{sess: sess}, nil
}

func (a agentConn) LoadSession(ctx context.Context, nativeID string, opt session.SessionOptions) (session.AgentSession, error) {
	sess, err := a.cli.LoadSession(ctx, nativeID, acpSessionOptions(opt))
	if err != nil {
		return nil, runtime.FoldError(err)
	}
	return agentSession{sess: sess}, nil
}

func (a agentConn) ResumeSession(ctx context.Context, nativeID string, opt session.SessionOptions) (session.AgentSession, error) {
	sess, err := a.cli.ResumeSession(ctx, nativeID, acpSessionOptions(opt))
	if err != nil {
		return nil, runtime.FoldError(err)
	}
	return agentSession{sess: sess}, nil
}

func (a agentConn) ListSessions(ctx context.Context, cwd string) ([]runtime.SessionRef, error) {
	if a.cli == nil {
		return nil, nil
	}
	items, err := a.cli.ListSessions(ctx, cwd)
	if err != nil {
		return nil, runtime.FoldError(err)
	}
	return runtime.SessionRefsOfACP(items), nil
}

// agentSession 一条 ACP 会话的平台视图。
type agentSession struct{ sess *acp.Session }

var _ session.AgentSession = agentSession{}

func (s agentSession) Prompt(ctx context.Context, text string) (string, error) {
	out, err := s.sess.Prompt(ctx, text)
	if err != nil {
		return "", runtime.FoldError(err)
	}
	return out, nil
}

func (s agentSession) Cancel(ctx context.Context) error { return runtime.FoldError(s.sess.Cancel(ctx)) }

func (s agentSession) ConfigOptions() []runtime.ConfigOption {
	return runtime.ConfigOptionsOfACP(s.sess.ConfigOptions())
}

func (s agentSession) ID() string { return s.sess.ID() }

func (s agentSession) Capabilities() runtime.CapabilityFacts {
	return runtime.CapabilityFactsOfACP(s.sess.Client().Capabilities())
}

func (s agentSession) Close(ctx context.Context) error { return runtime.FoldError(s.sess.Close(ctx)) }

func (s agentSession) SetConfigOption(ctx context.Context, optionID, value string) ([]runtime.ConfigOption, error) {
	opts, err := s.sess.SetConfigOption(ctx, optionID, value)
	if err != nil {
		return nil, runtime.FoldError(err)
	}
	return runtime.ConfigOptionsOfACP(opts), nil
}

// acpSessionOptions 平台入参 → 协议入参（写面方向的唯一转换点）。
func acpSessionOptions(opt session.SessionOptions) acp.SessionOptions {
	return acp.SessionOptions{
		Cwd:                   opt.Cwd,
		AdditionalDirectories: append([]string(nil), opt.AdditionalDirectories...),
		McpServers:            runtime.McpServersToACP(opt.McpServers),
	}
}
