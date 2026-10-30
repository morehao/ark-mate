import { useLocation } from 'react-router-dom'
import { Dot, tokens } from '@ark-mate/ui'
import { railContextOf } from '../console/navView'
import { useMate } from '../store'

/**
 * 全局底部状态条：左侧一句概览，右侧最靠边的是连接状态灯。
 *
 * 面向普通用户：不展示 eventbus / seq / attach 这类内部诊断词；连接异常只在真出问题时出现，
 * 且给出用户能理解的说明。真正的诊断信息在诊断面与日志里。
 */
export function StatusBar() {
  const pathname = useLocation().pathname
  const wsState = useMate((s) => s.wsState)
  const sessions = useMate((s) => s.sessions)
  const diag = useMate((s) => s.runtimeDiagnostics)

  const wsMeta =
    wsState === 'open'
      ? { color: tokens.success, text: '已连接' }
      : wsState === 'connecting'
        ? { color: tokens.primary, text: '连接中…', pulse: true }
        : wsState === 'closed'
          ? { color: tokens.error, text: '连接断开（自动重连中…）' }
          : { color: tokens.textPlaceholder, text: '未连接' }

  const leftText = sessions.length > 0 ? `共 ${sessions.length} 个会话` : ''

  // 只有确实出过问题才占位：这里回答的是"为什么跑不起来 / 为什么刚断了"。
  const err = diag?.last_error

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
        fontSize: 10.5,
        color: tokens.textSecondary,
        overflow: 'hidden',
        whiteSpace: 'nowrap',
      }}
    >
      <span style={{ minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>{leftText}</span>
      <span style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 12 }}>
        {railContextOf(pathname) === 'chat' && err && (
          <span
            title={[err.details, err.stderr_tail?.length ? err.stderr_tail[err.stderr_tail.length - 1] : ''].filter(Boolean).join('\n')}
            style={{ display: 'inline-flex', alignItems: 'center', gap: 6, color: tokens.error, maxWidth: 420, overflow: 'hidden' }}
          >
            <Dot color={tokens.error} size={5} />
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
              助手最近一次运行失败（{err.code || err.class}）
            </span>
          </span>
        )}
        {/* 常驻状态灯：永远在最右下角 */}
        <span
          style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}
          title={wsState === 'open' ? '连接正常' : '断开后会自动重连'}
        >
          <Dot color={wsMeta.color} pulse={'pulse' in wsMeta && !!wsMeta.pulse} size={6} />
          {wsMeta.text}
        </span>
      </span>
    </footer>
  )
}
