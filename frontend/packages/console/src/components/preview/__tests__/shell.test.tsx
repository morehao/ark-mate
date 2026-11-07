// ─────────────────────────────────────────────────────────────────────────────
// FE-P3 单测：PreviewShell 下载出口（Uint8Array → Blob → a[download]）+ 边界态渲染。
// 对齐 `pnpm test:component`（vitest run）。
// ─────────────────────────────────────────────────────────────────────────────
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { createElement } from 'react'
import { render, fireEvent, screen } from '@testing-library/react'
import { PreviewShell } from '../PreviewShell'
import { OversizedState } from '../states/OversizedState'
import { UnsupportedState } from '../states/UnsupportedState'

describe('PreviewShell 下载出口', () => {
  let createSpy: ReturnType<typeof vi.spyOn>
  let revokeSpy: ReturnType<typeof vi.spyOn>
  const clicks: HTMLAnchorElement[] = []

  beforeEach(() => {
    createSpy = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:mock-url')
    revokeSpy = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    // 拦截 a.click 并记录被点的 anchor（不真导航）。
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      clicks.push(this)
    })
    clicks.length = 0
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('Uint8Array 内容 → 造 Blob 并用 fileName 触发 a[download]', async () => {
    const bytes = new TextEncoder().encode('hello world')
    render(
      createElement(PreviewShell, {
        shape: 'drawer',
        title: 'PDF',
        fileName: 'report.pdf',
        sizeBytes: bytes.byteLength,
        loading: false,
        error: null,
        content: bytes,
        onClose: () => {},
        children: createElement('div', null, 'body'),
      }),
    )
    fireEvent.click(screen.getByRole('button', { name: /下\s*载/ }))
    expect(createSpy).toHaveBeenCalledTimes(1)
    expect(clicks.length).toBe(1)
    expect(clicks[0].download).toBe('report.pdf')
    expect(clicks[0].href).toBe('blob:mock-url')
    // objectURL 的释放是 setTimeout(…,0) 异步执行的：用假定时器推进确认被调用。
    await vi.waitFor(() => expect(revokeSpy).toHaveBeenCalled())
  })

  it('renderer 类 { url } → 直接用签名 url 触发下载（不造 Blob）', () => {
    render(
      createElement(PreviewShell, {
        shape: 'drawer',
        title: 'Office',
        fileName: 'deck.pptx',
        loading: false,
        error: null,
        content: { url: 'https://signed.example/deck.pptx' },
        onClose: () => {},
        children: null,
      }),
    )
    fireEvent.click(screen.getByRole('button', { name: /下\s*载/ }))
    expect(createSpy).not.toHaveBeenCalled()
    expect(clicks.length).toBe(1)
    expect(clicks[0].download).toBe('deck.pptx')
    expect(clicks[0].href).toBe('https://signed.example/deck.pptx')
  })

  it('加载中时下载按钮禁用', () => {
    render(
      createElement(PreviewShell, {
        shape: 'drawer',
        title: 'PDF',
        fileName: 'x.pdf',
        loading: true,
        error: null,
        content: undefined,
        onClose: () => {},
        children: null,
      }),
    )
    const btn = screen.getByRole('button', { name: /下\s*载/ }) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
  })
})

describe('边界态渲染', () => {
  it('Oversized 展示文件名/大小并暴露下载按钮', () => {
    const onDownload = vi.fn()
    render(createElement(OversizedState, { fileName: 'big.bin', sizeBytes: 200 * 1024 * 1024, limitBytes: 100 * 1024 * 1024, onDownload }))
    expect(screen.getByText('文件过大，无法在线预览').textContent).toBe('文件过大，无法在线预览')
    fireEvent.click(screen.getByRole('button', { name: /下\s*载/ }))
    expect(onDownload).toHaveBeenCalledTimes(1)
  })

  it('Unsupported 暴露下载出口', () => {
    const onDownload = vi.fn()
    render(createElement(UnsupportedState, { fileName: 'a.xyz', onDownload }))
    fireEvent.click(screen.getByRole('button', { name: /下\s*载/ }))
    expect(onDownload).toHaveBeenCalledTimes(1)
  })
})
