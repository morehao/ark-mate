import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'

/**
 * 刀 1.5 · ModelSettingsPanel（设置 · 模型）——首屏渲染与关键分支。
 *
 * 这个组件是「设置 · 模型」那一节（对应现状 `ui:accept model` 场景的最重一块）。
 * 这里只测**渲染/分支**这一层（不测完整表单提交——那依赖 antd Form 的复杂交互、且真栈层
 * `ui:accept model` 已用真服务覆盖了写面）；覆盖点：
 *   - 空态：无供应商 → 两块各自的空态文案 + 「新建供应商」入口；
 *   - 有供应商：渲染名称/协议/模型数；
 *   - 默认模型：页头只读状态显示 `默认模型：<名>`（A14 唯一标记 aria-label="默认模型"）；
 *   - 模型清单：按供应商分组，渲染模型行 + 「添加模型」入口；
 *   - 同步状态/生效结论区域渲染；
 *   - 清单取不到：toast（store.showToast 被调用）。
 *
 * 依赖全 mock：@ark-mate/api（列表/目录/写面）、store(showToast)。
 * 写面函数（create/delete/...）都给 vi.fn()，本用例不触发。
 */

const toast = vi.fn()
vi.mock('../../src/store', () => ({
  useMate: (selector: (s: { showToast: typeof toast }) => unknown) => selector({ showToast: toast }),
}))

const listProviders = vi.fn()
const listModels = vi.fn()
const listModelCatalog = vi.fn()
vi.mock('@ark-mate/api', () => ({
  listProviders: (...a: unknown[]) => listProviders(...a),
  listModels: (...a: unknown[]) => listModels(...a),
  listModelCatalog: (...a: unknown[]) => listModelCatalog(...a),
  createProvider: vi.fn(),
  patchProvider: vi.fn(),
  deleteProvider: vi.fn(),
  probeProvider: vi.fn(),
  createModel: vi.fn(),
  patchModel: vi.fn(),
  deleteModel: vi.fn(),
  syncModels: vi.fn(),
  toErrorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))

import { ModelSettingsPanel } from '../../src/components/settings/ModelSettingsPanel'

const provider = (over: Record<string, unknown> = {}) => ({
  id: 'prov_openai',
  name: '我的端点',
  protocol: 'openai',
  base_url: 'https://api.example.com',
  has_key: true,
  revision: 1,
  model_count: 0,
  problems: [],
  refs: { runtimes: [] },
  ...over,
})

const modelRow = (over: Record<string, unknown> = {}) => ({
  id: 'mdl_a',
  provider_id: 'prov_openai',
  model: 'gpt-x',
  input_modalities: ['text'],
  max_input_tokens: null,
  max_output_tokens: null,
  revision: 1,
  ...over,
})

const emptyView = { models: [], default_model: '', effective: 'applied', effective_at: 'now', sync: [] }

beforeEach(() => {
  toast.mockReset()
  listProviders.mockReset()
  listModels.mockReset()
  listModelCatalog.mockReset()
  listModelCatalog.mockResolvedValue({ image_input: null })
})

describe('ModelSettingsPanel', () => {
  it('无供应商：两块各自空态 + 新建供应商入口', async () => {
    listProviders.mockResolvedValue([])
    listModels.mockResolvedValue(emptyView)
    render(<ModelSettingsPanel />)
    expect(await screen.findByText('还没有供应商')).toBeInTheDocument()
    expect(screen.getByText('还没有可挂靠的供应商')).toBeInTheDocument()
    // 唯一「新建」入口（列表最后一格）
    expect(screen.getByRole('button', { name: /新建供应商/ })).toBeInTheDocument()
  })

  it('有供应商：渲染名称、协议标签与模型数', async () => {
    listProviders.mockResolvedValue([provider({ model_count: 3 })])
    listModels.mockResolvedValue(emptyView)
    render(<ModelSettingsPanel />)
    // 供应商名在「供应商」卡片与「模型」分组头都会出现 → 用 findAllByText。
    expect((await screen.findAllByText('我的端点')).length).toBeGreaterThan(0)
    expect(screen.getByText('openai')).toBeInTheDocument()
    expect(screen.getByText('3 个模型')).toBeInTheDocument()
  })

  it('默认模型：页头只读状态显示「默认模型：<名>」', async () => {
    listProviders.mockResolvedValue([provider({ model_count: 1 })])
    listModels.mockResolvedValue({
      ...emptyView,
      models: [modelRow({ id: 'mdl_a', name: '旗舰模型' })],
      default_model: 'mdl_a',
    })
    render(<ModelSettingsPanel />)
    // 唯一标记 aria-label="默认模型"（A14 结构判据）
    const badge = await screen.findByLabelText('默认模型')
    expect(badge.textContent).toContain('默认模型：旗舰模型')
  })

  it('模型清单按供应商分组：分组头 + 添加模型入口', async () => {
    listProviders.mockResolvedValue([provider({ model_count: 1 })])
    listModels.mockResolvedValue({
      ...emptyView,
      models: [modelRow({ id: 'mdl_a', name: '旗舰模型' })],
      default_model: '',
    })
    render(<ModelSettingsPanel />)
    await screen.findAllByText('我的端点')
    expect(screen.getByRole('button', { name: /添加模型/ })).toBeInTheDocument()
    // 模型行渲染（展示名回落或直显）
    expect(screen.getAllByText(/旗舰模型/).length).toBeGreaterThan(0)
  })

  it('同步状态区域渲染（生效结论：已生效/待生效）', async () => {
    listProviders.mockResolvedValue([provider()])
    listModels.mockResolvedValue({ ...emptyView, effective: 'applied' })
    render(<ModelSettingsPanel />)
    await screen.findAllByText('我的端点')
    expect(screen.getByText(/同步状态：/)).toBeInTheDocument()
  })

  it('供应商清单取不到时 toast 报错', async () => {
    listProviders.mockRejectedValueOnce(new Error('boom'))
    listModels.mockResolvedValue(emptyView)
    render(<ModelSettingsPanel />)
    await waitFor(() => expect(toast).toHaveBeenCalledWith('boom'))
  })
})
