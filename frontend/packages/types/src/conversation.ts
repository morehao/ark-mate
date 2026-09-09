export type ConversationStatus = 'in_progress' | 'completed' | 'cancelled' | 'failed' | 'interrupted'

export interface ConversationSummary {
  id: string
  agent_id: string
  status: ConversationStatus
  title: string // 会话标题（服务端由首条用户消息推导；可 PATCH 覆盖）
  pinned: boolean
  archived: boolean
  created_at: string // ISO 时间戳（UTC RFC3339）
  updated_at: string
}

/** 单轮用量快照（解析 runs.usage_json；used/size 为上下文窗口占用，快照非增量）。 */
export interface RunUsage {
  used: number
  size: number
  cost_amount?: number
  cost_currency?: string
}

/** 轮次明细（GET /conversations/:id/runs；runs 表读模型，内存模式为空数组）。 */
export interface ConversationRun {
  id: string
  user_text: string
  status: string // running | completed | failed | cancelled
  error_code?: string
  stop_reason?: string
  first_seq: number
  last_seq: number
  started_at: string
  ended_at?: string
  /** 本轮从用户消息到首个 assistant 输出的耗时（ms；未记录/无输出为 0）。 */
  first_token_ms?: number
  /** 该轮结束后的用量快照（无上报则缺省）。 */
  usage?: RunUsage
}

/** 会话搜索结果 = 会话概要 + 命中片段/次数（Matches≥1：元数据或正文命中；含归档）。 */
export interface ConversationSearchHit extends ConversationSummary {
  snippet: string
  matches: number
}

/** 跨会话消息级搜索命中（M3 ②：seq 为锚点，前端据此滚动定位）。 */
export interface MessageSearchHit {
  conversation_id: string
  title?: string
  agent_id?: string
  role: 'user' | 'assistant' | string
  seq: number
  seq_hi?: number
  message_id?: number
  run_id?: string
  snippet: string
  created_at: string
}

export interface MessageSearchResp {
  query: string
  /** 数据面：sqlite（messages 读模型）| transcript（无 DB 时的回扫降级）。 */
  source: 'sqlite' | 'transcript' | string
  hits: MessageSearchHit[]
  total: number
  has_more: boolean
}

/** 批量导出产物（M3 ②：content 可直接落盘）。 */
export interface ExportFile {
  conversation_id: string
  title?: string
  agent_id?: string
  status?: string
  filename: string
  format: 'markdown' | 'text' | 'json' | string
  events: number
  bytes: number
  truncated?: boolean
  content: string
}

export interface ExportResp {
  generated_at: string
  format: string
  exported: number
  total_bytes: number
  skipped?: string[]
  files: ExportFile[]
}

export interface ConversationDetail extends ConversationSummary {
  pending: PermissionResp[] // 挂起权限请求（确认通道待决项）
  memories?: PermissionMemory[] // 会话级权限记忆（allow/reject 双侧，可逐条/一键撤销）
}

/** 会话级权限记忆（decision 区分「始终允许」与「始终拒绝」，对称语义）。 */
export interface PermissionMemory {
  title: string
  kind: string
  decision: 'allow' | 'reject'
  since: string // ISO（RFC3339 UTC），授予时刻
}

/** 撤销会话级权限记忆（title+kind 均空 = 一键撤销全部）。 */
export interface RevokeMemoryReq {
  title?: string
  kind?: string
}

/** 会话元数据子集更新（nil/零值不更新）。 */
export interface ConversationPatch {
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

/**
 * 决策回执：描述**实际投递给 agent** 的结果（不是请求参数回显）。
 * `option_id` 可能被兜底为第一个选项，或显式拒绝因无 deny 选项降级 `cancelled`——
 * 以 `outcome`/`selected_option_id` 为准。
 */
export interface ResolvePermissionResp {
  call_id: string
  allowed: boolean
  rejected: boolean
  always: boolean
  /** 请求里指定的 option_id（原样回带，便于核对）。 */
  option_id: string
  /** 实际 ACP 回执形态。 */
  outcome: 'selected' | 'cancelled'
  /** 实际选中的 option_id（outcome=cancelled 时为空串）。 */
  selected_option_id: string
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
  /** 归属会话（C3 scope：与 session_id 同值；决策必须由该会话发起，跨会话按「不存在」处理）。 */
  conversation_id: string
  /** 归属来源：wire（wire 提供）| inferred_inflight（宿主按连接上唯一在途 turn 推断）。 */
  scope_source: string
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

export interface CreateConversationReq {
  /** 空 = 后端默认播种 Agent（dsh）。 */
  agent_id?: string
  prompt: string
  cwd?: string
}

export interface CreateConversationResp {
  id: string
  agent_id: string
  status: ConversationStatus
  event_seq: number // 已发布事件水位：WS attach 续传起点
}

export interface PromptReq {
  prompt: string
}

export interface ConversationPromptResp {
  id: string
  status: ConversationStatus
  event_seq: number
}

export interface CancelConversationResp {
  id: string
  status: ConversationStatus
}
