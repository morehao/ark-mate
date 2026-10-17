import { tokens } from '@ark-mate/ui'
import type { StreamEntry } from '@ark-mate/types'
import { ElicitationCard, PermissionCard } from './Transcript'

interface Props {
  entries: StreamEntry[]
}

/**
 * **确认区**（S9 / Q2 选①）：输入区上方的常驻块，把「现在需要你做什么」摆在离输入框最近的位置。
 *
 * 三条判据：
 *
 *  1. **优先级：权限 > 提问**。两者都要用户做个决定，但权限是"这一轮卡在这里动不了"，
 *     提问是"这一轮在等一个信息"——前者更急，且它的超时更短。同时有多个时**先显示权限**，
 *     其余的排在其后（计数如实说，不藏起来）。
 *  2. **空则整块不占位**。留一个空框会让人以为"这里本来要有什么"。
 *  3. 卡片本体复用 `Transcript` 里那两张（`PermissionCard` / `ElicitationCard`）——
 *     **一份实现**：各画一份就是"同一件事在历史里和确认区里长得不一样"。
 *
 * 这不是"多一个固定面板"：条目流里未结算的卡已经不再渲染（见 `EntryView` 的那两个 case），
 * 所以用户能作答的地方**只有这里**。两处都画等于给同一张卡两个入口，而它们各自持有自己的一份
 * 表单状态。
 */
export function ConfirmArea({ entries }: Props) {
  const pending = entries.filter(
    (en) => (en.kind === 'permission' || en.kind === 'elicitation') && !en.resolved,
  )
  if (pending.length === 0) return null

  // 权限优先（同优先级按入流顺序，先来的先答）。
  const head = pending.find((en) => en.kind === 'permission') ?? pending[0]
  const rest = pending.length - 1

  return (
    <div style={{ flex: 'none', padding: '0 20px', background: tokens.bg }}>
      <div style={{ maxWidth: 840, margin: '0 auto' }}>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            padding: '6px 10px',
            border: `1px solid ${tokens.primary}`,
            borderRadius: 10,
            marginBottom: 6,
          }}
        >
          <span style={{ fontSize: 11.5, fontWeight: 600, color: tokens.text, whiteSpace: 'nowrap' }}>
            需要你确认
          </span>
          <span style={{ flex: 1 }} />
          {rest > 0 && (
            <span style={{ fontSize: 11, color: tokens.textPlaceholder, whiteSpace: 'nowrap' }}>
              还有 {rest} 条
            </span>
          )}
        </div>
        {head.kind === 'permission' ? <PermissionCard e={head} /> : <ElicitationCard e={head} />}
      </div>
    </div>
  )
}
