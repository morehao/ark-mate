// ─────────────────────────────────────────────────────────────────────────────
// ErrorState 单测：失败态能否把 message 里的实际请求 URL/origin 透出来。
// 对齐 `pnpm --filter @ark-mate/console test:ui`（vitest + jsdom）。
// 注意：本文件位于 src/ 下会被 tsc 纳入类型检查，jest-dom 的 matcher 类型未在此
// 声明，故只用 Testing Library 的 getByText/queryByText（与 shell.test.tsx 一致）。
// ─────────────────────────────────────────────────────────────────────────────
import { describe, it, expect } from 'vitest'
import { createElement } from 'react'
import { render, screen } from '@testing-library/react'
import { ErrorState } from '../states/ErrorState'

describe('ErrorState 请求地址透出', () => {
  it('message 含 URL → 展示原文并单列「请求地址：<url>」', () => {
    const url = 'http://127.0.0.1:10001/api/files/abc'
    render(createElement(ErrorState, { message: `接口不存在：GET ${url} 404` }))

    // 原始 message 原样透传（不裁剪上下文）。
    expect(screen.getByText(/接口不存在/).textContent).toContain(url)
    // 单独一行展示请求地址，肉眼可辨端口。
    expect(screen.getByText(`请求地址：${url}`).textContent).toBe(`请求地址：${url}`)
  })

  it('message 含带查询串/路径的 https URL → 正确抽出（不吞尾部）', () => {
    const url = 'https://api.example.com/v1/file?id=42&x=1'
    render(createElement(ErrorState, { message: `加载失败 ${url}` }))
    expect(screen.getByText(`请求地址：${url}`).textContent).toBe(`请求地址：${url}`)
  })

  it('message 不含 URL → 行为向后兼容，不渲染「请求地址」行', () => {
    render(createElement(ErrorState, { message: '网络错误' }))
    expect(screen.getByText('预览加载失败：网络错误').textContent).toBe('预览加载失败：网络错误')
    expect(screen.queryByText(/请求地址/)).toBeNull()
  })

  it('URL 后紧跟全角标点/中文 → 只抽 URL，不吞后面整句（2026-11-08 实际踩到的回归）', () => {
    // 复现真实文案：「…（网络错误，POST http://127.0.0.1:10001/）：请求未拿到响应…」
    const msg = '预览取字节失败（网络错误，POST http://127.0.0.1:10001/）：请求未拿到响应——后端可能未启动'
    render(createElement(ErrorState, { message: msg }))
    // 「请求地址」行必须只有 URL 本身，不能包含「）：请求未拿到响应…」尾巴。
    expect(screen.getByText('请求地址：http://127.0.0.1:10001/').textContent).toBe(
      '请求地址：http://127.0.0.1:10001/',
    )
  })
})
