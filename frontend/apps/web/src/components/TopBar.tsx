import { useEffect, useState, type CSSProperties } from 'react'
import { NavLink, useLocation } from 'react-router-dom'
import { LockOutlined, MenuOutlined, UnlockOutlined } from '@ant-design/icons'
import { Button, Input, Modal } from 'antd'
import { tokens } from '@ark/ui'
import { useMate } from '../store'

const NAV = [
  { to: '/conversations', label: '问答' },
  { to: '/agents', label: 'Agents' },
  { to: '/capabilities', label: '能力' },
  { to: '/usage', label: '用量' },
  { to: '/automations', label: '定时任务' },
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
  const conversations = useMate((s) => s.conversations)
  const wsState = useMate((s) => s.wsState)
  const setDrawer = useMate((s) => s.setDrawer)

  const token = useMate((s) => s.token)
  const tokenModalOpen = useMate((s) => s.tokenModalOpen)
  const saveToken = useMate((s) => s.saveToken)
  const setTokenModal = useMate((s) => s.setTokenModal)
  const [draft, setDraft] = useState(token)

  // 弹窗打开时同步当前令牌（外部清除后再次打开不应显示旧值）。
  useEffect(() => {
    if (tokenModalOpen) setDraft(token)
  }, [tokenModalOpen, token])

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
        title="ark-mate 问答控制台"
        style={{
          fontSize: 15,
          fontWeight: 660,
          letterSpacing: '-0.02em',
          color: tokens.text,
          textDecoration: 'none',
          whiteSpace: 'nowrap',
        }}
      >
        ark<span style={{ color: tokens.primary }}>·</span>mate
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

      {/* A6 单操作员令牌：后端启用鉴权时此处设置；未设置时按钮呈灰色提示 */}
      <button
        type="button"
        onClick={() => setTokenModal(true)}
        title={token ? '已配置访问令牌（点击修改/清除）' : '未配置访问令牌（后端启用 MATE_TOKEN 时必填）'}
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 5,
          padding: '4px 9px',
          borderRadius: 8,
          border: `1px solid ${token ? tokens.borderStrong : tokens.border}`,
          background: token ? tokens.softFill : 'transparent',
          color: token ? tokens.text : tokens.textPlaceholder,
          fontSize: 11.5,
          cursor: 'pointer',
          whiteSpace: 'nowrap',
        }}
      >
        {token ? <LockOutlined style={{ fontSize: 12 }} /> : <UnlockOutlined style={{ fontSize: 12 }} />}
        令牌
      </button>

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

      <Modal
        title="访问令牌（单操作员）"
        open={tokenModalOpen}
        onCancel={() => setTokenModal(false)}
        onOk={() => saveToken(draft)}
        okText="保存并重连"
        cancelText="取消"
        width={460}
      >
        <div style={{ fontSize: 12.5, lineHeight: 1.8, color: tokens.textSecondary, marginBottom: 10 }}>
          后端以 <code style={{ fontFamily: tokens.fontMono }}>MATE_TOKEN</code>（或
          <code style={{ fontFamily: tokens.fontMono }}> .ark-mate/config</code> 的 <code>token</code>）启用鉴权后，
          除 <code>/health</code> 外的所有接口都需要该令牌。令牌只保存在本浏览器（localStorage），
          不会写入 URL 或日志。
        </div>
        <Input.Password
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onPressEnter={() => saveToken(draft)}
          placeholder="粘贴 MATE_TOKEN；留空 = 清除"
          autoComplete="off"
        />
        <div style={{ marginTop: 10 }}>
          <Button size="small" onClick={() => saveToken('')} disabled={!token && !draft}>
            清除令牌
          </Button>
        </div>
      </Modal>
    </header>
  )
}
