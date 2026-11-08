// 状态页：类型不支持预览（FE-P3：含下载出口）。
import { tokens } from '@ark-mate/ui'

export interface UnsupportedStateProps {
  fileName: string
  /** 下载出口：有则展示"下载"按钮（不支持预览仍可下载）。 */
  onDownload?: () => void
}

export function UnsupportedState({ fileName, onDownload }: UnsupportedStateProps) {
  return (
    <div style={{ padding: 32, textAlign: 'center', color: tokens.textSecondary, fontSize: 13, lineHeight: 1.8 }}>
      <div style={{ fontSize: 15, color: tokens.text, marginBottom: 6 }}>暂不支持在线预览此类型</div>
      <div style={{ wordBreak: 'break-all' }}>{fileName}</div>
      {onDownload && (
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
          下载
        </button>
      )}
    </div>
  )
}
