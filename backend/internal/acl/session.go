package acl

import (
	"context"

	acpsdk "github.com/coder/acp-go-sdk"
)

// Session 是 ACP 层会话句柄：绑定所属 Client（连接），提供 prompt/cancel/close。
// 上层（manager）负责把会话事件（acl.Event）转成域事件进 eventbus。
type Session struct {
	client *Client
	id     acpsdk.SessionId
}

// ID 返回 ACP session id。
func (s *Session) ID() string { return string(s.id) }

// Client 返回所属连接。
func (s *Session) Client() *Client { return s.client }

// Prompt 发起一次 prompt（阻塞至本 turn 结束），返回 stop reason。
// 流式输出经 ClientOptions.OnEvent（SessionUpdate → 归一化事件）到达上层。
func (s *Session) Prompt(ctx context.Context, text string) (string, error) {
	resp, err := s.client.conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: s.id,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock(text)},
	})
	if err != nil {
		return "", Classify(err)
	}
	return string(resp.StopReason), nil
}

// Cancel 取消当前 turn（session/cancel notification，非阻塞）。
func (s *Session) Cancel(ctx context.Context) error {
	if err := s.client.conn.Cancel(ctx, acpsdk.CancelNotification{SessionId: s.id}); err != nil {
		return Classify(err)
	}
	return nil
}

// Close 关闭会话（session/close）。agent 不支持 close 时静默成功（进程随连接回收）。
func (s *Session) Close(ctx context.Context) error {
	if s.client.caps.Raw.SessionCapabilities.Close == nil {
		return nil
	}
	if _, err := s.client.conn.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: s.id}); err != nil {
		return Classify(err)
	}
	return nil
}

// SessionOptions 用于 new/load/resume 的会话参数（M0 仅 cwd + 附加目录）。
type SessionOptions struct {
	Cwd                   string
	AdditionalDirectories []string
}

// NewSession 创建 fresh session（对应 ACP session/new）。
func (c *Client) NewSession(ctx context.Context, opt SessionOptions) (*Session, error) {
	resp, err := c.conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd:                   opt.Cwd,
		AdditionalDirectories: opt.AdditionalDirectories,
		McpServers:            []acpsdk.McpServer{},
	})
	if err != nil {
		return nil, Classify(err)
	}
	return &Session{client: c, id: resp.SessionId}, nil
}

// LoadSession 载入既有会话。仅当 agent 声明 loadSession 时可用，否则返回 ClassUnsupported
// （deepseek-harness 即为该情形，见 docs/architect-design.md §6.2）。
func (c *Client) LoadSession(ctx context.Context, sessionID string, opt SessionOptions) (*Session, error) {
	if !c.caps.Raw.LoadSession {
		return nil, NewError(ClassUnsupported, "load_session_unsupported",
			"该 runtime 不支持 session/load（仅 fresh session）", nil)
	}
	resp, err := c.conn.LoadSession(ctx, acpsdk.LoadSessionRequest{
		Cwd:                   opt.Cwd,
		AdditionalDirectories: opt.AdditionalDirectories,
		McpServers:            []acpsdk.McpServer{},
		SessionId:             acpsdk.SessionId(sessionID),
	})
	if err != nil {
		return nil, Classify(err)
	}
	_ = resp
	return &Session{client: c, id: acpsdk.SessionId(sessionID)}, nil
}

// ResumeSession 恢复既有会话。仅当 agent 声明 sessionCapabilities.resume 时可用。
func (c *Client) ResumeSession(ctx context.Context, sessionID string, opt SessionOptions) (*Session, error) {
	if c.caps.Raw.SessionCapabilities.Resume == nil {
		return nil, NewError(ClassUnsupported, "resume_session_unsupported",
			"该 runtime 不支持 session/resume", nil)
	}
	if _, err := c.conn.ResumeSession(ctx, acpsdk.ResumeSessionRequest{
		Cwd:                   opt.Cwd,
		AdditionalDirectories: opt.AdditionalDirectories,
		McpServers:            []acpsdk.McpServer{},
		SessionId:             acpsdk.SessionId(sessionID),
	}); err != nil {
		return nil, Classify(err)
	}
	return &Session{client: c, id: acpsdk.SessionId(sessionID)}, nil
}

// ListSessions 列出连接内会话 ID。仅当 agent 声明 sessionCapabilities.list 时可用。
func (c *Client) ListSessions(ctx context.Context) ([]string, error) {
	if c.caps.Raw.SessionCapabilities.List == nil {
		return nil, NewError(ClassUnsupported, "list_sessions_unsupported",
			"该 runtime 不支持 session/list", nil)
	}
	resp, err := c.conn.ListSessions(ctx, acpsdk.ListSessionsRequest{})
	if err != nil {
		return nil, Classify(err)
	}
	out := make([]string, 0, len(resp.Sessions))
	for _, s := range resp.Sessions {
		out = append(out, string(s.SessionId))
	}
	return out, nil
}
