import type { WorkspaceListResp } from '@ark-mate/types'

// ——— 工作空间读面的视图模型（v14.1；纯函数，便于 Node 侧校验） ———

/**
 * 「最近使用的工作空间」= 登记表里 `last_used_at` 最新的那一条。
 *
 * 用途只有一个：新建会话面板**默认预选**它（对齐 dsh：新会话落在你上次干活的那个工作空间）。
 * 三条判据都不是外观考量：
 *   - 目录不可用（`dir_status != ok`）的登记**不参选**：选中它等于给用户一个发送必被拒的默认值；
 *   - `last_used_at` 空 = 从未用过，谈不上"最近"；
 *   - 时间比较用 `Date.parse` 而不是字符串比较：服务端下发的是 RFC3339，`+08:00` 与 `Z`
 *     两种写法的字典序与时间序**不一致**（只差时区就会排错）。
 *
 * 返回 null = 没有可预选的登记（memory 持久层 / 服务不可达 / 一条都没用过），
 * 此时调用方保持「未指定」——会话由平台落在默认工作空间，与 v14 的行为逐字相同。
 */
export function mostRecentlyUsedWorkspaceId(list: WorkspaceListResp | null): string | null {
  if (!list) return null
  let bestId: string | null = null
  let bestAt = Number.NEGATIVE_INFINITY
  for (const ws of list.items) {
    if (ws.dir_status !== 'ok' || !ws.last_used_at) continue
    const at = Date.parse(ws.last_used_at)
    if (Number.isNaN(at) || at <= bestAt) continue
    bestId = ws.id
    bestAt = at
  }
  return bestId
}

// ——— 左栏「视图选项」的模型（v14.2） ———

/**
 * 分组方式：`workspace` = 按工作空间分组；`timeline` = 按创建日的平铺时间线。
 *
 * 两者都保留而不是二选一：分组视图回答"我在哪个项目里有哪些会话"，时间线回答
 * "我最近干了什么"。它还有一条硬用途——**登记表不可用**（memory 持久层 / 老服务端）
 * 时没有分组可挂，必须能退回时间线，否则会话列表会整个消失。
 */
export type RailGroupMode = 'workspace' | 'timeline'

/** 视图选项里「会话筛选」的两态。 */
export type RailArchivedFilter = 'hide' | 'only'

export const RAIL_GROUP_MODES: readonly RailGroupMode[] = ['workspace', 'timeline']
export const RAIL_ARCHIVED_FILTERS: readonly RailArchivedFilter[] = ['hide', 'only']

export function railGroupModeLabel(mode: RailGroupMode): string {
  return mode === 'workspace' ? '按工作空间' : '时间线'
}

/**
 * 会话筛选**只有两态**，这是被接口定的，不是界面偷懒：`railMode` 直接决定服务端查询
 * （`listConversations(archived)`），wire 上没有"两条都要"的第三态可表达。
 *
 * 照搬 dsh 的三态（隐藏已归档 / 全部对话 / 仅显示已归档）需要先给 `GET /conversations`
 * 加一个三值过滤参数——那是一次对外契约改动（要同步 README 路由表与 `TestReadmeRoutesMatchRouter`），
 * 不该混在一次界面调整里；更不该用一个"看着能选、选了什么都不变"的菜单项糊过去。
 */
export function railArchivedFilterLabel(filter: RailArchivedFilter): string {
  return filter === 'only' ? '仅显示已归档' : '隐藏已归档'
}

/** 筛选 → `railMode`（store 的 `setRailMode` 是唯一写入口，这里只做映射，不持有状态）。 */
export function archivedFilterToRailMode(filter: RailArchivedFilter): 'active' | 'archived' {
  return filter === 'only' ? 'archived' : 'active'
}

/** `railMode` → 筛选（反向那份，供菜单打勾）。 */
export function railModeToArchivedFilter(mode: 'active' | 'archived'): RailArchivedFilter {
  return mode === 'archived' ? 'only' : 'hide'
}
