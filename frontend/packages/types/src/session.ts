export type SessionStatus = 'in_progress' | 'completed' | 'cancelled' | 'failed'

export interface SessionSummary {
  id: string
  agent_id: string
  runtime: string
  status: SessionStatus
  title: string // 会话标题（服务端由首条用户消息推导；可 PATCH 覆盖）
  pinned: boolean
  archived: boolean
  created_at: string // ISO 时间戳（UTC RFC3339）
  updated_at: string
}

export interface SessionDetail extends SessionSummary {
  pending: PermissionResp[] // 挂起权限请求（确认通道待决项）
}

/** 会话元数据子集更新（nil/零值不更新）。 */
export interface SessionPatch {
  title?: string
  pinned?: boolean
  archived?: boolean
}

// ——— 权限确认通道（ACP session/request_permission ↔ 用户决策） ———

export type PermissionOptionKind = 'allow_once' | 'allow_always' | 'reject_once' | 'reject_always'

export interface PermissionOption {
  option_id: string
  name: string
  kind: PermissionOptionKind
}

export interface PermissionResp {
  call_id: string
  session_id: string
  title: string
  tool_call_id: string
  kind: string
  input?: unknown
  options: PermissionOption[]
  timeout_at: string // ISO；过期即 service 自动拒绝
}

/** 决策体：allowed=true 允许 + 可选 option_id（选中项）+ always（会话级记忆）。 */
export interface ResolvePermissionReq {
  call_id: string
  option_id?: string
  always: boolean
  allowed: boolean
}

export interface ResolvePermissionResp {
  call_id: string
  allowed: boolean
}

export interface CreateSessionReq {
  agent_id: string
  prompt: string
  cwd?: string
}

export interface CreateSessionResp {
  id: string
  agent_id: string
  runtime: string
  status: SessionStatus
  event_seq: number // 已发布事件水位：WS attach 续传起点
}

export interface PromptReq {
  prompt: string
}

export interface PromptResp {
  id: string
  status: SessionStatus
  event_seq: number
}

export interface CancelSessionResp {
  id: string
  status: SessionStatus
}
