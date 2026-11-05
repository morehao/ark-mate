import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

/**
 * 刀 1 · StatusBar（全局底部状态条）——按 store 状态渲染的分支。
 *
 * 覆盖点：
 *   - wsState 三态文案（已连接 / 连接中 / 连接断开）；
 *   - 会话数概览（sessions 非空时"共 N 个会话"）；
 *   - 聊天页（pathname 属 chat）且运行失败时显示失败提示。
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
  it('wsState=open 显示"已连接"', () => {
    storeState = { wsState: 'open', sessions: [], runtimeDiagnostics: null }
    renderAt('/')
    expect(screen.getByText('已连接')).toBeInTheDocument()
  })

  it('wsState=connecting 显示"连接中…"', () => {
    storeState = { wsState: 'connecting', sessions: [], runtimeDiagnostics: null }
    renderAt('/')
    expect(screen.getByText('连接中…')).toBeInTheDocument()
  })

  it('wsState=closed 显示"连接断开"', () => {
    storeState = { wsState: 'closed', sessions: [], runtimeDiagnostics: null }
    renderAt('/')
    expect(screen.getByText(/连接断开/)).toBeInTheDocument()
  })

  it('会话非空时显示会话数', () => {
    storeState = { wsState: 'open', sessions: [{ id: 'a' }, { id: 'b' }], runtimeDiagnostics: null }
    renderAt('/')
    expect(screen.getByText('共 2 个会话')).toBeInTheDocument()
  })

  it('聊天页且运行失败时显示失败提示', () => {
    storeState = {
      wsState: 'open',
      sessions: [],
      runtimeDiagnostics: { last_error: { class: 'runtime', code: 'E42', details: 'x' } },
    }
    // `/sessions...` 属 chat 上下文（railContextOf），失败提示应出现并带 code。
    renderAt('/sessions/s1')
    const node = screen.getByText(/助手最近一次运行失败/)
    expect(node.textContent).toContain('E42')
  })
})
