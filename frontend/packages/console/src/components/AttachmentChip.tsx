import { useEffect, useState } from 'react'
import { FileOutlined, CloseOutlined, FileImageOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { AttachmentMeta } from '@ark-mate/types'
import { fetchFileUrl } from '@ark-mate/api'
import { usePreview } from './preview/PreviewProvider'

interface Props {
  /** 附件元数据（id/name/mime_type/size_bytes/kind/file_id）。 */
  attachment: AttachmentMeta
  /** 可选：移除按钮（仅**待发** chip 有；气泡里的已发 chip 不传）。 */
  onRemove?: () => void
}

// 把字节数折成可读大小（1.2 MB / 340 KB / 512 B）。
function humanSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

/**
 * AttachmentChip 一个附件 chip（v32）：图标 + 文件名 + 大小；点击经统一预览壳打开
 * （图片走居中大图、文档走右侧侧栏）。待发的 chip 带「移除 ×」。
 * 字节取数一律经 file_id + 签名通路的预览层，不再直拼 content URL。
 * 颜色/间距全走 token（不硬编码色值）。
 */
export function AttachmentChip({ attachment, onRemove }: Props) {
  const { openPreview } = usePreview()
  const isImage = attachment.kind === 'image'

  // 图片 chip 的缩略图：按 file_id 取一次签名 url（失败静默回落到图标）。
  const [thumbUrl, setThumbUrl] = useState<string>('')
  useEffect(() => {
    if (!isImage || !attachment.file_id) return
    let cancelled = false
    fetchFileUrl(attachment.file_id)
      .then((u) => {
        if (!cancelled) setThumbUrl(u)
      })
      .catch(() => {
        /* 缩略图失败不影响点击预览 */
      })
    return () => {
      cancelled = true
    }
  }, [isImage, attachment.file_id])

  const open = () => {
    openPreview({ file_id: attachment.file_id, name: attachment.name, size_bytes: attachment.size_bytes })
  }

  return (
    <span
      className="fg-att-chip"
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 6,
        maxWidth: 240,
        padding: '3px 8px',
        borderRadius: tokens.radiusSm,
        background: tokens.softFill,
        border: `1px solid ${tokens.border}`,
        fontSize: 12,
        color: tokens.text,
        verticalAlign: 'middle',
      }}
    >
      {isImage && thumbUrl ? (
        <img src={thumbUrl} alt="" style={{ width: 18, height: 18, borderRadius: 3, objectFit: 'cover' }} />
      ) : isImage ? (
        <FileImageOutlined style={{ color: tokens.textSecondary }} />
      ) : (
        <FileOutlined style={{ color: tokens.textSecondary }} />
      )}
      <button
        type="button"
        onClick={open}
        title={`${attachment.name}（${humanSize(attachment.size_bytes)}）`}
        style={{
          border: 0,
          background: 'transparent',
          padding: 0,
          color: tokens.text,
          font: 'inherit',
          fontSize: 12,
          cursor: 'pointer',
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
          maxWidth: 150,
        }}
      >
        {attachment.name}
      </button>
      <span style={{ color: tokens.textSecondary, fontFamily: tokens.fontMono, fontSize: 10.5 }}>
        {humanSize(attachment.size_bytes)}
      </span>
      {onRemove && (
        <button
          type="button"
          onClick={onRemove}
          title="移除"
          aria-label="移除附件"
          style={{
            border: 0,
            background: 'transparent',
            padding: 0,
            lineHeight: 1,
            color: tokens.textSecondary,
            cursor: 'pointer',
          }}
        >
          <CloseOutlined style={{ fontSize: 10 }} />
        </button>
      )}
    </span>
  )
}
