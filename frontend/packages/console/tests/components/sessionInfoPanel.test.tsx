import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

/**
 * 会话信息面合并后的组件层（2026-11-06）：
 *   SessionInfoPanel（唯一内容源）+ SessionDetailDrawer（窄屏/资料库抽屉外壳）。
 *
 * 覆盖点：
 *   - SessionInfoPanel 真能渲染会话元数据（标题 / 工作目录 / 模型 / 产物区）——合并后内容体没被搬坏；
 *   - SessionDetailDrawer 是薄壳，open 时把 SessionInfoPanel 挂起来，标题「会话信息」；
 *   - 抽屉复用点（资料库产物详情）签名 open/sessionId/onClose 不变，行为不变。
 *
 * 依赖 mock：store（返回空 views 等假 state）、@ark-mate/api（getSession/history/artifacts 等）。
 */

const state = {
  views: {} as Record<string, unknown>,
  respondPermission: () => {},
  renameSession: async () => {},
  togglePin: async () => {},
  archiveSession: async () => {},
  showToast: () => {},
  modelCatalog: null,
  loadModelCatalog: async () => {},
  updateSessionModel: async () => {},
}

vi.mock('../../src/store', () => ({
  useMate: (sel: (s: typeof state) => unknown) => sel(state),
}))

const DETAIL = {
  id: 's1',
  title: '验证会话',
  runtime_id: 'dsh',
  status: 'completed',
  cwd: '/tmp/proj',
  workspace_title: '默认工作空间',
  model: null,
  created_at: 1_700_000_000_000,
  updated_at: 1_700_000_100_000,
  pinned: false,
  archived: false,
  capabilities: null,
  pending: [],
  memories: [],
}

vi.mock('@ark-mate/api', () => ({
  getSession: vi.fn(async () => DETAIL),
  sessionHistory: vi.fn(async () => ({ events: [] })),
  listSessionArtifacts: vi.fn(async () => ({ artifacts: [], total: 0 })),
  listMaterialNodes: vi.fn(async () => ({ nodes: [] })),
  importArtifact: vi.fn(async () => ({ id: 'n1', title: 'x' })),
  promptSession: vi.fn(async () => ({})),
  revokeMemory: vi.fn(async () => ({})),
  toErrorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))

import { SessionInfoPanel, SessionDetailDrawer } from '../../src/components/SessionDetail'

const renderRouter = (ui: React.ReactNode) => render(<MemoryRouter>{ui}</MemoryRouter>)

describe('会话信息面合并', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('SessionInfoPanel 渲染会话元数据：标题 / 工作目录', async () => {
    renderRouter(<SessionInfoPanel sessionId="s1" />)
    expect(await screen.findByText('验证会话')).toBeTruthy()
    // 工作目录行确实渲染（路径全文挂在 title / 文本里）
    await waitFor(() => expect(screen.getByText(/\/tmp\/proj/)).toBeTruthy())
  })

  it('SessionDetailDrawer 打开时挂载 SessionInfoPanel（外壳标题「会话信息」）', async () => {
    renderRouter(<SessionDetailDrawer open sessionId="s1" onClose={() => {}} />)
    expect(await screen.findByText('会话信息')).toBeTruthy()
    expect(await screen.findByText('验证会话')).toBeTruthy()
  })
})
