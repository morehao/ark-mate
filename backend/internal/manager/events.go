package manager

// 域事件类型（manager Publish 进 eventbus 的 type 判别词；agg_id = conversation id）。
// 与设计文档 §4.1 事件模型对齐：token/thought/tool_call/message 归一为「消息类增量 + 终态」。
const (
	// BusSessionCreated 会话建立（含 conversation id）。
	BusSessionCreated = "session.created"
	// BusSessionResumed 进程重启后会话按需恢复（P3 F2：acquire → Load/Resume 原生会话 →
	// 重建投影）。payload = {session_id, agent_id, runtime, reason}。
	BusSessionResumed = "session.resumed"
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
	// BusPermissionResolved 挂起权限终局（用户决策 / 超时自动拒绝 / 会话取消）——前端收敛权限卡的
	// 唯一事实（不依赖前端时钟）。payload = {conversation_id, call_id, outcome(selected|cancelled),
	// reason(user|timeout|cancel), allowed, rejected, option_id, always}。
	BusPermissionResolved = "session.permission_resolved"
	// BusElicitationRequest 结构化提问请求（elicitation/create 挂起态：payload = 表单 schema /
	// URL + message + id；前端据此渲染提问卡，用户提交/放弃经 POST /conversations/:id/elicitation 回传）。
	BusElicitationRequest = "session.elicitation_request"
	// BusElicitationResolved 挂起提问终局（accept/decline/cancel/超时/取消）——前端收敛提问卡的
	// 唯一事实（不依赖前端时钟）。payload = {conversation_id, elicitation_id, action(accept|decline|cancel),
	// reason(user|timeout|cancel)}。
	BusElicitationResolved = "session.elicitation_resolved"
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
	ConversationStatusInProgress  = "in_progress"
	ConversationStatusCompleted   = "completed"
	ConversationStatusFailed      = "failed"
	ConversationStatusCancelled   = "cancelled"
	ConversationStatusInterrupted = "interrupted" // 恢复后残留 in_progress（崩溃/重启中断，无终态）
)

// AgentState 取值。
const (
	AgentStateRunning = "running"
	AgentStateStopped = "stopped"
)
