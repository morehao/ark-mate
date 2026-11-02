package acp

import (
	"context"
	"fmt"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"sync"

	acpsdk "github.com/coder/acp-go-sdk"
)

// Session 是 ACP 层会话句柄：绑定所属 Client（连接），提供 prompt/cancel/close。
// 上层（manager）负责把会话事件（acp.Event）转成域事件进 eventbus。
//
// 会话还持有该会话的**配置项快照**（session config options）：模型及其它选项
// 全部由运行时自报，acp 只做归一化与保序更新，不做任何语义猜测。
//
// **不持有命令快照**（available commands）：与配置项不同，命令没有"读当前值"的消费方——
// 平台上的会话重开靠事件重放，而快照只在会话活跃期间存在（空闲/断连后句柄即摘除），
// 拿它做读端点对非活跃会话必然返回空，比事件重放更差。故命令只归一化 + 上抛（见 client.go）。
type Session struct {
	client *Client
	id     acpsdk.SessionId

	mu  sync.RWMutex
	cfg []ConfigOption
}

// ID 返回 ACP session id。
func (s *Session) ID() string { return string(s.id) }

// Client 返回所属连接。
func (s *Session) Client() protocol.Conn { return s.client }

// ConfigOptions 返回该会话当前的配置项快照（副本，调用方可安全持有）。
// 来源：session/new、session/resume、session/load 的响应，或 session/set_config_option
// 的响应，或 agent 主动推送的 config_option_update —— 四者都是**全量**状态。
func (s *Session) ConfigOptions() []ConfigOption {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.cfg) == 0 {
		return nil
	}
	out := make([]ConfigOption, len(s.cfg))
	copy(out, s.cfg)
	return out
}

// ConfigValue 取某 category 的当前值（未命中 → 空串）。
func (s *Session) ConfigValue(category string) string {
	return CurrentConfigValue(s.ConfigOptions(), category)
}

// setConfigOptions 整体替换配置项快照（全量语义；空切片同样覆盖，表示运行时不再提供任何选项）。
func (s *Session) setConfigOptions(opts []ConfigOption) {
	s.mu.Lock()
	s.cfg = opts
	s.mu.Unlock()
}

// Prompt 发起一次 prompt（阻塞至本 run 结束），返回 stop reason。
// 流式输出经 ClientOptions.OnEvent（SessionUpdate → 归一化事件）到达上层。
func (s *Session) Prompt(ctx context.Context, parts []protocol.PromptPart) (string, error) {
	blocks, bErr := s.buildContentBlocks(ctx, parts)
	if bErr != nil {
		return "", bErr
	}
	resp, err := s.client.conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: s.id,
		Prompt:    blocks,
	})
	if err != nil {
		return "", Classify(err)
	}
	return string(resp.StopReason), nil
}

// buildContentBlocks 把平台值 parts 编成 ACP 内容块。
//
// P2 只处理 text（→ TextBlock），行为与旧 `TextBlock(text)` 逐字一致；image（→ ImageBlock）
// 由 P3 引入（届时在此取字节编 base64）。**document 不是内容块**（由 sessionruntime 拼进 text）。
func (s *Session) buildContentBlocks(_ context.Context, parts []protocol.PromptPart) ([]acpsdk.ContentBlock, error) {
	blocks := make([]acpsdk.ContentBlock, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case protocol.PromptPartText, "":
			blocks = append(blocks, acpsdk.TextBlock(p.Text))
		default:
			// P2 阶段不会有其它 kind；出现即上层组装有误，明确报错而不是静默丢弃。
			return nil, fmt.Errorf("acp 会话 Prompt: 未知的内容块种类 %q", p.Kind)
		}
	}
	return blocks, nil
}

// Cancel 取消当前 run（session/cancel notification，非阻塞）。
func (s *Session) Cancel(ctx context.Context) error {
	if err := s.client.conn.Cancel(ctx, acpsdk.CancelNotification{SessionId: s.id}); err != nil {
		return Classify(err)
	}
	return nil
}

// Close 关闭会话（session/close，agent 未声明该能力时静默成功——进程随连接回收）。
// 注意：close 只释放运行时的活跃资源，**不删除**运行时的持久会话记录（协议未定义删除语义，
// 两家 runtime 也都没有 session/delete），故不得用「建会话→close」当探针。
func (s *Session) Close(ctx context.Context) error {
	defer s.client.unregisterSession(s.id)
	if s.client.rawCaps.SessionCapabilities.Close == nil {
		return nil
	}
	if _, err := s.client.conn.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: s.id}); err != nil {
		return Classify(err)
	}
	return nil
}

// SetConfigOption 对本会话设置一个会话配置选项（ACP session/set_config_option），
// 返回设置后的**全量**配置项状态（协议保证响应携带完整 configOptions）。
//
// configID/value 都是运行时自报的不透明标识：configID 取自 ConfigOption.ID，
// value 取自 ConfigOptionValue.Value，**不得**自行构造或解析。
// agent 拒绝该值时会返回错误（历史实测：dsh 为 invalidParams + 文本，另一底座（已下线）
// 为 invalidParams + data），
// 该错误即「这个取值不可用」的权威判据。
func (s *Session) SetConfigOption(ctx context.Context, configID, value string) ([]ConfigOption, error) {
	resp, err := s.client.conn.SetSessionConfigOption(ctx, acpsdk.SetSessionConfigOptionRequest{
		ValueId: &acpsdk.SetSessionConfigOptionValueId{
			SessionId: s.id,
			ConfigId:  acpsdk.SessionConfigId(configID),
			Value:     acpsdk.SessionConfigValueId(value),
		},
	})
	if err != nil {
		return nil, Classify(err)
	}
	opts := configOptionsFromSDK(resp.ConfigOptions)
	s.setConfigOptions(opts)
	return opts, nil
}

// NewSession 创建 fresh session（对应 ACP session/new），并捕获其配置项（模型目录来源之一）。
func (c *Client) NewSession(ctx context.Context, opt protocol.SessionOptions) (protocol.Session, error) {
	resp, err := c.conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd:                   opt.Cwd,
		AdditionalDirectories: opt.AdditionalDirectories,
		McpServers:            toAcpMcpServers(opt.McpServers),
	})
	if err != nil {
		return nil, Classify(err)
	}
	s := &Session{client: c, id: resp.SessionId}
	s.setConfigOptions(configOptionsFromSDK(resp.ConfigOptions))
	c.registerSession(s)
	return s, nil
}

// LoadSession 载入既有会话。仅当 agent 声明 loadSession 时可用，否则返回 ClassUnsupported
// （dsh 即为该情形，见 docs/dsh-acp-adaptation.md §2）。
func (c *Client) LoadSession(ctx context.Context, sessionID string, opt protocol.SessionOptions) (protocol.Session, error) {
	if !c.rawCaps.LoadSession {
		return nil, NewError(ClassUnsupported, "load_session_unsupported",
			"该 runtime 不支持 session/load（仅 fresh session）", nil)
	}
	resp, err := c.conn.LoadSession(ctx, acpsdk.LoadSessionRequest{
		Cwd:                   opt.Cwd,
		AdditionalDirectories: opt.AdditionalDirectories,
		McpServers:            toAcpMcpServers(opt.McpServers),
		SessionId:             acpsdk.SessionId(sessionID),
	})
	if err != nil {
		return nil, Classify(err)
	}
	s := &Session{client: c, id: acpsdk.SessionId(sessionID)}
	s.setConfigOptions(configOptionsFromSDK(resp.ConfigOptions))
	c.registerSession(s)
	return s, nil
}

// ResumeSession 恢复既有会话。仅当 agent 声明 sessionCapabilities.resume 时可用。
// 响应同样携带配置项，是「不新建会话即读到模型目录」的协议入口。
func (c *Client) ResumeSession(ctx context.Context, sessionID string, opt protocol.SessionOptions) (protocol.Session, error) {
	if c.rawCaps.SessionCapabilities.Resume == nil {
		return nil, NewError(ClassUnsupported, "resume_session_unsupported",
			"该 runtime 不支持 session/resume", nil)
	}
	resp, err := c.conn.ResumeSession(ctx, acpsdk.ResumeSessionRequest{
		Cwd:                   opt.Cwd,
		AdditionalDirectories: opt.AdditionalDirectories,
		McpServers:            toAcpMcpServers(opt.McpServers),
		SessionId:             acpsdk.SessionId(sessionID),
	})
	if err != nil {
		return nil, Classify(err)
	}
	s := &Session{client: c, id: acpsdk.SessionId(sessionID)}
	s.setConfigOptions(configOptionsFromSDK(resp.ConfigOptions))
	c.registerSession(s)
	return s, nil
}

// ListSessions 列出运行时的持久会话（ACP session/list）。仅当 agent 声明该能力时可用。
// 用于冷启动找「最近一条可 resume 的会话」以读取模型目录，避免创建探针会话。
func (c *Client) ListSessions(ctx context.Context, cwd string) ([]SessionInfo, error) {
	if c.rawCaps.SessionCapabilities.List == nil {
		return nil, NewError(ClassUnsupported, "list_sessions_unsupported",
			"该 runtime 不支持 session/list", nil)
	}
	req := acpsdk.ListSessionsRequest{}
	if cwd != "" {
		req.Cwd = &cwd
	}
	resp, err := c.conn.ListSessions(ctx, req)
	if err != nil {
		return nil, Classify(err)
	}
	out := make([]SessionInfo, 0, len(resp.Sessions))
	for _, s := range resp.Sessions {
		out = append(out, SessionInfo{
			ID:                    string(s.SessionId),
			Cwd:                   s.Cwd,
			Title:                 firstNonEmptyPtr(s.Title),
			UpdatedAt:             firstNonEmptyPtr(s.UpdatedAt),
			AdditionalDirectories: s.AdditionalDirectories,
		})
	}
	return out, nil
}

// ——— 会话注册表：把 config_option_update（按 sessionId 推送）路由到对应会话 ———

// sessionRegistry 维护本连接内活跃会话的索引（Client 与 hostClient 共用一个实例）。
type sessionRegistry struct {
	mu   sync.RWMutex
	byID map[string]*Session
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{byID: map[string]*Session{}}
}

func (r *sessionRegistry) put(s *Session) {
	r.mu.Lock()
	r.byID[s.ID()] = s
	r.mu.Unlock()
}

func (r *sessionRegistry) drop(id string) {
	r.mu.Lock()
	delete(r.byID, id)
	r.mu.Unlock()
}

func (r *sessionRegistry) get(id string) *Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byID[id]
}

// registerSession 把新会话登记到连接注册表（幂等覆盖同 id）。
func (c *Client) registerSession(s *Session) {
	if c.reg != nil {
		c.reg.put(s)
	}
}

// unregisterSession 注销会话（Close 时调用；连接整体关闭时无需逐个注销）。
func (c *Client) unregisterSession(id acpsdk.SessionId) {
	if c.reg != nil {
		c.reg.drop(string(id))
	}
}

// Session 按 ACP session id 取本连接内已登记的会话（未登记 → nil）。
func (c *Client) Session(id string) *Session {
	if c.reg == nil {
		return nil
	}
	return c.reg.get(id)
}
