import { tokens } from '@ark/ui'
import { Dot } from '@ark/ui'
import { useForge } from '../store'

/** 底部状态条（console.html .statusbar）：WS 连接态 + eventbus 水位 + 语义提示。 */
export function StatusBar() {
  const wsState = useForge((s) => s.wsState)
  const activeId = useForge((s) => s.activeId)
  const views = useForge((s) => s.views)
  const conversations = useForge((s) => s.conversations)

  const wsMeta =
    wsState === 'open'
      ? { color: tokens.success, text: 'WS 已连接' }
      : wsState === 'connecting'
        ? { color: tokens.primary, text: 'WS 连接中…', pulse: true }
        : wsState === 'closed'
          ? { color: tokens.error, text: 'WS 断开（自动重连中…）' }
          : { color: tokens.textPlaceholder, text: 'WS 未连接' }

  const seq = activeId ? (views[activeId]?.lastSeq ?? 0) : 0
  const seqText = activeId
    ? `eventbus seq ${seq} · 已 attach ${activeId.length > 16 ? `${activeId.slice(0, 16)}…` : activeId}`
    : `eventbus seq ${seq} · 未 attach 会话（共 ${conversations.length} 个）`

  return (
    <footer
      style={{
        flex: 'none',
        height: 30,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        gap: 14,
        padding: '0 16px',
        borderTop: `1px solid ${tokens.border}`,
        background: tokens.surface,
        fontFamily: tokens.fontMono,
        fontSize: 10.5,
        color: tokens.textSecondary,
        overflow: 'hidden',
        whiteSpace: 'nowrap',
      }}
    >
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 10, minWidth: 0, overflow: 'hidden' }}>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <Dot color={wsMeta.color} pulse={'pulse' in wsMeta && !!wsMeta.pulse} size={6} />
          {wsMeta.text}
        </span>
        <span style={{ opacity: 0.6 }}>·</span>
        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{seqText}</span>
      </span>
      <span style={{ flex: 'none', opacity: 0.85 }}>问答会话绑定唯一 Agent · 切换 Agent = 新建会话</span>
    </footer>
  )
}
