import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'

/**
 * GeneralSettingsPanel（设置 · 通用）——v14.6 改版后只剩两件事：
 *   1. 只读展示**平台分配的默认工作区**路径；
 *   2. 说明输入区的键盘操作（Enter 发送 / Shift+Enter 换行）。
 *
 * 「工作区起始位置」的可编辑偏好列表已随前后端一起删除，这里改为**反向**断言：
 * 面板不再出现任何编辑输入 / 保存入口（那一节已不存在）。
 *
 * 依赖全部 mock：store(useMate) 注入假 workspaces。
 */

const workspaces = {
  items: [
    { id: 'wsp-1', path: '/Users/me/ark-mate', title: 'ark-mate', is_default: true, list_order: 0 },
    { id: 'wsp-2', path: '/Users/me/other', title: 'other', is_default: false, list_order: 1 },
  ],
  default_id: 'wsp-1',
  writable: true,
}

vi.mock('../../src/store', () => ({
  useMate: (selector: (s: { workspaces: typeof workspaces }) => unknown) => selector({ workspaces }),
}))

import { GeneralSettingsPanel } from '../../src/components/settings/GeneralSettingsPanel'

describe('GeneralSettingsPanel', () => {
  it('只读展示平台分配的默认工作区路径', () => {
    render(<GeneralSettingsPanel />)
    // 「默认工作区」既是卡片标题也是行标签，两处都合理存在
    expect(screen.getAllByText('默认工作区').length).toBeGreaterThan(0)
    expect(screen.getByText('/Users/me/ark-mate')).toBeInTheDocument()
  })

  it('展示键盘操作：Enter 发送 / Shift+Enter 换行', () => {
    render(<GeneralSettingsPanel />)
    expect(screen.getByText('Enter')).toBeInTheDocument()
    expect(screen.getByText('Shift + Enter')).toBeInTheDocument()
  })

  it('不再提供「工作区起始位置」的编辑入口（该设置面已删除）', () => {
    render(<GeneralSettingsPanel />)
    expect(screen.queryByText('工作区起始位置')).toBeNull()
    expect(screen.queryByPlaceholderText(/输入目录绝对路径/)).toBeNull()
    expect(screen.queryByRole('button', { name: /保存起点/ })).toBeNull()
  })
})
