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

/** 会话搜索结果 = 会话概要 + 命中片段/次数（Matches≥1：元数据或正文命中；含归档）。 */
export interface SessionSearchHit extends SessionSummary {
  snippet: string
  matches: number
}

export interface SessionDetail extends SessionSummary {
  pending: PermissionResp[] // 挂起权限请求（确认通道待决项）
  always_allowed?: AlwaysAllowItem[] // 会话级「始终允许」记忆（allow_always 已授）
}

/** 会话级「始终允许」记忆条目。 */
export interface AlwaysAllowItem {
  title: string
  kind: string
  since: string // ISO（RFC3339 UTC），授予时刻
}

/** 撤销「始终允许」记忆（title+kind 均空 = 一键撤销全部）。 */
export interface RevokeAlwaysReq {
  title?: string
  kind?: string
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

// ——— 结构化提问通道（ACP elicitation/* ↔ 用户提交） ———

/** 单选枚举选项（oneOf：带标题的 const 选项；enum 为无标题裸值列表）。 */
export interface ElicitationEnumOption {
  const: string
  title: string
}

/** elicitation 表单字段的 JSON Schema 属性（宿主据此渲染控件；宽类型透传服务端原文）。 */
export interface ElicitationFieldSchema {
  type?: 'string' | 'integer' | 'number' | 'boolean' | 'array'
  title?: string
  description?: string
  default?: unknown
  enum?: string[]
  oneOf?: ElicitationEnumOption[]
  minLength?: number
  maxLength?: number
  pattern?: string
  minimum?: number
  maximum?: number
  minItems?: number
  maxItems?: number
  /** array（多选）模式的允许项定义。 */
  items?: { type?: string; enum?: string[]; oneOf?: ElicitationEnumOption[] }
}

/** elicitation form 请求的 JSON Schema 对象。 */
export interface ElicitationFormSchema {
  type?: string
  title?: string
  description?: string
  properties?: Record<string, ElicitationFieldSchema>
  required?: string[]
}

export type ElicitationMode = 'form' | 'url'

/** 挂起结构化提问（服务端 /elicitations 与 WS 事件共用载荷）。 */
export interface ElicitationResp {
  elicitation_id: string
  session_id: string
  mode: ElicitationMode
  message: string
  schema?: ElicitationFormSchema // form 模式
  url?: string // url 模式
  timeout_at: string // ISO；过期即 service 自动取消
}

export type ElicitationAction = 'accept' | 'decline' | 'cancel'

/** 决策体：accept=提交（content 为表单内容对象）；decline=拒绝提供；cancel=取消本次提问。 */
export interface ResolveElicitationReq {
  elicitation_id: string
  action: ElicitationAction
  content?: Record<string, unknown>
}

export interface ResolveElicitationResp {
  elicitation_id: string
  action: ElicitationAction
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
