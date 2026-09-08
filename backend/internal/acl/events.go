package acl

// 归一化事件 Kind（ACP notification → acl.Event，manager 再转域事件进 eventbus）。
const (
	// KindMessage 会话消息增量（agent 回复文本 chunk）。
	KindMessage = "message"
	// KindThought 思考过程 chunk（agent 内部推理）。
	KindThought = "thought"
	// KindToolCall 工具调用发起。
	KindToolCall = "tool_call"
	// KindToolUpdate 工具调用状态/结果更新。
	KindToolUpdate = "tool_update"
	// KindUserMessage 用户消息回显 chunk。
	KindUserMessage = "user_message"
	// KindUsage 用量更新。
	KindUsage = "usage"
	// KindPermission 权限请求（acl 决策后上抛供上层留痕）。
	KindPermission = "permission"
)

// 工具调用状态（对齐 ACP ToolCallStatus 四态；空串 = 仅发起未带状态）。
const (
	ToolStatusPending    = "pending"
	ToolStatusInProgress = "in_progress"
	ToolStatusCompleted  = "completed"
	ToolStatusFailed     = "failed"
)

// Event 是归一化后的 ACP 事件，交给注入的 OnEvent 回调（manager 的 event sink）。
type Event struct {
	Kind      string // 见 Kind* 常量
	SessionID string // ACP session id
	Payload   any    // 见 MessageDelta / ThoughtDelta / ToolCallInfo 等
}

// MessageDelta 消息文本增量。
type MessageDelta struct {
	Text string `json:"text"`
	// MessageID 该 chunk 所属逻辑消息（ACP unstable messageId，uuid；agent 未实现时为空）。
	// 同 MessageID 的 chunk 属于同一条消息：用于跨工具穿插的消息段归并/回放/会话导入
	// （决策 v5①，透传字段向后兼容，为空时前端回退「同 kind 尾拼接」）。
	MessageID string `json:"message_id,omitempty"`
}

// ThoughtDelta 思考文本增量。
type ThoughtDelta struct {
	Text      string `json:"text"`
	MessageID string `json:"message_id,omitempty"`
}

// ToolCallInfo 工具调用信息。
type ToolCallInfo struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status,omitempty"` // ToolStatus* 或 runtime 原样值（未归一）
	Kind   string `json:"kind,omitempty"`   // ACP ToolKind（read/edit/execute/…），可空
	Input  any    `json:"input,omitempty"`  // 调用入参（rawInput）
	Output any    `json:"output,omitempty"` // 调用结果（rawOutput；仅 ToolCallUpdate 携带时非空）
}

// PermissionInfo 权限请求决策留痕（每次决策后上抛，供上层留痕/审计）。
type PermissionInfo struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Allowed   bool   `json:"allowed"`
	Rejected  bool   `json:"rejected,omitempty"` // 显式拒绝（区别于取消/超时）
	OptionID  string `json:"option_id,omitempty"`
	Always    bool   `json:"always,omitempty"`
}
