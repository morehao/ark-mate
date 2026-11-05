import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

/**
 * 刀 1 · GeneralSettingsPanel（设置 · 通用：工作区起始位置）。
 *
 * 覆盖点（对应现状 `ui:accept model` 之外最常被手工点的一节）：
 *   - 加载列出 configured；
 *   - 手输相对路径 → 拒绝并 toast；重复 → 拒绝；合法 → 入列；
 *   - 空列表 / 有值两种渲染分支；
 *   - 只读（writable=false / locked_by）时输入区整块隐藏、显示锁定 Alert；
 *   - 保存（全量替换）调用 updateWorkspaceSettings 并刷快照。
 *
 * 网络与外部依赖全部 mock：@ark-mate/api、store(showToast)、directoryPicker、DirectoryBrowser。
 * 这几处 mock 是**刀 1 的注入点探针**——证明组件可脱离真后端渲染。
 */

const toast = vi.fn()
vi.mock('../../src/store', () => ({
  useMate: (selector: (s: { showToast: typeof toast }) => unknown) => selector({ showToast: toast }),
}))

vi.mock('../../src/directoryPicker', () => ({
  directoryPickerKind: () => 'browse',
  pickDirectoryNative: async () => ({ cancelled: true }),
}))

// DirectoryBrowser 内部还会拉 @ark-mate/api，这里替换成哑组件，聚焦被测组件本身。
vi.mock('../../src/components/DirectoryBrowser', () => ({
  DirectoryBrowser: () => null,
}))

const getWorkspaceSettings = vi.fn()
const updateWorkspaceSettings = vi.fn()
vi.mock('@ark-mate/api', () => ({
  getWorkspaceSettings: (...a: unknown[]) => getWorkspaceSettings(...a),
  updateWorkspaceSettings: (...a: unknown[]) => updateWorkspaceSettings(...a),
  toErrorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))

import { GeneralSettingsPanel } from '../../src/components/settings/GeneralSettingsPanel'

const baseSnap = (over: Record<string, unknown> = {}) => ({
  configured: [],
  prefer_roots: false,
  provision_included: false,
  source: 'default',
  writable: true,
  ...over,
})

beforeEach(() => {
  toast.mockReset()
  getWorkspaceSettings.mockReset()
  updateWorkspaceSettings.mockReset()
})

describe('GeneralSettingsPanel', () => {
  it('加载后列出已配置的起点', async () => {
    getWorkspaceSettings.mockResolvedValue(baseSnap({ configured: ['/srv/docs'], prefer_roots: true }))
    render(<GeneralSettingsPanel />)
    expect(await screen.findByText('/srv/docs')).toBeInTheDocument()
    expect(screen.getByText('已设起点')).toBeInTheDocument()
  })

  it('空列表给出"没有起点偏好"说明', async () => {
    getWorkspaceSettings.mockResolvedValue(baseSnap())
    render(<GeneralSettingsPanel />)
    await waitFor(() => expect(getWorkspaceSettings).toHaveBeenCalled())
    expect(screen.getByText(/列表为空/)).toBeInTheDocument()
  })

  it('手输相对路径被本地预检拒绝并 toast', async () => {
    getWorkspaceSettings.mockResolvedValue(baseSnap())
    const user = userEvent.setup()
    render(<GeneralSettingsPanel />)
    await screen.findByText(/列表为空/)
    const input = screen.getByPlaceholderText(/输入目录绝对路径/)
    await user.type(input, 'relative/path')
    await user.click(screen.getByRole('button', { name: /加入/ }))
    expect(toast).toHaveBeenCalledWith('起始目录必须是绝对路径')
  })

  it('重复路径不入列并 toast', async () => {
    getWorkspaceSettings.mockResolvedValue(baseSnap({ configured: ['/a'], prefer_roots: true }))
    const user = userEvent.setup()
    render(<GeneralSettingsPanel />)
    await screen.findByText('/a')
    const input = screen.getByPlaceholderText(/输入目录绝对路径/)
    await user.type(input, '/a')
    await user.click(screen.getByRole('button', { name: /加入/ }))
    expect(toast).toHaveBeenCalledWith('该目录已在列表中')
  })

  it('合法绝对路径入列并出现"有未保存的改动"', async () => {
    getWorkspaceSettings.mockResolvedValue(baseSnap())
    const user = userEvent.setup()
    render(<GeneralSettingsPanel />)
    await screen.findByText(/列表为空/)
    const input = screen.getByPlaceholderText(/输入目录绝对路径/)
    await user.type(input, '/srv/new{Enter}')
    expect(screen.getByText('/srv/new')).toBeInTheDocument()
    expect(screen.getByText('有未保存的改动')).toBeInTheDocument()
  })

  it('保存调用 updateWorkspaceSettings(全量) 并刷新快照', async () => {
    getWorkspaceSettings.mockResolvedValue(baseSnap())
    updateWorkspaceSettings.mockResolvedValue(baseSnap({ configured: ['/srv/x'], prefer_roots: true }))
    const user = userEvent.setup()
    render(<GeneralSettingsPanel />)
    await screen.findByText(/列表为空/)
    await user.type(screen.getByPlaceholderText(/输入目录绝对路径/), '/srv/x{Enter}')
    await user.click(screen.getByRole('button', { name: '保存起点' }))
    await waitFor(() => expect(updateWorkspaceSettings).toHaveBeenCalledWith(['/srv/x']))
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('已保存'), 'success')
  })

  it('锁定只读：显示锁定 Alert 且隐藏输入区', async () => {
    getWorkspaceSettings.mockResolvedValue(baseSnap({ writable: false, locked_by: 'env', configured: ['/ro'] }))
    render(<GeneralSettingsPanel />)
    expect(await screen.findByText(/由 env 锁定/)).toBeInTheDocument()
    expect(screen.queryByPlaceholderText(/输入目录绝对路径/)).toBeNull()
    expect(screen.queryByRole('button', { name: '保存起点' })).toBeNull()
  })
})
