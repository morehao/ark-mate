import { tokens } from '@ark-mate/ui'
import type { StreamEntry } from '@ark-mate/types'
import { ElicitationCard, PermissionCard } from './Transcript'

interface Props {
  entries: StreamEntry[]
}

/**
 * **确认区**（S9 / Q2 选①）：输入区上方的常驻块，把「现在需要你做什么」摆在离输入框最近的位置。
 *
 * 四条判据：
 *
 *  1. **优先级：权限 > 提问**。两者都要用户做个决定，但权限是"这一轮卡在这里动不了"，
 *     提问是"这一轮在等一个信息"——前者更急，且它的超时更短。同时有多个时**权限全部排在
 *     提问前面**，但**并列显示**（同优先级按入流顺序）。
 *
 *     O19（已移入 C35）之前只画一张、其余以「还有 N 条」计数：用户必须逐条等下一张出现，而卡片之间
 *     没有先后依赖——藏起来只会把"我还要处理几件事"变成一道来回翻看的记忆题。
 *  2. **空则整块不占位**。留一个空框会让人以为"这里本来要有什么"。
 *  3. 卡片本体复用 `Transcript` 里那两张（`PermissionCard` / `ElicitationCard`）——
 *     **一份实现**：各画一份就是"同一件事在历史里和确认区里长得不一样"。
 *  4. **有高度上限**：并列显示不能把输入框挤出屏幕——超出时确认区自己滚动、标题吸顶。
 *     两张卡各自持有自己的表单状态（`PermissionCard` / `ElicitationCard` 内部 state），
 *     滚动与并列都不影响它们。
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

  // 权限优先（同优先级按入流顺序，先来的先答）；两组都**全量**展开，不折叠、不截断。
  const ordered = [
    ...pending.filter((en) => en.kind === 'permission'),
    ...pending.filter((en) => en.kind !== 'permission'),
  ]

  return (
    <div style={{ flex: 'none', padding: `0 ${tokens.chatColumnGutter}px`, background: tokens.bg }}>
      <div
        style={{
          maxWidth: tokens.chatColumnMaxWidth,
          margin: '0 auto',
          // 上限按视口高给：确认区是"当下的问题"，不该把输入框与最近历史挤出屏幕。
          maxHeight: '46vh',
          overflowY: 'auto',
        }}
      >
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            padding: '6px 10px',
            border: `1px solid ${tokens.primary}`,
            borderRadius: 10,
            marginBottom: 6,
            position: 'sticky',
            top: 0,
            background: tokens.bg,
            zIndex: 1,
          }}
        >
          <span style={{ fontSize: 11.5, fontWeight: 600, color: tokens.text, whiteSpace: 'nowrap' }}>
            需要你确认
          </span>
          <span style={{ flex: 1 }} />
          <span style={{ fontSize: 11, color: tokens.textPlaceholder, whiteSpace: 'nowrap' }}>
            {pending.length} 条
          </span>
        </div>
        {ordered.map((en) =>
          en.kind === 'permission' ? (
            <PermissionCard key={en.id} e={en} />
          ) : (
            <ElicitationCard key={en.id} e={en} />
          ),
        )}
      </div>
    </div>
  )
}
