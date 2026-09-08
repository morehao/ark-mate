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
}

export interface Tool {
  name: string
  description?: string
  /** builtin | mcp | observed */
  source: string
  /** 参数 JSON Schema（原样保真）。 */
  schema?: unknown
}
