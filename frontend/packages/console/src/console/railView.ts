import type { SessionSummary } from '@ark-mate/types'

// ——— 会话侧栏视图模型（P3 余留：列表分页 + active 提升 + 相对时间；纯函数，便于 Node 侧校验） ———

/**
 * 会话行的**状态徽标**（`标题 + 状态点+状态文案` 那一格）：`null` = 不显示（idle，无话可说）。
 *
 * 取值来源只有两处，都来自后端事实，**绝不猜**（对齐 archtest `TestNoClientSideRunInference`）：
 *   1. `SessionSummary.run`（`run.status` 读面投影，ADR-6）——优先，连没打开过的会话也能说「待授权」；
 *   2. `SessionSummary.status`（REST 快照）——`run` 缺席（历史会话 / 本轮还没开始）时回落。
 *
 * 映射到原型四态：运行中（run）/ 待授权（wait）/ 失败（fail）/ 已完成（done）。
 * `cancelled`、`interrupted`、`idle` 一律不显示——它们不是"在跑"也不是"成功/失败"，
 * 硬塞一个徽标只会把「取消/中断」说成「已完成」。
 */
export type SessionBadgeKind = 'run' | 'wait' | 'fail' | 'done'

export interface SessionBadge {
  readonly kind: SessionBadgeKind
  readonly label: string
}

export function sessionBadge(summary: Pick<SessionSummary, 'status' | 'run'>): SessionBadge | null {
  const run = summary.run
  if (run) {
    switch (run.status) {
      case 'waiting_for_permission':
      case 'waiting_for_user':
        return { kind: 'wait', label: '待授权' }
      case 'preparing':
      case 'model_requesting':
      case 'model_streaming':
      case 'tool_executing':
      case 'retrying':
      case 'compacting':
      case 'cancelling':
        return { kind: 'run', label: '运行中' }
      default:
        // idle / interrupted：落到下面的快照口径（不从这里直接下结论）。
        break
    }
  }
  switch (summary.status) {
    case 'failed':
      return { kind: 'fail', label: '失败' }
    case 'completed':
      return { kind: 'done', label: '已完成' }
    case 'in_progress':
      return { kind: 'run', label: '运行中' }
    default:
      return null
  }
}

/** 一页渲染的会话条数（超出的以「显示更早」增量展开）。 */
export const RAIL_PAGE = 40

export interface RailPage {
  /** 本次渲染的会话（顺序沿用原列表：置顶/日期分组由调用方继续处理）。 */
  visible: SessionSummary[]
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
export function pageSessions(
  all: SessionSummary[],
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
 * 一个工作空间分组默认渲染的**空闲**会话条数（对齐 dsh 的 `COLLAPSED_SESSION_LIMIT`）。
 * 超出的部分收进「展开其余 N 个会话」，由用户按需展开。
 */
export const GROUP_IDLE_LIMIT = 5

export interface GroupQuota {
  /** 本次渲染的会话（原顺序不变：置顶在前、其余按时间，由调用方保证）。 */
  rows: SessionSummary[]
  /** 被收起的条数（0 = 全都在）。 */
  hidden: number
}

/**
 * 组内配额：只对**空闲**会话计数。两类行永远不占配额、也不会被收起——
 *   - **运行中**的会话（`in_progress`）：把它藏起来等于用户看不到自己刚发起的活儿，
 *     而它恰恰是最需要被看见的那一条；
 *   - **当前打开的**会话（`activeId`）：侧栏必须始终能指出"你在哪"。配额一满就把它
 *     挤出列表的话，用户会以为会话丢了（与 P3 `pageSessions` 的提升同一条纪律）。
 *
 * 折叠/展开的状态由调用方持有（它是浏览器本地的视图偏好，不进 db）；本函数只回答
 * "这一页渲染哪些、还剩几条"。`limit <= 0` = 不设配额（调用方要全量时传 0）。
 */
export function quotaSessions(
  all: SessionSummary[],
  limit: number = GROUP_IDLE_LIMIT,
  activeId?: string | null,
): GroupQuota {
  if (limit <= 0) return { rows: all, hidden: 0 }
  let idle = 0
  const rows: SessionSummary[] = []
  for (const c of all) {
    if (c.status === 'in_progress' || c.id === activeId) {
      rows.push(c)
      continue
    }
    if (idle >= limit) continue
    idle += 1
    rows.push(c)
  }
  return { rows, hidden: all.length - rows.length }
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

/**
 * 把一条绝对路径缩成**末两级**（`/a/b/c/d` → `…/c/d`），末尾对齐用户认得出的那一段。
 *
 * 为什么不做"中间省略"（`/a/…/c/d`）：目录名在两端的概率一样高，而末两级几乎总是全部有用
 * 信息所在（项目名 + 子目录）。只有一级时原样返回，**空串/根路径**也原样返回——那两种情况下
 * "缩写"只会得到一个比原文更含糊的东西。
 */
export function shortenPath(path: string): string {
  if (!path || path === '/' || path === '.') return path
  const parts = path.split('/').filter(Boolean)
  if (parts.length <= 2) return path
  return `…/${parts.slice(-2).join('/')}`
}
