// P1 能力数据面对外类型（backend /v1/forge/skills|mcps|tools；见
// backend/internal/dto/dtocapability 的 SkillResp/McpServerResp/ToolResp）。
// MCP headers/凭证后端不读回，故此处无 headers 字段。

/** 一条装载期正文扫描告警（§13.2 S1）。critical 的定义在装载时已被拒载，不会出现在列表里。 */
export interface SkillWarning {
  /** info | warning | critical */
  severity: string
  /** 稳定规则 id（如 ignore-previous-instructions / sensitive-path-reference）。 */
  pattern: string
  message: string
}

export interface Skill {
  id: string
  name: string
  description?: string
  /** SKILL.md 内容（P2 物化源；目录条目可空）。 */
  body?: string
  /**
   * 投放对象（§13.4；M1 起取代 sources）。
   *
   * **空数组 = 全部 runtime**（升级安全网：旧定义没有该字段）。落点与格式由 runtime 的投影
   * 声明决定（不再由每个条目各写一遍 path/format）。
   */
  runtimes?: string[]
  /** global | workspace。M1 只声明不消费（workspace 落点待实测），非法取值装载期即拒。 */
  scope?: string
  /** 放行了但该看一眼的告警。 */
  warnings?: SkillWarning[]
  /** 是否投放（§13.4 S4；未声明 enabled 的定义 = true）。禁用只影响投放，定义照旧保留。 */
  enabled?: boolean
}

/** 一处物化目标的现状（§13.4 S2 审计面；GET /skills/:id/materialization）。 */
export interface SkillMaterializationState {
  root: string
  runtime?: string
  dir: string
  /**
   * in_sync | missing | tampered | stale | disabled | user_owned | not_applicable | untested | not_configured
   *
   * untested = 有投影、根也解得开，但该形态未端到端实测过（产物照常写，如实标注）；
   * not_configured = 投影根解析失败（占位符变量没设 / 落点非法），detail 里带原因。
   */
  status: string
  /** 期望摘要（注册表侧）。 */
  hash?: string
  /** 磁盘实测摘要。 */
  actual_hash?: string
  /** 产物 sidecar 记录的写入时间（RFC3339）。 */
  written_at?: string
  /** 归属标记里登记的**投影方**集合（共享 root 下各投影方各登记一次）。 */
  owners?: string[]
  detail?: string
}

/** 一个 skill 的物化体检（只读）。 */
export interface SkillMaterializationResp {
  id: string
  enabled: boolean
  states: SkillMaterializationState[]
  /** 无法逐个给状态时的说明（如本进程没登记任何物化目标）。 */
  notes?: string[]
}

/** 启停一个 skill 的结果（PATCH /skills/:id）。 */
export interface SkillToggleResp {
  id: string
  enabled: boolean
  /** 写回的定义文件路径。 */
  path?: string
  /** 立刻重校物化的结果：启用写文件、禁用撤回副本。 */
  written: number
  removed: number
  /** 重校失败原因（定义文件已改成功，故不回滚）。 */
  materialize_errors?: string[]
  /** 这一步之后该知道的事。 */
  next_action?: string
}

export interface McpServer {
  id: string
  name?: string
  /** stdio | sse | http | streamable_http */
  transport: string
  command?: string
  args?: string[]
  url?: string
  /** 定义级投放开关（`/mcps` 回，未声明 = true）：关掉则任何 agent 都拿不到。 */
  enabled?: boolean
  /** 明文环境变量的键（§13.3 M1；值不回）。 */
  env_keys?: string[]
  /** 密钥型环境变量的键（**只有键名**，值/引用都不回）。 */
  secret_env_keys?: string[]
  /** 密钥型 header 的键（**只有键名**）。 */
  secret_header_keys?: string[]
  /** 已绑定但当前解析不了的密钥键（vault 缺密文 / env 未设）。 */
  unresolved_secret_keys?: string[]
  /** —— P2 §5.2 live 探测状态（从未探测 → 0/空） —— */
  tool_count: number
  /** 最近一次探测时间（RFC3339）。 */
  last_probe_at?: string
  /** 最近一次探测失败原因（成功则空）。 */
  last_probe_error?: string
}

export interface Tool {
  name: string
  description?: string
  /** builtin | mcp | observed */
  source: string
  /** source=mcp 时的归属 MCP server id（按 agent 可见性过滤依据）。 */
  mcp_server?: string
  /** 参数 JSON Schema（原样保真）。 */
  schema?: unknown
}

// ——— B1 MCP 工具调用（POST /mcps/:id/tools/:tool/call） ———

/** 一次 MCP 工具调用请求体。 */
export interface McpToolCallReq {
  /** 工具入参对象（原样透传；缺省 = 无参数）。 */
  arguments?: unknown
  /** 调用超时（毫秒；0/缺省 = 后端默认 30s，上限 5min）。 */
  timeout_ms?: number
}

/**
 * 一次 MCP 工具调用结果。
 *
 * `content` 保持 MCP 联合类型原样（文本/图片/资源链接…）；`text` 是文本块拼接的摘要；
 * `is_error` 是**工具级**失败（服务端明确报告），与传输/协议错误（信封 code≠0）区分。
 */
export interface McpToolCallResult {
  server_id: string
  tool: string
  is_error: boolean
  content: unknown[]
  text?: string
  structured_content?: unknown
  server_name?: string
  server_version?: string
  duration_ms: number
}

// ——— B2 MCP 资源与提示词（live 拉取，不落盘/不目录化） ———

/** 一条可读资源（resources/list）。 */
export interface McpResource {
  uri: string
  name?: string
  title?: string
  description?: string
  mime_type?: string
  size?: number
}

/** 一条 URI 模板（resources/templates/list）。 */
export interface McpResourceTemplate {
  uri_template: string
  name?: string
  title?: string
  description?: string
  mime_type?: string
}

/** resources/list + resources/templates/list 的实时结果。 */
export interface McpResourcesResult {
  server_id: string
  resources: McpResource[]
  templates?: McpResourceTemplate[]
  /** 非空 = 该 server 未实现 templates 接口（服务端能力差异，非平台错误）。 */
  templates_error?: string
  server_name?: string
  server_version?: string
}

/** 一条资源内容（text 与 blob 二选一）。 */
export interface McpResourceContent {
  uri: string
  mime_type?: string
  text?: string
  /** base64（二进制资源原样保真，前端不自动解码）。 */
  blob?: string
}

export interface McpResourceReadResult {
  server_id: string
  uri: string
  contents: McpResourceContent[]
}

/** 提示词参数声明。 */
export interface McpPromptArgument {
  name: string
  title?: string
  description?: string
  required?: boolean
}

/** 一条提示词模板（prompts/list）。 */
export interface McpPrompt {
  name: string
  title?: string
  description?: string
  arguments?: McpPromptArgument[]
}

export interface McpPromptsResult {
  server_id: string
  prompts: McpPrompt[]
  server_name?: string
}

/** prompts/get 的一条消息（content 保持 MCP 联合类型原样）。 */
export interface McpPromptMessage {
  role: string
  content: unknown
}

export interface McpPromptGetResult {
  server_id: string
  name: string
  description?: string
  messages: McpPromptMessage[]
}

// ——— P2 §8.1 统一能力目录（GET /agents/:id/capabilities） ———

/** 某 Agent 视角的 skill（runtimes 已裁到「候选 ∩ 技能声明 ∩ 本进程已解析投影」）。
 *  §13.4 S5：不适用**不等于**不列出——启用与适用正交，两者都要能解释。 */
export interface AgentSkill {
  id: string
  name: string
  description?: string
  /** 该 Agent 的候选中真的会投放到的 runtime（空 = 这个 agent 用不上，reasons 里有为什么）。 */
  runtimes?: string[]
  /** 平台是否投放（false = 已被禁用，可恢复）。 */
  enabled?: boolean
  /** 该 agent 现在能不能真的用上（启用 ⟂ 适用）。 */
  applicable?: boolean
  /** 不适用的原因（人话；applicable=true 时为空）。 */
  reasons?: string[]
}

/** 某 Agent 视角的 MCP server：enabled=白名单是否启用；runtimes=候选中可承载此传输的底座。 */
export interface AgentMcpServer {
  id: string
  name?: string
  transport: string
  enabled: boolean
  runtimes: string[]
}

export interface AgentCapabilities {
  agent_id: string
  name?: string
  /** 该 Agent 的候选 runtime（能力可用性依据，非对外路由承诺）。 */
  runtimes: string[]
  skills: AgentSkill[]
  mcp_servers: AgentMcpServer[]
  tools: Tool[]
  /** 裁剪/降级原因（未注册候选 runtime、mcps: [] 禁用等）。 */
  notes?: string[]
}

/** 一次 MCP live 探测结果（POST /mcps/:id/probe）。 */
export interface McpProbeResult {
  id: string
  transport: string
  server_name?: string
  server_version?: string
  probed_at: string
  added: number
  removed: number
  skipped: number
  tool_count: number
  tools: Tool[]
}

// ——— M2 定义写面（POST/PUT/DELETE /skills|/mcps）与 Library 统一读模型（GET /library） ———

/**
 * 定义写面的请求体：**原文 JSON 对象**（`Record<string, unknown>`）。
 *
 * 刻意不是结构体：`PUT` 的语义是「只改请求体里**出现过的**键」——未出现的保留原值、`null` 删键、
 * `""`/`[]` 显式清空。带可选字段的 TS 接口分不清「没传 name」与「传了 name: ''」，
 * 也表达不了 `null`；而键的出现与否在这里是语义，不是编码细节。
 */
export type CapabilityWriteBody = Record<string, unknown>

/** 新建/覆盖写一个 skill 的结果。 */
export interface SkillWriteResp {
  id: string
  /** 事实源文件路径。 */
  path?: string
  /** true = 新建了文件（覆盖写时为 false）。 */
  created: boolean
  /** 重校物化时写入的落点数。 */
  written: number
  /** 重校物化时撤回的落点数（禁用/收窄 runtimes 时）。 */
  removed: number
  /** 装载期正文扫描告警（放行了但该看一眼）。 */
  warnings?: SkillWarning[]
  /** 重校失败原因（**不回滚定义**：定义已改成功，物化下次自愈）。 */
  materialize_errors?: string[]
  next_action?: string
}

/** 删除一个 skill 的结果（幂等：本来就不存在 → `deleted:false` 且 code=0）。 */
export interface SkillDeleteResp {
  id: string
  deleted: boolean
  path?: string
  /** 撤回的已物化副本数。 */
  removed: number
  materialize_errors?: string[]
  next_action?: string
}

/** 新建/覆盖写一个 MCP server 的结果。 */
export interface McpWriteResp {
  id: string
  path?: string
  created: boolean
  /** 绑了引用但当前解析不出值的密钥键（**只有键名**，值/引用不回）。 */
  unresolved_secret_keys?: string[]
  next_action?: string
}

/** 删除一个 MCP server 的结果（幂等）。 */
export interface McpDeleteResp {
  id: string
  deleted: boolean
  path?: string
  /** 一并摘除的归属该 server 的 mcp 工具条目数。 */
  tools_removed: number
  next_action?: string
}

/**
 * 一条能力在**某一个** runtime 上的可达性（Library 读模型）。
 *
 * `status` 五值各自对应一条不同的用户动作：
 * `projected`（可直接选）/ `untested`（会投放但该 runtime 的投影声明尚未实测）/
 * `not_configured`（声明了但本进程没解析出落点）/ `not_applicable`（底座不承载/未注册/白名单没它）/
 * `disabled`（被显式关掉）。`reason` 是给人看的一句话，可直接显示。
 */
export interface LibraryApplicability {
  runtime: string
  ok: boolean
  status: string
  reason?: string
}

/** Library 统一能力条目（GET /library；skill | mcp | agent | tool）。 */
export interface LibraryItem {
  id: string
  /** skill | mcp | agent | tool */
  kind: string
  name: string
  description?: string
  /** local（本机定义文件）/ imported（随平台来或运行时探测出的）。 */
  origin: string
  /** global | workspace。 */
  scope: string
  enabled: boolean
  /** 事实源文件（代码注入/运行时探测的条目为空）。 */
  source_file?: string
  /** 逐候选 runtime 的可达性——**刻意不给「整体可用吗」的合并结论**。 */
  applicability: LibraryApplicability[]
  warnings?: string[]
}
