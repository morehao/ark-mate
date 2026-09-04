package manager

// 域事件类型（manager Publish 进 eventbus 的 type 判别词；agg_id = session id）。
// 与设计文档 §4.1 事件模型对齐：token/thought/tool_call/message 归一为「消息类增量 + 终态」。
const (
	// BusSessionCreated 会话建立（含 session id）。
	BusSessionCreated = "session.created"
	// BusUserMessage 用户消息回显。
	BusUserMessage = "session.user_message"
	// BusMessage agent 消息文本增量。
	BusMessage = "session.message"
	// BusThought 思考增量。
	BusThought = "session.thought"
	// BusToolCall 工具调用发起。
	BusToolCall = "session.tool_call"
	// BusToolUpdate 工具调用更新。
	BusToolUpdate = "session.tool_update"
	// BusUsage 用量更新（M0 仅占位）。
	BusUsage = "session.usage"
	// BusTurnEnded 一轮 prompt 正常结束（stop_reason）。
	BusTurnEnded = "turn.ended"
	// BusTurnError 一轮 prompt 失败（含稳定 code/分类，对齐 acl.Class）。
	BusTurnError = "turn.error"
	// BusRunCancelled 一轮 prompt 被取消。
	BusRunCancelled = "run.cancelled"
)

// 会话状态（进程内存储的展示值）。
const (
	SessionStatusInProgress = "in_progress"
	SessionStatusCompleted  = "completed"
	SessionStatusFailed     = "failed"
	SessionStatusCancelled  = "cancelled"
)

// AgentState 取值。
const (
	AgentStateRunning = "running"
	AgentStateStopped = "stopped"
)
