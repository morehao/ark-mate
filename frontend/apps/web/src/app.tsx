// 应用壳（console.html .app）：左栏（导航 + 会话区 + 状态）+ WS reconnecting 横幅 + 路由区 + 全局 toast。
import { useEffect, type ReactNode } from 'react'
import { MenuOutlined } from '@ant-design/icons'
import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { tokens } from '@ark-mate/ui'
import { consoleStyleSheet } from './console.css'
import { NavRail } from './components/NavRail'
import { StatusBar } from './components/StatusBar'
import { SettingsDialog } from './components/settings/SettingsDialog'
import { NAV_ENTRIES, isEntryActive } from './console/navView'
import { useMate } from './store'
import { ChatPage } from './pages/ChatPage'
import { AutomationsPage } from './pages/AutomationsPage'
import { OverviewPage } from './pages/extensions/OverviewPage'
import { SkillsPage } from './pages/extensions/SkillsPage'
import { ConnectorsPage } from './pages/extensions/ConnectorsPage'
import { UsagePage } from './pages/UsagePage'

function Manage({ children }: { children: ReactNode }) {
  return (
    <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', display: 'flex', background: tokens.bg }}>
      {children}
    </div>
  )
}

/**
 * 设置深链接：设置已经不是页面了（左栏 footer 菜单 → 居中弹窗），但 `/settings`、`/settings/agents`、
 * `/agents` 这些旧路径仍然可点。做法是**打开对应分节弹窗 + 落回问答页**——弹窗是覆盖层，底下总要有个页面，
 * 而问答页是默认落点；旧书签因此不会静默落到问答页什么都不发生。
 */
function SettingsDeepLink({ section }: { section: string }) {
  const openSettingsSection = useMate((s) => s.openSettingsSection)

  useEffect(() => {
    openSettingsSection(section)
  }, [openSettingsSection, section])

  return <Navigate to="/conversations" replace />
}

/** 窄屏顶条上的当前入口名（导航定义只有一份，这里不重复写别名）。 */
function currentEntryLabel(pathname: string): string {
  return NAV_ENTRIES.find((e) => isEntryActive(pathname, e))?.label ?? '问答'
}

export function App() {
  // 启动一次：常驻 WS 连接 + 拉取 agents/conversations（跨路由共享一份状态）
  useEffect(() => {
    useMate.getState().boot()
  }, [])

  const wsState = useMate((s) => s.wsState)
  const carrierError = useMate((s) => s.carrierError)
  const toast = useMate((s) => s.toast)
  const drawerOpen = useMate((s) => s.drawerOpen)
  const setDrawer = useMate((s) => s.setDrawer)
  const location = useLocation()

  return (
    <div
      style={{
        height: '100dvh',
        display: 'flex',
        flexDirection: 'row',
        minHeight: 0,
        background: tokens.bg,
        color: tokens.text,
      }}
    >
      <style>{consoleStyleSheet}</style>

      <NavRail />

      {/* 窄屏（≤920px）左栏抽屉化的遮罩：点空白处收起 */}
      {drawerOpen && (
        <div
          className="fg-scrim-mobile"
          style={{ position: 'fixed', inset: 0, background: 'rgba(15, 23, 42, 0.34)', zIndex: 35 }}
          onClick={() => setDrawer(false)}
        />
      )}

      <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
        {/* 窄屏顶条：左栏收成抽屉后需要唯一的开启入口（宽屏隐藏） */}
        <div className="fg-mobilebar">
          <button type="button" className="fg-burger" aria-label="打开主导航" onClick={() => setDrawer(true)}>
            <MenuOutlined style={{ fontSize: 15 }} />
          </button>
          <span className="fg-mobilebar-title">{currentEntryLabel(location.pathname)}</span>
        </div>

        {/* 载体接入失败（M1-8）：比"一直重连中"更明确——这时连的是错误的地址，重连没有意义 */}
        {carrierError && (
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              padding: '6px 16px',
              fontSize: 12,
              background: tokens.warningBg,
              color: tokens.error,
              borderBottom: `1px solid ${tokens.warningBorder}`,
              flex: 'none',
            }}
          >
            {carrierError}（请重启应用；若长期存在，请把这条信息连同壳日志一起反馈）
          </div>
        )}

        {/* reconnecting 提示条：非 open 时可见（首连前的 idle 除外） */}
        {!carrierError && wsState !== 'idle' && wsState !== 'open' && (
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              padding: '6px 16px',
              fontSize: 12,
              background: tokens.warningBg,
              color: tokens.warning,
              borderBottom: `1px solid ${tokens.warningBorder}`,
              flex: 'none',
            }}
          >
            <span
              className="fg-anim-pulse"
              style={{ width: 7, height: 7, borderRadius: '50%', background: tokens.warning, display: 'inline-block' }}
            />
            {wsState === 'connecting' ? '连接中…' : 'WS 已断开，正在自动重连（重连后按会话水位续传）…'}
          </div>
        )}

        <div style={{ flex: 1, minHeight: 0, display: 'flex' }}>
          <Routes>
            <Route path="/" element={<Navigate to="/conversations" replace />} />
            {/* 新建会话不再是独立页面：/conversations 无 id 时问答区即新建面板 */}
            <Route path="/new" element={<Navigate to="/conversations" replace />} />
            <Route path="/conversations" element={<ChatPage />} />
            <Route path="/conversations/:id" element={<ChatPage />} />
            {/* 旧「能力」路径 → 扩展总览（一行重定向：书签/深链接不静默落到问答页） */}
            <Route path="/capabilities" element={<Navigate to="/extensions" replace />} />
            <Route
              path="/extensions"
              element={
                <Manage>
                  <OverviewPage />
                </Manage>
              }
            />
            <Route
              path="/extensions/skills"
              element={
                <Manage>
                  <SkillsPage />
                </Manage>
              }
            />
            <Route
              path="/extensions/connectors"
              element={
                <Manage>
                  <ConnectorsPage />
                </Manage>
              }
            />
            <Route
              path="/usage"
              element={
                <Manage>
                  <UsagePage />
                </Manage>
              }
            />
            <Route
              path="/automations"
              element={
                <Manage>
                  <AutomationsPage />
                </Manage>
              }
            />
            {/* 设置已不是页面（左栏 footer 菜单 → 居中弹窗）。旧深链接仍然可用：
                打开对应分节弹窗，然后落回问答页（弹窗是覆盖层，需要底下有个页面）。 */}
            <Route path="/settings" element={<SettingsDeepLink section="general" />} />
            <Route path="/settings/agents" element={<SettingsDeepLink section="agents" />} />
            <Route path="/agents" element={<SettingsDeepLink section="agents" />} />
            <Route path="*" element={<Navigate to="/conversations" replace />} />
          </Routes>
        </div>

        {/* 全局状态条：常驻每个页面（WS 状态在最右下角） */}
        <StatusBar />
      </div>

      {/* 设置分节的居中弹窗宿主（全局一份；开哪一节由 store.settingsSection 决定） */}
      <SettingsDialog />

      {/* 全局 toast（错误/提示，轻量浮层） */}
      {toast && (
        <div
          style={{
            position: 'fixed',
            right: 16,
            top: 16,
            maxWidth: 420,
            zIndex: 90,
            background: tokens.surface,
            border: `1px solid ${tokens.borderStrong}`,
            borderRadius: 12,
            boxShadow: '0 8px 30px rgba(15, 23, 42, 0.14)',
            padding: '10px 14px',
            display: 'flex',
            alignItems: 'flex-start',
            gap: 8,
            fontSize: 12.5,
            lineHeight: 1.6,
            color: tokens.text,
          }}
        >
          <span style={{ color: tokens.error, flex: 'none' }}>⚠</span>
          <span style={{ overflowWrap: 'anywhere' }}>{toast.msg}</span>
        </div>
      )}
    </div>
  )
}
