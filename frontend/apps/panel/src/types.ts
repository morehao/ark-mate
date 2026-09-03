// 与 backend/internal/server/handler 契约对齐的类型（REST + WS 帧）。

// REST 信封（gincontext 包装）：成功 code=0；业务错误 code!=0（HTTP 200）。
export interface Envelope<T> {
  code: number
  requestID: string
  msg: string
  data: T
}

export interface ApiError {
  code: number
  msg: string
}

export interface Agent {
  id: string
  name: string
  runtime: string
  state: string // running | stopped
}

export interface Runtime {
  name: string
  version: string
}

export type SessionStatus = "in_progress" | "completed" | "cancelled" | "failed"

export interface SessionSummary {
  id: string
  agent_id: string
  runtime: string
  status: SessionStatus
}

export interface CreateSessionResp {
  id: string
  agent_id: string
  runtime: string
  status: SessionStatus
  event_seq: number // 已发布事件水位：WS attach 续传起点
}

export interface PromptResp {
  id: string
  status: SessionStatus
  event_seq: number
}

// ——— WS 帧 ———

// 客户端 → 服务端
export type WsClientFrame =
  | { action: "attach"; agg_id: string; since_seq?: number | null }
  | { action: "detach"; agg_id: string }
  | { action: "ping" }

// 服务端 → 客户端
export type WsServerFrame =
  | { type: "snapshot"; agg_id: string; event_seq: number; state: Record<string, unknown> }
  | { type: "event"; agg_id: string; event_seq: number; event: BusEvent }
  | { type: "detached"; agg_id: string; reason?: string }
  | { type: "pong" }

export interface BusEvent {
  type: string
  seq: number
  agg_id: string
  version: number
  payload: Record<string, unknown>
}

// ——— 域事件 type 常量（与 backend manager/events.go 一致） ———
export const Ev = {
  SessionCreated: "session.created",
  UserMessage: "session.user_message",
  Message: "session.message",
  Thought: "session.thought",
  ToolCall: "session.tool_call",
  ToolUpdate: "session.tool_update",
  Usage: "session.usage",
  TurnEnded: "turn.ended",
  TurnError: "turn.error",
  RunCancelled: "run.cancelled",
} as const

// ——— 会话运行视图（前端侧拼接的事件模型） ———

export type EntryKind = "user" | "assistant" | "thought" | "tool" | "system"

export interface StreamEntry {
  kind: EntryKind
  id: string // 稳定 id（生成/合并时复用）
  text?: string
  toolId?: string
  title?: string
  status?: string
  tone?: "ok" | "err" | "cancel" | "info"
}
