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
    const { container } = render(<PermissionCard e={entry} />)
    // 卡片本体：background #fffbe6、border #ffe58f
    const card = Array.from(container.querySelectorAll('div')).find(
      (d) => d.style.background === 'rgb(255, 251, 230)',
    )
    expect(card).toBeTruthy()
    expect(card!.style.border).toContain('rgb(255, 229, 143)')
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
