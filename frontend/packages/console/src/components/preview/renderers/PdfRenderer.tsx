// ─────────────────────────────────────────────────────────────────────────────
// renderers/PdfRenderer.tsx —— PDF 正文（pdfjs-dist，懒加载 chunk，契约见 PreviewBodyProps）
//
// 用法：被 routes.ts 的 pdf 定义 lazy() 加载；PreviewProvider 以 loading='bytes-complete'
// 先把 Uint8Array 取好，经 props.content 注入（本组件不再自己 fetch）。
//
// pdfjs 版本：pdfjs-dist **v6.5.438**（console package.json: "pdfjs-dist": "^6.5.438"）。
//
// 【v6 API 与旧 refimpl 的差异（实测结论）】
//   1. page.render({ canvas, viewport })：v6 的 RenderParameters 已主推 `canvas: HTMLCanvasElement`，
//      旧版的 `canvasContext` 标为 deprecated（仅兼容用，二者互斥、优先 canvas）。本实现用 `canvas`。
//   2. 销毁：v6 的 PDFDocumentProxy **没有** destroy()；销毁要回到 PDFDocumentLoadingTask.destroy()。
//      所以这里保留 loadingTask 引用，cleanup 时调 loadingTask.destroy()。
//
// 【worker 最终做法（计划未决问题，此处实测定论）】
//   `pdfjsLib.GlobalWorkerOptions.workerSrc = new URL('pdfjs-dist/build/pdf.worker.min.mjs', import.meta.url).toString()`
//   —— vite 会识别 `new URL(..., import.meta.url)` 并把 worker 作为独立资产 emit（v6 worker 产物为 .mjs）。
//   实测 `pnpm --filter @ark-mate/web build` 后：pdfjs 主体与 worker 均落在**独立的懒 chunk**，
//   主入口 index-*.js 不含 pdfjs（懒加载按预期生效），无需改 ?url / optimizeDeps。
//
// 【一期技术债登记】
//   本实现为「全量渲染」：把 PDF 所有页一次性渲染成 canvas 竖向排列（随缩放重渲全部页）。
//   未做虚拟化 / 按需渲染 / 分页懒加载——大页数 PDF 首屏会一次性创建大量 canvas，内存与耗时偏高。
//   生产化时应改为「可视页 ±N」的虚拟窗口渲染（对齐 dsh 的 LazyPdfBody 虚拟化方案）。
//
// 【缩放说明】缩放改变时整份重渲（scale 进 useEffect 依赖）。渲染按 devicePixelRatio 放大
//  backing store、再用 CSS 尺寸缩回，保证高分屏清晰；同时处理 0 尺寸 / 超大画布的安全边界。
// ─────────────────────────────────────────────────────────────────────────────
import { useEffect, useRef, useState } from 'react'
import * as pdfjsLib from 'pdfjs-dist'
import type { PreviewBodyProps } from '../routes'

// worker 走构建器（vite: new URL(..., import.meta.url)）；指向打包后的 .mjs worker 资产。
pdfjsLib.GlobalWorkerOptions.workerSrc = new URL(
  'pdfjs-dist/build/pdf.worker.min.mjs',
  import.meta.url,
).toString()

/** 缩放上下限。 */
const MIN_SCALE = 0.5
const MAX_SCALE = 3
const SCALE_STEP = 0.1
/** 单张 canvas 像素边长上限（避免超大页面撑爆 canvas 上限而渲染失败）。 */
const MAX_CANVAS_SIDE = 16384

/** 取当前设备像素比（SSR / 无 window 时退化为 1）。 */
function devicePixelRatioSafe(): number {
  if (typeof window === 'undefined') return 1
  const ratio = window.devicePixelRatio
  return Number.isFinite(ratio) && ratio > 0 ? ratio : 1
}

export default function PdfRenderer({ content, fileName }: PreviewBodyProps) {
  const canvasHost = useRef<HTMLDivElement>(null)
  const [pages, setPages] = useState<number>(0)
  const [error, setError] = useState<string | null>(null)
  const [scale, setScale] = useState(1)

  // content 必须是 Uint8Array（loading='bytes-complete'）。
  const bytes = content instanceof Uint8Array ? content : null

  useEffect(() => {
    if (!bytes) {
      setError('内容不是有效的 PDF 字节')
      return
    }
    let cancelled = false
    let loadingTask: pdfjsLib.PDFDocumentLoadingTask | null = null
    const host = canvasHost.current
    if (!host) return

    setError(null)
    setPages(0)

    void (async () => {
      try {
        loadingTask = pdfjsLib.getDocument({ data: bytes.slice() })
        const doc = await loadingTask.promise
        if (cancelled) return
        setPages(doc.numPages)
        host.replaceChildren()

        // 按 devicePixelRatio 放大 backing store，CSS 尺寸缩回，保证高分屏清晰。
        const dpr = devicePixelRatioSafe()

        for (let n = 1; n <= doc.numPages; n++) {
          const page = await doc.getPage(n)
          if (cancelled) return

          // viewport 用 CSS 像素尺寸（scale 为用户缩放）；backing store 再乘 dpr。
          const viewport = page.getViewport({ scale })
          const cssW = Math.max(1, Math.floor(viewport.width))
          const cssH = Math.max(1, Math.floor(viewport.height))
          // 安全上限：超过 canvas 边长上限时回退到 1x，避免 create/渲染直接抛错。
          const pixelRatio =
            cssW * dpr <= MAX_CANVAS_SIDE && cssH * dpr <= MAX_CANVAS_SIDE ? dpr : 1
          // render 用带 dpr 的 viewport 得到更高分辨率位图。
          const renderViewport = page.getViewport({ scale: scale * pixelRatio })

          const canvas = document.createElement('canvas')
          canvas.width = Math.max(1, Math.floor(renderViewport.width))
          canvas.height = Math.max(1, Math.floor(renderViewport.height))
          // CSS 尺寸固定为 CSS 像素，位图按 pixelRatio 放大 → 视觉清晰。
          canvas.style.width = `${cssW}px`
          canvas.style.height = `${cssH}px`
          canvas.style.display = 'block'
          canvas.style.margin = '16px auto'
          canvas.style.boxShadow = '0 1px 3px rgba(15,23,42,.12)'
          canvas.style.background = '#fff'
          host.appendChild(canvas)

          // pdfjs-dist v6：render 直接传 canvas（canvasContext 已 deprecated，二者互斥）。
          await page.render({ canvas, viewport: renderViewport }).promise
          if (cancelled) return
        }
      } catch (e) {
        if (!cancelled) {
          const detail = e instanceof Error ? e.message : String(e)
          setError(detail || '渲染失败')
        }
      }
    })()

    return () => {
      cancelled = true
      // v6 PDFDocumentProxy 无 destroy；销毁走 loadingTask.destroy()。
      void loadingTask?.destroy()
    }
  }, [bytes, scale])

  if (error) {
    return (
      <div style={{ padding: 24, color: 'var(--error, #d92d20)' }}>无法预览 PDF：{error}</div>
    )
  }

  return (
    <div style={{ height: '100%', overflow: 'auto', background: 'var(--codeBg, #fafafa)' }}>
      <div
        style={{
          position: 'sticky',
          top: 0,
          display: 'flex',
          gap: 8,
          alignItems: 'center',
          padding: '6px 12px',
          background: 'var(--surface,#fff)',
          borderBottom: '1px solid var(--border,#eceef2)',
          fontSize: 12,
          zIndex: 1,
        }}
      >
        <button
          type="button"
          aria-label="缩小"
          onClick={() => setScale((s) => Math.max(MIN_SCALE, Number((s - SCALE_STEP).toFixed(2))))}
        >
          −
        </button>
        <span style={{ fontFamily: 'monospace', minWidth: 42, textAlign: 'center' }}>
          {Math.round(scale * 100)}%
        </span>
        <button
          type="button"
          aria-label="放大"
          onClick={() => setScale((s) => Math.min(MAX_SCALE, Number((s + SCALE_STEP).toFixed(2))))}
        >
          ＋
        </button>
        <button type="button" onClick={() => setScale(1)} aria-label="重置缩放">
          重置
        </button>
        <span style={{ color: 'var(--text2,#6b7280)', marginLeft: 'auto' }}>
          {fileName}
          {pages > 0 ? ` · ${pages} 页` : ''}
        </span>
      </div>
      <div ref={canvasHost} />
    </div>
  )
}
