// ─────────────────────────────────────────────────────────────────────────────
// renderers/ImageRenderer.tsx —— 图片正文（antd Image，自带放大/旋转/多图预览）
//
// 契约见 PreviewBodyProps：image 定义 loading='bytes-complete'（content 为 Uint8Array），
// 但 Provider 也可能在别处传 { url }（renderer 模式 / 已签名 url），故两种都兼容：
//   · Uint8Array  → Blob → URL.createObjectURL 造本地 blob URL（useEffect cleanup 里 revoke，防泄漏）
//   · { url }     → 直接当 src
//   · string      → 误传时也兼容：当 url（http/data/blob）或 dataURL 使用
// 渲染用 antd Image：自带放大、旋转、多图预览；居中 + 合理 max 尺寸。
// 取不到可用 src 或图片加载失败 → 落失败态。
// ─────────────────────────────────────────────────────────────────────────────
import { useEffect, useMemo, useState } from 'react'
import { Image } from 'antd'
import { tokens } from '@ark-mate/ui'
import type { PreviewBodyProps } from '../routes'

/** 从各种 content 形态里解析出可用的 src；Uint8Array 走 objectURL（返回受管标记）。 */
function resolveSrc(content: PreviewBodyProps['content']): { src: string; managed: boolean } {
  if (content instanceof Uint8Array) {
    // slice() 拷贝一份，避免底层 buffer 被 detach/复用导致 Blob 失效。
    return { src: URL.createObjectURL(new Blob([content.slice()])), managed: true }
  }
  if (typeof content === 'string') {
    // 误传 string：非空即可当 url / dataURL（http(s)、data:、blob: 等）。
    return { src: content.trim(), managed: false }
  }
  if (content && typeof content === 'object' && 'url' in content) {
    return { src: typeof content.url === 'string' ? content.url.trim() : '', managed: false }
  }
  return { src: '', managed: false }
}

export default function ImageRenderer({ content, fileName }: PreviewBodyProps) {
  // src 随 content 计算；同时记录它是否是我们创建的 objectURL，便于 cleanup。
  const resolved = useMemo(() => resolveSrc(content), [content])
  const [failed, setFailed] = useState(false)

  // content 变化时重置失败态（同一个 blob src 变化要重新尝试）。
  useEffect(() => {
    setFailed(false)
  }, [resolved.src])

  // 清理：只 revoke 自己创建的 blob URL，避免泄漏；外部 url 不动。
  useEffect(() => {
    if (!resolved.managed) return
    const { src } = resolved
    return () => {
      URL.revokeObjectURL(src)
    }
  }, [resolved])

  if (!resolved.src || failed) {
    return (
      <div
        style={{
          height: '100%',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          padding: 24,
          color: tokens.error,
          fontSize: 13,
        }}
      >
        {failed ? '图片加载失败' : '图片内容不可用'}
      </div>
    )
  }

  return (
    <div
      style={{
        height: '100%',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: 16,
        overflow: 'auto',
        background: tokens.codeBg,
      }}
    >
      <Image
        src={resolved.src}
        alt={fileName}
        // 预览（点开大图）时同样用这份 src，自带缩放/旋转。
        style={{ maxWidth: '100%', maxHeight: '100%', objectFit: 'contain' }}
        onError={() => setFailed(true)}
        // antd Image 的预览层在 modal 内再套一层，给个合理宽度。
        preview={{ mask: '预览' }}
      />
    </div>
  )
}
