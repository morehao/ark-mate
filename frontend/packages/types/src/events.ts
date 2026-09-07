// 域事件 type 常量（与 backend manager/events.go 一致）。
export const Ev = {
  SessionCreated: 'session.created',
  UserMessage: 'session.user_message',
  Message: 'session.message',
  Thought: 'session.thought',
  ToolCall: 'session.tool_call',
  ToolUpdate: 'session.tool_update',
  Usage: 'session.usage',
  Permission: 'session.permission',
  PermissionRequest: 'session.permission_request',
  ElicitationRequest: 'session.elicitation_request',
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

import type { ElicitationFormSchema, PermissionOption } from './session'

export type EntryKind = 'user' | 'assistant' | 'thought' | 'tool' | 'system' | 'permission' | 'elicitation'

export interface StreamEntry {
  kind: EntryKind
  id: string // 稳定 id（生成/合并时复用）
  text?: string
  msgId?: string // 可选：ACP unstable messageId（决策 v5①；同 msgId 文本段跨工具归并）
  toolId?: string
  title?: string
  status?: string
  tone?: 'ok' | 'err' | 'cancel' | 'info'
  // —— 权限确认卡（kind='permission'）——
  callId?: string // 决策回传锚点（POST /sessions/:id/permission）
  optionKinds?: PermissionOption[] // ACP 可选项（allow/reject × once/always）
  toolKind?: string // 底层工具 kind（bash/read/write…）
  toolCallId?: string
  resolved?: boolean // 已由用户决策（按钮禁用/展示结果）
  decision?: string // resolved 时的决策摘要
  timeoutAt?: string // 服务端超时时刻（ISO）；过期即自动拒绝，UI 据此收敛卡片
  // —— 结构化提问卡（kind='elicitation'）——
  elicitationId?: string // 决策回传锚点（POST /sessions/:id/elicitation）
  mode?: 'form' | 'url' // form=表单渲染 schema；url=引导链接
  message?: string // agent 的提问引导文案
  schema?: ElicitationFormSchema // form 模式字段 schema（UI 据此渲染控件）
  url?: string // url 模式链接
  content?: Record<string, unknown> // accept 时用户提交的内容（结果展示）
}
