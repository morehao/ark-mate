package acp

// ——— 载荷类型的转发（P3，2026-10-21）———
//
// 这些类型的事实源在 `internal/core/protocol`（平台值）：域、用例层、执行面都必须认它，
// 而协议包只负责"线与平台值之间的翻译"。这里保留别名，让协议侧的代码继续用 `acp.X`
// 读起来自然——**类型只有一处定义**，别名不新增行为，也不构成第二事实源。
//
// 新代码（尤其是 domain / sessionruntime / runtime）请直接 import `core/protocol`：
// 让域 import 协议包等于把"换底座要改几行"的承诺变成假的（决策 v30 的 G2 判据）。
import "github.com/morehao/ark-mate/internal/core/protocol"

type (
	// Event 见 `protocol.Event`。
	Event = protocol.Event
	// MessageDelta 见 `protocol.MessageDelta`。
	MessageDelta = protocol.MessageDelta
	// ThoughtDelta 见 `protocol.ThoughtDelta`。
	ThoughtDelta = protocol.ThoughtDelta
	// ToolCallInfo 见 `protocol.ToolCallInfo`。
	ToolCallInfo = protocol.ToolCallInfo
	// PermissionInfo 见 `protocol.PermissionInfo`。
	PermissionInfo = protocol.PermissionInfo
	// ConfigOptionsInfo 见 `protocol.ConfigOptionsInfo`。
	ConfigOptionsInfo = protocol.ConfigOptionsInfo
	// CommandsInfo 见 `protocol.CommandsInfo`。
	CommandsInfo = protocol.CommandsInfo
	// PermissionOption 见 `protocol.PermissionOption`。
	PermissionOption = protocol.PermissionOption
	// PermissionRequest 见 `protocol.PermissionRequest`。
	PermissionRequest = protocol.PermissionRequest
	// PermissionDecision 见 `protocol.PermissionDecision`。
	PermissionDecision = protocol.PermissionDecision
	// ElicitationSchema 见 `protocol.ElicitationSchema`。
	ElicitationSchema = protocol.ElicitationSchema
	// ElicitationRequest 见 `protocol.ElicitationRequest`。
	ElicitationRequest = protocol.ElicitationRequest
	// ElicitationOutcome 见 `protocol.ElicitationOutcome`。
	ElicitationOutcome = protocol.ElicitationOutcome
	// ConfigOptionValue 见 `protocol.ConfigOptionValue`。
	ConfigOptionValue = protocol.ConfigOptionValue
	// ConfigOption 见 `protocol.ConfigOption`。
	ConfigOption = protocol.ConfigOption
	// CommandInfo 见 `protocol.CommandInfo`。
	CommandInfo = protocol.CommandInfo
	// McpServer 见 `protocol.McpServer`。
	McpServer = protocol.McpServer
	// SessionInfo 见 `protocol.SessionInfo`。
	SessionInfo = protocol.SessionInfo
	// Capabilities 见 `protocol.Capabilities`（**不含**原始 SDK 载荷：那在 Client 内部）。
	Capabilities = protocol.Capabilities
	// AuthGuidance 见 `protocol.AuthGuidance`。
	AuthGuidance = protocol.AuthGuidance
	// SessionOptions 见 `protocol.SessionOptions`。
	SessionOptions = protocol.SessionOptions
)

// 事件 Kind 与工具状态的转发（与 `protocol` 逐字同值）。
const (
	KindMessage       = protocol.KindMessage
	KindThought       = protocol.KindThought
	KindToolCall      = protocol.KindToolCall
	KindToolUpdate    = protocol.KindToolUpdate
	KindUserMessage   = protocol.KindUserMessage
	KindUsage         = protocol.KindUsage
	KindPermission    = protocol.KindPermission
	KindConfigOptions = protocol.KindConfigOptions
	KindCommands      = protocol.KindCommands

	ToolStatusPending    = protocol.ToolStatusPending
	ToolStatusInProgress = protocol.ToolStatusInProgress
	ToolStatusCompleted  = protocol.ToolStatusCompleted
	ToolStatusFailed     = protocol.ToolStatusFailed

	CategoryModel        = protocol.CategoryModel
	CategoryThoughtLevel = protocol.CategoryThoughtLevel
	CategoryMode         = protocol.CategoryMode
	CategoryModelConfig  = protocol.CategoryModelConfig

	OptionTypeSelect  = protocol.OptionTypeSelect
	OptionTypeBoolean = protocol.OptionTypeBoolean

	ScopeWire      = protocol.ScopeWire
	ScopeInferred  = protocol.ScopeInferred
	ScopeAmbiguous = protocol.ScopeAmbiguous

	OutcomeSelected  = protocol.OutcomeSelected
	OutcomeCancelled = protocol.OutcomeCancelled
)

// FindConfigOption 见 `protocol.FindConfigOption`。
func FindConfigOption(opts []ConfigOption, category string) (ConfigOption, bool) {
	return protocol.FindConfigOption(opts, category)
}

// CurrentConfigValue 见 `protocol.CurrentConfigValue`。
func CurrentConfigValue(opts []ConfigOption, category string) string {
	return protocol.CurrentConfigValue(opts, category)
}

// HasConfigValue 见 `protocol.HasConfigValue`。
func HasConfigValue(opts []ConfigOption, category, value string) bool {
	return protocol.HasConfigValue(opts, category, value)
}

// EffectivePermissionOutcome 见 `protocol.EffectivePermissionOutcome`。
func EffectivePermissionOutcome(d PermissionDecision, opts []PermissionOption) (kind, optionID string) {
	return protocol.EffectivePermissionOutcome(d, opts)
}
