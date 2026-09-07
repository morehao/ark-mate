package acl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// defaultDialTimeout Dial 未显式配置且调用方 ctx 无截止时间时的握手上限。
const defaultDialTimeout = 15 * time.Second

// ClientOptions 配置一次 ACP 连接。
type ClientOptions struct {
	// OnEvent 接收归一化后的 ACP notification（acl 不感知总线，由注入方决定去向）。
	OnEvent func(Event)
	// Logger 透传给 SDK 连接诊断（nil → 静默）。
	Logger *slog.Logger
	// RequestPermission 处理 agent 的一次权限请求并**阻塞等待宿主决策**（同步变体，
	// 由 SDK 在独立的 goroutine 中调用，见 acp-go-sdk connection.go 的 go func 分发，
	// 阻塞不会卡死通知读循环）。nil 时回退旧 AllowPermission（若也 nil 则一律拒绝）。
	// 实现应返回 PermissionDecision（Allowed + Always + OptionID）；供确认通道（`/permission`）复用。
	RequestPermission func(ctx context.Context, req PermissionRequest) PermissionDecision
	// AllowPermission 旧式同步布尔决策（兼容 demo/测试注入）；RequestPermission 非空时忽略。
	AllowPermission func(ctx context.Context, sessionID, toolTitle string, options int) bool
	// CreateElicitation 处理 agent 的一次结构化提问（elicitation/create：表单或 URL）并
	// **阻塞等待宿主决策**（由 SDK 在独立 goroutine 中调用，阻塞不卡通知读循环）。
	// nil 时一律按取消（cancelled）回应。实现应返回 ElicitationOutcome
	// （accept/decline/cancel；accept 可带用户提交的 content）。
	CreateElicitation func(ctx context.Context, req ElicitationRequest) ElicitationOutcome
	// DialTimeout 限制 initialize/authenticate 握手时长；0 → 调用方 ctx 有截止时间则沿用，
	// 否则用 defaultDialTimeout。runtime 无响应（如等待交互登录）时拨号不会无限挂起。
	DialTimeout time.Duration
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

	// 握手限时：runtime 只起进程不答 initialize（如卡在交互登录）时拨号有界返回，
	// 归类 connection/timeout，避免 REST 创建会话无限挂起。
	dctx := ctx
	if opts.DialTimeout > 0 {
		var cancel context.CancelFunc
		dctx, cancel = context.WithTimeout(ctx, opts.DialTimeout)
		defer cancel()
	} else if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		dctx, cancel = context.WithTimeout(ctx, defaultDialTimeout)
		defer cancel()
	}

	impl := acpsdk.Implementation{
		Name:    firstNonEmpty(opts.ClientName, "ark-forge"),
		Version: firstNonEmpty(opts.ClientVersion, "dev-m0"),
	}
	// 宿主能力声明：本层实现了 elicitation（表单/URL）与 session/request_permission 等交互通道，
	// 声明后 agent 才可能在初始化阶段得知可用交互（opencode/dsh 等不支持的 agent 会忽略，
	// 不影响既有流程；elicitation 为 unstable 能力，随 SDK 演进集中在此收敛）。
	clientCaps := acpsdk.ClientCapabilities{
		Elicitation: &acpsdk.ElicitationCapabilities{
			Form: &acpsdk.ElicitationFormCapabilities{},
			Url:  &acpsdk.ElicitationUrlCapabilities{},
		},
	}
	initResp, err := conn.Initialize(dctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: clientCaps,
		ClientInfo:         &impl,
	})
	if err != nil {
		_ = t.Close()
		if errors.Is(dctx.Err(), context.DeadlineExceeded) {
			return nil, NewError(ClassConnection, "timeout", "runtime 未在握手限期内响应 initialize", dctx.Err())
		}
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
		if _, err := conn.Authenticate(dctx, acpsdk.AuthenticateRequest{MethodId: agentIDs[0]}); err != nil {
			_ = t.Close()
			if errors.Is(dctx.Err(), context.DeadlineExceeded) {
				return nil, NewError(ClassConnection, "timeout", "runtime 未在握手限期内完成鉴权", dctx.Err())
			}
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
	var d PermissionDecision
	switch {
	case h.opts.RequestPermission != nil:
		// 现代确认通道：阻塞等待宿主决策（含 once/always + 会话级记忆）。
		d = h.opts.RequestPermission(ctx, permissionRequestFromSDK(p))
	case h.opts.AllowPermission != nil:
		// 旧式同步布尔决策（demo/测试注入）：放行即选第一个选项。
		allowed := h.opts.AllowPermission(ctx, string(p.SessionId), firstNonEmptyPtr(p.ToolCall.Title), len(p.Options))
		d = PermissionDecision{Allowed: allowed}
	default:
		// 两者皆 nil → 一律拒绝。
		d = PermissionDecision{}
	}
	if h.opts.OnEvent != nil {
		h.opts.OnEvent(Event{
			Kind:      KindPermission,
			SessionID: string(p.SessionId),
			Payload: PermissionInfo{SessionID: string(p.SessionId),
				Title: firstNonEmptyPtr(p.ToolCall.Title), Allowed: d.Allowed},
		})
	}
	return permissionResponseFromDecision(d, p.Options), nil
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
			add(KindUserMessage, MessageDelta{Text: txt, MessageID: idOf(u.UserMessageChunk.MessageId)})
		}
	case u.AgentMessageChunk != nil:
		if txt := textOf(u.AgentMessageChunk.Content); txt != "" {
			add(KindMessage, MessageDelta{Text: txt, MessageID: idOf(u.AgentMessageChunk.MessageId)})
		}
	case u.AgentThoughtChunk != nil:
		if txt := textOf(u.AgentThoughtChunk.Content); txt != "" {
			add(KindThought, ThoughtDelta{Text: txt, MessageID: idOf(u.AgentThoughtChunk.MessageId)})
		}
	case u.ToolCall != nil:
		tc := u.ToolCall
		add(KindToolCall, ToolCallInfo{
			ID:     string(tc.ToolCallId),
			Title:  tc.Title,
			Status: string(tc.Status),
			Kind:   string(tc.Kind),
			Input:  tc.RawInput,
			Output: tc.RawOutput,
		})
	case u.ToolCallUpdate != nil:
		tu := u.ToolCallUpdate
		status := ""
		if tu.Status != nil {
			status = string(*tu.Status)
		}
		kind := ""
		if tu.Kind != nil {
			kind = string(*tu.Kind)
		}
		add(KindToolUpdate, ToolCallInfo{
			ID:     string(tu.ToolCallId),
			Title:  firstNonEmptyPtr(tu.Title),
			Status: status,
			Kind:   kind,
			Input:  tu.RawInput,
			Output: tu.RawOutput,
		})
	case u.UsageUpdate != nil:
		add(KindUsage, nil)
	default:
		// plan/session 元数据类更新：M0 不建模（前端尚不消费）。
	}
	return evs
}

// idOf 解引用 ACP unstable messageId（nil → 空串，表示 agent 未实现消息分组）。
func idOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
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
