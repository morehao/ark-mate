import { Modal } from 'antd'
import { SETTINGS_MENU, settingsSectionOf, settingsSections } from '../../console/settingsView'
import { useMate } from '../../store'
import { GeneralSettingsPanel } from './GeneralSettingsPanel'
import { ModelSettingsPanel } from './ModelSettingsPanel'
import { TokenSettingsPanel } from './TokenSettingsPanel'

/**
 * 设置弹窗（一体式）：标题「设置」+ 左侧分节列表 + 右侧内容，挂在 app.tsx（全局一份）。
 *
 * 形态口径（对参照图）：点左栏「设置」**直接**开这一个弹窗，分节在弹窗内切换——不是"先展开左栏二级菜单、
 * 再按节各开一个小窗"。所以：
 *  - `destroyOnHidden`：关掉即卸载，下次打开重新拉数据（供应商/模型清单都是实时面）；
 *  - 宽 1120 / 高 `min(64vh, 640px)` 写死：模型那一节的一行按钮最宽，换节时弹窗也不跳高矮；
 *  - `footer={null}`：动作在内容体里（保存 / 探测 / 同步），弹窗自己不造"确定/取消"假动作。
 */
export function SettingsDialog() {
  const open = useMate((s) => s.settingsOpen)
  const sectionId = useMate((s) => s.settingsSection)
  const openSettingsSection = useMate((s) => s.openSettingsSection)
  const close = useMate((s) => s.closeSettings)

  // 分节清单由**入口形态**决定（桌面壳里「访问令牌」那一节是死 UI，见 settingsSections 的注释）。
  // 请求的那一节若不在清单里（401 自动跳转、或上一次留下的状态），**落到第一节**——不能出现
  // "左侧没有这一项、右侧却是它的内容"这种自相矛盾的界面。
  const sections = settingsSections()
  const active = sections.find((s) => s.id === settingsSectionOf(sectionId)?.id) ?? sections[0]

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
          {sections.map((s) => {
            const on = s.id === active?.id
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
          {active?.id === 'general' && <GeneralSettingsPanel />}
          {active?.id === 'model' && <ModelSettingsPanel />}
          {active?.id === 'token' && <TokenSettingsPanel />}
        </div>
      </div>
    </Modal>
  )
}
