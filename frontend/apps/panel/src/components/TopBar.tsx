import type { CSSProperties } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import { MenuOutlined } from '@ant-design/icons'
import { tokens } from '@ark/ui'
import { useForge } from '../store'

const NAV = [
  { to: '/conversations', label: '问答' },
  { to: '/agents', label: 'Agents' },
  { to: '/settings', label: '设置' },
] as const

function isNavActive(pathname: string, to: string): boolean {
  if (to === '/conversations') return pathname.startsWith('/conversations')
  return pathname === to
}

function navStyle(active: boolean): CSSProperties {
  return {
    color: active ? tokens.text : tokens.textSecondary,
    textDecoration: 'none',
    fontSize: 13,
    fontWeight: active ? 600 : 400,
    borderBottom: active ? `2px solid ${tokens.primary}` : '2px solid transparent',
    padding: '4px 2px 2px',
    whiteSpace: 'nowrap',
  }
}

/** 顶栏（console.html .appbar）：burger + logo + 视图 chip + 管理入口 + WS 状态。 */
export function TopBar() {
  const location = useLocation()
  const conversations = useForge((s) => s.conversations)
  const wsState = useForge((s) => s.wsState)
  const setDrawer = useForge((s) => s.setDrawer)

  const current = NAV.find((n) => isNavActive(location.pathname, n.to)) ?? NAV[0]
  const others = NAV.filter((n) => n !== current)

  const wsMeta =
    wsState === 'open'
      ? { color: tokens.success, text: 'WS 已连接' }
      : wsState === 'connecting'
        ? { color: tokens.primary, text: '连接中…' }
        : wsState === 'closed'
          ? { color: tokens.error, text: 'WS 断开' }
          : { color: tokens.textPlaceholder, text: 'WS 未连接' }

  return (
    <header
      style={{
        flex: 'none',
        display: 'flex',
        alignItems: 'center',
        gap: 14,
        height: 52,
        padding: '0 16px',
        background: tokens.surface,
        borderBottom: `1px solid ${tokens.border}`,
        position: 'relative',
        zIndex: 20,
      }}
    >
      <button
        type="button"
        className="fg-burger"
        aria-label="打开会话列表"
        onClick={() => setDrawer(true)}
        style={{
          width: 34,
          height: 34,
          placeItems: 'center',
          borderRadius: 8,
          border: `1px solid ${tokens.border}`,
          color: tokens.text,
          background: tokens.surface,
          cursor: 'pointer',
        }}
      >
        <MenuOutlined style={{ fontSize: 15 }} />
      </button>

      <NavLink
        to="/conversations"
        title="ark-forge 问答控制台"
        style={{
          fontSize: 15,
          fontWeight: 660,
          letterSpacing: '-0.02em',
          color: tokens.text,
          textDecoration: 'none',
          whiteSpace: 'nowrap',
        }}
      >
        ark<span style={{ color: tokens.primary }}>·</span>forge
      </NavLink>

      <span style={{ width: 1, height: 18, background: tokens.borderStrong, flex: 'none' }} aria-hidden="true" />

      <span
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 7,
          padding: '5px 11px',
          borderRadius: 8,
          background: tokens.softFill,
          fontWeight: 600,
          fontSize: 13,
          color: tokens.text,
          whiteSpace: 'nowrap',
        }}
      >
        {current.label}
        {current.to === '/conversations' && (
          <span style={{ fontFamily: tokens.fontMono, fontSize: 11, color: tokens.textSecondary }}>
            {String(conversations.length).padStart(2, '0')}
          </span>
        )}
      </span>

      <span style={{ flex: 1 }} aria-hidden="true" />

      <nav className="fg-nav" style={{ display: 'flex', gap: 16, alignItems: 'center' }} aria-label="视图切换">
        {others.map((n) => (
          <NavLink key={n.to} to={n.to} style={() => navStyle(isNavActive(location.pathname, n.to))}>
            {n.label}
          </NavLink>
        ))}
      </nav>

      <span
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 7,
          fontFamily: tokens.fontMono,
          fontSize: 11.5,
          color: tokens.textSecondary,
          whiteSpace: 'nowrap',
        }}
      >
        <span
          className={wsState === 'connecting' ? 'fg-anim-pulse' : undefined}
          style={{ flex: 'none', width: 7, height: 7, borderRadius: '50%', background: wsMeta.color, display: 'inline-block' }}
        />
        {wsMeta.text}
      </span>
    </header>
  )
}
