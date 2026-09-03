package acl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	acpsdk "github.com/coder/acp-go-sdk"
)

// ClientOptions 配置一次 ACP 连接。
type ClientOptions struct {
	// OnEvent 接收归一化后的 ACP notification（acl 不感知总线，由注入方决定去向）。
	OnEvent func(Event)
	// Logger 透传给 SDK 连接诊断（nil → 静默）。
	Logger *slog.Logger
	// AllowPermission 决定 agent 的权限请求（tool 执行许可）是否放行；nil 表示一律拒绝。
	// 参数：ctx/sessionID/tool 标题/可选动作数；返回 true 表示选择第一个动作放行。
	AllowPermission func(ctx context.Context, sessionID, toolTitle string, options int) bool
	// ClientName / ClientVersion 用于 initialize 的 clientInfo。
	ClientName    string
	ClientVersion string
}

// Client 是面向 runtime adapter 的高层句柄：一条 ACP 连接（一个子进程）。
// Dial 完成后能力断言已冻结在 Capabilities()。
type Client struct {
	t    Transport
	conn *acpsdk.ClientSideConnection
	caps Capabilities
	opts ClientOptions
	host *hostClient

	closeOnce sync.Once
	closed    chan struct{}
}

// Dial 完成「拉起子进程 → initialize → 能力断言」：Transport.Start 后建立
// ClientSideConnection 并完成 v1 握手。失败时负责回收 Transport。
func Dial(ctx context.Context, t Transport, opts ClientOptions) (*Client, error) {
	if err := t.Start(ctx); err != nil {
		return nil, err
	}
	host := &hostClient{opts: opts}
	conn := acpsdk.NewClientSideConnection(host, t.Stdin(), t.Stdout())
	if opts.Logger != nil {
		conn.SetLogger(opts.Logger)
	}

	impl := acpsdk.Implementation{
		Name:    firstNonEmpty(opts.ClientName, "ark-forge"),
		Version: firstNonEmpty(opts.ClientVersion, "dev-m0"),
	}
	initResp, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
		ClientInfo:         &impl,
	})
	if err != nil {
		_ = t.Close()
		return nil, Classify(err)
	}
	if initResp.ProtocolVersion != acpsdk.ProtocolVersionNumber {
		_ = t.Close()
		return nil, NewError(ClassUnsupported, "protocol_mismatch",
			fmt.Sprintf("agent 协商到协议 v%d，本层仅支持 v%d", initResp.ProtocolVersion, acpsdk.ProtocolVersionNumber), nil)
	}
	caps := capabilityFromAgent(initResp.AgentCapabilities, initResp.AuthMethods)
	// M0 鉴权收敛：仅支持 agent 自托管方式（agent 自行完成登录，opencode 即此形态，methodId
	// 取自 AuthMethodAgent.Id）。其余方式（env_var/terminal）返回 access/auth_required，
	// M3 起补完整 Authenticate 流程。
	if len(initResp.AuthMethods) > 0 {
		var agentIDs []string
		for _, m := range initResp.AuthMethods {
			if m.Agent != nil {
				id := m.Agent.Id
				if id == "" {
					id = "agent"
				}
				agentIDs = append(agentIDs, id)
			}
		}
		if len(agentIDs) == 0 {
			_ = t.Close()
			return nil, NewError(ClassAccess, "auth_required",
				fmt.Sprintf("agent 要求鉴权（%v），M0 仅支持 agent 自托管鉴权", describeAuthMethods(initResp.AuthMethods)), nil)
		}
		if _, err := conn.Authenticate(ctx, acpsdk.AuthenticateRequest{MethodId: agentIDs[0]}); err != nil {
			_ = t.Close()
			return nil, Classify(err)
		}
	}

	return &Client{
		t:      t,
		conn:   conn,
		caps:   caps,
		opts:   opts,
		host:   host,
		closed: make(chan struct{}),
	}, nil
}

// Capabilities 返回 initialize 阶段冻结的能力断言。
func (c *Client) Capabilities() Capabilities { return c.caps }

// Done 在 peer（agent）断开时关闭，供 manager 处理「断连即失效」会话。
func (c *Client) Done() <-chan struct{} { return c.conn.Done() }

// StderrTail 透出子进程最近诊断输出（进程异常退出排查用）。
func (c *Client) StderrTail() []string { return c.t.StderrTail() }

// Close 关闭连接并回收子进程（幂等）。
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.t.Close()
	})
	return err
}

// hostClient 实现 acp-go-sdk 的 acpsdk.Client：接收 agent 发来的请求/通知。
type hostClient struct {
	opts ClientOptions
}

var _ acpsdk.Client = (*hostClient)(nil)

func (h *hostClient) SessionUpdate(ctx context.Context, n acpsdk.SessionNotification) error {
	if h.opts.OnEvent == nil {
		return nil
	}
	for _, ev := range normalizeUpdate(n) {
		h.opts.OnEvent(ev)
	}
	return nil
}

func (h *hostClient) RequestPermission(ctx context.Context, p acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	title := firstNonEmptyPtr(p.ToolCall.Title)
	allowed := false
	if h.opts.AllowPermission != nil {
		allowed = h.opts.AllowPermission(ctx, string(p.SessionId), title, len(p.Options))
	}
	if h.opts.OnEvent != nil {
		h.opts.OnEvent(Event{
			Kind:      KindPermission,
			SessionID: string(p.SessionId),
			Payload:   PermissionInfo{SessionID: string(p.SessionId), Title: title, Allowed: allowed},
		})
	}
	if allowed && len(p.Options) > 0 {
		return acpsdk.RequestPermissionResponse{
			Outcome: acpsdk.RequestPermissionOutcome{
				Selected: &acpsdk.RequestPermissionOutcomeSelected{
					OptionId: p.Options[0].OptionId,
					Outcome:  "selected",
				},
			},
		}, nil
	}
	return acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.RequestPermissionOutcome{
			Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{Outcome: "cancelled"},
		},
	}, nil
}

// hostClient 未开放的能力（fs/terminal）一律返回错误；ark-forge M0 不在
// clientCapabilities 中声明它们，正常 agent 不应发起这些请求。
func (h *hostClient) ReadTextFile(context.Context, acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{}, errors.New("fs.readTextFile 未开放")
}
func (h *hostClient) WriteTextFile(context.Context, acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, errors.New("fs.writeTextFile 未开放")
}
func (h *hostClient) CreateTerminal(context.Context, acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{}, errors.New("terminal 未开放")
}
func (h *hostClient) KillTerminal(context.Context, acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, errors.New("terminal 未开放")
}
func (h *hostClient) TerminalOutput(context.Context, acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{}, errors.New("terminal 未开放")
}
func (h *hostClient) ReleaseTerminal(context.Context, acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, errors.New("terminal 未开放")
}
func (h *hostClient) WaitForTerminalExit(context.Context, acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, errors.New("terminal 未开放")
}

// normalizeUpdate 把一条 SessionNotification 归一化为 0..n 条 acl.Event。
func normalizeUpdate(n acpsdk.SessionNotification) []Event {
	u := n.Update
	sid := string(n.SessionId)
	var evs []Event
	add := func(kind string, payload any) {
		evs = append(evs, Event{Kind: kind, SessionID: sid, Payload: payload})
	}
	switch {
	case u.UserMessageChunk != nil:
		if txt := textOf(u.UserMessageChunk.Content); txt != "" {
			add(KindUserMessage, MessageDelta{Text: txt})
		}
	case u.AgentMessageChunk != nil:
		if txt := textOf(u.AgentMessageChunk.Content); txt != "" {
			add(KindMessage, MessageDelta{Text: txt})
		}
	case u.AgentThoughtChunk != nil:
		if txt := textOf(u.AgentThoughtChunk.Content); txt != "" {
			add(KindThought, ThoughtDelta{Text: txt})
		}
	case u.ToolCall != nil:
		tc := u.ToolCall
		add(KindToolCall, ToolCallInfo{
			ID:     string(tc.ToolCallId),
			Title:  tc.Title,
			Status: string(tc.Status),
			Input:  tc.RawInput,
		})
	case u.ToolCallUpdate != nil:
		tu := u.ToolCallUpdate
		status := ""
		if tu.Status != nil {
			status = string(*tu.Status)
		}
		add(KindToolUpdate, ToolCallInfo{
			ID:     string(tu.ToolCallId),
			Title:  firstNonEmptyPtr(tu.Title),
			Status: status,
		})
	case u.UsageUpdate != nil:
		add(KindUsage, nil)
	default:
		// plan/session 元数据类更新：M0 不建模（前端尚不消费）。
	}
	return evs
}

// textOf 提取 ContentBlock 的文本（仅 text 变体；其余变体 M0 忽略）。
func textOf(b acpsdk.ContentBlock) string {
	if b.Text != nil {
		return b.Text.Text
	}
	return ""
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func firstNonEmptyPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
