// ─────────────────────────────────────────────────────────────────────────────
// renderers/OfficeRenderer.tsx —— Word / PPT（docx/pptx…）正文
//
// 为什么要这一层：docx/pptx 是 ZIP 二进制，直接按文本/字节渲染必然乱码。这里在**服务端**把文档
// 转成 PDF（@ark-mate/api 的 fetchFilePdf → POST /files/:id/convert-pdf），再**复用现有 PdfRenderer**
// 渲染（不自己写 PDF 渲染——pdfjs 的加载 / worker / 缩放都在 PdfRenderer 里收口）。
//
// 数据来源：本组件需要 **fileId**（= core_file_upload.id，convert-pdf 端点的入参）。
//   · 首选：PreviewBodyProps 上的可选 `fileId`（由 PreviewProvider 用 open.file.file_id 注入）。
//   · 兜底：若某入口只在 content 里给了 { url }（签名的 /objects 链接），URL 里是**存储 key**
//     而非 upload id，**无法**可靠还原 fileId —— 此时落明确的失败态，提示需要 fileId，
//     而不是把 ZIP 二进制当 PDF 硬渲染。
//
// 加载 / 失败态：转 PDF 是一次服务端进程调用（可能几百毫秒），期间显示「正在转换…」；
// 失败（payload 缺失 40501 / 文件不存在 40401 / 转换失败 50001）原样展示后端 msg。
// ─────────────────────────────────────────────────────────────────────────────
import { useEffect, useRef, useState } from 'react'
import { fetchFilePdf } from '@ark-mate/api'
import type { PreviewBodyProps } from '../routes'
import PdfRenderer from './PdfRenderer'

/**
 * OfficeRenderer 的 props：在 PreviewBodyProps 基础上补一个可选 `fileId`。
 *
 * `fileId` 若 Lead 尚未加进 PreviewBodyProps，本组件用交叉类型自身声明即可编译；一旦它在
 * PreviewBodyProps 上落地，交叉类型自动收窄为其子集，无需再改。
 */
export type OfficeRendererProps = PreviewBodyProps & {
  /** mate 体系 file_id（= core_file_upload.id）——convert-pdf 的入参。 */
  fileId?: string
}

/** 中心的加载/失败态。 */
function CenterNote({ children, error }: { children: React.ReactNode; error?: boolean }) {
  return (
    <div
      style={{
        height: '100%',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: 24,
        textAlign: 'center',
        color: error ? 'var(--error, #d92d20)' : 'var(--text2, #6b7280)',
      }}
    >
      {children}
    </div>
  )
}

export default function OfficeRenderer({ content, fileName, fileId }: OfficeRendererProps) {
  const [pdf, setPdf] = useState<Uint8Array | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  // 从 content 兜底猜 fileId：仅当调用方明确把 upload id 塞进了 content（本仓约定不这么做，
  // 因为 renderer 模式的 content 是 { url } 签名链接）。留作防御，避免误把 url 当 id。
  const resolvedFileId = fileId && fileId.trim() !== '' ? fileId : ''

  // 用 ref 记录已请求的 key，避免 StrictMode 双调用 / 重渲染重复发起转换。
  const requestedRef = useRef<string | null>(null)

  useEffect(() => {
    if (resolvedFileId === '') {
      setLoading(false)
      setPdf(null)
      setError(
        content && typeof content === 'object' && 'url' in content
          ? '无法预览 Office 文档：缺少 fileId（预览入口需把 core_file_upload.id 作为 fileId 传入，仅凭签名 URL 无法转 PDF）'
          : '无法预览 Office 文档：缺少 fileId',
      )
      return
    }
    const key = `${resolvedFileId}::${fileName}`
    if (requestedRef.current === key) return
    requestedRef.current = key

    let cancelled = false
    setLoading(true)
    setPdf(null)
    setError(null)

    void (async () => {
      try {
        const bytes = await fetchFilePdf(resolvedFileId, fileName)
        if (cancelled) return
        setPdf(bytes)
      } catch (e) {
        if (cancelled) return
        setError(e instanceof Error ? e.message : 'Office 转 PDF 失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()

    return () => {
      cancelled = true
      // 允许 effect 重跑时重新请求（fileId/fileName 变化）。
      requestedRef.current = null
    }
  }, [resolvedFileId, fileName, content])

  if (loading) {
    return <CenterNote>正在把 {fileName} 转换为 PDF…</CenterNote>
  }
  if (error) {
    return <CenterNote error>{error}</CenterNote>
  }
  if (!pdf) {
    return <CenterNote>尚无内容</CenterNote>
  }
  // 复用 PdfRenderer：把转好的 PDF 字节按 bytes-complete 契约喂给它。
  return <PdfRenderer content={pdf} fileName={fileName} />
}
