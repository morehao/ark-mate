import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

/**
 * 面 A · SessionPane（左栏会话列表）组件层：会话行状态徽标 + 会话信息浮层。
 *
 * 覆盖点（对应原型 §15 / §19.3）：
 *   - 会话行单行显示「标题 + 状态点+文案」，状态来自 railView.sessionBadge；
 *   - 无状态（idle / completed 之外的 cancelled…）时不渲染状态徽标；
 *   - 悬停会话行弹出信息浮层，含标题 / 所属空间 / 模型 / 更新时间四行，空值行跳过。
 *
 * 依赖 mock：store（构造假 state）、@ark-mate/api（搜索/导出）。RailSectionHeader /
 * WorkspaceGroupPane / SessionInfoPopover（antd Popover）走真实现。
 */

const state = {
  sessions: [] as unknown[],
  views: {},
  activeId: null,
  railMode: 'active',
  workspaces: null,
  setDrawer: () => {},
  togglePin: () => {},
  archiveSession: () => {},
  renameSession: () => {},
  setRailMode: () => {},
  jumpToAnchor: () => {},
  showToast: () => {},
  setPreferredWorkspace: () => {},
}

vi.mock('../../src/store', () => ({
  useMate: (sel: (s: typeof state) => unknown) => sel(state),
}))

vi.mock('@ark-mate/api', () => ({
  sessionSearch: vi.fn(async () => []),
  messageSearch: vi.fn(async () => ({ query: '', source: 'sqlite', hits: [], total: 0, has_more: false })),
  exportSessions: vi.fn(async () => ({ files: [] })),
  toErrorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))

import { SessionPane } from '../../src/components/SessionPane'

// antd Popover 依赖 rc-resize-observer；jsdom 无 ResizeObserver，测试环境补一个空实现。
class RO {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (!('ResizeObserver' in globalThis)) {
  ;(globalThis as unknown as { ResizeObserver: typeof RO }).ResizeObserver = RO
}

const baseSession = {
  id: 's1',
  runtime_id: 'dsh',
  status: 'in_progress',
  title: '重构左侧导航',
  pinned: false,
  archived: false,
  workspace_id: 'w1',
  workspace_title: '默认工作空间',
  model: 'mdl_abc',
  created_at: '2026-11-05T01:00:00Z',
  updated_at: '2026-11-05T02:00:00Z',
}

function renderPane() {
  return render(
    <MemoryRouter>
      <SessionPane onCreate={() => {}} />
    </MemoryRouter>,
  )
}

beforeEach(() => {
  localStorage.clear()
  state.sessions = []
})

describe('SessionPane · 会话行状态徽标', () => {
  it('运行中（run=waiting_for_permission）渲染「待授权」状态', () => {
    state.sessions = [{ ...baseSession, run: { status: 'waiting_for_permission' } }]
    renderPane()
    expect(screen.getByText('重构左侧导航')).toBeInTheDocument()
    expect(screen.getByText('待授权')).toBeInTheDocument()
  })

  it('已完成（status=completed，无 run）渲染「已完成」', () => {
    state.sessions = [{ ...baseSession, status: 'completed' }]
    renderPane()
    expect(screen.getByText('已完成')).toBeInTheDocument()
  })

  it('cancelled 不渲染状态徽标', () => {
    state.sessions = [{ ...baseSession, status: 'cancelled' }]
    renderPane()
    expect(screen.queryByText('运行中')).not.toBeInTheDocument()
    expect(screen.queryByText('已完成')).not.toBeInTheDocument()
    expect(screen.queryByText('待授权')).not.toBeInTheDocument()
  })
})

describe('SessionPane · 会话信息浮层', () => {
  it('悬停会话行弹出信息卡，含标题 / 所属空间 / 模型 / 更新时间', async () => {
    const userEvent = (await import('@testing-library/user-event')).default
    state.sessions = [{ ...baseSession, status: 'completed' }]
    const user = userEvent.setup()
    renderPane()
    await user.hover(screen.getByText('重构左侧导航'))
    // Popover 内容（延迟 0.35s 后显示）
    expect(await screen.findByLabelText('会话信息', undefined, { timeout: 2000 })).toBeInTheDocument()
    expect(screen.getByText('所属空间')).toBeInTheDocument()
    expect(screen.getByText('默认工作空间')).toBeInTheDocument()
    expect(screen.getByText('模型')).toBeInTheDocument()
    expect(screen.getByText('mdl_abc')).toBeInTheDocument()
    expect(screen.getByText('更新于')).toBeInTheDocument()
  })

  it('workspace_title / model 为空时跳过对应行', async () => {
    const userEvent = (await import('@testing-library/user-event')).default
    state.sessions = [{ ...baseSession, status: 'completed', workspace_title: '', model: undefined }]
    const user = userEvent.setup()
    renderPane()
    await user.hover(screen.getByText('重构左侧导航'))
    expect(await screen.findByLabelText('会话信息', undefined, { timeout: 2000 })).toBeInTheDocument()
    expect(screen.queryByText('所属空间')).not.toBeInTheDocument()
    expect(screen.queryByText('模型')).not.toBeInTheDocument()
    // 标题与更新时间仍在
    expect(screen.getByText('更新于')).toBeInTheDocument()
  })
})
