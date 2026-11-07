import { Popover } from 'antd'
import { LockOutlined, UnlockOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import { isDanger, PERM_ROWS, permLabel } from '../console/permissionView'

/**
 * 输入区的「权限」chip（v4 · 设计 §3.3 P0 / §3.4 / §3.5）。
 *
 * ## 口径（重要，和旧版完全不同）
 * 旧版是两态假菜单（默认权限 / 每次都问）+ `useState`，点完只改显示、零接口零落库，
 * 且「每次都问」在 DSH 里根本不存在——选择即动画，不改变任何执行行为。**已删。**
 *
 * 新版（D11 事实驱动）：
 *   - 权限来自会话**创建时钉住**的原生模式（`sessions.native_mode`，只读回显）；
 *   - 当前接入方式下（`dsh-acp` 不暴露运行期切换面）**会话内不可切**，所以 chip 是
 *     **锁定态**：锁图标、无下拉箭头、不可选；
 *   - 点开是**解释**（三档说明 + 当前档高亮 + 「会话内暂不可切换」+ 新建会话入口），
 *     不是假菜单、不做假切换；
 *   - 终局：底座在 ACP 暴露运行期切换后，**同一槽位**去掉锁、加回菜单即可（布局零迁移）。
 *
 * ## 范围
 * 纯展示 + 解释。**不**调用任何写接口、**不**改协议、**不**读会话级权限记忆
 * （那块走既有的 `POST /permission` 与「设置 · 命令规则」，与本 chip 无关）。
 */
export function PermissionChip({
  nativeMode,
  disabled,
  onCreateSession,
}: {
  /** 会话钉住的原生权限模式（`sessions.native_mode`；空/缺省 = 跟随默认）。 */
  nativeMode?: string | null
  disabled?: boolean
  /** 「新建会话时可选」入口：跳/打开新建会话面板（可选，缺省只显示文案）。 */
  onCreateSession?: () => void
}) {
  const label = permLabel(nativeMode)
  const danger = isDanger(nativeMode)
  const Icon = danger ? UnlockOutlined : nativeMode ? SafetyCertificateOutlined : LockOutlined

  const content = (
    <div className="fg-perm-pop">
      <div className="fg-perm-pop-title">本会话权限</div>
      <div className="fg-perm-pop-list">
        {PERM_ROWS.map((row) => {
          const current = (nativeMode ?? '') === row.mode
          return (
            <div key={row.mode || 'default'} className={`fg-perm-pop-row${current ? ' fg-perm-current' : ''}`}>
              <div className="fg-perm-pop-row-head">
                {row.name}
                {row.risk && <span className="fg-perm-pop-risk">·风险</span>}
                {current && <span aria-hidden="true">·当前</span>}
              </div>
              <div className="fg-perm-pop-row-desc">{row.desc}</div>
            </div>
          )
        })}
      </div>
      <div className="fg-perm-pop-foot">
        当前接入方式下会话内暂不可切换。
        {onCreateSession ? (
          <>
            {' '}
            <a
              className="fg-perm-pop-link"
              role="button"
              tabIndex={0}
              onClick={(e) => {
                e.preventDefault()
                onCreateSession()
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  onCreateSession()
                }
              }}
            >
              新建会话时可选
            </a>
            。
          </>
        ) : (
          ' 新建会话时可选。'
        )}
        底座开放运行期切换后这里会变成可选项。
      </div>
    </div>
  )

  return (
    <Popover
      trigger={['click']}
      placement="bottomLeft"
      content={content}
      disabled={disabled}
      overlayStyle={{ maxWidth: 320 }}
    >
      <button
        type="button"
        className={`fg-chip fg-chip-locked${danger ? ' fg-on' : ''}`}
        title="本会话权限：当前接入方式下会话内暂不可切换，点开查看说明"
        aria-label={`本会话权限：${label}（锁定）`}
        aria-haspopup="dialog"
        disabled={disabled}
      >
        <span className="fg-chip-ico">
          <Icon />
        </span>
        <span className="fg-chip-label">{label}</span>
      </button>
    </Popover>
  )
}
