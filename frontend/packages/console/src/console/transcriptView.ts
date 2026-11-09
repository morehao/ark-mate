import type { StreamEntry } from '@ark-mate/types'

// ——— 会话流视图模型（P3 余留：长会话只渲染尾部窗口 + 滚动跟随判定；纯函数，便于 Node 侧校验） ———

/** 首屏渲染的条目数（更早的按需展开，避免长会话一次性挂满 DOM）。 */
export const TRANSCRIPT_WINDOW = 200

interface EntryWindow<T> {
  /** 本次渲染的条目（恒为尾部窗口：事件只追加在末尾）。 */
  visible: T[]
  /** 被折叠的较早条目数（0 = 全量渲染）。 */
  hidden: number
}

/** 尾部窗口：`limit + revealed` 条（超出的较早条目折叠）。 */
export function windowEntries<T>(entries: T[], limit: number = TRANSCRIPT_WINDOW, revealed = 0): EntryWindow<T> {
  if (limit <= 0) return { visible: entries, hidden: 0 }
  const size = limit + Math.max(0, revealed)
  if (entries.length <= size) return { visible: entries, hidden: 0 }
  return { visible: entries.slice(entries.length - size), hidden: entries.length - size }
}

interface ScrollMetrics {
  scrollTop: number
  scrollHeight: number
  clientHeight: number
}

/**
 * 是否应跟随到底部：距底 < `threshold` 视为「已在底部」（用户上翻阅读时不打扰）。
 * 内容未溢出（scrollHeight ≈ clientHeight）恒为 true。
 */
export function stickToBottom(m: ScrollMetrics, threshold = 80): boolean {
  return m.scrollHeight - m.scrollTop - m.clientHeight < threshold
}

/**
 * C2「加载更早」的 `before` 游标：当前视图里最小的已知位次；视图为空时用**服务端水位**推。
 *
 * 返回 `null` = 一个已知位次都没有（既没有条目、也没有水位）→ 调用方**省略** `before`
 * （后端从第 0 条起给第一页——"比空更早"唯一说得通的含义）。
 *
 * **v21 起 0 是合法位次**（`session.created` 就是第 0 条），所以这里不再把 `0` 当"没有"：
 *   - 视图里有 seq 0 → 游标就是 0 → `before=0` = "第 0 条之前" → 空页 → 界面如实说"已到最早"；
 *   - 视图为空（快照回退把视图重置了）→ 游标取 `watermark + 1`：服务端水位是"已应用到的
 *     最高位次"，`before = watermark + 1` 的开区间正好取回**最新那一页**，与"加载更早"的
 *     意图一致（若视图里已经有水位之后的实时条目，它们的最小位次更小，游标自然取它）。
 */
export function olderCursor(entries: StreamEntry[], watermark = -1): number | null {
  let min: number | null = watermark >= 0 ? watermark + 1 : null
  for (const e of entries) {
    const n = e.seq
    if (typeof n !== 'number' || n < 0) continue
    if (min === null || n < min) min = n
  }
  return min
}

/**
 * 把「更早一页」的条目前插到当前条目之前（C2）。
 * 契约：更早页的 seq 全部小于 `cursor`（服务端 before 开区间保证），故直接拼接即可；
 * 仍做一层防御——过滤掉 seq ≥ `cursor` 的条目，避免异常响应把历史插到中间。
 * `cursor === null`（省略 before、走"第一页"那条路）时不过滤：没有任何位次可与之比较。
 */
export function prependOlder(
  older: StreamEntry[],
  current: StreamEntry[],
  cursor: number | null,
): StreamEntry[] {
  if (older.length === 0) return current
  const fresh =
    cursor === null ? older : older.filter((e) => typeof e.seq !== 'number' || e.seq < cursor)
  if (fresh.length === 0) return current
  return [...fresh, ...current]
}

/** 流式状态行：阶段词 + 是否要那颗内联光标（正文还没出现时才给）。 */
interface TurnStatus {
  /** 状态行文案；空串 = 这一档不显示状态行。 */
  stage: string
  /** 正文尚未出现时，在阶段词后面挂一颗内联闪烁光标。 */
  showCaret: boolean
}

/**
 * 流式末尾的**状态行**（替代原来那颗孤立的闪烁光标）。
 *
 * 两种形态：
 *  - 本轮已有正文 → 退化成阶段词（「正在执行 Bash」/「正在输出」），不再显示「思考中」；
 *  - 还没有正文 → 「思考中」+ 内联光标。
 *
 * 非流式（轮次已收口 / 没发过）返回空 stage，调用方据此整行不渲染。
 */
export function turnStatusOf(entries: StreamEntry[], streaming: boolean, narrationLabel: string): TurnStatus {
  if (!streaming) return { stage: '', showCaret: false }
  // 只看**本轮**（最后一条用户消息之后）：否则在旧会话里发新消息时，历史的 assistant 正文
  // 会让状态行跳过「思考中」，从一开始就显示阶段词。
  let turnStart = 0
  for (let i = entries.length - 1; i >= 0; i--) {
    if (entries[i].kind === 'user') {
      turnStart = i + 1
      break
    }
  }
  const hasBody = entries
    .slice(turnStart)
    .some((e) => e.kind === 'assistant' && (e.text ?? '').trim().length > 0)
  // 阶段词优先：工具执行时即使正文还没出现，也显示「正在执行 Bash」而不是笼统的「思考中」。
  return {
    stage: narrationLabel || (hasBody ? '正在输出' : '正在准备'),
    showCaret: !hasBody && !narrationLabel,
  }
}

// ——— 思考过程折叠（需求 6 下半，D6） ———

/**
 * 一段相邻思考收成的一块。`id` 取该块**首条**条目 id（服务端已铸号，不新造身份），
 * 重渲染时不会抖。
 */
export interface ThoughtFold {
  kind: 'thought-fold'
  id: string
  text: string
  /** `streaming` = 这一折还在生长（不收起、无箭头）；`completed` = 已截断（默认收起）。 */
  variant: 'streaming' | 'completed'
  startedAtSeq?: number
  endedAtSeq?: number
}

/** 类型守卫：渲染分派用（`StreamEntry.kind` 是另一套联合，直接比较会被 TS 判为无重叠）。 */
export function isThoughtFold(e: StreamEntry | ThoughtFold): e is ThoughtFold {
  return e.kind === 'thought-fold'
}

/**
 * 把**相邻**的 `thought` 条目合并成一折，其余条目原样保留。
 *
 * 为什么按相邻性分组：前端已经能从事件形状推出「这是一段连续的思考」，
 * 不必为此加契约字段（`is_finished` 留作 O1 的备选）。底座在同一段思考里插入工具事件时
 * 会被切成两块——那是对事件流的**诚实反映**，不是分组算法出错。
 *
 * `streaming` 时**只有最后一折且它就在流末尾**算「还在生长」：一经出现正文/工具事件，
 * 那一段思考就结束了，应当收起（验收 A7：正文开始后自动收起、出现箭头）。
 */
export function foldThoughts(entries: StreamEntry[], streaming: boolean): Array<StreamEntry | ThoughtFold> {
  const out: Array<StreamEntry | ThoughtFold> = []
  let i = 0
  while (i < entries.length) {
    if (entries[i].kind !== 'thought') {
      out.push(entries[i])
      i++
      continue
    }
    const group: StreamEntry[] = []
    while (i < entries.length && entries[i].kind === 'thought') {
      group.push(entries[i])
      i++
    }
    const first = group[0]
    const last = group[group.length - 1]
    out.push({
      kind: 'thought-fold',
      id: first.id,
      text: group.map((e) => e.text ?? '').join('\n'),
      variant: 'completed',
      startedAtSeq: first.seq,
      endedAtSeq: last.seq,
    })
  }
  if (streaming) {
    const tail = out[out.length - 1]
    if (tail && isThoughtFold(tail) && entries[entries.length - 1]?.kind === 'thought') {
      tail.variant = 'streaming'
    }
  }
  return out
}


// ——— 对话：工具语义化 + 整轮折叠（对齐 WorkBuddy 的过程流形状） ———

/**
 * 工具图标/语义只在这里派生，组件不各自写一套 `switch title`。
 *
 * 输入有三档可信度：ACP `toolKind`（最可靠）→ title 关键词 → 兜底。分档的理由和
 * `resolveStatus` 一样：把"读文件"说成"执行命令"不会报错，只会让用户在错误的层级找东西。
 */
export type ToolIconName = 'read' | 'edit' | 'command' | 'search' | 'folder' | 'research' | 'delete' | 'other'
type ToolGroupName = 'read' | 'search' | 'modify' | 'command' | 'research' | 'other'

export interface ToolView {
  icon: ToolIconName
  group: ToolGroupName
  statusLabel: string
  primary?: string
  secondary?: string
  /** 完整的路径/命令，供 title 悬浮；primary 可能只显示 basename。 */
  title?: string
  link: boolean
  running: boolean
  failed: boolean
  cancelled: boolean
  input?: unknown
  output?: unknown
  failReason?: string
  bizCode?: string
}

interface ToolSummary {
  text: string
  icon: ToolIconName
  running: boolean
}

export type ProcessBlock =
  | { kind: 'text'; entry: StreamEntry }
  | { kind: 'thought'; fold: ThoughtFold }
  | { kind: 'tool'; entry: StreamEntry; tool: ToolView }
  /** 已结算的权限/提问卡：它是**这一轮的历史节点**，按原顺序留在过程流里（未结算的不进流）。 */
  | { kind: 'notice'; entry: StreamEntry }
  | {
      kind: 'tool-group'
      id: string
      items: Array<StreamEntry | ThoughtFold>
      tools: ToolView[]
      summary: ToolSummary
    }

interface CompletedBlocks {
  title: string
  before: ProcessBlock[]
  /** 每两个正文锚点之间的隐藏过程；`anchor` 是它前面那个锚点。 */
  between: Array<{ anchor: StreamEntry; blocks: ProcessBlock[] }>
  after: ProcessBlock[]
}

export interface TurnModel {
  kind: 'turn'
  id: string
  /** 首个条目在原始条目序列里的下标（搜索锚点滚动用）。 */
  index: number
  user?: StreamEntry
  /** 未完成/流式时的完整过程；完成且存在正文锚点时由 completed 接管。 */
  blocks: ProcessBlock[]
  anchors: StreamEntry[]
  completed?: CompletedBlocks
  /** 没有可折叠过程时的完成行（Agent 头下方的「已完成 1m 5s」）。 */
  completion?: string
  streaming: boolean
  live: boolean
  durationMS?: number
  cancelled: boolean
  failed: boolean
  /** 轮内全部条目 id（搜索命中高亮整轮用）。 */
  entryIds: string[]
  endSeq?: number
  stopReason?: string
}

type TranscriptRow = TurnModel | { kind: 'entry'; entry: StreamEntry }

interface BuildTurnOptions {
  streaming: boolean
  durationForSeq?: (seq: number | undefined) => number | undefined
  indexOfId?: (id: string) => number
}

interface RawTurn {
  user?: StreamEntry
  startId: string
  agent: Array<StreamEntry | ThoughtFold>
  entryIds: string[]
  endSeq?: number
  stopReason?: string
  cancelled: boolean
  failed: boolean
}

const TOOL_GROUP_BY_KIND: Record<string, ToolGroupName> = {
  read: 'read',
  edit: 'modify',
  write: 'modify',
  delete: 'modify',
  move: 'modify',
  execute: 'command',
  search: 'search',
  fetch: 'research',
  think: 'other',
  switch_mode: 'other',
}

const TOOL_RUNNING = new Set(['running', 'started', 'pending', 'queued', 'in_progress', 'executing', 'streaming'])
const TOOL_FAILED = new Set(['failed', 'error', 'err', 'failure'])
const TOOL_DONE = new Set(['completed', 'success', 'ok', 'done', 'executed', 'finished'])

const PATH_KEYS = ['file_path', 'filePath', 'path', 'target_file', 'targetFile', 'target_directory', 'targetDirectory', 'directory', 'root', 'file']
const COMMAND_KEYS = ['command', 'cmd', 'script', 'shell_command']
const QUERY_KEYS = ['pattern', 'query', 'queries', 'key', 'search', 'glob']
const URL_KEYS = ['url', 'uri', 'href']
const LABEL_KEYS = ['title', 'name', 'description', 'summary', 'skill', 'toolName']

function recordOf(value: unknown): Record<string, unknown> | undefined {
  if (!value) return undefined
  if (typeof value === 'string') {
    const text = value.trim()
    if (!text.startsWith('{') && !text.startsWith('[')) return undefined
    try {
      return recordOf(JSON.parse(text))
    } catch {
      return undefined
    }
  }
  if (typeof value === 'object' && !Array.isArray(value)) return value as Record<string, unknown>
  return undefined
}

function pickText(record: Record<string, unknown> | undefined, keys: string[]): string | undefined {
  if (!record) return undefined
  for (const key of keys) {
    const value = record[key]
    if (typeof value === 'string' && value.trim()) return value.trim()
    if (typeof value === 'number' || typeof value === 'boolean') return String(value)
    if (Array.isArray(value)) {
      const first = value.find((item) => typeof item === 'string' && item.trim())
      if (typeof first === 'string') return first.trim()
    }
  }
  return undefined
}

function basename(path: string): string {
  const parts = path.replace(/\\/g, '/').split('/').filter(Boolean)
  return parts[parts.length - 1] ?? path
}

function numberField(record: Record<string, unknown> | undefined, keys: string[]): number | undefined {
  if (!record) return undefined
  for (const key of keys) {
    const value = record[key]
    if (typeof value === 'number' && Number.isFinite(value)) return value
    if (typeof value === 'string' && value.trim() && Number.isFinite(Number(value))) return Number(value)
  }
  return undefined
}

function groupOf(title: string, kind: string): ToolGroupName {
  const byKind = TOOL_GROUP_BY_KIND[kind.trim().toLowerCase()]
  if (byKind) return byKind
  const name = title.trim().toLowerCase()
  if (/(read|view|cat|open|get_file|read_file)/.test(name)) return 'read'
  // 列目录也是「看文件」的一种：把它归进 read，详情才能按文件预览渲染。
  if (/(list_files|list_dir|listdir|read_dir|readdir|\bls\b|\blist\b|\bdir\b|\btree\b)/.test(name)) return 'read'
  if (/(write|edit|replace|patch|append|delete|remove|mkdir|move|rename)/.test(name)) return 'modify'
  if (/(bash|shell|command|exec|terminal|run|powershell|process)/.test(name)) return 'command'
  // web_search 先判 research：它同样含 search，若先走通用搜索分支会把「联网」误归成「搜代码」。
  if (/(web|fetch|http|url|browser|research)/.test(name)) return 'research'
  if (/(grep|search|glob|find|query|list_files|search_files)/.test(name)) return 'search'
  return 'other'
}

function iconOf(group: ToolGroupName, title: string): ToolIconName {
  if (/(delete|remove|rm_|unlink)/i.test(title)) return 'delete'
  switch (group) {
    case 'read':
      return 'read'
    case 'search':
      return 'search'
    case 'modify':
      return 'edit'
    case 'command':
      return 'command'
    case 'research':
      return 'research'
    default:
      return 'other'
  }
}

function statusLabelOf(group: ToolGroupName, title: string, running: boolean, failed: boolean, cancelled: boolean, done: boolean): string {
  const researchSearch = group === 'research' && /search/i.test(title)
  if (running) {
    if (group === 'read') return '读取中'
    if (group === 'search') return '搜索中'
    if (group === 'modify') return '修改中'
    if (group === 'command') return '执行中'
    if (researchSearch) return '搜索中'
    if (group === 'research') return '读取中'
    return '处理中'
  }
  if (cancelled) return '已取消'
  if (failed) {
    if (group === 'read') return '读取失败'
    if (group === 'search') return '搜索失败'
    if (group === 'modify') return '修改失败'
    if (group === 'command') return '执行失败'
    if (researchSearch) return '搜索失败'
    if (group === 'research') return '读取失败'
    return '处理失败'
  }
  if (!done) return ''
  // WorkBuddy 的 command 行只显示命令本身，成功时不额外挂「已完成」；失败才给状态词。
  if (group === 'command') return ''
  if (group === 'read') return '已读取'
  if (group === 'search') return '已搜索'
  if (group === 'modify') return '已修改'
  if (researchSearch) return '已搜索'
  if (group === 'research') return '已读取'
  return '已完成'
}

/** 单条工具 → 渲染所需的最小视图（状态词、对象、行号、详情）。 */
export function toolView(entry: StreamEntry): ToolView {
  const title = (entry.title ?? '').trim()
  const group = groupOf(title, entry.toolKind ?? '')
  const status = (entry.status ?? '').trim()
  const lower = status.toLowerCase()
  const running = TOOL_RUNNING.has(lower) || lower === ''
  const failed = TOOL_FAILED.has(lower) || !!entry.failReason
  const cancelled = lower.includes('cancel')
  const done = TOOL_DONE.has(lower)
  const input = recordOf(entry.input)
  const path = pickText(input, PATH_KEYS)
  const command = pickText(input, COMMAND_KEYS)
  const query = pickText(input, QUERY_KEYS)
  const url = pickText(input, URL_KEYS)
  const label = pickText(input, LABEL_KEYS)

  let primary: string | undefined
  let link = false
  if (group === 'read' || group === 'modify') {
    primary = path ? basename(path) : undefined
    link = !!path
  } else if (group === 'command') {
    primary = command
  } else if (group === 'search') {
    primary = query
  } else if (group === 'research') {
    primary = url ?? query
    link = !!url
  }
  primary = primary ?? label ?? (title || undefined)

  const start = numberField(input, ['start_line', 'startLine', 'line'])
  const limit = numberField(input, ['limit'])
  const end = numberField(input, ['end_line', 'endLine']) ?? (start != null && limit != null ? start + limit - 1 : undefined)
  const secondary = start != null
    ? end != null && end !== start
      ? `L${start}–${end}`
      : `L${start}`
    : undefined

  return {
    icon: iconOf(group, title),
    group,
    statusLabel: statusLabelOf(group, title, running, failed, cancelled, done),
    primary,
    secondary,
    title: path ?? command ?? url ?? title ?? undefined,
    link,
    running,
    failed,
    cancelled,
    input: entry.input,
    output: entry.output,
    failReason: entry.failReason,
    bizCode: entry.bizCode,
  }
}

function groupVerb(group: ToolGroupName): string {
  switch (group) {
    case 'read':
      return '查看'
    case 'search':
      return '搜索'
    case 'modify':
      return '修改'
    case 'command':
      return '运行'
    case 'research':
      return '收集'
    default:
      return '处理'
  }
}

function groupStageLabel(group: ToolGroupName): string {
  switch (group) {
    case 'read':
      return '查看文件'
    case 'search':
      return '定位代码'
    case 'modify':
      return '修改文件'
    case 'command':
      return '运行命令'
    case 'research':
      return '收集资料'
    default:
      return '处理步骤'
  }
}

function groupNoTopic(group: ToolGroupName): string {
  switch (group) {
    case 'read':
      return '查看相关文件'
    case 'search':
      return '搜索相关代码'
    case 'modify':
      return '修改文件'
    case 'command':
      return '运行命令'
    case 'research':
      return '收集资料'
    default:
      return '处理多个步骤'
  }
}

function shortTopic(value: string): string {
  const text = value.trim()
  return text.length > 42 ? `${text.slice(0, 42)}…` : text
}

/** 连续工具 → 一句 15px 摘要（查看 X / 正在查看 X / 查看文件、定位代码：X）。 */
export function summarizeToolGroup(tools: ToolView[], running: boolean): ToolSummary {
  const buckets = new Map<ToolGroupName, ToolView[]>()
  for (const tool of tools) {
    const list = buckets.get(tool.group)
    if (list) list.push(tool)
    else buckets.set(tool.group, [tool])
  }
  const ordered = [...buckets.entries()].sort((a, b) => b[1].length - a[1].length)
  const dominant = ordered[0]?.[0] ?? 'other'
  const topic = shortTopic(tools.find((tool) => !!tool.primary)?.primary ?? '')
  const isRunning = running || tools.some((tool) => tool.running)
  let text: string
  if (!topic) {
    text = groupNoTopic(dominant)
  } else if (ordered.length >= 2) {
    text = `${groupStageLabel(ordered[0][0])}、${groupStageLabel(ordered[1][0])}：${topic}`
  } else {
    text = `${groupVerb(dominant)} ${topic}`
  }
  if (isRunning) text = `正在${text}`
  return { text, icon: iconOf(dominant, ''), running: isRunning }
}

/** 一条 `session.tool_update` 里前端可用的字段（与后端载荷白名单逐条同源）。 */
export interface ToolUpdatePatch {
  title?: string
  status?: string
  toolKind?: string
  input?: unknown
  output?: unknown
  failReason?: string
  bizCode?: string
}

/**
 * 工具条目的增量合并（纯函数，store 与 Node 测试共用）。
 *
 * 为什么单独抽出来：终态 update 的 `title` 常为空（dsh 只在 `tool_call` 那一帧给标题），
 * 空串**不能**覆盖旧标题——否则工具行失去唯一的对象，只剩「处理失败」。同理 `input`/`output`
 * 缺省时保留旧值（终态帧只带 status 是常态，不是"清空"）。
 */
export function mergeToolUpdate(entry: StreamEntry, patch: ToolUpdatePatch): StreamEntry {
  return {
    ...entry,
    title: (patch.title ?? '').trim() || entry.title || entry.id,
    status: (patch.status ?? '').trim() || entry.status || '',
    toolKind: patch.toolKind || entry.toolKind,
    input: patch.input !== undefined ? patch.input : entry.input,
    output: patch.output !== undefined ? patch.output : entry.output,
    failReason: patch.failReason || entry.failReason,
    bizCode: patch.bizCode || entry.bizCode,
  }
}

// ——— 工具详情：按类别渲染（数据只取事件里已有的 input/output，缺什么就如实说什么） ———

type ToolDetailKind = 'read' | 'diff' | 'command' | 'search' | 'summary'

interface ToolDetailLine {
  /** 行号；只有调用参数给了起始行时才编号（不猜第 1 行）。 */
  no?: number
  text: string
}

interface ToolDiffLine {
  kind: 'add' | 'del' | 'context' | 'meta'
  text: string
}

/**
 * 一条工具调用的详情视图模型。
 *
 * **不做任何推断**：能渲染成代码预览/diff/命令输出/结果列表的前提是事件载荷里真的有那些数据；
 * 缺了就只写 `notes` 说明「不可用」，不拿标题或状态补一个看起来合理的值。
 */
export interface ToolDetail {
  kind: ToolDetailKind
  /** 文件路径（read/list）。 */
  path?: string
  /** 文件内容预览（read/list）。 */
  lines?: ToolDetailLine[]
  /** 预览是否被截断。 */
  truncated?: boolean
  /** diff 行（edit/write）。 */
  diff?: ToolDiffLine[]
  /** diff 的数据来源：`output` = 结果里的 unified diff；`input` = 调用参数里的替换/写入内容。 */
  diffSource?: 'output' | 'input'
  /** 命令行（execute）。 */
  command?: string
  /** 明确标记为 stdout 的输出。 */
  stdout?: string
  stderr?: string
  /** 未区分 stdout/stderr 的整段输出（分节标记缺失时如实标「输出」，不冒充 stdout）。 */
  output?: string
  exitCode?: number
  /** 查询词 / URL（search/web）。 */
  query?: string
  url?: string
  /** 结果列表（search/web）。 */
  results?: string[]
  /** 结果摘要（edit/write 没有 diff 数据时）。 */
  summary?: string
  /** 数据不足的如实说明（例如「结果不可用（底座未上报）」）。 */
  notes: string[]
}

const DIFF_KEYS = ['diff', 'patch', 'unified_diff', 'unifiedDiff']
const STDOUT_KEYS = ['stdout', 'std_out']
const STDERR_KEYS = ['stderr', 'std_err']
const EXIT_KEYS = ['exit_code', 'exitCode', 'returncode', 'return_code']
const RESULT_KEYS = ['results', 'items', 'matches', 'hits']
const TEXT_KEYS = ['text', 'content', 'output', 'result', 'summary', 'message']
const OLD_STRING_KEYS = ['old_string', 'oldString', 'old_str', 'oldStr', 'old_text', 'oldText']
const NEW_STRING_KEYS = ['new_string', 'newString', 'new_str', 'newStr', 'new_text', 'newText', 'replacement', 'replace']
const WRITE_CONTENT_KEYS = ['content', 'contents', 'file_text', 'fileText', 'new_content', 'newContent']

function textValue(value: unknown): string | undefined {
  if (typeof value === 'string') return value.trim() ? value : undefined
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  return undefined
}

/** 事件里的 `output`（rawOutput）→ 一段纯文本；结构不认识就返回 undefined（不猜）。 */
function outputText(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined
  const direct = textValue(value)
  if (direct) return direct
  if (Array.isArray(value)) {
    const parts = value
      .map((item) => {
        if (typeof item === 'string') return item
        const record = recordOf(item)
        return record ? pickText(record, TEXT_KEYS) : undefined
      })
      .filter((part): part is string => !!part && !!part.trim())
    return parts.length > 0 ? parts.join('\n') : undefined
  }
  const record = recordOf(value)
  if (record) return pickText(record, TEXT_KEYS)
  return undefined
}

function looksLikeUnifiedDiff(text: string): boolean {
  const lines = text.split('\n')
  const hasHeader = lines.some((line) => line.startsWith('@@') || line.startsWith('--- ') || line.startsWith('+++ '))
  const hasChange = lines.some((line) => /^[+-](?![+-]{2})/.test(line))
  return hasHeader && hasChange
}

function parseDiff(text: string): ToolDiffLine[] {
  return text.split('\n').map((raw) => {
    if (raw.startsWith('@@') || raw.startsWith('---') || raw.startsWith('+++') || raw.startsWith('diff ') || raw.startsWith('Index: ')) {
      return { kind: 'meta', text: raw }
    }
    if (raw.startsWith('+')) return { kind: 'add', text: raw.slice(1) }
    if (raw.startsWith('-')) return { kind: 'del', text: raw.slice(1) }
    if (raw.startsWith(' ')) return { kind: 'context', text: raw.slice(1) }
    return { kind: 'context', text: raw }
  })
}

function diffFromInput(input: Record<string, unknown> | undefined): ToolDiffLine[] | undefined {
  if (!input) return undefined
  const oldText = pickText(input, OLD_STRING_KEYS)
  const newText = pickText(input, NEW_STRING_KEYS)
  if (oldText !== undefined || newText !== undefined) {
    const lines: ToolDiffLine[] = [{ kind: 'meta', text: '替换（来自调用参数）' }]
    if (oldText !== undefined) {
      for (const line of oldText.split('\n')) lines.push({ kind: 'del', text: line })
    }
    if (newText !== undefined) {
      for (const line of newText.split('\n')) lines.push({ kind: 'add', text: line })
    }
    return lines
  }
  const content = pickText(input, WRITE_CONTENT_KEYS)
  if (content !== undefined) {
    return [
      { kind: 'meta', text: '写入内容（来自调用参数）' },
      ...content.split('\n').map((line) => ({ kind: 'add' as const, text: line })),
    ]
  }
  return undefined
}

/** 命令结果里的分节标记（`[stdout]` / `[stderr]` / `[exit code: N]`）→ 结构化输出。 */
function splitCommandResult(text: string): { stdout?: string; stderr?: string; output?: string; exitCode?: number } {
  let body = text
  let exitCode: number | undefined
  const exit = body.match(/\[exit code:\s*(-?\d+)\]/i)
  if (exit) {
    exitCode = Number(exit[1])
    body = body.replace(exit[0], '')
  }
  const marker = /^\[(stdout|stderr)\]\s*$/gim
  const sections: Array<{ label?: string; text: string }> = []
  let last = 0
  let current: string | undefined
  let match: RegExpExecArray | null
  while ((match = marker.exec(body)) !== null) {
    const chunk = body.slice(last, match.index)
    if (chunk.trim() || current) sections.push({ label: current, text: chunk })
    current = match[1].toLowerCase()
    last = match.index + match[0].length
  }
  const tail = body.slice(last)
  if (tail.trim() || current) sections.push({ label: current, text: tail })
  const collect = (label: 'stdout' | 'stderr'): string | undefined => {
    const textValue = sections
      .filter((section) => section.label === label)
      .map((section) => section.text.replace(/^\n+|\n+$/g, ''))
      .filter(Boolean)
      .join('\n')
    return textValue.trim() ? textValue : undefined
  }
  const structured: { stdout?: string; stderr?: string; output?: string; exitCode?: number } = { exitCode }
  const stdout = collect('stdout')
  const stderr = collect('stderr')
  if (stdout) structured.stdout = stdout
  if (stderr) structured.stderr = stderr
  if (!stdout && !stderr) {
    // 没有分节标记：只能说「这是输出」，说成 stdout 会把 stderr 的内容冒充成正常输出。
    const plain = body.replace(/^\n+|\n+$/g, '')
    if (plain.trim()) structured.output = plain
  }
  return structured
}

function resultLine(value: unknown): string | undefined {
  const direct = textValue(value)
  if (direct) return direct.trim()
  const record = recordOf(value)
  if (!record) return undefined
  const title = pickText(record, ['title', 'name', 'text', 'snippet', 'description'])
  const url = pickText(record, ['url', 'link', 'href'])
  if (title && url) return `${title} — ${url}`
  return title ?? url
}

function resultList(value: unknown): string[] {
  if (value === undefined || value === null) return []
  const record = recordOf(value)
  const array = Array.isArray(value)
    ? value
    : record
      ? RESULT_KEYS.map((key) => record[key]).find((item) => Array.isArray(item)) as unknown[] | undefined
      : undefined
  if (array) {
    return array.map(resultLine).filter((line): line is string => !!line)
  }
  const text = outputText(value)
  if (!text) return []
  return text.split('\n').map((line) => line.trim()).filter(Boolean)
}

const MAX_PREVIEW_LINES = 200
const MAX_DIFF_LINES = 400
const MAX_RESULTS = 50

/**
 * 单条工具 → 详情视图。返回 `null` 表示"没有可渲染的类别数据"，调用方退回原始入参/结果的纯文本。
 *
 * 四类的数据前提（都来自事件载荷，不新造字段）：
 *  - read/list：`input` 里的路径 + `output` 里的内容；起始行只认 `start_line`；
 *  - edit/write：`output` 里的 unified diff，或 `input` 里明确的 `old_string/new_string` / 写入内容；
 *  - execute：`input.command` + `output` 里的 stdout/stderr/exit code（结构化字段或分节标记）；
 *  - search/web：`input` 里的查询词 + `output` 里的结果列表。
 */
export function toolDetail(entry: StreamEntry, view?: ToolView): ToolDetail | null {
  const tool = view ?? toolView(entry)
  const input = recordOf(entry.input)
  const output = entry.output
  const text = outputText(output)
  const notes: string[] = []
  if (output === undefined || output === null) notes.push('结果不可用（未上报）')

  if (tool.group === 'read') {
    const path = pickText(input, PATH_KEYS)
    if (!path) return null
    const start = numberField(input, ['start_line', 'startLine', 'line'])
    const detail: ToolDetail = { kind: 'read', path, notes: [] }
    if (text !== undefined) {
      const all = text.split('\n')
      const capped = all.slice(0, MAX_PREVIEW_LINES)
      detail.lines = capped.map((line, index) => (start != null ? { no: start + index, text: line } : { text: line }))
      detail.truncated = all.length > capped.length
    } else {
      detail.notes.push(output !== undefined && output !== null
        ? '没有可识别的文件内容'
        : '没有可预览的内容（结果不可用）')
    }
    return detail
  }

  if (tool.group === 'modify') {
    const rawDiff = pickText(recordOf(output), DIFF_KEYS) ?? (text && looksLikeUnifiedDiff(text) ? text : undefined)
    let lines: ToolDiffLine[] | undefined
    let source: 'output' | 'input' = 'output'
    if (rawDiff) lines = parseDiff(rawDiff)
    if (!lines) {
      lines = diffFromInput(input)
      if (lines) source = 'input'
    }
    if (lines) {
      const capped = lines.slice(0, MAX_DIFF_LINES)
      const detail: ToolDetail = { kind: 'diff', diff: capped, diffSource: source, notes }
      if (lines.length > capped.length) detail.notes.push(`仅显示前 ${MAX_DIFF_LINES} 行改动。`)
      return detail
    }
    const summary = text ?? pickText(recordOf(output), ['summary', 'message'])
    if (!summary && output !== undefined && output !== null) notes.push('没有可展示的结果（事件里没有可识别的输出）')
    return { kind: 'summary', summary, notes }
  }

  if (tool.group === 'command') {
    const command = pickText(input, COMMAND_KEYS)
    if (!command) return null
    const record = recordOf(output)
    const detail: ToolDetail = { kind: 'command', command, notes }
    const structuredStdout = pickText(record, STDOUT_KEYS)
    const structuredStderr = pickText(record, STDERR_KEYS)
    const structuredOutput = pickText(record, ['output', 'result'])
    const structuredExit = numberField(record, EXIT_KEYS)
    if (
      structuredStdout !== undefined ||
      structuredStderr !== undefined ||
      structuredOutput !== undefined ||
      structuredExit !== undefined
    ) {
      if (structuredStdout !== undefined) detail.stdout = structuredStdout
      if (structuredStderr !== undefined) detail.stderr = structuredStderr
      if (structuredOutput !== undefined) detail.output = structuredOutput
      if (structuredExit !== undefined) detail.exitCode = structuredExit
    } else if (text !== undefined) {
      const parsed = splitCommandResult(text)
      if (parsed.stdout !== undefined) detail.stdout = parsed.stdout
      if (parsed.stderr !== undefined) detail.stderr = parsed.stderr
      if (parsed.output !== undefined) detail.output = parsed.output
      if (parsed.exitCode !== undefined) detail.exitCode = parsed.exitCode
    }
    if (
      output !== undefined &&
      output !== null &&
      detail.stdout === undefined &&
      detail.stderr === undefined &&
      detail.output === undefined &&
      detail.exitCode === undefined
    ) {
      notes.push('没有可展示的输出（事件里没有可识别的 stdout/stderr/退出码）')
    }
    return detail
  }

  if (tool.group === 'search' || tool.group === 'research') {
    const query = pickText(input, QUERY_KEYS)
    const url = pickText(input, URL_KEYS)
    if (!query && !url) return null
    const results = resultList(output).slice(0, MAX_RESULTS)
    const detail: ToolDetail = { kind: 'search', query, url, results, notes }
    if (results.length > 0) {
      detail.notes = notes.filter((note) => !note.startsWith('结果不可用'))
    } else if (output !== undefined && output !== null) {
      notes.push('没有可识别的结果项')
    }
    return detail
  }

  return null
}

interface ProcessOptions {
  running: boolean
  /** 从这个下标起才允许把连续项收成摘要；流式早期（正文之前）逐条显示。 */
  foldFrom?: number
}

function itemId(item: StreamEntry | ThoughtFold): string {
  return item.id
}

function singleBlock(item: StreamEntry | ThoughtFold): ProcessBlock | null {
  if (isThoughtFold(item)) return { kind: 'thought', fold: item }
  if (item.kind === 'thought') {
    return {
      kind: 'thought',
      fold: {
        kind: 'thought-fold',
        id: item.id,
        text: item.text ?? '',
        variant: 'completed',
        startedAtSeq: item.seq,
        endedAtSeq: item.seq,
      },
    }
  }
  if (item.kind === 'tool') return { kind: 'tool', entry: item, tool: toolView(item) }
  // 已结算的确认卡是这一轮的历史节点；未结算的只活在输入区上方的确认区（这里不画第二份）。
  if (item.kind === 'permission' || item.kind === 'elicitation') {
    return item.resolved ? { kind: 'notice', entry: item } : null
  }
  if (item.kind === 'assistant' && (item.text ?? '').trim()) return { kind: 'text', entry: item }
  return null
}

function isFoldable(item: StreamEntry | ThoughtFold): boolean {
  return isThoughtFold(item) || (!isThoughtFold(item) && item.kind === 'tool')
}

function isToolItem(item: StreamEntry | ThoughtFold): item is StreamEntry {
  return !isThoughtFold(item) && item.kind === 'tool'
}

/** 把一条 assistant 过程序列切成可渲染块；连续工具在允许的位置收成摘要。 */
export function buildProcessBlocks(items: Array<StreamEntry | ThoughtFold>, options: ProcessOptions): ProcessBlock[] {
  const out: ProcessBlock[] = []
  const foldFrom = options.foldFrom ?? 0
  let buffer: Array<StreamEntry | ThoughtFold> = []

  const flush = () => {
    if (buffer.length === 0) return
    const toolEntries = buffer.filter(isToolItem)
    if (buffer.length >= 2 && toolEntries.length > 0) {
      const tools = toolEntries.map(toolView)
      out.push({
        kind: 'tool-group',
        id: `tool-group-${itemId(buffer[0])}`,
        items: buffer,
        tools,
        summary: summarizeToolGroup(tools, options.running),
      })
    } else {
      for (const item of buffer) {
        const block = singleBlock(item)
        if (block) out.push(block)
      }
    }
    buffer = []
  }

  items.forEach((item, index) => {
    if (isFoldable(item) && index >= foldFrom) {
      buffer.push(item)
      return
    }
    flush()
    const block = singleBlock(item)
    if (block) out.push(block)
  })
  flush()
  return out
}

/** 时长口径（与 WorkBuddy 的 `formatDurationMs` 同量级）：65s → `1m 5s`。 */
export function formatDurationMS(ms: number | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms <= 0) return ''
  if (ms < 1000) return '<1s'
  const total = Math.round(ms / 1000)
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const seconds = total % 60
  if (hours > 0) return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`
  if (minutes > 0) return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`
  return `${seconds}s`
}

function isBodyText(item: StreamEntry | ThoughtFold): item is StreamEntry {
  return !isThoughtFold(item) && item.kind === 'assistant' && typeof item.text === 'string' && item.text.trim().length > 0
}

function createTurn(raw: RawTurn, opts: BuildTurnOptions): TurnModel {
  return {
    kind: 'turn',
    id: raw.startId,
    index: Math.max(0, opts.indexOfId?.(raw.startId) ?? 0),
    user: raw.user,
    blocks: [],
    anchors: [],
    streaming: false,
    live: false,
    cancelled: raw.cancelled,
    failed: raw.failed,
    entryIds: [...raw.entryIds],
    endSeq: raw.endSeq,
    stopReason: raw.stopReason,
  }
}

function finalizeTurn(turn: TurnModel, raw: RawTurn, live: boolean, opts: BuildTurnOptions): void {
  turn.live = live
  turn.streaming = live
  turn.cancelled = raw.cancelled
  turn.failed = raw.failed
  turn.endSeq = raw.endSeq
  turn.stopReason = raw.stopReason
  turn.entryIds = [...raw.entryIds]
  const bodyTexts = raw.agent.filter(isBodyText)
  const maxBodyLength = bodyTexts.reduce((max, item) => Math.max(max, (item.text ?? '').trim().length), 0)
  const lastBody = bodyTexts[bodyTexts.length - 1]
  turn.anchors = bodyTexts.filter((item) => item === lastBody || (item.text ?? '').trim().length === maxBodyLength)

  const durationMS = raw.endSeq != null ? opts.durationForSeq?.(raw.endSeq) : undefined
  turn.durationMS = durationMS
  const titleBase = raw.cancelled ? '已取消' : raw.failed ? '已中断' : '已完成'
  const durationText = formatDurationMS(durationMS)
  const title = durationText ? `${titleBase} ${durationText}` : titleBase
  turn.completion = title

  if (live) {
    const firstBody = bodyTexts.length > 0
      ? raw.agent.findIndex((item) => itemId(item) === bodyTexts[0].id)
      : Number.MAX_SAFE_INTEGER
    turn.blocks = buildProcessBlocks(raw.agent, { running: true, foldFrom: Math.max(0, firstBody) })
    return
  }

  if (raw.failed || turn.anchors.length === 0) {
    turn.blocks = buildProcessBlocks(raw.agent, { running: false, foldFrom: 0 })
    return
  }

  const anchorIds = new Set(turn.anchors.map((anchor) => anchor.id))
  const firstAnchor = raw.agent.findIndex((item) => anchorIds.has(itemId(item)))
  let lastAnchor = -1
  for (let i = raw.agent.length - 1; i >= 0; i--) {
    if (anchorIds.has(itemId(raw.agent[i]))) {
      lastAnchor = i
      break
    }
  }

  const before = buildProcessBlocks(
    raw.agent.slice(0, firstAnchor).filter((item) => !anchorIds.has(itemId(item))),
    { running: false, foldFrom: 0 },
  )
  const between = turn.anchors.slice(0, -1).map((anchor, index) => {
    const start = raw.agent.findIndex((item) => itemId(item) === anchor.id)
    const next = raw.agent.findIndex((item) => itemId(item) === turn.anchors[index + 1].id)
    return {
      anchor,
      blocks: buildProcessBlocks(
        raw.agent.slice(start + 1, next).filter((item) => !anchorIds.has(itemId(item))),
        { running: false, foldFrom: 0 },
      ),
    }
  })
  const after = buildProcessBlocks(
    raw.agent.slice(lastAnchor + 1).filter((item) => !anchorIds.has(itemId(item))),
    { running: false, foldFrom: 0 },
  )

  turn.blocks = []
  const hasFold = before.length > 0 || after.length > 0 || between.some((gap) => gap.blocks.length > 0)
  if (hasFold) turn.completed = { title, before, between, after }
}

/**
 * 把（已合并思考的）条目序列切成「轮」。
 *
 * 轮边界是 user；系统错误仍按独立行处理（它是轮外的事实）。权限/提问卡**不切轮**：
 *  - 未结算 → 不进条目流（只有输入区上方的确认区在画），也不打断这一轮；
 *  - 已结算 → 作为有序历史节点（`notice` 块）留在过程流里，身份头与正文锚点照旧只有一份。
 * 轮末的 system `tone=ok|cancel` 被吸收成 completed 的时长/取消态，不再单独画一条「已完成」。
 */
export function buildTurnRows(items: Array<StreamEntry | ThoughtFold>, opts: BuildTurnOptions): TranscriptRow[] {
  const rows: TranscriptRow[] = []
  const pending = new Map<TurnModel, RawTurn>()
  let raw: RawTurn | null = null

  const flush = () => {
    if (!raw) return
    if (!raw.user && raw.agent.length === 0) {
      raw = null
      return
    }
    const turn = createTurn(raw, opts)
    pending.set(turn, raw)
    rows.push(turn)
    raw = null
  }

  for (const item of items) {
    if (!isThoughtFold(item) && item.kind === 'user') {
      flush()
      raw = {
        user: item,
        startId: item.id,
        agent: [],
        entryIds: [item.id],
        cancelled: false,
        failed: false,
      }
      continue
    }

    if (!isThoughtFold(item) && (item.kind === 'permission' || item.kind === 'elicitation')) {
      // 确认卡**不切轮**：未结算的不属于历史（只有确认区在画），已结算的按原顺序进入过程流。
      // 此前这里 `flush()` 会导致卡片之后的工具/正文另起一个没有 user 气泡的 Agent 头。
      if (!item.resolved) continue
      raw ??= {
        startId: item.id,
        agent: [],
        entryIds: [],
        cancelled: false,
        failed: false,
      }
      raw.agent.push(item)
      raw.entryIds.push(itemId(item))
      continue
    }

    if (!isThoughtFold(item) && item.kind === 'system') {
      if (raw && raw.agent.length > 0 && (item.tone === 'ok' || item.tone === 'cancel')) {
        raw.endSeq = item.seq
        raw.stopReason = item.stopReason
        raw.cancelled = item.tone === 'cancel'
        continue
      }
      if (raw && item.tone === 'err') raw.failed = true
      flush()
      rows.push({ kind: 'entry', entry: item })
      continue
    }

    if (isThoughtFold(item) || item.kind === 'assistant' || item.kind === 'thought' || item.kind === 'tool') {
      raw ??= {
        startId: item.id,
        agent: [],
        entryIds: [],
        cancelled: false,
        failed: false,
      }
      raw.agent.push(item)
      raw.entryIds.push(itemId(item))
      continue
    }

    flush()
    rows.push({ kind: 'entry', entry: item })
  }
  flush()

  const turns = rows.filter((row): row is TurnModel => row.kind === 'turn')
  const last = turns[turns.length - 1]
  for (const turn of turns) {
    const source = pending.get(turn)
    if (source) finalizeTurn(turn, source, !!opts.streaming && turn === last, opts)
  }
  return rows
}
