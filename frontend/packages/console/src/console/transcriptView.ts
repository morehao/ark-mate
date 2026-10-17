import type { StreamEntry } from '@ark-mate/types'

// ——— 会话流视图模型（P3 余留：长会话只渲染尾部窗口 + 滚动跟随判定；纯函数，便于 Node 侧校验） ———

/** 首屏渲染的条目数（更早的按需展开，避免长会话一次性挂满 DOM）。 */
export const TRANSCRIPT_WINDOW = 200

export interface EntryWindow<T> {
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

export interface ScrollMetrics {
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
export interface TurnStatus {
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
  return { stage: hasBody ? narrationLabel || '正在输出' : '思考中', showCaret: !hasBody }
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
