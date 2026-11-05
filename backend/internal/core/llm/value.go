package llm

import (
	"errors"
	"fmt"
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
	// Schema 结构化输出约束（非空 = 要求按此 schema 产出结构化结果）。
	//
	// 类型刻意用 `any` 而非具体 schema 类型，保持本包（L1）不依赖上层；具体的 schema 表达
	// （JSON Schema / 函数调用参数等）由执行器按自己的协议适配。
	//
	// **仅 Stateless 执行器保证**：ACP 协议层不提供强制结构化输出能力（见 StatefulExecutor
	// 注释），Stateful 执行器收到非空 Schema 时不会产出结构化结果（应显式告警/拒绝）。
	Schema any
	// Strict 严格模式：调用失败时返回 error 而非以 Outcome 表达降级/失败。
	//
	// 默认（false）实现"旁路不阻塞主流程"：失败以 Outcome=Failed 表达、不抛业务错误。
	// 置 true 用于"失败必须显式拒绝"的场景（如风控类判别）。
	Strict bool

	// —— 以下为 Stateful 执行器的调用护栏；零值 = 不限制/全量（保持向后兼容）。仅 Stateful 消费 ——

	// MaxSteps 本轮内部会话最多允许的**工具调用步**上限（0 = 不限制）。
	//
	// 语义是近似：底座不回传真实推理步数，平台以可见的 `session.tool_call` 事件数计步
	// （见 llm_stateful.go 与执行计划 §7 G1 结论），**不等于**底座真实推理步。
	MaxSteps int
	// TokenBudget 本轮内部会话累计 token 上限（0 = 不限制）。累计输入+输出超过该值即中止。
	TokenBudget int64
	// Timeout 本轮内部会话的墙钟超时（0 = 不限制，仅靠调用方 ctx）。与 ctx 叠加，谁先到谁生效。
	Timeout time.Duration
	// AllowedSkills 内部会话可用的 skill 白名单（nil = 全量）。L7 会映射成领域 CapabilitySelection。
	AllowedSkills []string
	// AllowedMcps 内部会话可用的 MCP server 白名单（nil = 全量）。
	AllowedMcps []string
	// AllowUnbounded 显式允许"无护栏"的 agent 调用（默认 false）。
	//
	// 默认下，PurposeAgentTurn 且 MaxSteps/Timeout 均为零 → 拒绝（ErrUnboundedAgentTurn），
	// 避免内部会话跑飞。测试或可信内部场合可置 true 放行。
	AllowUnbounded bool

	// SessionID / RunID 关联的平台会话/轮次（可空）。填了 → 该调用的流水属于"会话调用"，
	// 用量聚合据此切分「会话 vs 旁路」。旁路辅助调用留空。这是**关联标签**，不参与执行。
	SessionID string
	RunID     string
}

// Outcome 一次调用的业务结果状态。与 error 分工：error 只表"调用/程序性异常"，
// Outcome 表达"业务上成不成、是否降级"。
type Outcome string

const (
	// OutcomeOK 正常，Text / Structured 可用。
	OutcomeOK Outcome = "ok"
	// OutcomeDegraded 用降级值兜底（有值但不保证质量）。
	OutcomeDegraded Outcome = "degraded"
	// OutcomeFailed 彻底失败，Text / Structured 为空。
	OutcomeFailed Outcome = "failed"
)

// Result 一次模型调用的结果。
type Result struct {
	// Text 模型输出正文。
	Text string
	// Structured 结构化输出（Request.Schema 非空且执行器支持时填充）。
	//
	// 语义是"**已解析并校验过的对象**"，不是原始 JSON 串——解析、校验、修复重试都在执行器内
	// 完成，调用方拿到的即是用得上的值。仅 Stateless 执行器会填充；Stateful 恒为 nil。
	Structured any
	// Usage 本次调用的 token 明细（执行器未上报时为零值，不代表消耗为 0）。
	Usage TokenUsage
	// CostAmount/CostCurrency 本次调用费用（可选；执行器/底座不上报时为零值）。
	CostAmount   float64
	CostCurrency string
	// Outcome 业务结果状态（ok / degraded / failed）。执行器未显式设置时按"无 error 即 ok"兜底。
	Outcome Outcome
}

// 调用状态（`LLMCall.Status` 取值）。
//
// Status 是**流水视角**：ok/error/timeout/cancelled/degraded 描述这次调用本身发生了什么；
// 与 Result.Outcome（业务视角：ok/degraded/failed）在语义上对齐但不完全等价——
// 例如超时既可能是 Status=timeout，也可能被上层归为 Outcome=failed。映射见网关 record。
const (
	StatusOK        = "ok"
	StatusError     = "error"
	StatusTimeout   = "timeout"
	StatusCancelled = "cancelled"
	// StatusDegraded 调用完成但结果被降级兜底（对应 Result.Outcome=Degraded）。
	StatusDegraded = "degraded"
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

// —— 会话执行器（Stateful）的护栏错误。均实现 error，供调用方判别。 ——

// ErrUnboundedAgentTurn 表示 PurposeAgentTurn 调用既无步数上限也无超时，被默认拒绝（防跑飞）。
// 需要无护栏的 agent 调用时置 Request.AllowUnbounded=true。
var ErrUnboundedAgentTurn = errors.New("llm: agent 轮次未设 MaxSteps/Timeout 护栏，默认拒绝（可置 AllowUnbounded 放行）")

// StepExceededError 工具调用步超出 MaxSteps（近似的工具步，见 Request.MaxSteps）。
type StepExceededError struct {
	Limit  int
	Actual int
}

func (e *StepExceededError) Error() string {
	return fmt.Sprintf("llm: 超出工具调用步上限 MaxSteps=%d（实际 %d）", e.Limit, e.Actual)
}

// BudgetExceededError token 预算超限。
type BudgetExceededError struct {
	Kind   string
	Limit  int64
	Actual int64
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf("llm: 超出 %s 预算 %d（实际 %d）", e.Kind, e.Limit, e.Actual)
}
