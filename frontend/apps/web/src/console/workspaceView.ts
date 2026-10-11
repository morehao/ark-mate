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
