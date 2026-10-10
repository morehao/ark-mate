import { Modal } from 'antd'
import { SETTINGS_MENU, SETTINGS_SECTIONS, settingsSectionOf } from '../../console/settingsView'
import { useMate } from '../../store'
import { AgentsSettingsPanel } from './AgentsSettingsPanel'
import { GeneralSettingsPanel } from './GeneralSettingsPanel'
import { TokenSettingsPanel } from './TokenSettingsPanel'

/**
 * 设置弹窗（一体式）：标题「设置」+ 左侧分节列表 + 右侧内容，挂在 app.tsx（全局一份）。
 *
 * 形态口径（对参照图）：点左栏「设置」**直接**开这一个弹窗，分节在弹窗内切换——不是"先展开左栏二级菜单、
 * 再按节各开一个小窗"。所以：
 *  - `destroyOnHidden`：关掉即卸载，下次打开重新拉数据（Agent 列表/白名单都是实时面）；
 *  - 宽 1120 / 高 `min(64vh, 640px)` 写死：Agent 管理一行按钮最宽的那节放得下，换节时弹窗也不跳高矮；
 *  - `footer={null}`：动作在内容体里（保存 / 注册 / 启停），弹窗自己不造"确定/取消"假动作。
 */
export function SettingsDialog() {
  const open = useMate((s) => s.settingsOpen)
  const sectionId = useMate((s) => s.settingsSection)
  const openSettingsSection = useMate((s) => s.openSettingsSection)
  const close = useMate((s) => s.closeSettings)

  return (
    <Modal
      open={open}
      title={SETTINGS_MENU.label}
      centered
      destroyOnHidden
      footer={null}
      width={1120}
      onCancel={close}
    >
      <div className="fg-setdlg">
        <nav className="fg-setdlg-nav" aria-label="设置分节">
          {SETTINGS_SECTIONS.map((s) => {
            const on = s.id === settingsSectionOf(sectionId)?.id
            return (
              <button
                key={s.id}
                type="button"
                className={`fg-setdlg-item${on ? ' fg-on' : ''}`}
                aria-current={on ? 'true' : undefined}
                onClick={() => openSettingsSection(s.id)}
              >
                <span className="fg-setdlg-icon">{s.icon}</span>
                <span className="fg-setdlg-text">
                  <span className="fg-setdlg-label">{s.label}</span>
                  <span className="fg-setdlg-hint">{s.hint}</span>
                </span>
              </button>
            )
          })}
        </nav>

        <div className="fg-setdlg-body">
          {sectionId === 'general' && <GeneralSettingsPanel />}
          {sectionId === 'agents' && <AgentsSettingsPanel />}
          {sectionId === 'token' && <TokenSettingsPanel />}
        </div>
      </div>
    </Modal>
  )
}
