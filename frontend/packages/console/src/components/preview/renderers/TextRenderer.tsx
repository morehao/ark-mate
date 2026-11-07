// ─────────────────────────────────────────────────────────────────────────────
// renderers/TextRenderer.tsx —— 纯文本兜底（TEXT_FALLBACK）
//
// 由 BODY-TEXT（task-10）定稿，覆盖 FE-SKELETON 阶段的最小占位。契约见 ../routes.ts：
// loading='text-pages' → content 是 string；本组件防御性兼容 Uint8Array（TextDecoder 兜底）。
//
// 行为：等宽字体、保留空白与换行（pre-wrap）、过长可滚动（横纵双向）。
// 编码：DECODE 统一按 UTF-8 解；若后端以其它 charset 提供文本，应由后端在读取/转码阶段
//   转成 UTF-8 再下发（前端不做 charset 嗅探），此口径登记于此，避免以后重复讨论。
// 失败：解码/渲染异常一律落失败态（不炸整个预览壳）。
// ─────────────────────────────────────────────────────────────────────────────
import { tokens } from '@ark-mate/ui'
import type { PreviewBodyProps } from '../routes'

/** 把 content 规整成 string（契约给 string；防御性兼容 Uint8Array）。 */
function toText(content: PreviewBodyProps['content']): string {
  if (typeof content === 'string') return content
  // 用 ArrayBuffer.isView 而非 instanceof Uint8Array：跨 realm（jsdom/worker）instanceof 会失效。
  if (ArrayBuffer.isView(content)) return new TextDecoder().decode(content as ArrayBufferView as Uint8Array)
  // 契约外（如 { url }）不猜测，返回空串走空态。
  return ''
}

export default function TextRenderer({ content }: PreviewBodyProps) {
  let text: string
  try {
    text = toText(content)
  } catch (err) {
    return (
      <div style={{ padding: 24, color: tokens.error, fontSize: 13 }}>
        预览加载失败：{err instanceof Error ? err.message : String(err)}
      </div>
    )
  }

  return (
    <pre
      style={{
        margin: 0,
        padding: 16,
        fontFamily: tokens.fontMono,
        fontSize: 12.5,
        lineHeight: 1.6,
        whiteSpace: 'pre-wrap',
        wordBreak: 'break-word',
        color: tokens.text,
        // 过长可滚动：外层 PreviewShell 已是 overflow:auto；这里再显式声明，保证单独使用时也能滚。
        overflow: 'auto',
        minHeight: '100%',
        boxSizing: 'border-box',
      }}
    >
      {text}
    </pre>
  )
}
