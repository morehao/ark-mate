// ─────────────────────────────────────────────────────────────────────────────
// PreviewShell.tsx —— 预览外壳（头 / 体 / 脚），复用 antd Drawer | Modal
//
// shape='drawer'（文档类）→ 右侧侧栏，会话不中断；shape='modal'（图片）→ 居中大图。
// 头：标题 + 类型 + 下载 + 关闭；体：children（已注入内容的渲染器）；失败/加载态在此统一兜底。
//
// 下载出口（FE-P3 §3.2）：用**已取内容**造 Blob → a[download]（文件名取 fileName）。
//   - 已取的是 Uint8Array：直接 Blob 下载；
//   - 已取的是 string：按 text/plain 下载（仅在确实需要时，例如 text-pages 但无渲染器下载时）；
//   - renderer 类（内容为 { url }）：优先用签名 url 直接 a[download]（不重复 fetch），
//     若 url 无法直接触发下载（跨域等）则退化为 fetch→Blob→下载。
// ─────────────────────────────────────────────────────────────────────────────
import { type ReactNode } from 'react'
import { Drawer, Modal, Button, Space } from 'antd'
import { tokens } from '@ark-mate/ui'
import type { Shape } from './routes'
import { LoadingState } from './states/LoadingState'
import { ErrorState } from './states/ErrorState'

export interface PreviewShellProps {
  shape: Shape
  title: string
  fileName: string
  sizeBytes?: number
  loading: boolean
  error: string | null
  /** 已取内容（用于下载出口）；renderer 类给 { url }。 */
  content?: string | Uint8Array | { url: string }
  onClose: () => void
  children: ReactNode
}

function humanSize(n?: number): string {
  if (!n || n <= 0) return ''
  const units = ['B', 'KB', 'MB', 'GB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

/** 触发浏览器下载：a[download] + objectURL。 */
function saveBlob(blob: Blob, fileName: string): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = fileName
  a.rel = 'noopener'
  document.body.appendChild(a)
  a.click()
  a.remove()
  // 稍后释放，确保 click 已被消费。
  setTimeout(() => URL.revokeObjectURL(url), 0)
}

/** 用已取内容下载；renderer 类（{ url }）先尝试直接下链接，失败再 fetch。 */
async function downloadContent(content: PreviewShellProps['content'], fileName: string): Promise<void> {
  // 用 ArrayBuffer.isView 而非 instanceof Uint8Array：跨 realm（jsdom/worker）instanceof 会失效。
  if (ArrayBuffer.isView(content)) {
    saveBlob(new Blob([(content as Uint8Array).slice()]), fileName)
    return
  }
  if (typeof content === 'string') {
    saveBlob(new Blob([content], { type: 'text/plain;charset=utf-8' }), fileName)
    return
  }
  if (content && typeof content === 'object' && 'url' in content) {
    // 签名 url：直接给下载链接（同源/允许 download 时最省事）。
    const a = document.createElement('a')
    a.href = content.url
    a.download = fileName
    a.rel = 'noopener'
    document.body.appendChild(a)
    a.click()
    a.remove()
    return
  }
  throw new Error('无可下载内容')
}

export function PreviewShell({ shape, title, fileName, sizeBytes, loading, error, content, onClose, children }: PreviewShellProps) {
  const canDownload = !loading && content !== undefined

  const handleDownload = () => {
    void downloadContent(content, fileName).catch((e) => {
      console.error('下载失败', e)
      // 兜底：打开签名 url 在新标签（若有）。
      if (content && typeof content === 'object' && 'url' in content) {
        window.open(content.url, '_blank', 'noopener,noreferrer')
      }
    })
  }

  const header = (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
      <div style={{ minWidth: 0 }}>
        <div style={{ fontSize: 13, fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {fileName}
        </div>
        <div style={{ fontSize: 10.5, color: tokens.textSecondary }}>
          {title}
          {sizeBytes ? ` · ${humanSize(sizeBytes)}` : ''}
        </div>
      </div>
      <div style={{ flex: 1 }} />
      <Space size={4}>
        <Button size="small" onClick={handleDownload} disabled={!canDownload}>
          下载
        </Button>
        <Button size="small" onClick={onClose}>
          关闭
        </Button>
      </Space>
    </div>
  )

  const body = (
    <div style={{ height: '100%', minHeight: 0, overflow: 'auto', background: tokens.codeBg }}>
      {error ? (
        <ErrorState message={error} />
      ) : loading ? (
        <LoadingState />
      ) : (
        children
      )}
    </div>
  )

  if (shape === 'modal') {
    return (
      <Modal open title={header} footer={null} onCancel={onClose} width="min(820px, 90vw)" centered
        styles={{ body: { height: '72vh', padding: 0 } }}>
        {body}
      </Modal>
    )
  }
  // drawer（文档类默认形态）
  return (
    <Drawer open title={header} placement="right" width="min(640px, 78vw)" onClose={onClose}
      styles={{ body: { padding: 0, height: '100%' } }}>
      {body}
    </Drawer>
  )
}
