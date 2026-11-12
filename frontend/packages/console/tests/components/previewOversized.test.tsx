import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'
import { useCallback, type ReactNode } from 'react'

/**
 * 预览体积守卫接线（WP-C2 §4.1）组件测试。
 *
 * 断言口径：
 *  - 超限文件（size_bytes >= 该分类 defaultLimit）→ 渲染既有 `OversizedState`「文件过大，无法在线预览」，
 *    且**不进入取数**（fetchFileText / fetchFileBytes 零调用）；
 *  - 未超限 → 正常取数（fetch 被调用），随后渲染正文，不出现 OversizedState；
 *  - `limits.ts` 成为真实消费者（categoryOf/defaultLimit/resolveOversized 全链路跑到）。
 */

const { fetchFileText, fetchFileBytes, fetchFileUrl } = vi.hoisted(() => ({
  fetchFileText: vi.fn<(id: string) => Promise<string>>(),
  fetchFileBytes: vi.fn<(id: string) => Promise<Uint8Array>>(),
  fetchFileUrl: vi.fn<(id: string) => Promise<string>>(),
}))
vi.mock('@ark-mate/api', () => ({
  fetchFileText: (id: string) => fetchFileText(id),
  fetchFileBytes: (id: string) => fetchFileBytes(id),
  fetchFileUrl: (id: string) => fetchFileUrl(id),
}))

import { PreviewProvider, usePreview } from '../../src/components/preview/PreviewProvider'

// antd Drawer/Modal 依赖 ResizeObserver；jsdom 无此实现，补一个空壳。
class RO {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (!('ResizeObserver' in globalThis)) {
  ;(globalThis as unknown as { ResizeObserver: typeof RO }).ResizeObserver = RO
}
// antd 的 Drawer 会用 matchMedia 探测响应式断点；jsdom 无，补一个恒 false 的实现。
if (!('matchMedia' in globalThis)) {
  ;(globalThis as unknown as { matchMedia: (q: string) => unknown }).matchMedia = (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener() {},
    removeListener() {},
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() {
      return false
    },
  })
}

/** 测试夹具：暴露 openPreview 给用例驱动，自身不渲染额外 UI。 */
function Harness({ onReady }: { onReady: (open: (f: { file_id: string; name: string; size_bytes?: number }) => void) => void }) {
  const { openPreview } = usePreview()
  const cb = useCallback(() => onReady(openPreview), [onReady, openPreview])
  // 挂载后把 openPreview 交给用例。
  cb()
  return <div data-testid="harness" />
}

function renderWithProvider(children: ReactNode) {
  return render(<PreviewProvider>{children}</PreviewProvider>)
}

beforeEach(() => {
  fetchFileText.mockReset()
  fetchFileBytes.mockReset()
  fetchFileUrl.mockReset()
  fetchFileText.mockResolvedValue('# hello')
  fetchFileBytes.mockResolvedValue(new Uint8Array([1, 2, 3]))
  fetchFileUrl.mockResolvedValue('https://example.test/x')
  // 默认开启 PREVIEW_V2（清掉可能残留的 off）。
  try {
    localStorage.removeItem('ark-mate.preview-v2')
  } catch {
    /* jsdom 无 localStorage 时忽略 */
  }
})

describe('PreviewProvider 体积守卫', () => {
  it('超限文件 → 渲染 OversizedState，且不进入取数', async () => {
    let open: ((f: { file_id: string; name: string; size_bytes?: number }) => void) | null = null
    renderWithProvider(<Harness onReady={(fn) => (open = fn)} />)
    // 图片分类上限 50MB；给 60MB → 超限。
    act(() => open!({ file_id: 'f-big', name: 'big.png', size_bytes: 60 * 1024 * 1024 }))

    await waitFor(() => {
      expect(screen.getByText('文件过大，无法在线预览')).toBeInTheDocument()
    })
    // 超限不取数：bytes-complete 的图片本会走 fetchFileBytes，这里必须零调用。
    expect(fetchFileBytes).not.toHaveBeenCalled()
    expect(fetchFileText).not.toHaveBeenCalled()
  })

  it('未超限 → 正常取数并渲染正文，不出现 OversizedState', async () => {
    let open: ((f: { file_id: string; name: string; size_bytes?: number }) => void) | null = null
    renderWithProvider(<Harness onReady={(fn) => (open = fn)} />)
    // markdown 分类上限 30MB；给 1KB → 未超限，走 text-pages → fetchFileText。
    act(() => open!({ file_id: 'f-small', name: 'note.md', size_bytes: 1024 }))

    await waitFor(() => {
      expect(fetchFileText).toHaveBeenCalledWith('f-small')
    })
    expect(screen.queryByText('文件过大，无法在线预览')).not.toBeInTheDocument()
  })
})
