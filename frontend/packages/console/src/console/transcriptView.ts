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
