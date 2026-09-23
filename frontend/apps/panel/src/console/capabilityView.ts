import type {
  AgentCapabilities,
  AgentSkill,
  McpProbeResult,
  McpServer,
  SkillMaterializationResp,
  SkillMaterializationState,
  SkillWarning,
  Tool,
} from '@ark/types'

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
  /** 密钥型绑定占用的键（§13.3 M6；**只有键名**，值后端从不读回）。 */
  secretKeys: string[]
  /** 已绑定但当前解析不了的密钥键（vault 缺密文 / env 未设）——半配置要看得见。 */
  unresolvedSecretKeys: string[]
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
      secretKeys: [...(srv?.secret_env_keys ?? []), ...(srv?.secret_header_keys ?? [])],
      unresolvedSecretKeys: srv?.unresolved_secret_keys ?? [],
    })
  }
  return rows
}

// ——— §13.4 S4/S5 + S2 + §13.3 M6：skill 启停/适用、物化体检、密钥键名（纯函数） ———

/** skill 投放视角：启用 ⟂ 适用（§13.4 S5——两者正交，谁都别替谁下结论）。 */
export interface SkillActivationView {
  /** 平台是否投放（false = 已禁用，可恢复）。 */
  enabled: boolean
  /** 该 agent 现在能不能真用上。 */
  applicable: boolean
  /** 不适用的原因（人话，applicable 时为空）。 */
  reasons: string[]
  /** 一句话结论（列表右侧用）。 */
  text: string
}

/** 由 Agent 视角条目推导投放结论（不适用时把原因带上，不做静默）。 */
export function skillActivationView(s: AgentSkill): SkillActivationView {
  const enabled = s.enabled ?? true
  const applicable = s.applicable ?? true
  const reasons = s.reasons ?? []
  const text = !enabled ? '已禁用' : applicable ? '可用' : `不适用（${reasons.length > 0 ? reasons.join('；') : '原因未给'}）`
  return { enabled, applicable, reasons, text }
}

/** 一个 skill 的投放对象标签（按 runtime 逐个；空 = 全部——**不是"没有"**）。 */
export interface SkillRuntimesView {
  labels: string[]
  /** 空 runtimes 的说明（读成"没有投放对象"会让人以为技能废弃了）。 */
  emptyHint: string
  /** scope 声明（M1 只声明不消费，如实标注而不是假装生效）。 */
  scopeTag?: { text: string; hint: string }
}

/** 由 skill 条目推出投放对象展示（runtimes + scope）。 */
export function skillRuntimesView(runtimes?: string[], scope?: string): SkillRuntimesView {
  const list = (runtimes ?? []).filter((r) => r !== '')
  return {
    labels: list.length > 0 ? list : ['全部 runtime'],
    emptyHint:
      list.length > 0
        ? '该 skill 的投放对象（runtimes）'
        : '该 skill 未声明 runtimes = 投放给所有支持 skill 投影的 runtime（升级安全默认）',
    scopeTag:
      scope === 'workspace'
        ? {
            text: '作用域 workspace',
            hint: '该技能声明为 workspace 作用域：声明已记录不会丢，但当前版本仍按底座全局落点投放（按工作区落点待实测后生效）',
          }
        : undefined,
  }
}

/** 装载期扫描告警（§13.2 S1）：critical 已拒载，这里只会是 warning/info。 */
export interface SkillWarningsView {
  color: string
  text: string
  /** tooltip 文案（逐条规则 + 说明）。 */
  hint: string
}

/** 告警汇总（无告警 → null；critical 兼容显示为红——理论上不该出现在已装载条目上）。 */
export function skillWarningsView(warnings?: SkillWarning[]): SkillWarningsView | null {
  if (!warnings || warnings.length === 0) return null
  const critical = warnings.filter((w) => w.severity === 'critical').length
  const color = critical > 0 ? 'red' : warnings.some((w) => w.severity === 'warning') ? 'gold' : 'default'
  const text = critical > 0 ? `${critical} 条 critical` : `${warnings.length} 条告警`
  return { color, text, hint: warnings.map((w) => `[${w.severity}] ${w.pattern}：${w.message}`).join('\n') }
}

/** 物化状态 → 展示元数据（颜色/短标签/含义）。取值见后端 skillMaterializationState。 */
export const MATERIALIZATION_META: Record<string, { color: string; label: string; hint: string }> = {
  in_sync: { color: 'green', label: '一致', hint: '磁盘产物摘要与注册表一致' },
  missing: { color: 'red', label: '缺失', hint: '应有产物但磁盘上没有（重校会补）' },
  tampered: { color: 'red', label: '已改动', hint: '磁盘内容与记录摘要不符（重校会覆盖回平台版本）' },
  stale: { color: 'gold', label: '待更新', hint: '正文或投放状态变了，产物还是旧的（重校会刷新）' },
  disabled: { color: 'default', label: '已禁用', hint: 'skill 已禁用，副本按设计撤回' },
  user_owned: { color: 'purple', label: '用户自有', hint: '该目录不是平台物化的（无归属标记），平台不覆盖' },
  not_applicable: { color: 'default', label: '不适用', hint: '该投影方不在此 skill 声明的 runtimes 里（技能没说要投给它）' },
  untested: { color: 'blue', label: '未实测', hint: '投影形态已声明、根也解得开，但「底座开箱即读到」这条链还没端到端实测过（产物照常写）' },
  not_configured: { color: 'gold', label: '未配置', hint: '该投影的落点解析失败（环境变量缺失或模板非法），detail 里有原因——这一项没有产物，也没有目标路径' },
}

/** 未知状态别崩：按「未知」原样显示（版本错配时也要能看出发生了什么）。 */
export function materializationMeta(status: string): { color: string; label: string; hint: string } {
  return MATERIALIZATION_META[status] ?? { color: 'default', label: status || '未知', hint: '未知状态（可能后端版本更新）' }
}

/** 体检结果 → 一行摘要（弹窗顶部）。 */
export function materializationSummary(res: SkillMaterializationResp): string {
  if (res.states.length === 0) {
    return res.notes && res.notes.length > 0 ? res.notes.join('；') : '本进程没登记物化目标（无 agent 目录或未配置 runtime）'
  }
  const counts = new Map<string, number>()
  for (const s of res.states) counts.set(s.status, (counts.get(s.status) ?? 0) + 1)
  const parts = [...counts.entries()].map(([st, n]) => `${materializationMeta(st).label} ${n}`)
  return `${res.states.length} 个物化目标：${parts.join(' / ')}`
}

/**
 * 状态行是否需要人工关注（红/黄＝值得看一眼；一致/用户自有/不适用/未实测＝正常）。
 *
 * `not_configured` 计入：它不是「正常的没配」而是「配了解析不出来」，产物与目标路径都没有。
 * `untested` 不计入：产物是写了的，它的含义是"我们对这条链的把握程度"，不是故障。
 */
export function materializationNeedsAttention(s: SkillMaterializationState): boolean {
  return s.status === 'missing' || s.status === 'tampered' || s.status === 'stale' || s.status === 'not_configured'
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
