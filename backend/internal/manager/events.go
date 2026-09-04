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
	// BusUsage 用量更新（turn 结束随 PromptResponse.usage 携带明细；无数据时仅占位）。
	BusUsage = "session.usage"
	// BusPermission 权限决策留痕（acl 决策后上抛：payload = acl.PermissionInfo）。
	BusPermission = "session.permission"
	// BusPermissionRequest 权限请求（待确认挂起态；M2 权限策略位起发布，当前默认拒绝不触发——
	// payload 契约与 use-case 见设计文档 §4.5「权限/确认位（决策 v5④）」）。
	BusPermissionRequest = "session.permission_request"
	// BusRuntime 会话/runtime 连接态事件（phase/pending_confirmations/can_send；M2「会话态与
	// runtime 态分离」起发布，M1 预留——payload 契约见设计文档 §4.5）。
	BusRuntime = "session.runtime"
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
