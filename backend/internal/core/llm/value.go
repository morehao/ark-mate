package llm

import (
	"time"

	"github.com/morehao/ark-mate/internal/core/object/objusage"
)

// TokenUsage 是 `objusage.TokenUsage` 的**别名**（定稿：不重复定义同构值）。
//
// 为什么别名而非独立类型：token 明细是跨层共享的值（协议层映射来、持久层落库、传输层回显），
// 只应有一份定义。这里给别名只是让 `llm` 包的调用方以 `llm.TokenUsage` 书写，语义与
// `objusage.TokenUsage` 完全相同、可自由互换。
type TokenUsage = objusage.TokenUsage

// Purpose 一次模型调用的用途标签。它决定选哪个执行器、哪个模型 profile，也是成本归因维度。
//
// 未列出的用途**不许**用空串糊弄：新增用途就新增常量（"未知用途显式存在，不猜"）。
type Purpose string

const (
	// PurposeAgentTurn 会话驱动的 agent 一轮（默认走 Stateful 执行器）。
	PurposeAgentTurn Purpose = "agent_turn"
	// PurposeSummary 摘要/归纳。
	PurposeSummary Purpose = "summary"
	// PurposeJudge 判别/打分（如相关性、毒性、意图分类的输出）。
	PurposeJudge Purpose = "judge"
	// PurposeEmbed 向量化。
	PurposeEmbed Purpose = "embed"
	// PurposeRewrite 改写/润色/翻译。
	PurposeRewrite Purpose = "rewrite"
	// PurposeTitle 会话标题生成（典型高频旁路）。
	PurposeTitle Purpose = "title"
	// PurposeExtract 结构化抽取（从自由文本抽字段/实体）。
	PurposeExtract Purpose = "extract"
)

// ExecutorKind 执行策略的类别。
type ExecutorKind string

const (
	// ExecutorStateful 内部复用/池化 ACP 会话：重、有状态、拿得到工具与多轮能力。
	ExecutorStateful ExecutorKind = "stateful"
	// ExecutorStateless HTTP 直连 provider：轻、无状态、适合高频纯文本调用。
	ExecutorStateless ExecutorKind = "stateless"
)

// Message 一条对话消息（执行器无关的平台值；各执行器自行编成自己的协议形态）。
type Message struct {
	// Role 角色：system / user / assistant（执行器按各自约定映射；空按 user 处理）。
	Role string
	// Text 文本内容。
	Text string
}

// 消息角色常量（供调用方书写；执行器最终仍按自己的协议映射）。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Request 一次模型调用请求。
type Request struct {
	// Purpose 用途（必填；决定默认执行器与模型 profile）。
	Purpose Purpose
	// Provider 目标供应商（空 = 由网关按 Purpose 的 profile 兜底）。
	Provider string
	// Model 目标模型（空 = 由网关按 Purpose 的 profile 兜底）。
	Model string
	// System 系统提示（快捷字段；与 Messages 里的 system 消息并存时由执行器决定合并顺序）。
	System string
	// Messages 对话消息序列。
	Messages []Message
	// Prefer 显式指定执行器（空 = 网关按 Purpose 的 policy 选）。
	Prefer ExecutorKind
}

// Result 一次模型调用的结果。
type Result struct {
	// Text 模型输出正文。
	Text string
	// Usage 本次调用的 token 明细（执行器未上报时为零值，不代表消耗为 0）。
	Usage TokenUsage
	// CostAmount/CostCurrency 本次调用费用（可选；执行器/底座不上报时为零值）。
	CostAmount   float64
	CostCurrency string
}

// 调用状态（`LLMCall.Status` 取值）。
const (
	StatusOK        = "ok"
	StatusError     = "error"
	StatusTimeout   = "timeout"
	StatusCancelled = "cancelled"
)

// LLMCall 一条调用流水（**一次调用 = 一条**；可跨调用累加）。
//
// `SessionID` / `RunID` 可空是明确设计：旁路（无会话）调用两者皆空。会话调用的流水就是
// `SessionID != ""` 那批——聚合时按 `Purpose / Model / Provider / SessionID` 任意切。
type LLMCall struct {
	// ID 平台铸号（如 call_<ULID>；由 Recorder 实现或网关铸造）。
	ID string
	// At 调用发生时刻。
	At time.Time
	// Purpose 用途。
	Purpose Purpose
	// SessionID 关联的平台会话 id（旁路调用为空）。
	SessionID string
	// RunID 关联的轮次 id（旁路调用为空）。
	RunID string
	// Provider / Model 实际使用的供应商与模型。
	Provider string
	Model    string
	// Usage 本次 token 明细。
	Usage TokenUsage
	// CostAmount/CostCurrency 本次费用（可选）。
	CostAmount   float64
	CostCurrency string
	// LatencyMS 本次调用耗时（毫秒）。
	LatencyMS int64
	// Status 结果状态（Status* 常量）。
	Status string
}
