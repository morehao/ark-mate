import { useLocation } from 'react-router-dom'
import { Dot, runtimeStateMeta, tokens } from '@ark-mate/ui'
import { railContextOf } from '../console/navView'
import { useMate } from '../store'

/**
 * 全局底部状态条（console.html .statusbar）：**常驻在每个页面**的右列底部。
 *
 * 版面：左侧是 eventbus 水位（诊断面），右侧最靠边的是 WS 连接态（常驻状态灯），
 * 中间那一格**只在底座确实出过问题时出现**。WS 不再在左栏 footer 重复一份。
 *
 * v21：中间那格原来是常驻的底座事实（「会话跑在底座 X · 已停止」）。它有两个毛病：
 * ① 底座由构建期固定、用户没有可选项，对话框页面不该常驻报它（面板与会话头同批删掉）；
 * ② "已停止"根本不是异常——底座按需拉起，空闲时本来就是 stopped，常驻报它等于给每个会话
 *    都挂一条噪声。所以判据收窄成"**有失败记录**"（`last_error`：起不来 / 掉了），
 * 事实源仍是 `GET /runtime/diagnostics`（不让它变成没人读的端点），而"没取到诊断"与
 * "底座坏了"是两件事——前者不画成后者，否则会把人引去查一个不存在的问题。
 */
export function StatusBar() {
  const pathname = useLocation().pathname
  const wsState = useMate((s) => s.wsState)
  const activeId = useMate((s) => s.activeId)
  const views = useMate((s) => s.views)
  const sessions = useMate((s) => s.sessions)
  const runtime = useMate((s) => s.runtime)
  const diag = useMate((s) => s.runtimeDiagnostics)

  const wsMeta =
    wsState === 'open'
      ? { color: tokens.success, text: 'WS 已连接' }
      : wsState === 'connecting'
        ? { color: tokens.primary, text: 'WS 连接中…', pulse: true }
        : wsState === 'closed'
          ? { color: tokens.error, text: 'WS 断开（自动重连中…）' }
          : { color: tokens.textPlaceholder, text: 'WS 未连接' }

  // 位次是**会话内**的（v21）：未 attach 用 0 占位；已 attach 但还没有任何事件是 -1
  //（0 是合法位次，不能当"没有"）。
  const seq = activeId ? (views[activeId]?.lastSeq ?? -1) : 0
  const seqText = activeId
    ? `eventbus seq ${seq} · 已 attach ${activeId.length > 16 ? `${activeId.slice(0, 16)}…` : activeId}`
    : `eventbus seq ${seq} · 未 attach 会话（共 ${sessions.length} 个）`

  // 底座异常（有失败记录）才占位：这里要回答的是"为什么跑不起来 / 为什么刚断了"，
  // 而不是"底座是谁"。措辞保持事实性，出路（修复步骤）在诊断面与医生命令里。
  const err = diag?.last_error
  const runtimeName = runtime?.name || runtime?.id || diag?.runtime_name || diag?.runtime || '底座'

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
        {railContextOf(pathname) === 'chat' && err && (
          <span
            title={[err.details, err.stderr_tail?.length ? err.stderr_tail[err.stderr_tail.length - 1] : ''].filter(Boolean).join('\n')}
            style={{ display: 'inline-flex', alignItems: 'center', gap: 6, color: tokens.error, maxWidth: 420, overflow: 'hidden' }}
          >
            <Dot color={tokens.error} size={5} />
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {runtimeName} 最近一次失败（{err.code || err.class}） · {runtimeStateMeta(diag.state).label}
            </span>
          </span>
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
