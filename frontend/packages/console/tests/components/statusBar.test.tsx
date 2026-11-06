import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

/**
 * 刀 1 · StatusBar —— 收窄为「只在真正出问题时才占位」的底条。
 *
 * 会话数量已挪进左栏 tab（`会话 / 归档`），连接状态已收进左栏 footer。
 * 本组件现在只回答「助手最近一次运行失败了，为什么」；正常时**不渲染任何节点**。
 *
 * 依赖 mock：store(useMate) 按 selector 返回；react-router 用 MemoryRouter 提供 pathname。
 */

let storeState: Record<string, unknown>
vi.mock('../../src/store', () => ({
  useMate: (selector: (s: Record<string, unknown>) => unknown) => selector(storeState),
}))

import { StatusBar } from '../../src/components/StatusBar'

const renderAt = (path: string) =>
  render(
    <MemoryRouter initialEntries={[path]}>
      <StatusBar />
    </MemoryRouter>,
  )

describe('StatusBar', () => {
  it('正常时（无运行失败）不渲染任何节点，把高度让还给内容区', () => {
    storeState = { runtimeDiagnostics: null }
    const { container } = renderAt('/sessions/s1')
    expect(container).toBeEmptyDOMElement()
  })

  it('不再显示会话数量，也不再显示连接状态（这两样已迁往左栏）', () => {
    storeState = { wsState: 'open', sessions: [{ id: 'a' }, { id: 'b' }], runtimeDiagnostics: null }
    const { container } = renderAt('/')
    expect(container).toBeEmptyDOMElement()
    expect(screen.queryByText('共 2 个会话')).not.toBeInTheDocument()
  })

  it('聊天页且运行失败时显示失败提示并带 code', () => {
    storeState = {
      runtimeDiagnostics: { last_error: { class: 'runtime', code: 'E42', details: 'x' } },
    }
    // `/sessions...` 属 chat 上下文（railContextOf），失败提示应出现并带 code。
    renderAt('/sessions/s1')
    const node = screen.getByText(/助手最近一次运行失败/)
    expect(node.textContent).toContain('E42')
  })

  it('非聊天页即使有失败也不占用底条', () => {
    storeState = { runtimeDiagnostics: { last_error: { class: 'runtime', code: 'E42' } } }
    const { container } = renderAt('/usage')
    expect(container).toBeEmptyDOMElement()
  })
})
