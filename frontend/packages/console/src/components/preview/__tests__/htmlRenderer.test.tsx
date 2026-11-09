// ─────────────────────────────────────────────────────────────────────────────
// V6 单测：HtmlRenderer —— 以受控 sandboxed iframe 渲染页面（而非显示源码），失败态兜底。
//
// 对齐 `pnpm test:component`（= vitest run，jsdom）。key 断言：
//   1) content 是 Uint8Array 时，解成 string 且**作为 srcDoc** 注入 <iframe>（不是当文本节点渲染）；
//   2) sandbox=""（最严格，不允许脚本/同源）—— 安全边界；
//   3) 空字节 / 非法 content（{ url }）→ 落失败态文案，不抛错、不渲染 iframe。
// ─────────────────────────────────────────────────────────────────────────────
import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import HtmlRenderer from '../renderers/HtmlRenderer'
import type { PreviewBodyProps } from '../routes'

const enc = (s: string): Uint8Array => new TextEncoder().encode(s)

function renderWith(content: PreviewBodyProps['content']) {
  return render(<HtmlRenderer fileName="report.html" content={content} sizeBytes={100} />)
}

describe('HtmlRenderer —— HTML 走 iframe 渲染页面，非源码', () => {
  it('Uint8Array 解码为 srcDoc，注入 sandboxed iframe', () => {
    const html = '<!doctype html><html><body><h1>你好 HTML</h1></body></html>'
    const { container } = renderWith(enc(html))

    const iframe = container.querySelector('iframe')
    expect(iframe).not.toBeNull()
    // 关键：内容是 srcDoc（浏览器会把它当文档渲染），而不是把源码当文本子节点塞进 DOM。
    expect(iframe!.getAttribute('srcdoc')).toBe(html)
    expect(iframe!.textContent ?? '').not.toContain('<h1>')
    // 最严格沙箱：属性存在且为空串（不允许脚本 / 同源）。
    expect(iframe!.getAttribute('sandbox')).toBe('')
    expect(iframe!.getAttribute('title')).toBe('HTML 预览')
    // 不应出现失败态。
    expect(container.textContent).not.toContain('预览加载失败')
  })

  it('也兼容 string 形态（防御性）', () => {
    const html = '<p>string 形态</p>'
    const { container } = renderWith(html)
    expect(container.querySelector('iframe')!.getAttribute('srcdoc')).toBe(html)
  })

  it('空字节 → 失败态，不渲染 iframe', () => {
    const { container } = renderWith(new Uint8Array())
    expect(container.querySelector('iframe')).toBeNull()
    expect(container.textContent).toContain('预览加载失败')
  })

  it('契约外 content（{ url }）→ 失败态，不渲染 iframe', () => {
    const { container } = renderWith({ url: 'http://127.0.0.1:10001/x' })
    expect(container.querySelector('iframe')).toBeNull()
    expect(container.textContent).toContain('预览加载失败')
  })
})
