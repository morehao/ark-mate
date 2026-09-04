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

// Event 是归一化后的 ACP 事件，交给注入的 OnEvent 回调（manager 的 event sink）。
type Event struct {
	Kind      string // 见 Kind* 常量
	SessionID string // ACP session id
	Payload   any    // 见 MessageDelta / ThoughtDelta / ToolCallInfo 等
}

// MessageDelta 消息文本增量。
type MessageDelta struct {
	Text string `json:"text"`
}

// ThoughtDelta 思考文本增量。
type ThoughtDelta struct {
	Text string `json:"text"`
}

// ToolCallInfo 工具调用信息。
type ToolCallInfo struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status,omitempty"`
	Input  any    `json:"input,omitempty"`
}

// PermissionInfo 权限请求（按策略拒绝后仍上抛，供上层留痕）。
type PermissionInfo struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Allowed   bool   `json:"allowed"`
}
