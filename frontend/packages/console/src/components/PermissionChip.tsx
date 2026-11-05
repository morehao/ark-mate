import { useState } from 'react'
import { CheckOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import { Dropdown } from 'antd'
import type { MenuProps } from 'antd'
import { useMate } from '../store'

/** 两态：默认权限 / 每次都问（设计 §19.1——**去掉"完全放行"**）。 */
type PermMode = 'default' | 'always'

/**
 * 输入区的「权限」chip（原型 §2 面 B / 设计 §19.1）。
 *
 * ## 口径（重要）
 * 后端有**两层**权限，别混：
 *   ① 命令门禁 `command-policy`（`allow|prompt|forbidden`，按命令前缀规则，`types/commandPolicy.ts`）——
 *      在「设置 · 命令规则」里；
 *   ② 会话级 `PermissionOption`（`allow_once/allow_always/reject_once/reject_always`，针对单次工具调用
 *      + 会话记忆，`types/session.ts`）——**这里这个 chip 说的是它**。
 *
 * ## 范围（已定稿：纯展示 + 提示，不接数据不落库）
 * 后端**没有**"每次都问"这个全局开关（也没有只在 UI 层就能安全切换的写接口），所以本 chip：
 *   - 只在前端本地记住当前选择（`useState`），会话内切一下只改**显示**并给一句提示；
 *   - **不**读 `GET /sessions/:id` 的权限记忆、**不**调用任何写接口、**不**改协议；
 *   - 默认显示「默认权限」。
 * 真正持久化"记住 always"仍走既有的会话级权限记忆（`POST /permission` 的 `always` + 撤销入口），
 * 这件 chip 只是给用户一个"我现在的权限姿态"的呈现与说明，绝不是命令规则的开关。
 */
export function PermissionChip({ disabled }: { disabled?: boolean }) {
  const showToast = useMate((s) => s.showToast)
  const [mode, setMode] = useState<PermMode>('default')

  const items: MenuProps['items'] = [
    {
      type: 'group',
      label: '本会话权限',
      children: [
        {
          key: 'default',
          icon: mode === 'default' ? <CheckOutlined /> : undefined,
          label: (
            <span className="fg-perm-opt">
              <span className="fg-perm-opt-name">默认权限</span>
              <span className="fg-perm-opt-desc">按平台的命令规则执行，敏感操作再问你</span>
            </span>
          ),
        },
        {
          key: 'always',
          icon: mode === 'always' ? <CheckOutlined /> : undefined,
          label: (
            <span className="fg-perm-opt">
              <span className="fg-perm-opt-name">每次都问</span>
              <span className="fg-perm-opt-desc">本会话所有工具调用都先征求你同意</span>
            </span>
          ),
        },
      ],
    },
  ]

  return (
    <Dropdown
      trigger={['click']}
      placement="bottomLeft"
      disabled={disabled}
      menu={{
        items,
        selectable: true,
        selectedKeys: [mode],
        onClick: ({ key }) => {
          const next = key as PermMode
          if (next === mode) return
          setMode(next)
          // 只改显示 + 提示（零接口、零落库）；持久化"记住 always"走会话级权限记忆，不在这里。
          showToast(next === 'always' ? '本会话将每次都问' : '已恢复默认权限', 'success')
        },
      }}
    >
      <button
        type="button"
        className={`fg-chip fg-perm-chip${mode === 'always' ? ' fg-on' : ''}`}
        title="本会话权限（仅展示；命令规则在「设置 · 命令规则」）"
        aria-label="本会话权限"
        disabled={disabled}
      >
        <span className="fg-chip-ico">
          {mode === 'default' ? <CheckOutlined /> : <SafetyCertificateOutlined />}
        </span>
        <span className="fg-chip-label">{mode === 'always' ? '每次都问' : '默认权限'}</span>
        <span className="fg-chip-chev" aria-hidden="true">
          ▾
        </span>
      </button>
    </Dropdown>
  )
}
