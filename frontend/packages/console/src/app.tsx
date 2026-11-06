// 应用壳（console.html .app）：左栏（导航 + 会话区 + 状态）+ WS reconnecting 横幅 + 路由区 + 全局 toast。
import { useEffect, type ReactNode } from 'react'
import { MenuOutlined } from '@ant-design/icons'
import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { tokens } from '@ark-mate/ui'
import { consoleStyleSheet } from './console.css'
import { NavRail } from './components/NavRail'
import { StatusBar } from './components/StatusBar'
import { SettingsDialog } from './components/settings/SettingsDialog'
import { NAV_ENTRIES, activeChildOf, isEntryActive, isGroup } from './console/navView'
import { useMate } from './store'
import { ChatPage } from './pages/ChatPage'
import { AutomationsPage } from './pages/AutomationsPage'
import { SkillsPage } from './pages/extensions/SkillsPage'
import { ConnectorsPage } from './pages/extensions/ConnectorsPage'
import { ExpertsPage } from './pages/extensions/ExpertsPage'
import { UsagePage } from './pages/UsagePage'
import { MaterialPage } from './pages/MaterialPage'

/** 全局 toast 的层级。
 *
 * 必须**高于 antd 的弹窗层**（Modal 基础 1000、嵌套一层 +200，本仓的"设置"+"供应商/模型"两层
 * 实测就是 1000/1200；antd 自己的 message 用 2010）。旧值 90 看着"够高"，实际被弹窗遮罩压住：
 * 2026-10-11 实测——设置弹窗里点保存，提示被两层遮罩叠加压到约 30% 亮度（toast 90、遮罩
 * 1000/1200，`elementFromPoint` 命中的是 `.ant-modal-wrap`），用户看到的就是"点了保存什么都不
 * 发生"。设置弹窗里**每一个**动作（供应商/模型/工作区）的成败提示都在这个坑里，
 * 所以修在这里而不是各面板里。 */
const TOAST_Z = 2010

/** toast 三态的图标与颜色（与 `store.ts` 的 `ToastTone` 一一对应）。
 *
 * 之前这里写死红色 `⚠`，成功回执也戴红警告——分不开「做成了」和「出错了」。 */
const TOAST_TONE_META = {
  error: { color: tokens.error, glyph: '⚠' },
  success: { color: tokens.success, glyph: '✓' },
  info: { color: tokens.textSecondary, glyph: 'ℹ' },
} as const

function Manage({ children }: { children: ReactNode }) {
  return (
    <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', display: 'flex', background: tokens.bg }}>
      {children}
    </div>
  )
}

/**
 * 设置深链接：设置已经不是页面了（左栏 footer 菜单 → 居中弹窗），但 `/settings` 这类旧路径仍然可点。
 * 做法是**打开对应分节弹窗 + 落回问答页**——弹窗是覆盖层，底下总要有个页面，而问答页是默认落点；
 * 旧书签因此不会静默落到问答页什么都不发生。
 *
 * v19：`/settings/agents` 与 `/agents` 两条深链接随「Agent 管理」那一节删除。**刻意不改成别的分节**：
 * 旧书签指的那个设置面已经不存在了，硬把它接到「通用」上会让用户以为自己点对了地方。
 */
function SettingsDeepLink({ section }: { section: string }) {
  const openSettingsSection = useMate((s) => s.openSettingsSection)

  useEffect(() => {
    openSettingsSection(section)
  }, [openSettingsSection, section])

  return <Navigate to="/sessions" replace />
}

/** 窄屏顶条上的当前入口名（导航定义只有一份，这里不重复写别名）。
 *  命中组时显示具体二级项（「扩展」本身已不是页面，报组名等于没说在哪）。 */
function currentEntryLabel(pathname: string): string {
  const entry = NAV_ENTRIES.find((e) => isEntryActive(pathname, e))
  if (!entry) return '会话'
  return (isGroup(entry) && activeChildOf(pathname, entry)?.label) || entry.label
}

export function App() {
  // 启动一次：常驻 WS 连接 + 拉取 meta/runtime 诊断与 sessions（跨路由共享一份状态）
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
              // 高度与行高钉成整数：否则 line-height:normal 会算出 31.84px 的横幅高，
              // 余下 flex:1 内容区因此带小数，导致所有控件文字落在亚像素上发虚（全站）。
              height: 32,
              lineHeight: '20px',
              padding: '0 16px',
              boxSizing: 'border-box',
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
              // 高度与行高钉成整数：否则 line-height:normal 会算出 31.84px 的横幅高，
              // 余下 flex:1 内容区因此带小数，导致所有控件文字落在亚像素上发虚（全站）。
              height: 32,
              lineHeight: '20px',
              padding: '0 16px',
              boxSizing: 'border-box',
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
            {wsState === 'connecting' ? '连接中…' : '连接已断开，正在自动重连…'}
          </div>
        )}

        <div style={{ flex: 1, minHeight: 0, display: 'flex' }}>
          <Routes>
            <Route path="/" element={<Navigate to="/sessions" replace />} />
            {/* 新建会话不再是独立页面：/sessions 无 id 时问答区即新建面板 */}
            <Route path="/new" element={<Navigate to="/sessions" replace />} />
            <Route path="/sessions" element={<ChatPage />} />
            <Route path="/sessions/:id" element={<ChatPage />} />
            {/* 「扩展」没有总览页了（点左栏组行只展开二级菜单）：旧路径一律落到默认二级页，
                书签/深链接不会静默落到问答页。 */}
            <Route path="/capabilities" element={<Navigate to="/extensions/skills" replace />} />
            <Route path="/extensions" element={<Navigate to="/extensions/skills" replace />} />
            <Route
              path="/extensions/skills"
              element={
                <Manage>
                  <SkillsPage />
                </Manage>
              }
            />
            <Route
              path="/extensions/experts"
              element={
                <Manage>
                  <ExpertsPage />
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
            <Route
              path="/materials"
              element={
                <Manage>
                  <MaterialPage />
                </Manage>
              }
            />
            {/* 设置已不是页面（左栏 footer 菜单 → 居中弹窗）。旧深链接仍然可用：
                打开对应分节弹窗，然后落回问答页（弹窗是覆盖层，需要底下有个页面）。 */}
            <Route path="/settings" element={<SettingsDeepLink section="general" />} />
            <Route path="*" element={<Navigate to="/sessions" replace />} />
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
            zIndex: TOAST_Z,
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
          <span style={{ color: TOAST_TONE_META[toast.tone].color, flex: 'none' }}>
            {TOAST_TONE_META[toast.tone].glyph}
          </span>
          <span style={{ overflowWrap: 'anywhere' }}>{toast.msg}</span>
        </div>
      )}
    </div>
  )
}
