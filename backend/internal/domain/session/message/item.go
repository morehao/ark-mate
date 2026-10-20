// Package message 是会话域的**消息读模型值类型**（叶子包，理由见 `session/queue` 的包注释）。
//
// messages 是**可重建**读模型（events 仍是唯一事实源）；行形状属于域，重建口径也属于域。
package message

import "time"

// 消息角色（与 messages.role 列同值）。
const (
	// RoleUser 用户消息。
	RoleUser = "user"
	// RoleAssistant 助手消息。
	RoleAssistant = "assistant"
)

// Item 一条会话内消息（一张 `messages` 行的完整形状）。
type Item struct {
	// ID 平台铸号 `msg_<26 位 ULID>`。
	ID string
	// SessionID 所属会话。
	SessionID string
	// RunID 所属轮次。
	RunID string
	// Role 消息角色（`RoleUser` / `RoleAssistant`）。
	Role string
	// Text 正文（已折叠的展示文本）。
	Text string
	// EventsJSON 构成这条消息的事件 id 列表（可回放）。
	EventsJSON string
	// ToolDetailsJSON 工具调用详情（前端折叠面板用）。
	ToolDetailsJSON string
	// Ordinal 会话内第几条（1 起、稠密、唯一）。
	Ordinal int64
	// Kind 投影行种类（扩展位；当前恒空）。
	Kind string
	// SeqLo / SeqHi 该消息覆盖的会话内位次区间。
	SeqLo int64
	SeqHi int64
	// CreatedAt 物化时刻。
	CreatedAt time.Time
}
