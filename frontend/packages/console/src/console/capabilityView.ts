import type {
  LibraryApplicability,
  LibraryItem,
  McpProbeResult,
  McpServer,
  SkillMaterializationResp,
  SkillMaterializationState,
  SkillWarning,
  Tool,
} from '@ark-mate/types'

// ——— 能力卡视图模型（P2 §8.1/§5.2；纯函数，便于单测/Node 侧校验） ———

/**
 * MCP 一行：定义级开关 + 逐 runtime 可达性 + 全局探测状态（tool_count/last_probe_*）合并结果。
 *
 * v19 前这里还有 `enabled`（agent 白名单）与 `runtimes`（该 agent 的候选底座）两个字段——
 * 它们的事实来源是 `GET /agents/:id/capabilities`，随 agent 概念一起撤掉了。可达性改从
 * `GET /library` 的同 id 条目取（逐候选 runtime 的 `applicability[]`，每条带人话原因），
 * 于是"哪些底座投不出来、为什么"这条信息一点没少，只是不再有"按谁裁剪"这一层。
 */
export interface McpRow {
  id: string
  name: string
  transport: string
  /** **定义级**投放开关（`<dir>/mcp/<id>.json` 的 `enabled`；未声明 = true）：关掉 = 谁都拿不到。 */
  defEnabled: boolean
  /**
   * 逐候选 runtime 的可达性（取自 `/library` 的同 id 条目；空数组有两种可能：
   * 库读面还没取到，或这个 server 没有任何候选底座——两者在 `mcpOptionView` 里都按"不可投递"处理，
   * 因为**把取不到当作可用**会让用户勾上一个必然被服务端拒的项）。
   */
  applicability: LibraryApplicability[]
  toolCount: number
  lastProbeAt?: string
  lastProbeError?: string
  /** 是否可触发 live 探测（transport 为空/未知 → false；B3 起 sse 已支持）。 */
  probeable: boolean
  /** 连接端点展示（stdio=command+args；http/sse=url）。 */
  endpoint: string
  /** 密钥型绑定占用的键（§13.3 M6；**只有键名**，值后端从不读回）。 */
  secretKeys: string[]
  /** 已绑定但当前解析不了的密钥键（`store:` 引用在库里没有值 / `env:` 未设）——半配置要看得见。 */
  unresolvedSecretKeys: string[]
}

/**
 * 合并「MCP 注册表（含探测状态）」与「`/library` 的可达性」为行模型。
 *
 * `library` 缺省为空数组：连接器面板只要注册表本身，可达性列在那里不显示；
 * 而**选择器**必须传它——那里的勾选判据依赖可达性，缺了就会把必然被拒的项画成可勾。
 */
export function mcpRows(servers: McpServer[], library: LibraryItem[] = []): McpRow[] {
  const libById = new Map(library.filter((it) => it.kind === 'mcp').map((it) => [it.id, it]))
  const rows: McpRow[] = []
  for (const srv of servers) {
    const id = srv.id
    const lib = libById.get(id)
    const transport = srv.transport ?? ''
    rows.push({
      id,
      name: srv.name || id,
      transport,
      defEnabled: srv.enabled ?? true,
      applicability: lib?.applicability ?? [],
      toolCount: srv.tool_count ?? 0,
      lastProbeAt: srv.last_probe_at,
      lastProbeError: srv.last_probe_error,
      // B3 起 stdio/http/streamable_http/sse 均可探测；空/未知传输不可。
      probeable: transport !== '',
      endpoint: srv.command ? [srv.command, ...(srv.args ?? [])].join(' ') : srv.url ?? '',
      secretKeys: [...(srv.secret_env_keys ?? []), ...(srv.secret_header_keys ?? [])],
      unresolvedSecretKeys: srv.unresolved_secret_keys ?? [],
    })
  }
  return rows
}

// ——— §13.4 S4/S5 + S2 + §13.3 M6：skill 启停/适用、物化体检、密钥键名（纯函数） ———

// v19：`skillActivationView` / `SkillActivationTags`（"启用 ⟂ 适用"的两个并列标签）随
// 「某 Agent 视角」那张卡片一起删除——它们的输入 `AgentSkill` 已经不存在。
// 那层语义没有丢：`/library` 的行本身就把两件事分开显示（`enabled` 出「已禁用」标签，
// 逐 runtime 的 `applicability[]` 出「可用/未实测/未配置/不适用/已禁用」+ 人话原因），
// 而且判据从"按某个 agent 裁剪"变成了"每个候选底座各一条"，比原来更细。

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
    return res.notes && res.notes.length > 0 ? res.notes.join('；') : '本进程没登记物化目标（未配置底座投影落点）'
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
    case 'tool':
      return '工具'
    default:
      return kind
  }
}

/** Library 条目的类型标签色（与能力页其它标签同一语言：skill 中性、MCP 绿、工具金）。 */
export function libraryKindColor(kind: string): string {
  switch (kind) {
    case 'mcp':
      return 'green'
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
    hint: '随平台来的或运行时探测出来的（builtin·observed 工具 / MCP 探测出的工具）：没有事实源文件可改',
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
 *  - `null` = 该 kind 未声明 → 回落平台默认投放（技能另按其 runtimes 声明）
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
      ? '技能：不加限制（按平台默认投放）'
      : choice.skills.length === 0
        ? '技能：一个都不要'
        : `技能：只用这 ${choice.skills.length} 个`,
  )
  lines.push(
    choice.mcps === null
      ? 'MCP：不加限制（按平台默认投放）'
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
 * 判据与**服务端校验同源**（都取 `/library` 的逐 runtime 可达性）：这里挡下来的项服务端也会拒，
 * 前端先挡只是省一次往返 + 把原因就地写清。所以它**不是**安全边界，只是同一事实的提前呈现。
 *
 * v19 前这里读的是 agent 视角的 `applicable`/`reasons`（一个布尔 + 一串原因）；现在改成读
 * `applicability[]`——**每条候选底座各一条**状态与人话原因。不可勾的判据因此更严也更准：
 * "没有任何一条可达"才是不可勾，"部分可达"时列出投不出来的那几个与原因。
 */
export function skillOptionView(item: Pick<LibraryItem, 'applicability' | 'enabled'>): {
  selectable: boolean
  note: string
} {
  if (item.enabled === false) {
    // 定义级禁用是**平台侧**状态（定义与正文都保留，可恢复），与"底座用不上"是两件事。
    return { selectable: false, note: '这个技能已被禁用（enabled:false）：在能力页改回 true 即可恢复' }
  }
  return applicabilityOptionView(item.applicability, '这个技能')
}

/**
 * 逐 runtime 可达性 → 能不能勾 + 原因（技能与 MCP 共用的那一半判据）。
 *
 * 空数组按**不可勾**处理：它要么是库读面还没取到、要么是真没有候选底座，
 * 两种情况下"允许勾选"都会把用户送到一次必然被拒的提交上（服务端才是权威判定）。
 */
function applicabilityOptionView(rows: LibraryApplicability[], what: string): { selectable: boolean; note: string } {
  const blocked = rows.filter((a) => !a.ok)
  if (rows.length === 0) {
    return { selectable: false, note: `取不到${what}的投放对象（能力清单未装载或没有任何候选底座）：只能按默认投放` }
  }
  if (blocked.length === rows.length) {
    return { selectable: false, note: blocked.map((a) => `${a.runtime}：${a.reason || a.status}`).join('；') }
  }
  if (blocked.length > 0) {
    return { selectable: true, note: blocked.map((a) => `${a.runtime}：${a.reason || a.status}`).join('；') }
  }
  return { selectable: true, note: '' }
}

/**
 * 一个 MCP 项在选定列表里的状态。两层判据（都取自定义 + `/library` 的可达性）：
 * 定义级开关（谁都拿不到）→ 逐 runtime 可达性（底座能不能注入这种传输方式）。
 *
 * v19 去掉的是第三层「agent 白名单」：会话选定不再需要覆盖谁——没有上层白名单可覆盖了。
 */
export function mcpOptionView(row: Pick<McpRow, 'defEnabled' | 'applicability'>): {
  selectable: boolean
  note: string
} {
  if (!row.defEnabled) {
    return { selectable: false, note: '这个 MCP 已被禁用（enabled:false）：在能力页改回 true 即可恢复' }
  }
  return applicabilityOptionView(row.applicability, '这个 MCP')
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
