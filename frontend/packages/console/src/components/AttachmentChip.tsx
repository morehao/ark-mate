import { FileOutlined, CloseOutlined } from '@ant-design/icons'
import { Modal } from 'antd'
import { tokens } from '@ark-mate/ui'
import type { AttachmentMeta } from '@ark-mate/types'
import { attachmentContentUrl } from '@ark-mate/api'

interface Props {
  /** 附件元数据（id/name/mime_type/size_bytes/kind）。 */
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
 * AttachmentChip 一个附件 chip（v32）：图标 + 文件名 + 大小；图片点开看大图，文档点开预览；
 * 待发的 chip 带「移除 ×」。颜色/间距全走 token（不硬编码色值）。
 */
export function AttachmentChip({ attachment, onRemove }: Props) {
  const isImage = attachment.kind === 'image'
  const href = attachmentContentUrl(attachment.id)
  const open = () => {
    if (isImage) {
      Modal.info({
        title: attachment.name,
        icon: null,
        width: 720,
        content: (
          <img
            src={href}
            alt={attachment.name}
            style={{ maxWidth: '100%', maxHeight: '60vh', display: 'block', margin: '0 auto' }}
          />
        ),
        okText: '关闭',
      })
    } else {
      // 文档：新标签打开字节流（inline，浏览器据 Content-Type 决定渲染/下载）。
      window.open(href, '_blank', 'noopener,noreferrer')
    }
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
      {isImage ? (
        <img src={href} alt="" style={{ width: 18, height: 18, borderRadius: 3, objectFit: 'cover' }} />
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
