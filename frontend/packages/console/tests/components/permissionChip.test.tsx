import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

/**
 * 面 B · PermissionChip（输入区权限 chip）——v4 锁定态（只读回显 + 解释）。
 *
 * 口径（对应组件头注释，与旧两态假菜单**完全不同**）：
 *   - 权限来自会话创建时钉住的原生模式，chip 是**锁定态**（锁图标、无下拉、不可切）；
 *   - 默认（nativeMode 空）= 「跟随默认」，按钮 aria-label 带「（锁定）」；
 *   - 点开是**解释**（四档说明 + 当前档高亮 + 「会话内暂不可切换」），不是可点菜单、不做假切换；
 *   - 点开不调任何写接口、不打 toast。
 */

const toast = vi.fn()
vi.mock('../../src/store', () => ({
  useMate: (sel: (s: { showToast: typeof toast }) => unknown) => sel({ showToast: toast }),
}))

import { PermissionChip } from '../../src/components/PermissionChip'

// antd Popover 依赖 rc-resize-observer；jsdom 无 ResizeObserver，测试环境补一个空实现。
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
  it('默认（未钉模式）显示「跟随默认」且锁定', () => {
    render(<PermissionChip />)
    const btn = screen.getByRole('button', { name: /本会话权限：跟随默认（锁定）/ })
    expect(btn).toHaveTextContent('跟随默认')
    expect(btn).toHaveAttribute('aria-haspopup', 'dialog')
  })

  it('钉住 workspace-write 时回显「工作区内修改」', () => {
    render(<PermissionChip nativeMode="workspace-write" />)
    expect(screen.getByRole('button', { name: /本会话权限：工作区内修改（锁定）/ })).toBeInTheDocument()
  })

  it('点开是解释（四档说明 + 会话内暂不可切换），不是可切换菜单', async () => {
    const user = userEvent.setup()
    render(<PermissionChip />)
    await user.click(screen.getByRole('button', { name: /本会话权限/ }))
    // 四档说明都在（跟随默认 / 仅可查看 / 工作区内修改 / 完全权限）
    expect(await screen.findByText('沿用工作空间默认值，新建会话时可选具体档位。')).toBeInTheDocument()
    expect(screen.getByText('只能读、不能改；需要写入时会向你申请升级。')).toBeInTheDocument()
    expect(screen.getByText('会话工作区内的文件可改，越界与敏感操作再问你。')).toBeInTheDocument()
    expect(screen.getByText('不限制写入，也不再询问敏感操作（风险自担）。')).toBeInTheDocument()
    // 明确告知会话内不可切换
    expect(screen.getByText(/会话内暂不可切换/)).toBeInTheDocument()
    // 点开不落库、不打 toast（纯解释）
    expect(toast).not.toHaveBeenCalled()
  })
})
