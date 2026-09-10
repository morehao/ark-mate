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
  PermissionResolved: 'session.permission_resolved', // 挂起权限终局（用户决策/超时/取消）——前端收敛权限卡的唯一事实
  ElicitationRequest: 'session.elicitation_request',
  ElicitationResolved: 'session.elicitation_resolved', // 挂起提问终局（accept/decline/cancel/超时/取消）
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
