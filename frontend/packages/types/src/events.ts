// 域事件 type 常量（与 backend manager/events.go 一致）。
export const Ev = {
  SessionCreated: 'session.created',
  UserMessage: 'session.user_message',
  Message: 'session.message',
  Thought: 'session.thought',
  ToolCall: 'session.tool_call',
  ToolUpdate: 'session.tool_update',
  Usage: 'session.usage',
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

export type EntryKind = 'user' | 'assistant' | 'thought' | 'tool' | 'system'

export interface StreamEntry {
  kind: EntryKind
  id: string // 稳定 id（生成/合并时复用）
  text?: string
  toolId?: string
  title?: string
  status?: string
  tone?: 'ok' | 'err' | 'cancel' | 'info'
}
