export type SessionStatus = 'in_progress' | 'completed' | 'cancelled' | 'failed' | 'interrupted'

export interface SessionSummary {
  id: string
  agent_id: string
  status: SessionStatus
  title: string // 会话标题（服务端由首条用户消息推导；可 PATCH 覆盖）
  pinned: boolean
  archived: boolean
  /**
   * 会话工作空间（服务端规范化后的绝对路径；创建时定稿、之后不可变）。
   * 它同时是底座 ACP session 的 cwd 与原生权限 `workspace-write` 的边界。
   * 历史会话可能缺省（早于工作空间模型落库的行）。
   */
  cwd?: string
  /**
   * 归属的工作空间登记 id（v14；**读面解析后**的值——历史会话（库里 `workspace_id=''`）
   * 下发的是默认工作空间的 id，不是空串，所以前端可以直接拿它分组）。
   */
  workspace_id?: string
  /** 归属工作空间的展示名（空标题已按末级目录名回落；默认工作空间读作「默认工作空间」）。 */
  workspace_title?: string
  /**
   * 该工作空间是否由**平台**给出（= 落在默认工作空间上）。
   *
   * v14 起语义从「平台按会话 id 代建的一次性目录」变成「这是共享的默认工作空间」，
   * 但字段名与 wire key 保留（换名是纯破坏且没有信息增益）。
   * **权威标志**：判定「这个目录不是你选的」只读它，不要从路径形状/前缀反推（前缀可配置）。
   */
  workspace_temporary?: boolean
  /** provider 档案 id（会话级模型选择；空 = 跟随 agent）。 */
  provider?: string
  /** 会话级模型 id（空 = 底座默认）。 */
  model?: string
  /**
   * 钉住的能力选定（M3）。**整个字段缺失 = 未声明能力选择**（走 agent 白名单 /
   * 各自的 runtimes 声明），不是"没有能力"。
   */
  capabilities?: SessionCapabilities | null
  /**
   * 会话来源（平台判定，非用户输入）：缺省 = 人工创建，`automation:<id>` = 该定时任务触发。
   * 面板据此给会话打标并可跳回任务页。
   */
  origin?: string
  created_at: string // ISO 时间戳（UTC RFC3339）
  updated_at: string
}

/**
 * 会话的能力选定快照（与落库的 `capability_json` 逐键一致）。
 *
 * 每个 kind 三态，**缺键与空数组语义相反**，不要用 `?? []` 抹平：
 *  - 整个对象缺失 → 未声明能力选择（回落 agent 白名单）
 *  - 某 kind 缺键   → 该 kind 未声明（回落上一层）
 *  - 某 kind `[]`   → **明确一个都不要**
 *  - 某 kind 非空   → 白名单
 */
export interface SessionCapabilities {
  skills?: string[]
  mcps?: string[]
  /** 钉住的代际指纹（解析后的能力集的摘要；同指纹 = 同一个进程代际）。 */
  fingerprint?: string
}

/** 单轮用量快照（解析 runs.usage_json；used/size 为上下文窗口占用，快照非增量）。 */
export interface RunUsage {
  used: number
  size: number
  cost_amount?: number
  cost_currency?: string
}

/** 轮次明细（GET /sessions/:id/runs；runs 表读模型，内存模式为空数组）。 */
export interface SessionRun {
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
export interface SessionSearchHit extends SessionSummary {
  snippet: string
  matches: number
}

/** 跨会话消息级搜索命中（M3 ②：seq 为锚点，前端据此滚动定位）。 */
export interface MessageSearchHit {
  session_id: string
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
  session_id: string
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

export interface SessionDetail extends SessionSummary {
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
export interface SessionPatch {
  title?: string
  pinned?: boolean
  archived?: boolean
  /** 会话级模型（opencode 仅创建时可选；dsh 会话内即时生效）。 */
  model?: string
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
  /**
   * 归属会话（C3 scope；决策必须由该会话发起，跨会话按「不存在」处理）。
   *
   * v15 之前这里有两个**同值**字段（`session_id` + `conversation_id`），是「一个东西两个词」
   * 的遗迹；改名后收敛成一个键。
   */
  session_id: string
  /** 归属来源：wire（`_meta` 提供）| inferred_inflight（宿主按唯一在途 turn 推断）| ambiguous（两者都无，不归属）。 */
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

export interface CreateSessionReq {
  /** 空 = 后端默认播种 Agent（dsh）。 */
  agent_id?: string
  prompt: string
  /**
   * 会话工作空间（绝对路径）。空 = 落在**默认工作空间**（响应回 `workspace_temporary=true`；
   * 面板里的用户可见文案称「平台分配的目录」——同一件事，避免 UI 出现"代建"这类内部术语）。
   * 非空时服务端校验「绝对 / 存在 / 是目录」并按符号链接规范化——
   * 建议直接提交 `WorkspaceBrowseResp.entries[].path` 或 `Workspace.path` 的原文。
   *
   * v14 起**推荐改用 `workspace_id`**（选择器选中的是登记项而不是裸路径）；`cwd` 保留给
   * "直接用绝对路径"这条老路。两者同时给出且指向不同目录 → 服务端回 40001 `workspace_conflict`。
   */
  cwd?: string
  /**
   * 选中的工作空间登记 id（v14；`GET /workspaces` 的 id）。
   *
   * 给了它就**可以不传 `cwd`**（服务端按登记路径定 cwd）。它与 `cwd` 是两种意图表达：
   * 这个是"我选了那个有名字的项目"，那个是"就用这个路径"。
   */
  workspace_id?: string
  /** 会话级模型 id（空 = 底座默认；opencode 走拨号 env，dsh 走 set_config_option）。 */
  model?: string
  /**
   * 能力选定（M3）：只传**用户真的限制了的** kind。缺键 = 该 kind 不限制；
   * `[]` = 一个都不要；非空 = 白名单。服务端据此算代际指纹并把选定钉进会话。
   * 不可达的 id 会被拒（40001 `capability_not_applicable`，msg 里逐条给原因）。
   */
  capabilities?: { skills?: string[]; mcps?: string[] }
}

export interface CreateSessionResp {
  id: string
  agent_id: string
  status: SessionStatus
  event_seq: number // 已发布事件水位：WS attach 续传起点
  /** 定稿后的工作空间（创建即回读，无需二次查询）。 */
  cwd?: string
  /** 归属的工作空间登记 id（平台解析后的值）。 */
  workspace_id?: string
  /** 该工作空间是否由平台给出（权威标志）。 */
  workspace_temporary?: boolean
}

/** §6 投递模式：会话正忙时怎么办。空/reject = 报错（历史行为）；after_turn = 排队；
 *  interrupt_then_prompt = 取消当前轮随后投递。 */
export type DeliveryMode = 'reject' | 'after_turn' | 'interrupt_then_prompt'

export interface PromptReq {
  prompt: string
  delivery?: DeliveryMode
}

export interface SessionPromptResp {
  id: string
  status: SessionStatus
  event_seq: number
  /** 本次输入已入队（delivery=after_turn|interrupt_then_prompt 且会话正忙）。 */
  queued?: boolean
  /** 入队项的 id（可用 DELETE /v1/mate/sessions/:id/inputs/:input_id 撤下）。 */
  input_id?: string
}

/** §6 一条待投递的排队输入（GET /sessions/:id/inputs；当前态，投递/撤下后即消失）。 */
export interface QueuedInputResp {
  input_id: string
  text: string
  /** after_turn | interrupt_then_prompt */
  delivery: string
  target_turn_id?: string
  generation?: number
  created_at?: string
}

/** §6 撤下排队输入的响应（DELETE /sessions/:id/inputs/:input_id）。 */
export interface CancelQueuedInputResp {
  id: string
  input_id: string
  /** 真的撤下了；false = 该 id 已不在 queued（已投递/已作废/不存在）——幂等，不是错误。 */
  deleted: boolean
}

export interface CancelSessionResp {
  id: string
  status: SessionStatus
}
