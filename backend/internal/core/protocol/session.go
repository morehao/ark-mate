package protocol

import (
	"context"

	"github.com/morehao/ark-mate/internal/core/object/objusage"
)

// PromptPartKind 一次 prompt 里一个内容块的种类（平台值）。
//
// 首版只有 `text`（P2 纯重构刀：行为逐字不变，只把 Prompt 的入参从 string 升成 parts）。
// `image` 由 P3 引入（小图直投 ACP image 块）；`document` **不是独立内容块**——由
// sessionruntime 先把 handle 文本拼进 text part 再下发（见设计 §5.1）。
type PromptPartKind string

const (
	// PromptPartText 文本块（→ ACP TextBlock）。
	PromptPartText PromptPartKind = "text"
	// PromptPartImage 图片块（→ ACP ImageBlock(base64, mime)）；P3 起使用。
	PromptPartImage PromptPartKind = "image"
)

// PromptPart 一次 prompt 里的一个内容块（平台值，跨层传递）。
//
// 平台值只带**引用**（AttachmentID），不带字节/base64：image 的 base64 在 ACP 层用 resolver
// 现取现编，不进事件、不进落库（否则 transcript 会被 base64 撑爆）。
type PromptPart struct {
	Kind PromptPartKind
	// Text 文本块的内容（Kind=text 时有效）。
	Text string
	// AttachmentID 附件 id（Kind=image 时有效；ACP 层按它取字节编 base64）。P3 起使用。
	AttachmentID string
	// MimeType 图片 MIME（Kind=image 时有效）。
	MimeType string
	// Data 图片原始字节（Kind=image 时有效；仅供 ACP 层编 base64）。
	//
	// **transient**：只在「sessionruntime → ACP」这一跳里存活，不进事件、不进落库
	// （平台值不持久化，base64 也不进 transcript——设计 §5.1 的"字节编码决策"）。
	Data []byte
}

// TextParts 把一个纯文本包成单 text part（P2 及之后所有"只有文本"的调用点用它，
// 行为与旧 `Prompt(ctx, text)` 逐字一致）。
func TextParts(text string) []PromptPart {
	return []PromptPart{{Kind: PromptPartText, Text: text}}
}

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
	// Prompt 发一轮；返回 stop reason 与本轮 token 明细（usage 为零值 = 底座未上报）。
	Prompt(ctx context.Context, parts []PromptPart) (stop string, usage objusage.TokenUsage, err error)
	Cancel(ctx context.Context) error
	Close(ctx context.Context) error
	ConfigOptions() []ConfigOption
	SetConfigOption(ctx context.Context, configID, value string) ([]ConfigOption, error)
}
