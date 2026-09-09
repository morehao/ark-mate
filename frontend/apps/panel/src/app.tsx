// 应用壳（console.html .app）：顶栏 + WS reconnecting 横幅 + 路由区 + 全局 toast。
import { useEffect, type ReactNode } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { tokens } from '@ark/ui'
import { consoleStyleSheet } from './console.css'
import { TopBar } from './components/TopBar'
import { useForge } from './store'
import { ChatPage } from './pages/ChatPage'
import { StartPage } from './pages/StartPage'
import { AgentsPage } from './pages/AgentsPage'
import { CapabilitiesPage } from './pages/CapabilitiesPage'
import { SettingsPage } from './pages/SettingsPage'
import { UsagePage } from './pages/UsagePage'

function Manage({ children }: { children: ReactNode }) {
  return (
    <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', display: 'flex', background: tokens.bg }}>
      {children}
    </div>
  )
}

export function App() {
  // 启动一次：常驻 WS 连接 + 拉取 agents/conversations（跨路由共享一份状态）
  useEffect(() => {
    useForge.getState().boot()
  }, [])

  const wsState = useForge((s) => s.wsState)
  const toast = useForge((s) => s.toast)

  return (
    <div
      style={{
        height: '100dvh',
        display: 'flex',
        flexDirection: 'column',
        minHeight: 0,
        background: tokens.bg,
        color: tokens.text,
      }}
    >
      <style>{consoleStyleSheet}</style>
      <TopBar />

      {/* reconnecting 提示条：非 open 时可见（首连前的 idle 除外） */}
      {wsState !== 'idle' && wsState !== 'open' && (
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
          <Route path="/" element={<Navigate to="/new" replace />} />
          <Route path="/new" element={<StartPage />} />
          <Route path="/conversations" element={<ChatPage />} />
          <Route path="/conversations/:id" element={<ChatPage />} />
          <Route
            path="/agents"
            element={
              <Manage>
                <AgentsPage />
              </Manage>
            }
          />
          <Route
            path="/capabilities"
            element={
              <Manage>
                <CapabilitiesPage />
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
            path="/settings"
            element={
              <Manage>
                <SettingsPage />
              </Manage>
            }
          />
          <Route path="*" element={<Navigate to="/new" replace />} />
        </Routes>
      </div>

      {/* 全局 toast（错误/提示，轻量浮层） */}
      {toast && (
        <div
          style={{
            position: 'fixed',
            right: 16,
            top: 64,
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
