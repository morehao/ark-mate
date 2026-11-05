import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

/**
 * 刀 1 · ExpertsPanel（扩展 · 专家）——列表渲染与搜索分支。
 *
 * 覆盖点（对应现状 `ui:accept cap` 里的"能力清单长出来"）：
 *   - 加载后按卡片渲染专家（name/description）；
 *   - 搜索关键字过滤；
 *   - 搜索无匹配给出空态；
 *   - 清单取不到时显示错误 Alert + 重试。
 *
 * 依赖全部 mock：@ark-mate/api（列表/候选/写面）、store(showToast)；
 * PageContainer / ExpertCard / ExpertDialog / shared 是纯渲染，走真实现（不 mock）。
 */

const toast = vi.fn()
vi.mock('../../src/store', () => ({
  useMate: (selector: (s: { showToast: typeof toast }) => unknown) => selector({ showToast: toast }),
}))

const listExperts = vi.fn()
const listSkills = vi.fn()
const listMcpServers = vi.fn()
const listModels = vi.fn()
const getExpert = vi.fn()
vi.mock('@ark-mate/api', () => ({
  listExperts: (...a: unknown[]) => listExperts(...a),
  listSkills: (...a: unknown[]) => listSkills(...a),
  listMcpServers: (...a: unknown[]) => listMcpServers(...a),
  listModels: (...a: unknown[]) => listModels(...a),
  getExpert: (...a: unknown[]) => getExpert(...a),
  createExpert: vi.fn(),
  updateExpert: vi.fn(),
  deleteExpert: vi.fn(),
  setExpertEnabled: vi.fn(),
  toErrorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))

import { ExpertsPanel } from '../../src/components/capability/ExpertsPanel'

const expert = (id: string, name: string, over: Record<string, unknown> = {}) => ({
  id,
  name,
  enabled: true,
  has_instructions: false,
  ...over,
})

beforeEach(() => {
  toast.mockReset()
  listExperts.mockReset()
  listSkills.mockResolvedValue([])
  listMcpServers.mockResolvedValue([])
  listModels.mockResolvedValue({ models: [] })
})

describe('ExpertsPanel', () => {
  it('加载后渲染专家卡片', async () => {
    listExperts.mockResolvedValue([
      expert('exp_a', '写作助手', { description: '帮你润色文案' }),
      expert('exp_b', '代码审查', { description: '看 diff' }),
    ])
    render(<ExpertsPanel />)
    expect(await screen.findByText('写作助手')).toBeInTheDocument()
    expect(screen.getByText('代码审查')).toBeInTheDocument()
  })

  it('搜索按名称过滤', async () => {
    listExperts.mockResolvedValue([
      expert('exp_a', '写作助手'),
      expert('exp_b', '代码审查'),
    ])
    const user = userEvent.setup()
    render(<ExpertsPanel />)
    await screen.findByText('写作助手')
    await user.type(screen.getByPlaceholderText('搜索专家'), '代码')
    expect(screen.getByText('代码审查')).toBeInTheDocument()
    expect(screen.queryByText('写作助手')).toBeNull()
  })

  it('搜索无匹配给出空态提示', async () => {
    listExperts.mockResolvedValue([expert('exp_a', '写作助手')])
    const user = userEvent.setup()
    render(<ExpertsPanel />)
    await screen.findByText('写作助手')
    await user.type(screen.getByPlaceholderText('搜索专家'), 'zzz不存在')
    expect(screen.getByText(/没有找到匹配/)).toBeInTheDocument()
  })

  it('清单取不到时显示错误 Alert 且可重试', async () => {
    listExperts.mockRejectedValueOnce(new Error('boom')).mockResolvedValue([expert('exp_a', '写作助手')])
    const user = userEvent.setup()
    render(<ExpertsPanel />)
    expect(await screen.findByText(/专家清单取不到/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /重\s*试/ }))
    await waitFor(() => expect(screen.getByText('写作助手')).toBeInTheDocument())
  })
})
