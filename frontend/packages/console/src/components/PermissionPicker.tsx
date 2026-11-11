import { useState } from 'react'
import { Checkbox, Dropdown, Modal, type MenuProps } from 'antd'
import {
  DownOutlined,
  LockOutlined,
  SafetyCertificateOutlined,
  UnlockOutlined,
} from '@ant-design/icons'
import { isDanger, PERM_ROWS, permLabel } from '../console/permissionView'

/** 三档具体档位（没有「跟随默认」——新建/默认设置永远是一个具体值）。 */
export type PermMode = 'read-only' | 'workspace-write' | 'danger-full-access'

/** 从 PERM_ROWS 里摘出三档具体行（去掉 `''` 那一行「跟随默认」）。顺序即展示顺序。 */
const PICK_ROWS = PERM_ROWS.filter((r) => r.mode !== '')

/** 是否三档之一（用于把后端下发的任意字符串收敛成一个可用的初值）。 */
export function isPermMode(v: string | null | undefined): v is PermMode {
  return v === 'read-only' || v === 'workspace-write' || v === 'danger-full-access'
}

/** 严格档位图标（危险档换成开锁，与锁定态 chip 的图标语言一致）。 */
function PermIcon({ mode }: { mode: PermMode }) {
  if (isDanger(mode)) return <UnlockOutlined />
  if (mode === 'workspace-write') return <SafetyCertificateOutlined />
  return <LockOutlined />
}

/**
 * 「完全权限」二次确认框（对齐 dsh `RiskConfirmation` 的勾选门禁）。
 *
 * ## 口径（design §7）
 *   - 选 danger **不立即生效**：先弹此框；
 *   - **勾选框未勾时确认按钮 disabled**——不给"点一下确认"，那等于没确认；
 *   - 只有点确认才回调 `onConfirm`，调用方据此才把档位写进 state。
 *   - 受控：`acknowledged` 由父组件持有（每次打开须重置为 false）。
 *
 * 文案照抄 dsh 简体中文（design §7 表）。
 */
export function RiskConfirmation({
  open,
  acknowledged,
  onAcknowledgedChange,
  onCancel,
  onConfirm,
}: {
  open: boolean
  acknowledged: boolean
  onAcknowledgedChange: (v: boolean) => void
  onCancel: () => void
  onConfirm: () => void
}) {
  return (
    <Modal
      open={open}
      title="确认启用完全权限？"
      okText="启用完全权限"
      cancelText="取消"
      okButtonProps={{ danger: true, disabled: !acknowledged }}
      onOk={onConfirm}
      onCancel={onCancel}
      width={440}
      centered
      destroyOnHidden
    >
      <p style={{ margin: '4px 0 14px', fontSize: 13, lineHeight: 1.7 }}>
        启用完全权限后，智能体将减少确认步骤，并且可以直接执行更多操作，包括敏感操作、文件修改或外部命令。仅建议在你信任当前任务时使用。
      </p>
      <Checkbox checked={acknowledged} onChange={(e) => onAcknowledgedChange(e.target.checked)}>
        我已了解风险，并愿意继续
      </Checkbox>
    </Modal>
  )
}

/**
 * 三档权限选择器（新建面板 / 工作空间默认设置共用）。
 *
 * - 三档具体档（仅可查看 / 工作区内修改 / 完全权限），**无「跟随默认」**；
 * - 选「完全权限」→ 不立即生效，弹 `RiskConfirmation`，勾选确认后才把档位写进 state；
 * - 受控：`value` 是当前档，`onChange` 只会在「一个具体档生效」时被调用。
 *
 * 与 `PermissionChip`（会话内锁定+解释）分工：那个只读回显，这个在**可写面**（新建 / 默认设置）。
 */
export function PermissionPicker({
  value,
  onChange,
  disabled,
  variant = 'chip',
}: {
  value: PermMode
  onChange: (mode: PermMode) => void
  disabled?: boolean
  /** chip = 输入工具条里的药丸样式；用于工作空间默认设置时可沿用同一形态。 */
  variant?: 'chip'
}) {
  const [open, setOpen] = useState(false)
  // 待确认的危险档（非空 = 确认框开着）；确认后才写进 state。
  const [pendingDanger, setPendingDanger] = useState<PermMode | null>(null)
  const [acknowledged, setAcknowledged] = useState(false)

  const closeConfirm = () => {
    setAcknowledged(false)
    setPendingDanger(null)
  }

  const choose = (mode: PermMode) => {
    setOpen(false)
    if (mode === value) return
    // 危险档：不立即生效，先过勾选确认（即使 value 已经是 danger，重选也不走这里——已相同直接 return）。
    if (isDanger(mode)) {
      setAcknowledged(false)
      setPendingDanger(mode)
      return
    }
    onChange(mode)
  }

  const confirmDanger = () => {
    const mode = pendingDanger
    closeConfirm()
    if (mode) onChange(mode)
  }

  const items: MenuProps['items'] = PICK_ROWS.map((row) => ({
    key: row.mode,
    // 图标不用 antd 的 `icon` 槽（它把图标与 label 当两个兄弟节点，间距与垂直对齐都不好控，
    // 两行文案下会显得"图标贴着字"）。改为在 label 内自排：图标 + 文字列，gap / 对齐由 CSS 定。
    label: (
      <div className="fg-perm-opt">
        <span className="fg-perm-opt-ico">
          {isPermMode(row.mode) && <PermIcon mode={row.mode} />}
        </span>
        <span className="fg-perm-opt-text">
          <span className="fg-perm-opt-name">
            {row.name}
            {row.risk && <span className="fg-perm-pop-risk"> ·风险</span>}
          </span>
          <span className="fg-perm-opt-desc">{row.desc}</span>
        </span>
      </div>
    ),
  }))

  const onClick: MenuProps['onClick'] = ({ key }) => {
    if (isPermMode(key)) choose(key)
  }

  const danger = isDanger(value)
  const label = permLabel(value)
  const Icon = isDanger(value) ? UnlockOutlined : value === 'workspace-write' ? SafetyCertificateOutlined : LockOutlined

  return (
    <>
      <Dropdown
        menu={{ items, onClick, selectedKeys: [value] }}
        trigger={['click']}
        placement="bottomLeft"
        disabled={disabled}
        open={open}
        onOpenChange={(o) => setOpen(o)}
      >
        <button
          type="button"
          className={`fg-chip fg-perm-picker${danger ? ' fg-on' : ''}`}
          title="选择本会话的权限档位（新建时可选）"
          aria-label={`权限档位：${label}`}
          aria-haspopup="menu"
          disabled={disabled}
          data-variant={variant}
        >
          <span className="fg-chip-ico">
            <Icon />
          </span>
          <span className="fg-chip-label">{label}</span>
          <span className="fg-chip-chev">
            <DownOutlined />
          </span>
        </button>
      </Dropdown>

      <RiskConfirmation
        open={pendingDanger !== null}
        acknowledged={acknowledged}
        onAcknowledgedChange={setAcknowledged}
        onCancel={closeConfirm}
        onConfirm={confirmDanger}
      />
    </>
  )
}
