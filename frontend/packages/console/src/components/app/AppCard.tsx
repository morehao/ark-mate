import { type ReactNode } from 'react'
import { tokens } from '@ark-mate/ui'
import { CapabilityCard } from '../capability/shared'

/**
 * 应用卡片（应用市场 / 我的应用共用，2026-11-11 抽自 AppMarketPanel 的 MarketCard）。
 *
 * 骨架 = 能力卡片 `CapabilityCard`：`icon` 头像块 · `name`（+ 可选 `meta` 元信息）·
 * `statusTag`（状态点）→ `description` → 底栏 `actions`（动作常显）。
 * 与市场卡片渲染风格保持一致：单面白卡、hover 抬边、动作右端对齐。
 *
 * `onOpenDetail` 给出时，点卡片主体（头像/名称/描述区）可打开详情——市场卡片不传，保持无点击态。
 */
export function AppCard({
  icon,
  name,
  version,
  description,
  statusTag,
  meta,
  actions,
  onOpenDetail,
  dim,
}: {
  icon: ReactNode
  name: ReactNode
  /** 名称旁的小号版本标注（市场卡片用，`v{version}`）。 */
  version?: string
  description?: ReactNode
  statusTag?: ReactNode
  /** 底栏左侧元信息（id / 标签 chips 等）。缺省时底栏留空。 */
  meta?: ReactNode
  actions: ReactNode
  onOpenDetail?: () => void
  /** 置灰（如市场里「需远程分发」不可装的条目）。 */
  dim?: boolean
}) {
  const title = version ? (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 7 }}>
      {name}
      <span style={{ fontSize: 10.5, color: tokens.textPlaceholder, fontFamily: tokens.fontMono, fontWeight: 400 }}>
        v{version}
      </span>
    </span>
  ) : (
    name
  )

  return (
    <div
      onClick={onOpenDetail}
      style={onOpenDetail ? { cursor: 'pointer', minWidth: 0 } : { minWidth: 0 }}
    >
      <CapabilityCard
        dim={dim}
        avatar={icon}
        title={title}
        tags={statusTag}
        description={description ?? '（无摘要）'}
        meta={meta ?? null}
        actions={actions}
      />
    </div>
  )
}
