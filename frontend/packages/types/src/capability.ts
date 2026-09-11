// P1 能力数据面对外类型（backend /v1/forge/skills|mcps|tools；见
// backend/internal/dto/dtocapability 的 SkillResp/McpServerResp/ToolResp）。
// MCP headers/凭证后端不读回，故此处无 headers 字段。

export interface SkillSource {
  runtime: string
  path: string
  format: string
}

export interface Skill {
  id: string
  name: string
  description?: string
  /** SKILL.md 内容（P2 物化源；目录条目可空）。 */
  body?: string
  sources?: SkillSource[]
}

export interface McpServer {
  id: string
  name?: string
  /** stdio | sse | http | streamable_http */
  transport: string
  command?: string
  args?: string[]
  url?: string
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

/** 某 Agent 可见的 skill（sources 已按该 Agent 候选 runtime 裁剪）。 */
export interface AgentSkill {
  id: string
  name: string
  description?: string
  sources?: SkillSource[]
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
