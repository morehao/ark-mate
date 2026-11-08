// 状态页：文件超出大小上限（FE-P3：含下载出口）。
import { tokens } from '@ark-mate/ui'

export interface OversizedStateProps {
  fileName: string
  sizeBytes?: number
  /** 超过的分类上限（字节，用于展示）。 */
  limitBytes?: number
  /** 下载出口：有则展示"下载"按钮。 */
  onDownload?: () => void
}

export function humanSize(n?: number): string {
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

function DownloadButton({ onDownload, label = '下载' }: { onDownload: () => void; label?: string }) {
  return (
    <button
      type="button"
      onClick={onDownload}
      style={{
        marginTop: 16,
        padding: '6px 16px',
        fontSize: 13,
        color: tokens.onPrimary,
        background: tokens.accent,
        border: 'none',
        borderRadius: tokens.radiusSm,
        cursor: 'pointer',
      }}
    >
      {label}
    </button>
  )
}

export function OversizedState({ fileName, sizeBytes, limitBytes, onDownload }: OversizedStateProps) {
  return (
    <div style={{ padding: 32, textAlign: 'center', color: tokens.textSecondary, fontSize: 13, lineHeight: 1.8 }}>
      <div style={{ fontSize: 15, color: tokens.text, marginBottom: 6 }}>文件过大，无法在线预览</div>
      <div style={{ wordBreak: 'break-all' }}>{fileName}</div>
      <div>
        {sizeBytes ? humanSize(sizeBytes) : '大小未知'}
        {limitBytes ? ` · 上限 ${humanSize(limitBytes)}` : ''}
      </div>
      {onDownload && <DownloadButton onDownload={onDownload} />}
    </div>
  )
}
