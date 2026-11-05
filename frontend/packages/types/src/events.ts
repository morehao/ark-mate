import type { UnixMillis } from './time'

// 域事件 type 常量（与 backend manager/events.go 一致）。
export const Ev = {
  SessionCreated: 'session.created',
  // 进程重启 / 跨 runtime 后会话按需恢复（P3 会话可移植）：payload.reason = restart（原生会话
  // 续接）| handoff（历史摘要重灌）；payload.runtime = 实际承载 runtime。
  SessionResumed: 'session.resumed',
  UserMessage: 'session.user_message',
  Message: 'session.message',
  Thought: 'session.thought',
  ToolCall: 'session.tool_call',
  ToolUpdate: 'session.tool_update',
  Usage: 'session.usage',
  Permission: 'session.permission',
  PermissionRequest: 'session.permission_request',
  // 默认权限策略自动决策留痕（审计事件）：payload = 决策依据与结论。控制台目前
  // 不渲染它（策略本身不加限定），但它是真实发布的事件，枚举必须与后端 events.go 对齐——
  // 此前漏了这一条，由 archtest 的 TestFrontendEventTypesMatchRegistry 抓出。
  PermissionPolicy: 'session.permission_policy',
  PermissionResolved: 'session.permission_resolved', // 挂起权限终局（用户决策/超时/取消）——前端收敛权限卡的唯一事实
  ElicitationRequest: 'session.elicitation_request',
  ElicitationResolved: 'session.elicitation_resolved', // 挂起提问终局（accept/decline/cancel/超时/取消/底座收尾 complete）
  // §6 busy input 队列：会话正忙时用户输入入队 / 投递 / 撤下。队列视图只认这三条事件
  // （queued 建项、delivered|cancelled 收敛），不做本地推断。
  InputQueued: 'session.input_queued',
  InputDelivered: 'session.input_delivered',
  InputCancelled: 'session.input_cancelled',
  // 运行时主动推送的会话配置项全量更新（ACP session/config_option_update，如 dsh 在 provider
  // 拓扑变化时推）：payload = {session_id, runtime, options: ConfigOption[]}。
  ConfigOptions: 'session.config_options',
  // 底座自报的会话可用命令**全量**列表（ACP session/available_commands_update；**这是可选能力**：
  // ACP 面不含命令的底座永远收不到本事件——那是底座事实，文案不得说成"你还没配命令"）。
  // payload = {session_id, runtime, commands: CommandInfo[]}；空数组表示"通告了当前没有命令"。
  // 语义边界：这是**发现面**，不是平台声明的能力——执行只是把命令行当普通提示词发出去（ADR-17）。
  Commands: 'session.commands',
  // 会话/runtime 连接态（phase/pending_confirmations/can_send）。
  Runtime: 'session.runtime',
  // 轮次生命周期（v22 起 `run.started` 与三个终态都带 `run_id` = 该轮身份 `run_<ULID>`）。
  // 前端不把 `run.started` 渲染成条目（界面时间线读的是会话事件流，轮次明细另走
  // `GET /sessions/:id/runs`），但它在契约里：帧上出现未知 type 时前端只能丢弃，
  // 而"这一轮开始了"是能对齐 `runs` 行的唯一事实。
  RunStarted: 'run.started',
  // 一轮问答的**过程状态**（后端唯一铸造点 = domain/session 的 `RunStatusPayload`）。
  // payload = {session_id, run_id, status, phase?, detail?, tool_call_id?, tool_name?,
  // waiting_for?, retry?, at}。`status` 的 10 个活体取值见本文件下方的 `RunStatus`；
  // 恢复路径另有一支 `interrupted` 作收口（实时路径永不发它，ADR-8）。
  RunStatus: 'run.status',
  RunEnded: 'run.ended',
  RunError: 'run.error',
  RunCancelled: 'run.cancelled',
  // 一次 automation 触发的结果（含没产生会话的那些：duplicate/overlap/skipped）。
  // payload = {automation_id, trigger, scheduled_for, status, reason?, session_id?}。
  AutomationRun: 'automation.run',
} as const

export type EvType = (typeof Ev)[keyof typeof Ev]

/**
 * `run.status` 的取值（ADR-7：按本仓的 ACP 观测面收窄到 10 个**活体**状态）。
 *
 * 为什么不是参照产品的 13 态：那是它自己 CLI 状态机的投影，而本仓观测面是 ACP——声明一个
 * 观测不到的取值，会让界面在两种状态下显示同一个东西。`interrupted` 是**例外**：它不是活体
 * 阶段，而是**恢复路径**的收口取值（进程被杀、日志里没有终态），实时路径永不发它。
 */
export type RunStatus =
  | 'preparing'
  | 'model_requesting'
  | 'model_streaming'
  | 'tool_executing'
  | 'waiting_for_permission'
  | 'waiting_for_user'
  | 'retrying'
  | 'compacting'
  | 'cancelling'
  | 'idle'
  | 'interrupted'

/** `run.status` 的载荷（`Ev.RunStatus`；后端唯一铸造点是 domain/session 的 `RunStatusPayload`）。 */
export interface RunStatusPayload {
  session_id: string
  run_id: string
  status: RunStatus
  /** 底座给的更细阶段名，原样透传（平台不翻译、不重排；缺失时 status 仍可用）。 */
  phase?: string
  /** 人话旁白（界面头栏用；平台自己的判定产物）。 */
  detail?: string
  tool_call_id?: string
  tool_name?: string
  /** permission | elicitation | model | tool。 */
  waiting_for?: string
  retry?: { attempt?: number; reason?: string } & Record<string, unknown>
  /** Unix 毫秒（历史载荷可能是 RFC3339 字符串，读侧用 toMillis 归一）。 */
  at?: UnixMillis
  /** 仅恢复收口带：`restart`。 */
  reason?: string
}

export interface BusEvent {
  // id 事件行身份（`evt_<26 位 ULID>`，v17 起）。
  //
  // **可选**是有语义的，不是"还没补上"：只有**落库/进 transcript 的**事件才有身份，而
  // `automation.run` 这类只走实时通道的事件没有——于是"帧上有 id ⟺ 它在 durable 日志里查得到"
  // 这条不变式成立，客户端拿它去引用某一条事件（书签/诊断）才是安全的。
  id?: string
  type: string
  seq: number
  agg_id: string
  version: number
  payload: Record<string, unknown>
}

// ——— 会话运行视图（前端侧拼接的事件模型） ———

import type { AttachmentMeta, ElicitationFormSchema, PermissionOption } from './session'

export type EntryKind = 'user' | 'assistant' | 'thought' | 'tool' | 'system' | 'permission' | 'elicitation'

/**
 * §6 busy input 队列的一条队项（派生视图，不是 wire 契约）。
 *
 * 由 `session.input_queued` 建项、`session.input_delivered` / `session.input_cancelled` 收敛——
 * **只认事件、不做本地推断**：投递与撤下是两条不同的事实，前端不靠「POST 成功」或超时去猜。
 * 刷新/重连靠历史回放重建（服务端重启作废队项时也会补发 cancelled，见 reason=stale）。
 */
/**
 * 排队项**离队原因**（`session.input_cancelled` 的 `reason`）。
 *
 * 与后端 `domain/session/inputqueue.go` 的 `QueuedInputReasons` **逐条相等**（由 archtest 的
 * `TestQueuedInputReasonVocabularyIsClosed` 钉住）。为什么必须闭合：前端**只认事件**、不做本地
 * 推断——后端多发一个前端不认的值，那一行就会永远挂在队列里（或者被渲染成"未知原因"）。
 */
export type QueuedInputReason =
  | 'run_finished' // 因上一轮结束而投递（配合 input_delivered）
  | 'user_cancel' // 用户显式撤下
  | 'deliver_failed' // 投递时启动新一轮失败
  | 'stale' // 进程重启：等待的那一轮已随进程消失
  | 'user_edit' // 用户把队项取回编辑（ADR-10 的第三个动作）

export interface QueuedInput {
  inputId: string
  text: string
  /** after_run | interrupt_then_prompt（入队时不会出现 reject：那是「不排队、直接报错」）。 */
  delivery: string
  /** 入队时正在执行的那一轮（审计用；服务端投递判据是 generation）。 */
  targetRunId?: string
  /** 入队时会话执行代际：服务端投递前比对，不等即作废（旧队项绝不灌进重建后的上下文）。 */
  generation?: number
  seq?: number
}

export interface StreamEntry {
  kind: EntryKind
  id: string // 稳定 id（生成/合并时复用）
  /** 该条目首帧事件的全局 seq（搜索锚点跳转用；合并/续传保留最早值）。 */
  seq?: number
  text?: string
  msgId?: string // 可选：ACP unstable messageId（决策 v5①；同 msgId 文本段跨工具归并）
  toolId?: string
  title?: string
  status?: string
  /**
   * 工具 kind（ACP ToolKind；权限请求与工具条目共用这个字段）。
   * 工具条目用它选择图标/语义；为空时前端回退到 title 推断。
   */
  toolKind?: string
  /** 工具原始入参（ACP rawInput；可选，用于单行工具的对象与详情）。 */
  input?: unknown
  /** 工具原始结果（ACP rawOutput；可选，用于展开详情）。 */
  output?: unknown
  tone?: 'ok' | 'err' | 'cancel' | 'info'
  /**
   * 轮末收口原因（`run.ended.stop_reason`，D9）：**不进正文**（`end_turn` 是协议词），
   * 由系统条目的「本轮用量」悬浮展示成一行「结束原因」。
   */
  stopReason?: string
  // —— 权限确认卡（kind='permission'）——
  callId?: string // 决策回传锚点（POST /sessions/:id/permission）
  optionKinds?: PermissionOption[] // ACP 可选项（allow/reject × once/always）
  toolCallId?: string
  resolved?: boolean // 已由用户决策（按钮禁用/展示结果）
  decision?: string // resolved 时的决策摘要
  timeoutAt?: UnixMillis // Unix 毫秒；仅做倒计时展示，收敛以 session.permission_resolved 事件为准
  // —— 结构化提问卡（kind='elicitation'）——
  elicitationId?: string // 决策回传锚点（POST /sessions/:id/elicitation）
  mode?: 'form' | 'url' // form=表单渲染 schema；url=引导链接
  message?: string // 底座的提问引导文案
  schema?: ElicitationFormSchema // form 模式字段 schema（UI 据此渲染控件）
  url?: string // url 模式链接
  content?: Record<string, unknown> // accept 时用户提交的内容（结果展示）
  /**
   * 已**超时但问题仍可回答**（ADR-11 的 `action:"pending"`）。
   *
   * 与 `resolved` 互斥：`resolved` 说"这张卡没了"，它说"那一轮不等了，但你想答还是能答"。
   * 卡片据此保留按钮 + 显示一句「仍可回答」；回答时请求体带 `late:true`，
   * 那条回答**作为新一轮**的用户消息进去（不是塞回已经收尾的旧轮）。
   */
  pendingLate?: boolean
  /**
   * 工具失败的**平台侧归类**（服务端按底座的签名表给，S4）。
   *
   * `sandbox_denied`（被沙箱边界拒了，可以引导升权）与 `sandbox_unavailable`（宿主根本没有可用的
   * 沙箱后端，再点一次也一样失败）的处置**相反**，所以必须是两个值。
   *
   * 客户端**不**从 `status` 推断它：被沙箱拒绝的那一帧 `status` 就是 `completed`。
   */
  failReason?: string
  /** 与 `failReason` 配套的稳定业务码（可选；服务端不给就没有）。 */
  bizCode?: string
  /** 失败时的 runtime stderr 诊断尾巴（A3；错误条目可展开查看，空 = 无采集）。 */
  stderrTail?: string[]
  /**
   * user 消息携带的附件元数据（v32；`session.user_message.attachments[]`）。
   * 只元数据（id/name/mime_type/size_bytes/kind），气泡据此画 chip；图片缩略/文档预览走
   * `GET /attachments/:id/content`。
   */
  attachments?: AttachmentMeta[]
}
