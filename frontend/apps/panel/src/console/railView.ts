import type { ConversationSummary } from '@ark/types'

// ——— 会话侧栏视图模型（P3 余留：列表分页 + active 提升 + 相对时间；纯函数，便于 Node 侧校验） ———

/** 一页渲染的会话条数（超出的以「显示更早」增量展开）。 */
export const RAIL_PAGE = 40

export interface RailPage {
  /** 本次渲染的会话（顺序沿用原列表：置顶/日期分组由调用方继续处理）。 */
  visible: ConversationSummary[]
  /** 尚未渲染的条数（0 = 已全部展开）。 */
  remaining: number
  /**
   * 当前会话是否因分页被额外提升进本页（它排在列表末尾之外）。
   * 提升只保证「可见」——不做置顶排序（分组语义不变）。
   */
  promoted: boolean
}

/**
 * 按 `limit` 截断会话列表；若当前会话（activeId）不在页内则额外纳入并置页首，
 * 保证「打开任意会话时它在侧栏始终可见」（深链/搜索跳转/长列表）。
 */
export function pageConversations(
  all: ConversationSummary[],
  limit: number = RAIL_PAGE,
  activeId?: string | null,
): RailPage {
  if (limit <= 0) return { visible: all, remaining: 0, promoted: false }
  if (all.length <= limit) return { visible: all, remaining: 0, promoted: false }
  const head = all.slice(0, limit)
  if (!activeId || head.some((c) => c.id === activeId)) {
    return { visible: head, remaining: all.length - head.length, promoted: false }
  }
  const active = all.find((c) => c.id === activeId)
  if (!active) return { visible: head, remaining: all.length - head.length, promoted: false }
  const visible = [active, ...head.slice(0, limit - 1)]
  return { visible, remaining: all.length - visible.length, promoted: true }
}

/**
 * 相对时间标签（列表行用）：刚刚 / N 分钟前 / N 小时前 / 昨天 / N 天前 / MM-DD / YYYY-MM-DD。
 * `now` 可注入（测试稳定）；非法/空时间 → 空串（不显示噪声）。
 */
export function relTime(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return ''
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const diff = now - t
  if (diff < 0) return '刚刚' // 时钟偏差（服务端时间略超前）不显示「-1 分钟」
  const min = Math.floor(diff / 60_000)
  if (min < 1) return '刚刚'
  if (min < 60) return `${min} 分钟前`
  const hours = Math.floor(min / 60)
  if (hours < 24) return `${hours} 小时前`
  const days = Math.floor(hours / 24)
  if (days === 1) return '昨天'
  if (days < 7) return `${days} 天前`
  const d = new Date(t)
  const p = (n: number): string => String(n).padStart(2, '0')
  const sameYear = new Date(now).getFullYear() === d.getFullYear()
  return sameYear ? `${p(d.getMonth() + 1)}-${p(d.getDate())}` : `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}
