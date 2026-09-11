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
  // per-agent 默认权限策略自动决策留痕（审计事件）：payload = 决策依据与结论。控制台目前
  // 不渲染它（策略本身走 REST 展示），但它是真实发布的事件，枚举必须与后端 events.go 对齐——
  // 此前漏了这一条，由 archtest 的 TestFrontendEventTypesMatchRegistry 抓出。
  PermissionPolicy: 'session.permission_policy',
  PermissionResolved: 'session.permission_resolved', // 挂起权限终局（用户决策/超时/取消）——前端收敛权限卡的唯一事实
  ElicitationRequest: 'session.elicitation_request',
  ElicitationResolved: 'session.elicitation_resolved', // 挂起提问终局（accept/decline/cancel/超时/取消/agent 收尾 complete）
  // §6 busy input 队列：会话正忙时用户输入入队 / 投递 / 撤下。队列视图只认这三条事件
  // （queued 建项、delivered|cancelled 收敛），不做本地推断。
  InputQueued: 'session.input_queued',
  InputDelivered: 'session.input_delivered',
  InputCancelled: 'session.input_cancelled',
  // 运行时主动推送的会话配置项全量更新（ACP session/config_option_update，如 dsh 在 provider
  // 拓扑变化时推）：payload = {session_id, agent_id, runtime, options: ConfigOption[]}。
  ConfigOptions: 'session.config_options',
  // —— M2 预留（当前后端不发布；契约见设计文档 §4.5）——
  Runtime: 'session.runtime',
  TurnEnded: 'turn.ended',
  TurnError: 'turn.error',
  RunCancelled: 'run.cancelled',
} as const

export type EvType = (typeof Ev)[keyof typeof Ev]

export interface BusEvent {
  type: string
  seq: number
  agg_id: string
  version: number
  payload: Record<string, unknown>
}

// ——— 会话运行视图（前端侧拼接的事件模型） ———

import type { ElicitationFormSchema, PermissionOption } from './conversation'

export type EntryKind = 'user' | 'assistant' | 'thought' | 'tool' | 'system' | 'permission' | 'elicitation'

/**
 * §6 busy input 队列的一条队项（派生视图，不是 wire 契约）。
 *
 * 由 `session.input_queued` 建项、`session.input_delivered` / `session.input_cancelled` 收敛——
 * **只认事件、不做本地推断**：投递与撤下是两条不同的事实，前端不靠「POST 成功」或超时去猜。
 * 刷新/重连靠历史回放重建（服务端重启作废队项时也会补发 cancelled，见 reason=stale）。
 */
export interface QueuedInput {
  inputId: string
  text: string
  /** after_turn | interrupt_then_prompt（入队时不会出现 reject：那是「不排队、直接报错」）。 */
  delivery: string
  /** 入队时正在执行的那一轮（审计用；服务端投递判据是 generation）。 */
  targetTurnId?: string
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
  tone?: 'ok' | 'err' | 'cancel' | 'info'
  // —— 权限确认卡（kind='permission'）——
  callId?: string // 决策回传锚点（POST /conversations/:id/permission）
  optionKinds?: PermissionOption[] // ACP 可选项（allow/reject × once/always）
  toolKind?: string // 底层工具 kind（bash/read/write…）
  toolCallId?: string
  resolved?: boolean // 已由用户决策（按钮禁用/展示结果）
  decision?: string // resolved 时的决策摘要
  timeoutAt?: string // 服务端超时时刻（ISO）；仅做倒计时展示，收敛以 session.permission_resolved 事件为准
  // —— 结构化提问卡（kind='elicitation'）——
  elicitationId?: string // 决策回传锚点（POST /conversations/:id/elicitation）
  mode?: 'form' | 'url' // form=表单渲染 schema；url=引导链接
  message?: string // agent 的提问引导文案
  schema?: ElicitationFormSchema // form 模式字段 schema（UI 据此渲染控件）
  url?: string // url 模式链接
  content?: Record<string, unknown> // accept 时用户提交的内容（结果展示）
  /** 失败时的 runtime stderr 诊断尾巴（A3；错误条目可展开查看，空 = 无采集）。 */
  stderrTail?: string[]
}
