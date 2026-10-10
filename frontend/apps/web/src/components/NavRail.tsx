import { useEffect, useState } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router-dom'
import { DownOutlined, LockOutlined, RightOutlined, UnlockOutlined } from '@ant-design/icons'
import { Button, Input, Modal } from 'antd'
import { tokens } from '@ark-mate/ui'
import { useMate } from '../store'
import {
  NAV_ENTRIES,
  activeChildOf,
  groupExpanded,
  isChildActive,
  isEntryActive,
  isGroup,
  railContextOf,
  type NavEntry,
  type NavGroup,
  type NavLeaf,
} from '../console/navView'
import { ConversationPane } from './ConversationPane'

/**
 * 常驻左栏（唯一导航面）：品牌 + 一级/二级导航 + 会话区 + 状态 footer。
 *
 * 纪律：导航条目只在 `console/navView.ts` 定义；交互态（hover / 选中 / 展开）只在
 * `console.css.ts` 的 `.fg-rail*` / `.fg-nav*` 类里；本组件不写新的样式单。
 */
export function NavRail() {
  const location = useLocation()
  const navigate = useNavigate()
  const activate = useMate((s) => s.activate)
  const setDrawer = useMate((s) => s.setDrawer)
  const drawerOpen = useMate((s) => s.drawerOpen)
  const navExpanded = useMate((s) => s.navExpanded)
  const setNavGroupExpanded = useMate((s) => s.setNavGroupExpanded)
  const conversations = useMate((s) => s.conversations)

  const wsState = useMate((s) => s.wsState)
  const token = useMate((s) => s.token)
  const tokenModalOpen = useMate((s) => s.tokenModalOpen)
  const saveToken = useMate((s) => s.saveToken)
  const setTokenModal = useMate((s) => s.setTokenModal)
  const [draft, setDraft] = useState(token)

  // 弹窗打开时同步当前令牌（外部清除后再次打开不应显示旧值）。
  useEffect(() => {
    if (tokenModalOpen) setDraft(token)
  }, [tokenModalOpen, token])

  const pathname = location.pathname
  const showsConversations = railContextOf(pathname) === 'chat'

  // 左栏的「新建会话」：问答区落回新建面板（原 ConversationRail 的 onCreate 语义）。
  const openNew = () => {
    activate(null)
    if (pathname !== '/conversations') navigate('/conversations')
    setDrawer(false)
  }

  /** 点导航项后收窄屏抽屉（桌面态 setDrawer(false) 无副作用）。 */
  const leave = () => setDrawer(false)

  const renderChild = (child: NavLeaf) => {
    if (child.pending) {
      return (
        <span
          key={child.id}
          className="fg-navsub fg-pending"
          aria-disabled="true"
          title={child.hint ?? '未实现'}
        >
          <span className="fg-navsub-label">{child.label}</span>
          <span className="fg-navsub-tag">待开发</span>
        </span>
      )
    }
    const on = isChildActive(pathname, child)
    return (
      <NavLink
        key={child.id}
        to={child.to}
        className={`fg-navsub${on ? ' fg-on' : ''}`}
        aria-current={on ? 'page' : undefined}
        onClick={leave}
      >
        {child.label}
      </NavLink>
    )
  }

  const renderGroup = (group: NavGroup) => {
    const expanded = groupExpanded(navExpanded, pathname, group)
    const on = isEntryActive(pathname, group)
    const current = activeChildOf(pathname, group)
    return (
      <div className="fg-navgroup" key={group.id}>
        <div className={`fg-navitem fg-navgroup-row${on ? ' fg-on' : ''}`}>
          {/* 文字区：进总览 + 展开；箭头区：只切换展开（设计 §关键逻辑） */}
          <button
            type="button"
            className="fg-navgroup-text"
            onClick={() => {
              if (!expanded) setNavGroupExpanded(group.id, true)
              navigate(group.to)
              leave()
            }}
          >
            {group.label}
          </button>
          <button
            type="button"
            className="fg-navgroup-caret"
            aria-expanded={expanded}
            aria-label={`${expanded ? '收起' : '展开'}「${group.label}」`}
            onClick={() => setNavGroupExpanded(group.id, !expanded)}
          >
            {expanded ? <DownOutlined style={{ fontSize: 10 }} /> : <RightOutlined style={{ fontSize: 10 }} />}
          </button>
        </div>
        {expanded && (
          <div className="fg-navsub-list" role="group" aria-label={group.label}>
            {group.children.map(renderChild)}
            {/* 当前命中二级项：用于父组弱选中态的可读提示（视觉在 CSS） */}
            {current && <span className="fg-visually-hidden">当前：{current.label}</span>}
          </div>
        )}
      </div>
    )
  }

  const renderEntry = (entry: NavEntry) => {
    if (isGroup(entry)) return renderGroup(entry)
    const on = isEntryActive(pathname, entry)
    return (
      <NavLink
        key={entry.id}
        to={entry.to}
        className={`fg-navitem${on ? ' fg-on' : ''}`}
        aria-current={on ? 'page' : undefined}
        onClick={leave}
      >
        <span className="fg-navitem-label">{entry.label}</span>
        {entry.id === 'chat' && conversations.length > 0 && (
          <span className="fg-navitem-count">{String(conversations.length).padStart(2, '0')}</span>
        )}
      </NavLink>
    )
  }

  const wsMeta =
    wsState === 'open'
      ? { color: tokens.success, text: 'WS 已连接' }
      : wsState === 'connecting'
        ? { color: tokens.primary, text: '连接中…', pulse: true }
        : wsState === 'closed'
          ? { color: tokens.error, text: 'WS 断开' }
          : { color: tokens.textPlaceholder, text: 'WS 未连接', pulse: false }

  return (
    <aside className={`fg-rail${drawerOpen ? ' fg-open' : ''}`} aria-label="主导航">
      <NavLink to="/conversations" className="fg-rail-brand" title="ark-mate 问答控制台" onClick={leave}>
        ark<span style={{ color: tokens.primary }}>·</span>mate
      </NavLink>

      <nav className="fg-nav-rail" aria-label="视图切换">
        {NAV_ENTRIES.map(renderEntry)}
      </nav>

      {showsConversations && <ConversationPane onCreate={openNew} />}

      <div className="fg-rail-foot">
        <span className="fg-rail-ws">
          <span
            className={wsMeta.pulse ? 'fg-anim-pulse' : undefined}
            style={{ flex: 'none', width: 7, height: 7, borderRadius: '50%', background: wsMeta.color, display: 'inline-block' }}
          />
          {wsMeta.text}
        </span>

        {/* A6 单操作员令牌：后端启用鉴权时此处设置；未设置时按钮呈灰色提示 */}
        <button
          type="button"
          className="fg-rail-token"
          onClick={() => setTokenModal(true)}
          title={token ? '已配置访问令牌（点击修改/清除）' : '未配置访问令牌（后端启用 MATE_TOKEN 时必填）'}
        >
          {token ? <LockOutlined style={{ fontSize: 12 }} /> : <UnlockOutlined style={{ fontSize: 12 }} />}
          令牌
        </button>
      </div>

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
    </aside>
  )
}
