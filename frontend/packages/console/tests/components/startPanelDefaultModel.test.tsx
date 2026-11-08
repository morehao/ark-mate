import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import type { ConfigOptionValue, ModelCatalogResp } from '@ark-mate/types'

/**
 * StartPanel · 默认模型预选（修：「设置里设了默认模型，新建会话页不预选」）。
 *
 * 覆盖三点（对应 task-5 的验收）：
 *   (a) 无本地记忆 + 有默认模型且在候选 → 预选该默认模型；
 *   (b) 有本地合法记忆 → 沿用记忆，不被默认模型覆盖；
 *   (c) 默认模型不可选（广告了但平台清单里没有它）→ 不预选（不塞非法值）。
 *
 * 测法：mock 掉 store 的 useMate（按 selector 从一份可变 state 取值）+ @ark-mate/api + 路由，
 * 并把几颗与本 bug 无关的重子组件（工作区/能力/权限）换成空桩，隔离出「模型预选」这一条逻辑。
 * 模型选择器用**真组件**，用它关闭态展示的名字反证「到底预选了哪条」。
 */

const nav = vi.fn()
vi.mock('react-router-dom', () => ({
  useNavigate: () => nav,
}))

// 可变的 store 快照：每个用例前重置。
type FakeState = Record<string, unknown>
let state: FakeState = {}
const loadModelCatalog = vi.fn()
const loadDefaultModel = vi.fn()
const createAndRun = vi.fn()
function resetState(over: Partial<FakeState> = {}) {
  state = {
    createAndRun,
    modelCatalog: null,
    loadModelCatalog,
    defaultModelId: null,
    loadDefaultModel,
    workspaces: null,
    preferredWorkspaceId: null,
    setPreferredWorkspace: vi.fn(),
    pendingPrompt: null,
    consumePendingPrompt: vi.fn(),
    ...over,
  }
}
vi.mock('../../src/store', () => ({
  useMate: (selector: (s: FakeState) => unknown) => selector(state),
}))

vi.mock('@ark-mate/api', () => ({
  createSession: vi.fn(),
  uploadAttachment: vi.fn(),
  fetchFileUrl: (id: string) => `blob:${id}`,
  toErrorMessage: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))

// 与本 bug 无关的重子组件 → 空桩（各自有自己的用例，这里只测模型预选）。
vi.mock('../../src/components/WorkspacePicker', () => ({
  WorkspacePicker: () => null,
}))
vi.mock('../../src/components/CapabilityBar', () => ({
  CapabilityBar: () => null,
}))
vi.mock('../../src/components/PermissionChip', () => ({
  PermissionChip: () => null,
}))

import { StartPanel } from '../../src/components/StartPanel'

const LAST_MODEL_KEY = 'mate.web.lastModel'

// 目录：广告两条候选，都带平台行 id（= 都能选）。
const catalog: ModelCatalogResp = {
  source: 'session',
  options: [
    {
      category: 'model',
      values: [
        { value: '["p","a"]', name: '模型 A', model_id: 'mdl_A' },
        { value: '["p","b"]', name: '模型 B', model_id: 'mdl_B' },
      ] as ConfigOptionValue[],
    },
  ],
} as unknown as ModelCatalogResp

beforeEach(() => {
  nav.mockReset()
  loadModelCatalog.mockReset()
  loadDefaultModel.mockReset()
  createAndRun.mockReset()
  localStorage.clear()
  resetState()
})

describe('StartPanel · 默认模型预选', () => {
  it('(a) 无本地记忆 + 默认在候选 → 预选默认模型', () => {
    resetState({ modelCatalog: catalog, defaultModelId: 'mdl_B' })
    render(<StartPanel />)
    // 关闭态展示默认模型的名字（真 ModelSelect 按候选反查 name）。
    expect(screen.getByText('模型 B')).toBeInTheDocument()
  })

  it('(b) 有本地合法记忆 → 沿用记忆，不被默认覆盖', () => {
    localStorage.setItem(LAST_MODEL_KEY, 'mdl_A')
    resetState({ modelCatalog: catalog, defaultModelId: 'mdl_B' })
    render(<StartPanel />)
    expect(screen.getByText('模型 A')).toBeInTheDocument()
    expect(screen.queryByText('模型 B')).toBeNull()
  })

  it('(c) 默认模型不可选（广告了但平台没声明）→ 不预选', () => {
    // 目录里只有一条「广告了但 model_id 为空」的候选，默认模型指向它 → 不该被塞进状态。
    const badCatalog = {
      source: 'session',
      options: [
        {
          category: 'model',
          values: [{ value: '["p","x"]', name: '未声明模型' }],
        },
      ],
    } as unknown as ModelCatalogResp
    resetState({ modelCatalog: badCatalog, defaultModelId: 'mdl_missing' })
    render(<StartPanel />)
    // 没被预选 → 关闭态显示占位文案「跟随默认」，且不出现任何模型名。
    expect(screen.queryByText('未声明模型')).toBeNull()
    expect(screen.getByText('模型 · 跟随默认')).toBeInTheDocument()
  })

  it('(c2) 默认模型 id 不在任何候选里 → 不预选', () => {
    resetState({ modelCatalog: catalog, defaultModelId: 'mdl_orphan' })
    render(<StartPanel />)
    expect(screen.getByText('模型 · 跟随默认')).toBeInTheDocument()
  })
})
