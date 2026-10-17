import { NavLink, useLocation, useNavigate } from 'react-router-dom'
import { DownOutlined, PlusCircleOutlined, RightOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { useMate } from '../store'
import {
  NAV_ENTRIES,
  activeChildOf,
  groupExpanded,
  isChildActive,
  isEntryActive,
  isGroup,
  type NavEntry,
  type NavGroup,
  type NavLeaf,
} from '../console/navView'
import { SETTINGS_MENU } from '../console/settingsView'
import { SessionPane } from './SessionPane'

/**
 * 常驻左栏（唯一导航面）：品牌 + 一级/二级导航 + 会话区 + footer。
 * footer 只有「设置」一个入口：点一下直接开设置弹窗（分节在弹窗内切，含原来的「令牌」——
 * 见 console/settingsView.tsx 与 components/settings/SettingsDialog.tsx）。
 *
 * 纪律：导航条目只在 `console/navView.tsx` 定义；交互态（hover / 选中 / 展开）只在
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

  const openSettings = useMate((s) => s.openSettings)

  const pathname = location.pathname

  // 左栏顶部的「新建会话」（v14.1）：问答区落回新建面板（原 SessionRail 的 onCreate 语义）。
  // 它是**唯一的**新建入口——原先在会话面板头部还有一颗小按钮，两处并存的代价是
  // "新建"这件事看起来属于会话列表的一个筛选器，而不是这一栏的第一动作（对齐 dsh 的 sidebar）。
  const openNew = () => {
    activate(null)
    if (pathname !== '/sessions') navigate('/sessions')
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
            <span className="fg-navitem-icon">{group.icon}</span>
            <span className="fg-navitem-label">{group.label}</span>
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
        <span className="fg-navitem-icon">{entry.icon}</span>
        <span className="fg-navitem-label">{entry.label}</span>
      </NavLink>
    )
  }

  return (
    <aside className={`fg-rail${drawerOpen ? ' fg-open' : ''}`} aria-label="主导航">
      <NavLink to="/sessions" className="fg-rail-brand" title="ark-mate 问答控制台" onClick={leave}>
        ark<span style={{ color: tokens.primary }}>·</span>mate
      </NavLink>

      {/* 新建会话：品牌下方常驻的全宽按钮（v14.1）。它是这一栏的**第一动作**——
          从任何页面点一下都回到新建面板；样式只在这个类里（console.css.ts）。 */}
      {/* `/sessions` 没有一级导航项了（问答入口已去重，只留这一颗新建按钮）：
          用 `fg-on` 补「你在哪」——否则这个页面上左栏一处选中态都没有。
          `/sessions/:id` 不亮：那时用户在看具体会话，不是「新建面板」。 */}
      <button
        type="button"
        className={`fg-rail-new${pathname === '/sessions' ? ' fg-on' : ''}`}
        aria-current={pathname === '/sessions' ? 'page' : undefined}
        onClick={openNew}
      >
        <PlusCircleOutlined className="fg-rail-new-icon" />
        <span>新建会话</span>
      </button>

      <nav className="fg-nav-rail" aria-label="视图切换">
        {NAV_ENTRIES.map(renderEntry)}
      </nav>

      {/* 会话区常驻：不随路由出现/消失（切页不跳版），点会话/新建会话自带跳转到问答页 */}
      <SessionPane onCreate={openNew} />

      <div className="fg-rail-foot">
        {/* 固定左下：设置（全局配置，不跟内容导航抢位置，也不随窗口高度被挤走）。
            点一下**直接**开设置弹窗——分节在弹窗内切（不再有左栏二级菜单那一跳）。 */}
        <div className="fg-rail-footrow">
          <button
            type="button"
            className="fg-navitem"
            aria-haspopup="dialog"
            aria-label={SETTINGS_MENU.label}
            onClick={() => {
              openSettings()
              leave()
            }}
          >
            <span className="fg-navitem-icon">{SETTINGS_MENU.icon}</span>
            <span className="fg-navitem-label">{SETTINGS_MENU.label}</span>
          </button>
        </div>
      </div>
    </aside>
  )
}
