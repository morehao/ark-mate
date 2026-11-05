import type { SessionSummary } from '@ark-mate/types'

// ——— 会话侧栏视图模型（P3 余留：列表分页 + active 提升 + 状态点；纯函数，便于 Node 侧校验） ———
//
// 时间格式化已收口到 `console/time.ts`（后端返回 Unix 毫秒），本文件不再解析/格式化时间。

/**
 * 会话行的**状态点**（行右端 `● 日期` 里那颗点）：`null` = 不画点（只可能是服务端给了没见过的取值）。
 *
 * 取值来源只有两处，都来自后端事实，**绝不猜**（对齐 archtest `TestNoClientSideRunInference`）：
 *   1. `SessionSummary.run`（`run.status` 读面投影，ADR-6）——优先，连没打开过的会话也能说「待授权」；
 *   2. `SessionSummary.status`（REST 快照）——`run` 缺席（历史会话 / 本轮还没开始）时回落。
 *
 * 六态（2026-11-05 反馈：取消/中断也要有自己的颜色，已完成不再用灰）：
 *   运行中（run，蓝 + 呼吸）/ 待授权（wait，琥珀实心）/ 失败（fail，红）/ 已完成（done，绿）/
 *   已取消（cancel，灰——作废，不是失败）/ 已中断（interrupt，琥珀**空心环**——与「待授权」同族
 *   但形态不同：中断是"上一轮没正常收尾、可重发"，不是"现在在等你"）。
 *
 * 为什么取消/中断不再"不显示"：不给点等于让它们跟「已完成」长得一样（都只剩日期），
 * 而这两件事对用户的意义完全不同（作废 vs 可以重来）。颜色只表达**事实**，不表达"好坏"。
 */
export type SessionBadgeKind = 'run' | 'wait' | 'fail' | 'done' | 'cancel' | 'interrupt'

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
    case 'cancelled':
      return { kind: 'cancel', label: '已取消' }
    case 'interrupted':
      return { kind: 'interrupt', label: '已中断' }
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
