package runtime

import (
	"context"

	"github.com/morehao/ark-forge/internal/acl"
)

// Mode 平台统一权限模式（§5.5 词汇）。opencode/dsh 的原生 mode 映射见 §6.2 ModeMapping
// （待实测回填）；echo 投影无原生模式概念。
type Mode string

const (
	ModeAuto         Mode = "auto"
	ModeSmartApprove Mode = "smart_approve"
	ModeApprove      Mode = "approve"
	ModeChat         Mode = "chat"
)

// ModelConfig 平台侧模型选择。Provider 为空 = 跟随 provider 档案/env 注入（§5.5 模型面）。
type ModelConfig struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
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
// 单会话上的执行面——ID/ProviderSessionID/Prompt/Resume/Apply* 均操作该会话；事件流由连接层
// OnEvent 承载，故 Prompt 返回 stop reason 而非事件 channel。
//
// 契约：实现方须使 ID/ManagesOwnContext/Capabilities 与底层会话自洽；对不支持的操作（如某
// runtime 无 Resume/ApplyMode/ApplyModel）返回明确错误，不得静默吞掉。
type Runtime interface {
	// ID 返回投影 id（对应 Spec.ID：echo/opencode/deepseekharness）。
	ID() string
	// ManagesOwnContext 后端自管上下文/工具（§6.2 三投影均 true）。
	ManagesOwnContext() bool
	// Capabilities 返回底层连接握手到的能力（工具/MCP/权限面）。
	Capabilities() acl.Capabilities
	// ProviderSessionID 返回底层 ACP 会话 id（多轮续跑与诊断定位用）。
	ProviderSessionID() string
	// Prompt 发送一条用户消息并驱动到一轮停点，返回 stop reason（如 "end_turn"）。
	Prompt(ctx context.Context, req PromptRequest) (string, error)
	// ApplyMode 应用统一权限模式；runtime 无原生映射时返回不支持错误。
	ApplyMode(ctx context.Context, mode Mode) error
	// ApplyModel 切换模型；runtime 无模型概念/不可切换时返回不支持错误。
	ApplyModel(ctx context.Context, m ModelConfig) error
	// Resume 使 Runtime 回到指定会话（ProviderSessionID）；无持久会话的 runtime 返回不支持错误。
	Resume(ctx context.Context, sessionID string) error
}
