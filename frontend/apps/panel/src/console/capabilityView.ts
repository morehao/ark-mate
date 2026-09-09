import type { AgentCapabilities, McpProbeResult, McpServer, Tool } from '@ark/types'

// ——— 能力卡视图模型（P2 §8.1/§5.2；纯函数，便于单测/Node 侧校验） ———

/** MCP 一行：Agent 可见性（enabled/runtimes）+ 全局探测状态（tool_count/last_probe_*）合并结果。 */
export interface McpRow {
  id: string
  name: string
  transport: string
  /** agent 白名单是否启用（无 agent 视图时恒 true）。 */
  enabled: boolean
  /** 候选中可承载此传输的 runtime（无 agent 视图时为 []）。 */
  runtimes: string[]
  toolCount: number
  lastProbeAt?: string
  lastProbeError?: string
  /** 是否可触发 live 探测（transport 为空/未知 → false；B3 起 sse 已支持）。 */
  probeable: boolean
  /** 连接端点展示（stdio=command+args；http/sse=url）。 */
  endpoint: string
}

/**
 * 合并「Agent 视角 MCP 条目」与「全局 MCP 注册表（含探测状态）」为行模型。
 * 传入 null 表示不按 Agent 过滤（全局视图）。
 */
export function mcpRows(caps: AgentCapabilities | null, servers: McpServer[]): McpRow[] {
  const byId = new Map(servers.map((s) => [s.id, s]))
  const ids = caps ? caps.mcp_servers.map((m) => m.id) : servers.map((s) => s.id)
  const rows: McpRow[] = []
  for (const id of ids) {
    const agentView = caps?.mcp_servers.find((m) => m.id === id)
    const srv = byId.get(id)
    if (!srv && !agentView) continue
    const transport = agentView?.transport ?? srv?.transport ?? ''
    rows.push({
      id,
      name: agentView?.name || srv?.name || id,
      transport,
      enabled: agentView ? agentView.enabled : true,
      runtimes: agentView?.runtimes ?? [],
      toolCount: srv?.tool_count ?? 0,
      lastProbeAt: srv?.last_probe_at,
      lastProbeError: srv?.last_probe_error,
      // B3 起 stdio/http/streamable_http/sse 均可探测；空/未知传输不可。
      probeable: transport !== '',
      endpoint: srv ? (srv.command ? [srv.command, ...(srv.args ?? [])].join(' ') : srv.url ?? '') : '',
    })
  }
  return rows
}

/** 探测结果 → 一行可读摘要（toast / 状态行）。 */
export function probeSummary(res: McpProbeResult): string {
  const ver = res.server_name ? `${res.server_name}${res.server_version ? ` ${res.server_version}` : ''} · ` : ''
  const skipped = res.skipped > 0 ? `，跳过 ${res.skipped}` : ''
  return `${ver}新增 ${res.added} / 移除 ${res.removed}${skipped}，当前 ${res.tool_count} 个工具`
}

/** 工具来源标签：mcp 条目带归属 server（便于判断「哪个 server 带来的」）。 */
export function toolSourceLabel(t: Tool): string {
  if (t.source === 'mcp' && t.mcp_server) return `mcp · ${t.mcp_server}`
  return t.source
}

/** 探测时间展示（RFC3339 → 本地 MM-DD HH:mm:ss；空/非法 → '—'）。 */
export function fmtProbeAt(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const p = (n: number): string => String(n).padStart(2, '0')
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
