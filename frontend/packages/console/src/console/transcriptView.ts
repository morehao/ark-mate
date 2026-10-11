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
 * C2「加载更早」的 before 游标：当前视图中最小的正 seq（无 → 0）。
 * 0 表示「还没有任何事件」——后端把 before=0 当「取末尾一页」处理，正好覆盖冷挂载。
 */
export function olderCursor(entries: StreamEntry[]): number {
  let min = 0
  for (const e of entries) {
    const n = e.seq
    if (typeof n !== 'number' || n <= 0) continue
    if (min === 0 || n < min) min = n
  }
  return min
}

/**
 * 把「更早一页」的条目前插到当前条目之前（C2）。
 * 契约：更早页的 seq 全部小于当前最小 seq（服务端 before 开区间保证），故直接拼接即可；
 * 仍做一层防御——过滤掉 seq ≥ `cursor` 的条目，避免异常响应把历史插到中间。
 */
export function prependOlder(older: StreamEntry[], current: StreamEntry[], cursor: number): StreamEntry[] {
  if (older.length === 0) return current
  const fresh = cursor > 0 ? older.filter((e) => typeof e.seq !== 'number' || e.seq < cursor) : older
  if (fresh.length === 0) return current
  return [...fresh, ...current]
}
