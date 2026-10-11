import { useLocation } from 'react-router-dom'
import { Dot, tokens } from '@ark-mate/ui'
import { railContextOf } from '../console/navView'
import { useMate } from '../store'

/**
 * 全局底部状态条（console.html .statusbar）：**常驻在每个页面**的右列底部。
 *
 * 版面：左侧是 eventbus 水位（诊断面），右侧最靠边的是 WS 连接态（常驻状态灯），
 * 中间只在问答页显示会话语义提示。WS 不再在左栏 footer 重复一份。
 */
export function StatusBar() {
  const pathname = useLocation().pathname
  const wsState = useMate((s) => s.wsState)
  const activeId = useMate((s) => s.activeId)
  const views = useMate((s) => s.views)
  const sessions = useMate((s) => s.sessions)

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
    : `eventbus seq ${seq} · 未 attach 会话（共 ${sessions.length} 个）`

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
      <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>{seqText}</span>
      <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 12 }}>
        {railContextOf(pathname) === 'chat' && (
          <span style={{ opacity: 0.85 }}>问答会话绑定唯一 Agent · 切换 Agent = 新建会话</span>
        )}
        {/* 常驻状态灯：永远在最右下角 */}
        <span
          style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}
          title={wsState === 'open' ? '事件流已连接' : '事件流断开后会按会话水位自动续传'}
        >
          <Dot color={wsMeta.color} pulse={'pulse' in wsMeta && !!wsMeta.pulse} size={6} />
          {wsMeta.text}
        </span>
      </span>
    </footer>
  )
}
