import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
/**
 * 面 C · 权限卡视觉（Transcript.PermissionCard）。
 *
 * 只验视觉口径（原型 §面 C `.perm`）：黄底 warning-bg + warning-border 描边，动作按钮在。
 * 交互（respondPermission）本身不在这里测（那是既有行为）。
 */

vi.mock('../../src/store', () => ({
  useMate: (sel: (s: { respondPermission: () => void }) => unknown) => sel({ respondPermission: () => {} }),
}))

import { tokens } from '@ark-mate/ui'
import { PermissionCard } from '../../src/components/Transcript'

const entry = {
  id: 'p1',
  kind: 'permission',
  callId: 'call_1',
  title: '运行命令 rm -rf build',
  toolKind: 'bash',
  toolCallId: 'call_1',
  optionKinds: [],
  resolved: false,
} as unknown as Parameters<typeof PermissionCard>[0]['e']

describe('PermissionCard 视觉', () => {
  it('黄底 warning-bg + warning-border 描边', () => {
    // 断言口径取 token 事实源（不写死 hex）：换调色板时本用例自动跟随，不误报。
    const hexToRgb = (hex: string) => {
      const n = hex.replace('#', '')
      const [r, g, b] = [0, 2, 4].map((i) => parseInt(n.slice(i, i + 2), 16))
      return `rgb(${r}, ${g}, ${b})`
    }
    const { container } = render(<PermissionCard e={entry} />)
    const card = Array.from(container.querySelectorAll('div')).find(
      (d) => d.style.background === hexToRgb(tokens.warningBg),
    )
    expect(card).toBeTruthy()
    expect(card!.style.border).toContain(hexToRgb(tokens.warningBorder))
    // 标题可见
    expect(screen.getByText('运行命令 rm -rf build')).toBeInTheDocument()
  })

  it('无选项时给「允许·本次 / 允许·始终 / 拒绝」三个动作', () => {
    render(<PermissionCard e={entry} />)
    expect(screen.getByRole('button', { name: '允许 · 本次' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '允许 · 始终' })).toBeInTheDocument()
    // antd 对两个 CJK 字的按钮会插空格（「拒 绝」），用正则宽松匹配
    expect(screen.getByRole('button', { name: /拒\s*绝/ })).toBeInTheDocument()
  })
})
