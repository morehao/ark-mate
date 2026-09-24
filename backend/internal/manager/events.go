package manager

// 域事件类型（manager Publish 进 eventbus 的 type 判别词；agg_id = conversation id）。
// 与设计文档 §4.1 事件模型对齐：token/thought/tool_call/message 归一为「消息类增量 + 终态」。
const (
	// BusSessionCreated 会话建立（含 conversation id）。
	BusSessionCreated = "session.created"
	// BusSessionResumed 进程重启后会话按需恢复（P3 F2：acquire → Load/Resume 原生会话 →
	// 重建投影）。payload = {session_id, agent_id, runtime, reason}；reason=generation（M3-6
	// 轮次边界迁到新能力代际）时另带 capability_fingerprint（换到了哪一代）。
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
	// BusConfigOptions 运行时主动推送的会话配置项全量更新（ACP session/config_option_update；
	// 如 dsh 在 provider 拓扑变化时推）。payload = {session_id, agent_id, runtime, options}
	// （options = acl.ConfigOption 切片：运行时配置项的直投影，如模型目录）。
	BusConfigOptions = "session.config_options"
	// BusCommands 会话可用命令**全量**列表（ACP session/available_commands_update；
	// 如 opencode 会通告它的斜杠命令，dsh 的 ACP 面不含命令故永不产生本事件）。
	// payload = {session_id, agent_id, runtime, commands}
	// （commands = acl.CommandInfo 切片 {name, description?, hint?}，顺序由底座给出、平台不重排）。
	//
	// 语义边界（ADR-17）：这是**发现面**，不是声明面——命令由底座自报、执行只是把它当
	// 普通提示词发出去（ACP 没有独立的执行方法），平台既不拥有这些命令也不做投影/写面。
	// 空列表也会发布（"通告了当前没有命令"要让前端清空既有候选，不能当成"没发生"）。
	BusCommands = "session.commands"
	// BusPermissionRequest 权限请求（待确认挂起态；M2 权限策略位起发布，当前默认拒绝不触发——
	// payload 契约与 use-case 见设计文档 §4.5「权限/确认位（决策 v5④）」）。
	BusPermissionRequest = "session.permission_request"
	// BusPermissionPolicy per-agent 默认权限策略自动决策留痕（M2 #7）：payload =
	// {session_id, agent_id, action(allow|deny), title, kind, allowed, rejected, option_id}。
	BusPermissionPolicy = "session.permission_policy"
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
	// BusInputQueued §6：会话正忙，用户输入已入队等待投递（delivery=after_turn|interrupt_then_prompt）。
	// payload = {session_id, agent_id, input_id, text, delivery, target_turn_id, generation}。
	// 前端据此在输入区上方渲染队列；收敛靠 input_delivered / input_cancelled。
	BusInputQueued = "session.input_queued"
	// BusInputDelivered §6：排队输入已作为新一轮投递出去（payload = {session_id, agent_id,
	// input_id, delivery, reason(turn_finished)}）。其后紧跟该轮的 session.user_message。
	BusInputDelivered = "session.input_delivered"
	// BusInputCancelled §6：排队输入离队但**未被投递**（payload = {session_id, agent_id,
	// input_id, delivery, reason(user_cancel|deliver_failed)}）。与 delivered 分开是刻意的：
	// 「撤下」和「发出去了」是两件不同的事实，复用同一个 type 会让前端只能靠猜。
	BusInputCancelled = "session.input_cancelled"
	// BusRuntime 会话/runtime 连接态事件（phase/pending_confirmations/can_send；M2「会话态与
	// runtime 态分离」起发布，M1 预留——payload 契约见设计文档 §4.5）。
	BusRuntime = "session.runtime"
	// BusTurnEnded 一轮 prompt 正常结束（stop_reason）。
	BusTurnEnded = "turn.ended"
	// BusTurnError 一轮 prompt 失败（含稳定 code/分类，对齐 acl.Class）。
	BusTurnError = "turn.error"
	// BusRunCancelled 一轮 prompt 被取消。
	BusRunCancelled = "run.cancelled"

	// BusAutomationRun 一次 automation 触发的结果（ADR-12：**一个** type 承载全部结果，
	// 含没产生会话的那些）。payload = {automation_id, trigger(scheduled|recovery|manual),
	// scheduled_for, status(claimed|duplicate|overlap|failed|completed|skipped), reason?, conversation_id?}。
	//
	// 为什么不新建 `automation_runs` 表：events 表已具备持久化、按 agg_id 索引、since_seq 回放、
	// WS 推送、transcript 归档的全套能力——新表是把同一套再实现一遍还要多一份保留期策略。
	// 而 `conversations.origin` 列解决的是**反方向**查询（会话→是否自动化产物，逐行渲染不能
	// 靠回放），故两者并存、互不可替代。
	BusAutomationRun = "automation.run"
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
