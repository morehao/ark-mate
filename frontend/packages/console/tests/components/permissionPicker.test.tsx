import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

/**
 * PermissionPicker（新建面板 / 工作空间默认设置的三档选择器）+ RiskConfirmation 勾选门禁。
 *
 * 覆盖 design §6.3 / §7 / D4：
 *   - 三档具体档，无「跟随默认」；
 *   - 选 danger **不立即生效**：弹确认框；
 *   - 勾选框未勾 → 「启用完全权限」按钮 disabled；勾上才可点；
 *   - 确认后才回调 onChange（写入 state）。
 */

import { PermissionPicker, RiskConfirmation } from '../../src/components/PermissionPicker'

// antd Dropdown 依赖 rc-resize-observer；jsdom 无 ResizeObserver，补一个空实现。
class RO {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (!('ResizeObserver' in globalThis)) {
  ;(globalThis as unknown as { ResizeObserver: typeof RO }).ResizeObserver = RO
}
// antd 的某些内部测量读 matchMedia，jsdom 无——补空实现避免报错。
if (!('matchMedia' in window)) {
  ;(window as unknown as { matchMedia: () => unknown }).matchMedia = () => ({
    matches: false,
    addListener() {},
    removeListener() {},
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() {
      return false
    },
  })
}

describe('PermissionPicker', () => {
  it('非危险档直接生效（无确认框）', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<PermissionPicker value="workspace-write" onChange={onChange} />)
    // 打开下拉，点「仅可查看」
    await user.click(screen.getByRole('button', { name: /权限档位/ }))
    await user.click(await screen.findByText('仅可查看'))
    expect(onChange).toHaveBeenCalledWith('read-only')
    // 没有确认框
    expect(screen.queryByText('确认启用完全权限？')).not.toBeInTheDocument()
  })

  it('选 danger 不立即生效：弹确认框，勾选前按钮 disabled，勾选后可确认并回调', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<PermissionPicker value="workspace-write" onChange={onChange} />)
    await user.click(screen.getByRole('button', { name: /权限档位/ }))
    // 菜单里的「完全权限」（菜单项文案带 ·风险）
    const dangerItem = await screen.findByText('完全权限')
    await user.click(dangerItem)
    // 未生效
    expect(onChange).not.toHaveBeenCalled()
    // 确认框出现
    expect(await screen.findByText('确认启用完全权限？')).toBeInTheDocument()
    const confirmBtn = screen.getByRole('button', { name: '启用完全权限' })
    expect(confirmBtn).toBeDisabled()
    // 勾选后可用
    await user.click(screen.getByRole('checkbox', { name: '我已了解风险，并愿意继续' }))
    expect(confirmBtn).toBeEnabled()
    await user.click(confirmBtn)
    expect(onChange).toHaveBeenCalledWith('danger-full-access')
  })

  it('danger 确认框点取消不回调', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<PermissionPicker value="workspace-write" onChange={onChange} />)
    await user.click(screen.getByRole('button', { name: /权限档位/ }))
    await user.click(await screen.findByText('完全权限'))
    // 等确认框挂载后再点取消
    expect(await screen.findByText('确认启用完全权限？')).toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: /取\s*消/ }))
    expect(onChange).not.toHaveBeenCalled()
  })
})

describe('RiskConfirmation', () => {
  it('勾选门禁：未勾选 ok 按钮 disabled', () => {
    const { rerender } = render(
      <RiskConfirmation
        open
        acknowledged={false}
        onAcknowledgedChange={() => {}}
        onCancel={() => {}}
        onConfirm={() => {}}
      />,
    )
    expect(screen.getByText('确认启用完全权限？')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '启用完全权限' })).toBeDisabled()
    rerender(
      <RiskConfirmation
        open
        acknowledged
        onAcknowledgedChange={() => {}}
        onCancel={() => {}}
        onConfirm={() => {}}
      />,
    )
    expect(screen.getByRole('button', { name: '启用完全权限' })).toBeEnabled()
  })
})
