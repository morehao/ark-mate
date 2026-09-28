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
  /**
   * **定义级**投放开关（`<dir>/mcp/<id>.json` 的 `enabled`；未声明 = true）。
   * 与 `enabled`（agent 白名单）是两件事：定义级关掉 = 谁都拿不到，白名单只是"这个 agent 拿不拿"。
   */
  defEnabled: boolean
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
      defEnabled: srv?.enabled ?? true,
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

// ——— M2 Library 视图模型（只读聚合；纯函数） ———

/** Library 条目的类型标签文案（M5 之前不含 command）。 */
export function libraryKindLabel(kind: string): string {
  switch (kind) {
    case 'skill':
      return '技能'
    case 'mcp':
      return 'MCP'
    case 'agent':
      return 'Agent'
    case 'tool':
      return '工具'
    default:
      return kind
  }
}

/** Library 条目的类型标签色（与能力页其它标签同一语言：skill 中性、MCP 绿、agent 蓝、工具金）。 */
export function libraryKindColor(kind: string): string {
  switch (kind) {
    case 'mcp':
      return 'green'
    case 'agent':
      return 'blue'
    case 'tool':
      return 'gold'
    default:
      return 'default'
  }
}

/** 逐 runtime 可达性标签（颜色 + 短文案 + tooltip 里那句人话原因）。 */
export function libraryStatusView(status: string): { color: string; text: string } {
  switch (status) {
    case 'projected':
      return { color: 'green', text: '可用' }
    case 'untested':
      return { color: 'gold', text: '未实测' }
    case 'not_configured':
      return { color: 'orange', text: '未配置' }
    case 'not_applicable':
      return { color: 'default', text: '不适用' }
    case 'disabled':
      return { color: 'red', text: '已禁用' }
    default:
      return { color: 'default', text: status }
  }
}

/** origin 标签（local = 本机定义文件可改；imported = 随平台来或探测出的，没有事实源文件可改）。 */
export function libraryOriginView(origin: string): { color: string; text: string; hint: string } {
  if (origin === 'local') {
    return { color: 'cyan', text: '本机', hint: '来自本机定义文件（source_file），可在本页增删改' }
  }
  return {
    color: 'default',
    text: '内置',
    hint: '随平台来的或运行时探测出来的（种子 Agent / builtin·observed 工具 / MCP 探测出的工具）：没有事实源文件可改',
  }
}

/**
 * 写面错误 → 用户可读文案（在**后端那句已经说清问题的话**后面补一步动作）。
 *
 * 刻意不在前端重写后端文案：后端文案带着具体是哪一项不合法（"stdio 必须给绝对路径"…），
 * 前端再翻译一遍只会丢掉细节。这里只补「接下来做什么」，并且只在能确定下一步时补。
 */
export function writeErrorText(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err)
  const code = (err as { code?: number }).code
  switch (code) {
    case 40000:
      return `${msg}（检查字段名与取值：id 只能是小写字母/数字/-/_ 且 ≤64 字符）`
    case 40001:
      return `${msg}（按提示修正定义内容后重试：被拒的定义**不会**落盘）`
    case 40401:
      return `${msg}（这条定义可能已被外部删除：刷新列表后重试）`
    case 40901:
      return `${msg}（id 已被占用或被外部改动：换个 id，或刷新后重试——平台不会覆盖别人的改动）`
    default:
      return msg
  }
}

// ——— M3 会话能力选定（创建时勾选；纯函数） ———

/**
 * 会话能力选定（UI 三态，与 wire 的 `capabilities` 一一对应）。
 *
 * `null` 与 `[]` **语义相反**，不要用 `?? []` 抹平：
 *  - `null` = 该 kind 未声明 → 回落 agent 白名单（技能另按其 runtimes 声明）
 *  - `[]`   = 明确一个都不要
 *  - 非空   = 白名单
 */
export interface CapabilityChoice {
  skills: string[] | null
  mcps: string[] | null
}

/** 未声明（跟随默认）的选定。 */
export const FOLLOW_DEFAULT: CapabilityChoice = { skills: null, mcps: null }

/** 选定 → 请求体片段；两个 kind 都未声明时返回 undefined（键都不出现）。 */
export function selectionPayload(choice: CapabilityChoice): { skills?: string[]; mcps?: string[] } | undefined {
  const out: { skills?: string[]; mcps?: string[] } = {}
  if (choice.skills !== null) out.skills = choice.skills
  if (choice.mcps !== null) out.mcps = choice.mcps
  return out.skills === undefined && out.mcps === undefined ? undefined : out
}

/** 选定是否已限制任何 kind（未限制 = 缺省路径，请求体里一个键都不加）。 */
export function selectionIsDefault(choice: CapabilityChoice): boolean {
  return choice.skills === null && choice.mcps === null
}

/** 选定 → 药丸文案（短）。 */
export function selectionPillText(choice: CapabilityChoice): string {
  if (selectionIsDefault(choice)) return '能力 · 跟随默认'
  const parts: string[] = []
  if (choice.skills !== null) {
    parts.push(choice.skills.length === 0 ? '技能：无' : `技能 ${choice.skills.length}`)
  }
  if (choice.mcps !== null) {
    parts.push(choice.mcps.length === 0 ? 'MCP：无' : `MCP ${choice.mcps.length}`)
  }
  return parts.join(' · ')
}

/** 选定 → 悬停说明（把"这一个会话会被投什么"讲清楚，含没限制的那一半）。 */
export function selectionHint(choice: CapabilityChoice): string {
  const lines: string[] = []
  lines.push(
    choice.skills === null
      ? '技能：不加限制（按当前 Agent 的声明投放）'
      : choice.skills.length === 0
        ? '技能：一个都不要'
        : `技能：只用这 ${choice.skills.length} 个`,
  )
  lines.push(
    choice.mcps === null
      ? 'MCP：不加限制（按当前 Agent 的白名单）'
      : choice.mcps.length === 0
        ? 'MCP：一个都不要'
        : `MCP：只注入这 ${choice.mcps.length} 个`,
  )
  lines.push('能力集按进程绑定：不同选定 = 不同代际（各自一个进程），创建后按下一轮边界生效。')
  return lines.join('\n')
}

/**
 * 一个技能项在选定列表里的状态：能不能勾、勾不了为什么。
 *
 * 判据与**服务端校验同源**（都取 agent 视角的适用性）：这里挡下来的项服务端也会拒，
 * 前端先挡只是省一次往返 + 把原因就地写清。所以它**不是**安全边界，只是同一事实的提前呈现。
 */
export function skillOptionView(item: Pick<AgentSkill, 'applicable' | 'reasons'>): {
  selectable: boolean
  note: string
} {
  if (item.applicable !== true) {
    // 不适用的原因可能有多条（被禁用 / runtimes 没放行 / 没有投影落点），逐条带出。
    const why = (item.reasons ?? []).filter(Boolean).join('；')
    return { selectable: false, note: why || '当前 Agent 用不上这个技能' }
  }
  return { selectable: true, note: '' }
}

/**
 * 一个 MCP 项在选定列表里的状态。三层判据缺一不可（都取自 agent 视角的行模型）：
 * 定义级开关（谁都拿不到）→ 传输可承载（底座能不能注入）→ agent 白名单（可被会话覆盖）。
 */
export function mcpOptionView(row: Pick<McpRow, 'enabled' | 'defEnabled' | 'runtimes'>): {
  selectable: boolean
  note: string
} {
  if (!row.defEnabled) {
    return { selectable: false, note: '这个 MCP 已被禁用（enabled:false）：在能力页改回 true 即可恢复' }
  }
  if (row.runtimes.length === 0) {
    return { selectable: false, note: '当前 Agent 的候选底座都不承载它的传输方式' }
  }
  if (!row.enabled) {
    // 允许勾（服务端放行：会话选定覆盖 agent 白名单），但必须说清这是"越权"行为。
    return { selectable: true, note: '不在该 Agent 的白名单里：选中它只在本会话内覆盖白名单（不影响别的会话）' }
  }
  return { selectable: true, note: '' }
}

/**
 * 已建会话钉住的能力选定 → 详情展示（`null` = 该会话没有能力选定，走的是缺省路径）。
 *
 * 展示的是**回显快照**而不是"现在能用什么"：定义文件改了以后，这个会话仍跑在钉住的那一代上，
 * 两者的差别恰恰是用户需要看见的（也是 M3-6 迁移的对象）。
 */
export function pinnedCapabilitiesView(
  caps?: { skills?: string[]; mcps?: string[]; fingerprint?: string } | null,
): { text: string; hint: string; fingerprint: string } | null {
  if (!caps) return null
  const fmt = (list?: string[]) => (list === undefined ? '跟随默认' : list.length === 0 ? '无' : list.join('、'))
  return {
    text: `技能：${fmt(caps.skills)} ｜ MCP：${fmt(caps.mcps)}`,
    hint: '该会话创建时钉住的能力选定：定义文件之后被改动也不会影响它（改动在下一次轮次边界按新代际生效）。',
    fingerprint: caps.fingerprint ?? '',
  }
}
