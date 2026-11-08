// ─────────────────────────────────────────────────────────────────────────────
// PreviewProvider.tsx —— 全局单例：openPreview(file) / close()
//
// 职责（对照设计文档 §3.5.7 / §4.0）：
//   1) resolvePreview(file.name) → 得到 PreviewDefinition（含 shape / loading / body）
//   2) shape 决定外壳（drawer 侧栏 | modal 大图）——§4.0 的唯一事实源
//   3) loading 决定取数：
//        text-pages    → fetchFileText  (string)
//        bytes-complete→ fetchFileBytes (Uint8Array)
//        renderer      → fetchFileUrl   (string，body 自取)
//   4) 把 content 注入 def.body
//   5) PREVIEW_V2 关闭时回退旧行为：拿签名 url 后 window.open（灰度回退）
// 挂一次（App 根部）；AttachmentChip / SessionDetail 产物行 / MaterialPage 节点都调它。
// ─────────────────────────────────────────────────────────────────────────────
import { createContext, useCallback, useContext, useMemo, useState, Suspense, type ReactNode } from 'react'
import { resolvePreview, type PreviewDefinition } from './routes'
import { fetchFileBytes, fetchFileText, fetchFileUrl } from '@ark-mate/api'
import { PreviewShell } from './PreviewShell'
import { isPreviewV2Enabled } from './flags'

/** 打开预览所需的文件信息（各模块只需提供 file_id + 展示字段）。 */
export interface PreviewFile {
  /** 字节句柄（= ginupload file_id）；各模块返回体已带。 */
  file_id: string
  name: string
  size_bytes?: number
}

interface PreviewContextValue {
  openPreview: (file: PreviewFile) => void
  close: () => void
}

const PreviewContext = createContext<PreviewContextValue | null>(null)

/** 按 loading 模式取内容。 */
async function loadContent(def: PreviewDefinition, fileId: string): Promise<string | Uint8Array | { url: string }> {
  switch (def.loading) {
    case 'text-pages':
      return fetchFileText(fileId)
    case 'bytes-complete':
      return fetchFileBytes(fileId)
    case 'renderer':
      return { url: await fetchFileUrl(fileId) }
  }
}

/** 旧行为回退：拿签名 url 后新标签打开（PREVIEW_V2 关闭时用）。 */
async function fallbackOpenExternal(fileId: string): Promise<void> {
  const url = await fetchFileUrl(fileId)
  window.open(url, '_blank', 'noopener,noreferrer')
}

export function PreviewProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState<
    { def: PreviewDefinition; file: PreviewFile; content: string | Uint8Array | { url: string } } | null
  >(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const openPreview = useCallback((file: PreviewFile) => {
    if (!isPreviewV2Enabled()) {
      // 灰度回退：不进应用内预览壳，走旧的外部打开。
      void fallbackOpenExternal(file.file_id).catch((e) => {
        // 回退路径失败只能提示；不抛到调用方（openPreview 是同步签名）。
        console.error('预览外部打开失败', e)
      })
      return
    }
    const def = resolvePreview(file.name)
    setError(null)
    setOpen({ def, file, content: '' }) // 先开壳（骨架），再取数
    setLoading(true)
    loadContent(def, file.file_id)
      .then((content) => setOpen({ def, file, content }))
      // 失败态上下文不吞：error.message 原样透传给预览壳（可能已含实际请求 URL/origin，
      // 由 @ark-mate/api 的 fetchFile* 拼入），展示层再做 URL 识别，便于定位打到了哪个端口。
      .catch((e) => setError(e instanceof Error ? e.message : '加载失败'))
      .finally(() => setLoading(false))
  }, [])

  const close = useCallback(() => {
    setOpen(null)
    setError(null)
    setLoading(false)
  }, [])

  const value = useMemo(() => ({ openPreview, close }), [openPreview, close])

  return (
    <PreviewContext.Provider value={value}>
      {children}
      {open && (
        <PreviewShell
          shape={open.def.shape}
          title={open.def.title()}
          fileName={open.file.name}
          sizeBytes={open.file.size_bytes}
          loading={loading}
          error={error}
          content={open.content}
          onClose={close}
        >
          <Suspense fallback={<div style={{ padding: 24, color: 'var(--text2,#6b7280)' }}>正在加载渲染器…</div>}>
            <open.def.body fileName={open.file.name} content={open.content} sizeBytes={open.file.size_bytes} />
          </Suspense>
        </PreviewShell>
      )}
    </PreviewContext.Provider>
  )
}

/** 消费：任意组件里 const { openPreview } = usePreview()。 */
export function usePreview(): PreviewContextValue {
  const ctx = useContext(PreviewContext)
  if (!ctx) throw new Error('usePreview 必须在 <PreviewProvider> 内使用')
  return ctx
}
