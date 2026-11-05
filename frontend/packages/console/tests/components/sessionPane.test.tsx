import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

/**
 * 面 A · SessionPane（左栏会话列表）组件层：会话行右端（状态点 + 日期）+ 会话信息浮层。
 *
 * 覆盖点（对应原型 §15 / §19.3；2026-11-05 反馈改口径）：
 *   - 会话行单行显示「标题 + 状态点 + 日期」：状态词只在 title / 浮层里，行内不再占位；
 *   - 状态口径来自 railView.sessionBadge（点色，六态各一色），日期口径来自 console/time.ts 的 sessDate；
 *   - 取消 / 中断也有自己的点（作废 ≠ 没有状态）；
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
  model_name: 'GPT-4o',
  created_at: new Date('2026-11-05T01:00:00Z').getTime(),
  updated_at: new Date('2026-11-05T02:00:00Z').getTime(),
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

describe('SessionPane · 会话行右端（状态点 + 日期）', () => {
  it('运行中（run=waiting_for_permission）只出「待授权」的点，文字那格是日期', () => {
    state.sessions = [{ ...baseSession, run: { status: 'waiting_for_permission' } }]
    renderPane()
    expect(screen.getByText('重构左侧导航')).toBeInTheDocument()
    // 状态词不再占行内位置：它进了 title（悬停可见）与信息浮层。
    expect(screen.getByTitle('待授权')).toBeInTheDocument()
    expect(screen.queryByText('待授权')).not.toBeInTheDocument()
  })

  it('已完成（status=completed，无 run）不再渲染「已完成」文字，改为状态点 + 日期', () => {
    state.sessions = [{ ...baseSession, status: 'completed' }]
    const { container } = renderPane()
    expect(screen.getByTitle('已完成')).toBeInTheDocument()
    expect(screen.queryByText('已完成')).not.toBeInTheDocument()
    expect(container.querySelector('.fg-sess-dot')).not.toBeNull()
    // 日期格：同年 `MM-DD`，跨年 `YYYY-MM-DD`（口径见 console/time.ts 的 sessDate）。
    expect(container.querySelector('.fg-sess-date')?.textContent).toMatch(/^(\d{4}-)?\d{2}-\d{2}$/)
  })

  it('cancelled 有自己的状态点（已取消），不再跟「没有状态」混在一起', () => {
    state.sessions = [{ ...baseSession, status: 'cancelled' }]
    const { container } = renderPane()
    expect(screen.getByTitle('已取消')).toBeInTheDocument()
    expect(container.querySelector('.fg-sess-dot')).not.toBeNull()
    expect(screen.queryByText('运行中')).not.toBeInTheDocument()
    expect(screen.queryByText('已完成')).not.toBeInTheDocument()
    expect(screen.queryByText('待授权')).not.toBeInTheDocument()
    expect(container.querySelector('.fg-sess-date')?.textContent).toMatch(/^(\d{4}-)?\d{2}-\d{2}$/)
  })

  it('interrupted 出「已中断」的点', () => {
    state.sessions = [{ ...baseSession, status: 'interrupted' }]
    renderPane()
    expect(screen.getByTitle('已中断')).toBeInTheDocument()
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
    // 只显示名字：`model` 是平台行 id，不能出现在浮层里。
    expect(screen.getByText('GPT-4o')).toBeInTheDocument()
    expect(screen.queryByText('mdl_abc')).not.toBeInTheDocument()
    expect(screen.getByText('更新于')).toBeInTheDocument()
    // 时间已本地化：不再出现 RFC3339 的 `T…Z` 形态。
    expect(screen.queryByText(/T\d{2}:\d{2}:\d{2}Z/)).not.toBeInTheDocument()
  })

  it('模型名解析不到时给一句人话，也不摊平台行 id', async () => {
    const userEvent = (await import('@testing-library/user-event')).default
    state.sessions = [{ ...baseSession, status: 'completed', model_name: undefined }]
    const user = userEvent.setup()
    renderPane()
    await user.hover(screen.getByText('重构左侧导航'))
    expect(await screen.findByLabelText('会话信息', undefined, { timeout: 2000 })).toBeInTheDocument()
    expect(screen.getByText('已设置（模型清单里没有它）')).toBeInTheDocument()
    expect(screen.queryByText('mdl_abc')).not.toBeInTheDocument()
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
