import type {
  CapabilityWarning,
  CapabilityWriteBody,
  LibraryApplicability,
  LibraryItem,
  McpProbeResult,
  McpServer,
  Skill,
  UnixMillis,
  SkillWarning,
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
  lastProbeAt?: UnixMillis
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

/** 装载期扫描告警（§13.2 S1）：critical 已拒载，这里只会是 warning/info。 */
interface SkillWarningsView {
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
  const text = critical > 0 ? '内容需要处理' : `${warnings.length} 处提醒`
  return { color, text, hint: warnings.map((w) => w.message).join('\n') }
}

/** 物化状态 → 展示元数据（颜色/短标签/含义）。取值见后端 skillMaterializationState。 */
const MATERIALIZATION_META: Record<string, { color: string; label: string; hint: string }> = {
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

/** 探测结果 → 一行可读摘要（toast / 状态行）。 */
export function probeSummary(res: McpProbeResult): string {
  const ver = res.server_name ? `${res.server_name}${res.server_version ? ` ${res.server_version}` : ''} · ` : ''
  const skipped = res.skipped > 0 ? `，跳过 ${res.skipped}` : ''
  return `${ver}新增 ${res.added} / 移除 ${res.removed}${skipped}，当前 ${res.tool_count} 个工具`
}

// ——— M2 Library 视图模型（只读聚合；纯函数） ———

/** 逐 runtime 可达性标签（颜色 + 短文案 + tooltip 里那句人话原因）。 */
function libraryStatusView(status: string): { color: string; text: string } {
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
      return `${msg}（请检查填写的内容与格式）`
    case 40001:
      return `${msg}（按提示修正后重试；未通过的内容不会被保存）`
    case 40401:
      return `${msg}（这条可能已被外部删除：刷新后重试）`
    case 40901:
      return `${msg}（名称已被占用或被外部改动：换一个名称，或刷新后重试——平台不会覆盖别人的改动）`
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
  /** null = 跟随默认（请求不带 expert 键）；'' = 明确不用专家；非空 = 专家 id。 */
  expert: string | null
  skills: string[] | null
  mcps: string[] | null
  /** null = 未声明（→ 无 app 工具）；[] = 明确一个都不要；非空 = 启用的 app 白名单。 */
  apps: string[] | null
}

/** 未声明（跟随默认）的选定。 */
export const FOLLOW_DEFAULT: CapabilityChoice = { expert: null, skills: null, mcps: null, apps: null }

/** 选定 → 请求体片段；四个面都未声明时返回 undefined（键都不出现）。 */
export function selectionPayload(
  choice: CapabilityChoice,
): { expert?: string; skills?: string[]; mcps?: string[]; apps?: string[] } | undefined {
  const out: { expert?: string; skills?: string[]; mcps?: string[]; apps?: string[] } = {}
  // expert === '' 是**明确不用专家**，必须带空串（与"缺键"语义相反）。
  if (choice.expert !== null) out.expert = choice.expert
  if (choice.skills !== null) out.skills = choice.skills
  if (choice.mcps !== null) out.mcps = choice.mcps
  // apps 用 `!= null` 兜住 undefined（旧调用方构造的 choice 可能没这个字段）：undefined 与 null 都视为未声明。
  if (choice.apps != null) out.apps = choice.apps
  return out.expert === undefined && out.skills === undefined && out.mcps === undefined && out.apps === undefined
    ? undefined
    : out
}

/**
 * 创建响应里「专家推荐被跳过」的告警 → 一条**非阻断**提示文案（无告警 → null）。
 *
 * 只描述被跳过的事实与原因（不阻断创建、不进错误态）：专家是建议，陈旧建议不该把任务卡住，
 * 但用户必须能看见"我选的专家少带了两个技能/一台 MCP"。
 */
export function skippedWarningText(warnings?: CapabilityWarning[] | null): string | null {
  if (!warnings || warnings.length === 0) return null
  const detail = warnings.map((w) => `${w.id}（${w.reason}）`).join('；')
  return `专家推荐有 ${warnings.length} 项当前不可用，已跳过：${detail}`
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
    return { selectable: false, note: '这个技能已停用：在扩展页重新启用即可' }
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
    return { selectable: false, note: `暂时取不到${what}的可用信息：只能使用默认设置` }
  }
  if (blocked.length === rows.length) {
    return { selectable: false, note: blocked.map((a) => a.reason || libraryStatusView(a.status).text).join('；') }
  }
  if (blocked.length > 0) {
    return { selectable: true, note: blocked.map((a) => a.reason || libraryStatusView(a.status).text).join('；') }
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
    return { selectable: false, note: '这个连接器已停用：在扩展页启用即可恢复' }
  }
  return applicabilityOptionView(row.applicability, '这个连接器')
}

/**
 * 已建会话钉住的能力选定 → 详情展示（`null` = 该会话没有能力选定，走的是缺省路径）。
 *
 * 展示的是**回显快照**而不是"现在能用什么"：定义文件改了以后，这个会话仍跑在钉住的那一代上，
 * 两者的差别恰恰是用户需要看见的（也是 M3-6 迁移的对象）。
 */
export function pinnedCapabilitiesView(
  caps?: { expert?: string; expert_missing?: boolean; skills?: string[]; mcps?: string[]; fingerprint?: string } | null,
): { text: string; hint: string; fingerprint: string } | null {
  if (!caps) return null
  const fmt = (list?: string[]) => (list === undefined ? '跟随默认' : list.length === 0 ? '无' : list.join('、'))
  const expertText =
    caps.expert === undefined
      ? ''
      : caps.expert === ''
        ? '通用 ｜ '
        : `${caps.expert_missing ? `${caps.expert}（已删除）` : caps.expert} ｜ `
  return {
    text: `${expertText}技能：${fmt(caps.skills)} ｜ 连接器：${fmt(caps.mcps)}`,
    hint: '这是该会话创建时固定的能力选择，之后的改动不会影响它。',
    fingerprint: caps.fingerprint ?? '',
  }
}

// ——— 技能 / MCP 管理页（千问交互 · ark-mate 皮肤）的纯函数 ———
//
// 这一层只做「事实 → 展示 / 表单值 → 写面请求体」的纯变换，**不发请求、不碰 React**：
// 写面语义（只提交变化键、旧 http 不被静默改型、未知键不被回写）全部在这里成文，
// 组件只负责把结果画出来。这样 Node 侧用 `node --test` 就能逐条钉住那些
// 「看起来工作、实际会静默改语义」的分支。

/** UI 只给三种传输；类型里的 `http` 是**旧定义**，读出来一律按 Streamable HTTP 显示。 */
export type McpTransportValue = 'stdio' | 'sse' | 'streamable_http'

/** 旧 `http` 与 `streamable_http` 在界面上是同一项；未知值退化为 stdio（只影响显示，不改定义）。 */
export function mcpTransportView(transport: string): McpTransportValue {
  switch (transport) {
    case 'sse':
      return 'sse'
    case 'http':
    case 'streamable_http':
      return 'streamable_http'
    default:
      return 'stdio'
  }
}

/** 行内 tag（三值）。`http` 显示为 HTTP，不暴露第四种。 */
export function transportLabel(transport: string): string {
  const v = mcpTransportView(transport)
  return v === 'stdio' ? 'STDIO' : v === 'sse' ? 'SSE' : 'HTTP'
}

/** 详情里的完整传输名。 */
export function transportLongLabel(transport: string): string {
  const v = mcpTransportView(transport)
  return v === 'stdio' ? 'STDIO' : v === 'sse' ? 'SSE' : 'Streamable HTTP'
}

/** MCP 派生状态（**只用已有字段**，不新增后端状态列；顺序即优先级）。 */
type McpConnStateKey = 'disabled' | 'error' | 'needs_setup' | 'pending' | 'connected'

interface McpConnStateView {
  key: McpConnStateKey
  label: string
  color: string
  /** 行内状态点/告警文案的语义色（与 antd Tag 的 color 分开：一个给标签，一个给文字）。 */
  tone: 'muted' | 'error' | 'warning' | 'success'
}

export function mcpConnState(row: {
  defEnabled: boolean
  lastProbeError?: string
  lastProbeAt?: UnixMillis
  unresolvedSecretKeys: string[]
}): McpConnStateView {
  if (!row.defEnabled) return { key: 'disabled', label: '已停用', color: 'default', tone: 'muted' }
  if (row.lastProbeError) return { key: 'error', label: '探测失败', color: 'red', tone: 'error' }
  if (row.unresolvedSecretKeys.length > 0) {
    return { key: 'needs_setup', label: '待配置', color: 'gold', tone: 'warning' }
  }
  if (!row.lastProbeAt) return { key: 'pending', label: '未探测', color: 'default', tone: 'muted' }
  return { key: 'connected', label: '已连接', color: 'green', tone: 'success' }
}

/** MCP 行的描述行（错误/待配置时是**可行动的事实**，不是复述标签）。 */
export function mcpRowDescription(
  row: { toolCount: number; unresolvedSecretKeys: string[] },
  state: McpConnStateView,
  lastProbeError?: string,
): string {
  switch (state.key) {
    case 'error':
      return lastProbeError || '探测失败（原因未知）'
    case 'needs_setup':
      return row.unresolvedSecretKeys.length === 1
        ? `缺少 ${row.unresolvedSecretKeys[0]}，注入时会被跳过。`
        : `缺少 ${row.unresolvedSecretKeys.join('、')}，注入时会被跳过。`
    case 'disabled':
      return '已停用，工具不会进入模型上下文。'
    case 'pending':
      return '尚未探测；保存后点「探测」拉取工具列表。'
    default:
      return row.toolCount > 0
        ? '工具已进入模型上下文，随会话注入。'
        : '已连接，但该 server 没有暴露工具。'
  }
}

/**
 * skill 列表的状态点：**只陈述读模型里有的事实**。
 *
 * 不显示「一致」——列表没有逐条物化体检（那要 N 次请求），而把「没查」画成「一致」
 * 正是这一层最不该做的事。真正的物化状态在详情「投放体检」里现拉。
 */
export function skillStatusView(s: Pick<Skill, 'enabled' | 'warnings'>): {
  label: string
  color: string
  tone: 'muted' | 'warning' | 'success'
  hint: string
} {
  if (s.enabled === false) {
    return { label: '已停用', color: 'default', tone: 'muted', hint: '停用后助手暂不使用；可随时重新启用' }
  }
  if (s.warnings && s.warnings.length > 0) {
    return {
      label: '需处理',
      color: 'gold',
      tone: 'warning',
      hint: '内容检查有需要注意的地方，详情里可查看',
    }
  }
  return { label: '已启用', color: 'green', tone: 'success', hint: '启用中' }
}

// ——— 技能表单：读模型 → 表单值，表单值 → 写面请求体 ———

export interface SkillFormValues {
  id: string
  name: string
  description: string
  runtimes: string[]
  scope: 'global' | 'workspace'
  body: string
}

export function skillFormValues(s: Skill): SkillFormValues {
  return {
    id: s.id,
    name: s.name ?? '',
    description: s.description ?? '',
    runtimes: [...(s.runtimes ?? [])],
    scope: s.scope === 'workspace' ? 'workspace' : 'global',
    body: s.body ?? '',
  }
}

/** 新建：提交完整最小 body（`runtimes: []` = 全部，**不写 `["*"]`**）。 */
export function skillCreateBody(v: SkillFormValues): CapabilityWriteBody {
  return {
    id: v.id.trim(),
    name: v.name,
    description: v.description,
    runtimes: [...v.runtimes],
    scope: v.scope,
    body: v.body,
  }
}

/** runtimes 是**集合**语义（顺序不影响投放），比较时排序。 */
function sameStringSet(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  const sa = [...a].sort()
  const sb = [...b].sort()
  return sa.every((x, i) => x === sb[i])
}

/** 参数是**有序**语义（`--a --b` 与 `--b --a` 不是一回事），逐位比较。 */
function sameOrderedList(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i])
}

/**
 * 编辑：**只提交发生变化的键**。
 *
 * `id` 永远不提交（路径已带）；未变的键不出现 → 后端保留原值（含未知键与用户手写字段）。
 * 清空 description 是显式的 `""`（清空），不是「没改」。
 */
export function skillFormPatch(initial: SkillFormValues, next: SkillFormValues): CapabilityWriteBody {
  const patch: CapabilityWriteBody = {}
  if (next.name !== initial.name) patch.name = next.name
  if ((next.description ?? '') !== (initial.description ?? '')) patch.description = next.description
  if ((next.body ?? '') !== (initial.body ?? '')) patch.body = next.body
  if (!sameStringSet(next.runtimes, initial.runtimes)) patch.runtimes = [...next.runtimes]
  if (next.scope !== initial.scope) patch.scope = next.scope
  return patch
}

/** 编辑提交是否为空（空补丁不发请求：一次「打开又保存」不该写盘、也不该刷新 mtime）。 */
export function isEmptyPatch(patch: CapabilityWriteBody): boolean {
  return Object.keys(patch).length === 0
}

// ——— MCP 表单 ———

export interface McpBindingRow {
  key: string
  value: string
  secret: boolean
  /** 来自已存定义（值不回读，编辑态只允许看键名）。 */
  existed: boolean
  removed: boolean
}

export interface McpFormValues {
  id: string
  name: string
  /** UI 三值（旧 http 会显示成 streamable_http）。 */
  transport: McpTransportValue
  /** 定义里的原始 transport：编辑时用它判断「用户有没有改传输」。 */
  originalTransport: string
  command: string
  args: string
  url: string
  env: McpBindingRow[]
  headers: McpBindingRow[]
}

export function mcpFormValues(m: McpServer): McpFormValues {
  const env: McpBindingRow[] = [
    ...(m.env_keys ?? []).map((key) => ({ key, value: '', secret: false, existed: true, removed: false })),
    ...(m.secret_env_keys ?? []).map((key) => ({ key, value: '', secret: true, existed: true, removed: false })),
  ]
  const headers: McpBindingRow[] = [
    ...(m.secret_header_keys ?? []).map((key) => ({ key, value: '', secret: true, existed: true, removed: false })),
  ]
  return {
    id: m.id,
    name: m.name ?? '',
    transport: mcpTransportView(m.transport ?? ''),
    originalTransport: m.transport ?? '',
    command: m.command ?? '',
    args: (m.args ?? []).join(' '),
    url: m.url ?? '',
    env,
    headers,
  }
}

/** 参数串 → 数组：支持单/双引号与反斜杠转义，**不做变量展开**（这里不是 shell）。 */
export function splitArgs(input: string): string[] {
  const out: string[] = []
  let cur = ''
  let quote: '"' | "'" | null = null
  let escaped = false
  let started = false
  for (const ch of input) {
    if (escaped) {
      cur += ch
      escaped = false
      started = true
      continue
    }
    if (ch === '\\' && quote !== "'") {
      escaped = true
      continue
    }
    if (quote) {
      if (ch === quote) quote = null
      else cur += ch
      continue
    }
    if (ch === '"' || ch === "'") {
      quote = ch
      started = true
      continue
    }
    if (/\s/.test(ch)) {
      if (started) {
        out.push(cur)
        cur = ''
        started = false
      }
      continue
    }
    cur += ch
    started = true
  }
  if (started) out.push(cur)
  return out
}

function bindingMap(rows: McpBindingRow[], secret: boolean): Record<string, string> {
  const out: Record<string, string> = {}
  for (const row of rows) {
    const key = row.key.trim()
    if (!key || row.removed) continue
    if (row.secret !== secret) continue
    const value = row.value.trim()
    // 留空 = 不提交这个键（值不回读，空值既不能当引用也不能当明文）。
    if (!value) continue
    out[key] = value
  }
  return out
}

/**
 * 新建：提交完整 body。
 *
 * env/header 里 `secret` 的行进 `secret_env`/`secret_headers`（只接受 `store:`/`env:` 引用），
 * 其余进 `env`/`headers`。留空的行整个不提交——不猜「空串算已解析」。
 */
export function mcpCreateBody(v: McpFormValues): CapabilityWriteBody {
  const body: CapabilityWriteBody = { id: v.id.trim(), name: v.name, transport: v.transport }
  if (v.transport === 'stdio') {
    body.command = v.command.trim()
    body.args = splitArgs(v.args)
  } else {
    body.url = v.url.trim()
  }
  const env = bindingMap(v.env, false)
  const secretEnv = bindingMap(v.env, true)
  const headers = bindingMap(v.headers, false)
  const secretHeaders = bindingMap(v.headers, true)
  if (Object.keys(env).length > 0) body.env = env
  if (Object.keys(secretEnv).length > 0) body.secret_env = secretEnv
  if (Object.keys(headers).length > 0) body.headers = headers
  if (Object.keys(secretHeaders).length > 0) body.secret_headers = secretHeaders
  return body
}

/**
 * 编辑：只提交变化键。
 *
 * **transport 不被静默改型**：用户没动传输时，patch 里不出现 `transport`（旧 `http` 原样保留）；
 * 只有界面三值真的变了才提交。切换传输时把另一侧的字段显式 `null` 掉——否则 stdio→远程会留下
 * 一个同名的 command，定义看起来「同时是两种传输」。
 *
 * env/header **编辑态一律不提交**：它们的值后端从不回读（只有键名），而 `env` 是**顶层键整体替换**
 * 的语义——凭键名重建一遍等于把已存的引用全部删掉。新增/删除引用走「高级（JSON）」。
 */
export function mcpFormPatch(initial: McpFormValues, next: McpFormValues): CapabilityWriteBody {
  const patch: CapabilityWriteBody = {}
  if (next.name !== initial.name) patch.name = next.name
  const transportChanged = mcpTransportView(initial.originalTransport) !== next.transport
  if (transportChanged) {
    patch.transport = next.transport
    if (next.transport === 'stdio') {
      patch.command = next.command.trim()
      patch.args = splitArgs(next.args)
      patch.url = null
    } else {
      patch.url = next.url.trim()
      patch.command = null
      patch.args = null
    }
    return patch
  }
  if (next.transport === 'stdio') {
    if (next.command.trim() !== initial.command.trim()) patch.command = next.command.trim()
    if (!sameOrderedList(splitArgs(next.args), splitArgs(initial.args))) patch.args = splitArgs(next.args)
  } else if (next.url.trim() !== initial.url.trim()) {
    patch.url = next.url.trim()
  }
  return patch
}

// ——— MCP JSON 导入（不新增后端批量接口：前端解析、逐条 POST） ———

export interface McpJsonServer {
  id: string
  body: CapabilityWriteBody
}

interface McpJsonParseResult {
  servers: McpJsonServer[]
  /** 整份文档级错误（不是 JSON 对象 / 没有可导入条目）。 */
  errors: string[]
}

/**
 * 解析 `mcpServers` 片段（也接受**省略外层**的 `{ "<id>": {...} }`）。
 *
 * 传输推断与千问一致：有 `command` → stdio；`url` 含 `/sse` → sse；其余 URL → streamable_http。
 * env/header 的值以 `store:`/`env:` 开头归密钥字典，其余归明文——UI 不读回值，这一步只发生一次。
 */
export function parseMcpJson(text: string): McpJsonParseResult {
  const out: McpJsonParseResult = { servers: [], errors: [] }
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch (err) {
    out.errors.push(`不是合法 JSON：${err instanceof Error ? err.message : String(err)}`)
    return out
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    out.errors.push('配置必须是 JSON 对象（形如 {"mcpServers": {…}}）')
    return out
  }
  const root = parsed as Record<string, unknown>
  const block = root.mcpServers
  const servers =
    block && typeof block === 'object' && !Array.isArray(block)
      ? (block as Record<string, unknown>)
      : root
  for (const [rawID, rawEntry] of Object.entries(servers)) {
    const id = rawID.trim()
    if (!id) {
      out.errors.push('有一条配置的 id 为空')
      continue
    }
    if (!rawEntry || typeof rawEntry !== 'object' || Array.isArray(rawEntry)) {
      out.errors.push(`${id}：值必须是对象`)
      continue
    }
    const entry = rawEntry as Record<string, unknown>
    const command = typeof entry.command === 'string' ? entry.command : ''
    const url = typeof entry.url === 'string' ? entry.url : ''
    const declared = typeof entry.transport === 'string' ? entry.transport : ''
    if (!declared && !command && !url) {
      out.errors.push(`${id}：必须给 command（stdio）或 url（远程）`)
      continue
    }
    const transport: McpTransportValue =
      declared === 'stdio' || declared === 'sse' || declared === 'http' || declared === 'streamable_http'
        ? mcpTransportView(declared)
        : command
          ? 'stdio'
          : /\/sse(?:\/|$|\?)/.test(url)
            ? 'sse'
            : url
              ? 'streamable_http'
              : 'stdio'
    const body: CapabilityWriteBody = {
      id,
      name: typeof entry.name === 'string' && entry.name ? entry.name : id,
      transport,
    }
    if (transport === 'stdio') {
      if (!command) {
        out.errors.push(`${id}：stdio 必须给 command`)
        continue
      }
      body.command = command
      body.args = Array.isArray(entry.args) ? entry.args.map((a) => String(a)) : []
    } else {
      if (!url) {
        out.errors.push(`${id}：${transport} 必须给 url`)
        continue
      }
      body.url = url
    }
    const splitBindings = (value: unknown, plainKey: string, secretKey: string): void => {
      if (!value || typeof value !== 'object' || Array.isArray(value)) return
      const plain: Record<string, string> = {}
      const secret: Record<string, string> = {}
      for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
        const s = typeof v === 'string' ? v : String(v ?? '')
        if (s.startsWith('store:') || s.startsWith('env:')) secret[k] = s
        else plain[k] = s
      }
      if (Object.keys(plain).length > 0) body[plainKey] = plain
      if (Object.keys(secret).length > 0) body[secretKey] = secret
    }
    splitBindings(entry.env, 'env', 'secret_env')
    splitBindings(entry.headers, 'headers', 'secret_headers')
    out.servers.push({ id, body })
  }
  if (out.servers.length === 0 && out.errors.length === 0) {
    out.errors.push('没有可导入的 server（mcpServers 为空）')
  }
  return out
}

/** 一条导入结果（逐条汇总，不只 toast：批量操作要可复核）。 */
export interface McpImportOutcome {
  id: string
  status: 'created' | 'skipped' | 'failed'
  message: string
}
