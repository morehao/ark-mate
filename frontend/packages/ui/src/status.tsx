import type { CSSProperties, ReactNode } from 'react'
import { tokens } from './theme'

export interface StateMeta {
  label: string
  color: string
}

/** 会话状态 → 文案/语义色（backend SessionStatus）。 */
export function sessionStatusMeta(status: string): StateMeta {
  switch (status) {
    case 'in_progress':
      return { label: '进行中', color: tokens.primary }
    case 'completed':
      return { label: '已完成', color: tokens.success }
    case 'cancelled':
      return { label: '已取消', color: tokens.warning }
    case 'failed':
      return { label: '失败', color: tokens.error }
    case 'interrupted':
      return { label: '已中断（重启前）', color: tokens.warning }
    default:
      return { label: status, color: tokens.textSecondary }
  }
}

/** Agent 状态 → 文案/语义色。 */
export function agentStateMeta(state: string): StateMeta {
  return state === 'running' ? { label: '运行中', color: tokens.success } : { label: '已停止', color: tokens.textSecondary }
}

export function Dot({ color, pulse, size = 7 }: { color: string; pulse?: boolean; size?: number }) {
  return (
    <span
      className={pulse ? 'fg-anim-pulse' : undefined}
      style={{ flex: 'none', width: size, height: size, borderRadius: '50%', background: color, display: 'inline-block' }}
    />
  )
}

/** 等宽小胶囊（agent 名 / runtime / 能力 chips；状态语义色通过 color 传入）。 */
export function Pill({
  children,
  color,
  title,
  mono = true,
  maxWidth,
}: {
  children: ReactNode
  color?: string
  title?: string
  mono?: boolean
  maxWidth?: number
}) {
  const style: CSSProperties = {
    display: 'inline-flex',
    alignItems: 'center',
    gap: 5,
    border: `1px solid ${tokens.border}`,
    borderRadius: 999,
    padding: '1px 8px',
    color: color ?? tokens.textSecondary,
    whiteSpace: 'nowrap',
    lineHeight: 1.6,
    ...(mono ? { fontFamily: tokens.fontMono, fontSize: 10.5 } : { fontSize: 12 }),
    ...(maxWidth ? { maxWidth, overflow: 'hidden', textOverflow: 'ellipsis' } : {}),
  }
  return (
    <span title={title} style={style}>
      {children}
    </span>
  )
}
