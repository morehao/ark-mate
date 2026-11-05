import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

/**
 * 面 B · PermissionChip（输入区权限 chip）——两态展示 + 提示。
 *
 * 口径（设计 §19.1 + 本轮定稿：纯展示+提示，不接数据不落库）：
 *   - 默认显示「默认权限」；
 *   - 点开菜单可选「默认权限 / 每次都问」两态，**没有「完全放行」**；
 *   - 切到「每次都问」仅改显示 + toast，不调任何接口。
 */

const toast = vi.fn()
vi.mock('../../src/store', () => ({
  useMate: (sel: (s: { showToast: typeof toast }) => unknown) => sel({ showToast: toast }),
}))

import { PermissionChip } from '../../src/components/PermissionChip'

// antd Dropdown 依赖 rc-resize-observer；jsdom 无 ResizeObserver，测试环境补一个空实现。
class RO {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (!('ResizeObserver' in globalThis)) {
  ;(globalThis as unknown as { ResizeObserver: typeof RO }).ResizeObserver = RO
}

beforeEach(() => toast.mockClear())

describe('PermissionChip', () => {
  it('默认显示「默认权限」', () => {
    render(<PermissionChip />)
    expect(screen.getByRole('button', { name: '本会话权限' })).toHaveTextContent('默认权限')
  })

  it('菜单只有两态，无「完全放行」', async () => {
    const user = userEvent.setup()
    render(<PermissionChip />)
    await user.click(screen.getByRole('button', { name: '本会话权限' }))
    // 菜单项（含说明副文案）：默认权限 + 每次都问
    expect(await screen.findByText('按平台的命令规则执行，敏感操作再问你')).toBeInTheDocument()
    expect(screen.getByText('本会话所有工具调用都先征求你同意')).toBeInTheDocument()
    expect(screen.queryByText(/完全放行/)).not.toBeInTheDocument()
  })

  it('切到「每次都问」改显示并给提示（不落库）', async () => {
    const user = userEvent.setup()
    render(<PermissionChip />)
    await user.click(screen.getByRole('button', { name: '本会话权限' }))
    await user.click(await screen.findByText('每次都问'))
    expect(screen.getByRole('button', { name: '本会话权限' })).toHaveTextContent('每次都问')
    expect(toast).toHaveBeenCalledWith('本会话将每次都问', 'success')
  })
})
