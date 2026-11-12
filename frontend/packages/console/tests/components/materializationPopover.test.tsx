import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { SkillMaterializationResp } from '@ark-mate/types'

/**
 * 物化体检 popover（MaterializationPopover）组件测试。
 *
 * 断言口径（对应组件头注释与计划 WP-B1）：
 *  - mock `getSkillMaterialization`，逐 status 断言 → `materializationMeta` 的 Tag 文案一致；
 *  - 空态（states 为空）显示 `resp.notes[]`；
 *  - 错误态显示错误消息（仅取数失败，不发任何写请求）；
 *  - 9 个真实 status 以 capabilityView.ts 的 MATERIALIZATION_META 为准，不另造词。
 */

const { getSkillMaterialization } = vi.hoisted(() => ({
  getSkillMaterialization: vi.fn<(id: string) => Promise<SkillMaterializationResp>>(),
}))
vi.mock('@ark-mate/api', () => ({
  getSkillMaterialization: (id: string) => getSkillMaterialization(id),
}))

import { MaterializationPopover } from '../../src/components/MaterializationPopover'

// antd Popover 依赖 rc-resize-observer；jsdom 无 ResizeObserver，补一个空实现。
class RO {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (!('ResizeObserver' in globalThis)) {
  ;(globalThis as unknown as { ResizeObserver: typeof RO }).ResizeObserver = RO
}

const resp = (partial: Partial<SkillMaterializationResp>): SkillMaterializationResp => ({
  id: 'sk-1',
  enabled: true,
  states: [],
  ...partial,
})

beforeEach(() => {
  getSkillMaterialization.mockReset()
  // 每个用例默认先给一个空态实现；需要别的形态的用例再覆盖。
  getSkillMaterialization.mockResolvedValue(resp({ states: [] }))
})

describe('MaterializationPopover', () => {
  it('入口是「物化体检」图标钮，点击才取数', async () => {
    getSkillMaterialization.mockResolvedValue(resp({ states: [] }))
    render(<MaterializationPopover skillId="sk-1" skillName="演示技能" />)
    // 未打开时不请求。
    expect(getSkillMaterialization).not.toHaveBeenCalled()
    const btn = screen.getByRole('button', { name: /物化体检：演示技能/ })
    expect(btn).toHaveAttribute('title', '物化体检')
    await userEvent.setup().click(btn)
    await waitFor(() => expect(getSkillMaterialization).toHaveBeenCalledWith('sk-1'))
  })

  it('逐 status 渲染正确 Tag 文案（in_sync/missing/tampered/stale/disabled/user_owned/not_applicable/untested/not_configured）', async () => {
    getSkillMaterialization.mockResolvedValue(
      resp({
        states: [
          { root: '/r/a', dir: '/r/a/in_sync', status: 'in_sync' },
          { root: '/r/b', dir: '/r/b/missing', status: 'missing' },
          { root: '/r/c', dir: '/r/c/tampered', status: 'tampered' },
          { root: '/r/d', dir: '/r/d/stale', status: 'stale' },
          { root: '/r/e', dir: '/r/e/disabled', status: 'disabled' },
          { root: '/r/f', dir: '/r/f/user_owned', status: 'user_owned' },
          { root: '/r/g', dir: '/r/g/not_applicable', status: 'not_applicable' },
          { root: '/r/h', dir: '/r/h/untested', status: 'untested' },
          { root: '/r/i', dir: '/r/i/not_configured', status: 'not_configured' },
        ],
      }),
    )
    render(<MaterializationPopover skillId="sk-1" skillName="演示技能" />)
    await userEvent.setup().click(screen.getByRole('button', { name: /物化体检/ }))

    // 全部 9 个 Tag 文案（与 MATERIALIZATION_META 严格一致，不另造词）：按 antd tag 选择器逐个核对。
    const tags = Array.from(document.querySelectorAll('.fg-matck-tag')).map((el) => el.textContent)
    expect(tags).toEqual(['一致', '缺失', '已改动', '待更新', '已禁用', '用户自有', '不适用', '未实测', '未配置'])
  })

  it('Tag 颜色取自 materializationMeta（in_sync=green / missing=red / stale=gold / untested=blue）', async () => {
    getSkillMaterialization.mockResolvedValue(
      resp({
        states: [
          { root: '/a', dir: '/a', status: 'in_sync' },
          { root: '/b', dir: '/b', status: 'missing' },
          { root: '/c', dir: '/c', status: 'stale' },
          { root: '/d', dir: '/d', status: 'untested' },
        ],
      }),
    )
    const { baseElement } = render(<MaterializationPopover skillId="sk-1" />)
    await userEvent.setup().click(screen.getByRole('button', { name: /物化体检/ }))
    await screen.findByText('一致')
    // antd 用 `ant-tag-<color>` 类落色；Popover 挂到 body 下，取 baseElement。
    expect(baseElement.querySelector('.ant-tag-green')).toBeTruthy()
    expect(baseElement.querySelector('.ant-tag-red')).toBeTruthy()
    expect(baseElement.querySelector('.ant-tag-gold')).toBeTruthy()
    expect(baseElement.querySelector('.ant-tag-blue')).toBeTruthy()
  })

  it('逐行展示 target/dir/detail/written_at', async () => {
    getSkillMaterialization.mockResolvedValue(
      resp({
        states: [
          {
            root: '/r/a',
            runtime: 'dsh',
            dir: '/r/a/sub',
            status: 'tampered',
            detail: '摘要对不上',
            written_at: Date.UTC(2026, 10, 1, 3, 4, 5),
          },
        ],
      }),
    )
    render(<MaterializationPopover skillId="sk-1" />)
    await userEvent.setup().click(screen.getByRole('button', { name: /物化体检/ }))
    expect(await screen.findByText('/r/a · dsh')).toBeInTheDocument()
    expect(screen.getByText('/r/a/sub')).toBeInTheDocument()
    expect(screen.getByText('摘要对不上')).toBeInTheDocument()
    expect(screen.getByText(/写入于/)).toBeInTheDocument()
  })

  it('空态（states 为空）显示 resp.notes[]', async () => {
    getSkillMaterialization.mockResolvedValue(
      resp({ states: [], notes: ['本进程没有登记任何物化目标', '请在桌面端打开后重试'] }),
    )
    render(<MaterializationPopover skillId="sk-1" />)
    await userEvent.setup().click(screen.getByRole('button', { name: /物化体检/ }))
    expect(await screen.findByText('本进程没有登记任何物化目标')).toBeInTheDocument()
    expect(screen.getByText('请在桌面端打开后重试')).toBeInTheDocument()
  })

  it('空态且无 notes 时给一句兜底文案', async () => {
    getSkillMaterialization.mockResolvedValue(resp({ states: [] }))
    render(<MaterializationPopover skillId="sk-1" />)
    await userEvent.setup().click(screen.getByRole('button', { name: /物化体检/ }))
    expect(await screen.findByText('这个技能当前没有物化目标。')).toBeInTheDocument()
  })

  it('错误态显示错误消息，且只发一次 GET（只读，不发写请求）', async () => {
    // 用微任务延后 reject，避免 vitest 把「同步 reject 的裸 promise」记成未处理拒绝。
    getSkillMaterialization.mockImplementation(
      () =>
        new Promise<SkillMaterializationResp>((_resolve, reject) => {
          queueMicrotask(() => reject(new Error('网络挂了')))
        }),
    )
    render(<MaterializationPopover skillId="sk-1" />)
    await userEvent.setup().click(screen.getByRole('button', { name: /物化体检/ }))
    await waitFor(() => expect(getSkillMaterialization).toHaveBeenCalledTimes(1))
    // 让状态更新在 act 窗口内落定，再断言。
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('网络挂了'))
  })
})
