// 应用壳（§4.8）：顶栏（标题 + 导航 + WS 状态/刷新）+ reconnecting 横幅 + 路由区。
import { NavLink, Navigate, Route, Routes } from "react-router-dom"
import { PanelProvider, usePanelCtx } from "./panel-context"
import { AgentsPage } from "./pages/AgentsPage"
import { RuntimesPage } from "./pages/RuntimesPage"
import { SessionsPage } from "./pages/SessionsPage"
import { SettingsPage } from "./pages/SettingsPage"
import { c, s } from "./ui"

const navStyle = (active: boolean): React.CSSProperties => ({
  color: active ? c.text : c.muted,
  textDecoration: "none",
  fontSize: 13,
  fontWeight: active ? 700 : 400,
  borderBottom: active ? `2px solid ${c.accent}` : "2px solid transparent",
  paddingBottom: 2,
})

function Header() {
  const p = usePanelCtx()
  const { wsState } = p.state
  const wsColor = wsState === "open" ? c.ok : wsState === "connecting" ? c.busy : c.err
  const wsLabel = wsState === "open" ? "WS 已连接" : wsState === "connecting" ? "连接中…" : "WS 断开"
  return (
    <>
      <header style={s.header}>
        <span style={s.title}>ark-forge</span>
        <span style={s.sub}>ACP 会话控制面 · M1</span>
        <nav style={{ display: "flex", gap: 14, marginLeft: 16, alignItems: "center" }}>
          <NavLink to="/sessions" style={({ isActive }) => navStyle(isActive)}>
            会话
          </NavLink>
          <NavLink to="/agents" style={({ isActive }) => navStyle(isActive)}>
            Agents
          </NavLink>
          <NavLink to="/runtimes" style={({ isActive }) => navStyle(isActive)}>
            Runtimes
          </NavLink>
          <NavLink to="/settings" style={({ isActive }) => navStyle(isActive)}>
            Settings
          </NavLink>
        </nav>
        <span style={{ flex: 1 }} />
        <span style={{ display: "inline-flex", gap: 6, alignItems: "center", fontSize: 12, color: c.muted }}>
          <span style={{ ...s.dot, background: wsColor }} />
          {wsLabel}
        </span>
        <button style={s.btnGhost} onClick={() => void p.refreshSessions()}>
          刷新会话
        </button>
      </header>
      {/* reconnecting 提示条（§4.8）：非 open 时可见（首连前的 idle 除外），
          transport 指数退避自动重连 + 按水位重订阅，无需人工介入。 */}
      {wsState !== "idle" && wsState !== "open" && (
        <div style={s.banner}>
          <span style={{ ...s.dot, background: c.busy }} />
          {wsState === "connecting" ? "连接中…" : "WS 已断开，正在自动重连（重连后按上次水位续传）…"}
        </div>
      )}
    </>
  )
}

export function App() {
  return (
    <PanelProvider>
      <div style={s.page}>
        <style>{`@keyframes blink { 0%,100%{opacity:1} 50%{opacity:0} } body{margin:0}`}</style>
        <ReconnectToast />
        <Header />
        <Routes>
          <Route path="/" element={<Navigate to="/sessions" replace />} />
          <Route path="/sessions" element={<SessionsPage />} />
          <Route path="/sessions/:id" element={<SessionsPage />} />
          <Route path="/agents" element={<AgentsPage />} />
          <Route path="/runtimes" element={<RuntimesPage />} />
          <Route path="/settings" element={<SettingsPage />} />
          <Route path="*" element={<Navigate to="/sessions" replace />} />
        </Routes>
      </div>
    </PanelProvider>
  )
}

/** 全局 toast（跨页） */
function ReconnectToast() {
  const p = usePanelCtx()
  const toast = p.state.toast
  return toast ? <div style={s.toast}>⚠ {toast.msg}</div> : null
}
