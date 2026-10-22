package protocol

import "context"

// SessionOptions 打开/恢复一个底座会话的参数（平台值）。
// McpServers 为平台选出的 MCP 注入子集（nil/空 = 不下发；由能力数据面提供）。
type SessionOptions struct {
	Cwd                   string
	AdditionalDirectories []string
	McpServers            []McpServer
}

// Conn 是一次**已拨活**的底座连接（协议侧的 `*acp.Client` 结构上实现它）。
//
// 内圈（域 / 执行面 / 契约包）只认这一个接口：连接怎么建、消息怎么编解码都属于协议绑定，
// 而"在这个连接上开会话、读能力、取 stderr 尾巴"是平台自己的动作。
type Conn interface {
	// Capabilities 返回握手期冻结的能力断言。
	Capabilities() Capabilities
	// StderrTail 返回子进程 stderr 的最近若干行（诊断尾巴）。
	StderrTail() []string
	// NewSession 新建会话；LoadSession 按原生 id 装载；ResumeSession 按原生 id 恢复。
	NewSession(ctx context.Context, opt SessionOptions) (Session, error)
	LoadSession(ctx context.Context, sessionID string, opt SessionOptions) (Session, error)
	ResumeSession(ctx context.Context, sessionID string, opt SessionOptions) (Session, error)
	// ListSessions 列出该 cwd 下的底座会话（能力不支持时返回错误，由调用方判档）。
	ListSessions(ctx context.Context, cwd string) ([]SessionInfo, error)
	// Close 断开连接（不影响底座侧的会话记录）。
	Close() error
}

// Session 是一次已打开的底座会话（协议侧的 `*acp.Session` 结构上实现它）。
//
// 它是平台对"会话"的最小执行面：发一条消息、取消、关闭、读写配置项。
// 会话记录的持久化不在这里（那是平台自己的 `sessions` 表）。
type Session interface {
	ID() string
	Client() Conn
	Prompt(ctx context.Context, text string) (string, error)
	Cancel(ctx context.Context) error
	Close(ctx context.Context) error
	ConfigOptions() []ConfigOption
	SetConfigOption(ctx context.Context, configID, value string) ([]ConfigOption, error)
}
