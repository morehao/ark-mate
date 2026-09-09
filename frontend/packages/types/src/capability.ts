// P1 能力数据面对外类型（backend /v1/forge/skills|mcps|tools；见
// backend/internal/server/handler/handler.go 的 SkillResp/McpServerResp/ToolResp）。
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
