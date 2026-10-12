package runtime

import (
	"context"

	"github.com/morehao/ark-mate/pkg/acl"
)

// Mode 平台统一权限模式（§5.5 词汇）。opencode/dsh 的原生 mode 映射见 §6.2 ModeMapping
// （待实测回填）。
type Mode string

const (
	ModeAuto         Mode = "auto"
	ModeSmartApprove Mode = "smart_approve"
	ModeApprove      Mode = "approve"
	ModeChat         Mode = "chat"
)

// ModelConfig 平台侧模型选择（只含模型这一项）。
//
// 重要：Model 是**运行时的不透明值**，必须原样取自运行时广告的会话配置项（category=model）的
// ConfigOptionValue.Value —— dsh 是 `JSON.stringify([provider, model])`，opencode 是
// `provider/model`。平台不得拼接或解析该串，也不得自行编造候选：可用性以运行时
// set_config_option 的成败为唯一判据。
//
// 平台**不提供**思考档位（thought_level）选择面：运行时若给出该选项，只作为运行时状态原样
// 投影给前端，不参与平台侧下发（少一个「取值随模型变化」的耦合面）。
type ModelConfig struct {
	Provider string `json:"provider,omitempty"` // 平台 provider 档案 id（仅展示/留痕）
	Model    string `json:"model,omitempty"`    // 不透明模型值；空 = 用运行时默认，不下发设置
}

// PromptRequest 一次 Prompt 的入参（§4.2 PromptRequest）。本版「会话即连接」：Text 为已接入
// 字段；McpServers/HandoffMemo 为规划占位（当前 MCP 注入在 acl 会话层 SessionOptions 完成，
// handoff 语义文档未定稿），先留签名避免将来破坏接口。
type PromptRequest struct {
	Text        string
	McpServers  []acl.McpServer // 规划：本轮注入在 acl 会话层完成
	HandoffMemo string          // 规划：handoff（未定稿）
}

// Runtime 描述一个「活着的后端会话」的可执行投影（§4.2 的「会话边界版」）。
//
// 与 §4.2 backend-wide Runtime 的偏差（文档化，见 design doc §15）：本平台一个对话即一条活跃
// ACP 连接（连接复用/回收与权限/elicitation 决策钩子由 Orchestrator 管理），Runtime 是编排器在
// 单会话上的执行面——ID/RuntimeSessionID/Prompt/Resume/Apply* 均操作该会话；事件流由连接层
// OnEvent 承载，故 Prompt 返回 stop reason 而非事件 channel。
//
// 契约：实现方须使 ID/ManagesOwnContext/Capabilities 与底层会话自洽；对不支持的操作（如某
// runtime 无 Resume/ApplyMode/ApplyModel）返回明确错误，不得静默吞掉。
type Runtime interface {
	// ID 返回投影 id（对应 Spec.ID：opencode/dsh）。
	ID() string
	// ManagesOwnContext 后端自管上下文/工具（§6.2 三投影均 true）。
	ManagesOwnContext() bool
	// Capabilities 返回底层连接握手到的能力（工具/MCP/权限面）。
	Capabilities() acl.Capabilities
	// RuntimeSessionID 返回底层 ACP 会话 id（多轮续跑与诊断定位用）。
	RuntimeSessionID() string
	// Cancel 取消本会话进行中的 run（会话级执行原语；best-effort，见 acl.Session.Cancel）。
	// 连接生命周期仍归 Orchestrator（§4.2 偏差），本方法只作用于会话执行面。
	Cancel(ctx context.Context) error
	// Prompt 发送一条用户消息并驱动到一轮停点，返回 stop reason（如 "end_turn"）。
	Prompt(ctx context.Context, req PromptRequest) (string, error)
	// ApplyMode 应用统一权限模式；runtime 无原生映射时返回不支持错误。
	ApplyMode(ctx context.Context, mode Mode) error
	// ApplyModel 切换模型，返回应用后的**全量**会话配置项状态。
	// runtime 无模型配置项时返回不支持错误；值非法/不可用时返回运行时原始错误（不得吞）。
	ApplyModel(ctx context.Context, m ModelConfig) ([]acl.ConfigOption, error)
	// ConfigOptions 返回该会话当前的会话配置项（运行时自报：模型目录及其它选项，空 = 未提供）。
	ConfigOptions() []acl.ConfigOption
	// Resume 使 Runtime 回到指定会话（RuntimeSessionID）；无持久会话的 runtime 返回不支持错误。
	Resume(ctx context.Context, sessionID string) error
}
