// ─────────────────────────────────────────────────────────────────────────────
// renderers/HtmlRenderer.tsx —— HTML 正文（受控 sandboxed iframe 渲染页面，而非源码）
//
// 契约见 ../routes.ts：本渲染器对应 html 定义，loading='bytes-complete'（content 为
// Uint8Array 整块字节）。这里把字节用 TextDecoder 解成字符串，注入 iframe 的 srcDoc 让浏览器
// 真正「渲染页面」，而不是像代码视图那样把 HTML 源码按文本显示。
//
// 【为什么用 sandbox=""（最严格）而不是 allow-scripts allow-same-origin】
//   · 参考 dsh 的 HtmlBody：静态预览用 `sandbox=""`，交互预览才开 `allow-scripts`——
//     且**从不**同时给 `allow-scripts` + `allow-same-origin`（二者共存等于把沙箱废掉：
//     脚本可 parent 访问 / 读同源 cookie / 逃逸）。
//   · 本仓预览只需「看」HTML 产物（图表/报告等静态页），不需要跑脚本，故一律最严格：
//     `sandbox=""`（无脚本、无同源、无表单、无嵌套框架、无 top 导航）。
//   · 产物里即使带 <script>，沙箱下也不会执行——安全边界由浏览器强制，而非我做 HTML 正则
//     清洗（正则清洗 HTML 自古不可靠，交给 sandbox + 不 exec 是最稳的口径）。
//   · 需要交互脚本的 HTML 若日后确要支持，应单开一个明确的「交互模式」开关，届时用
//     `sandbox="allow-scripts"`（不同时给 allow-same-origin），并做来源隔离——本期不做。
//
// 【失败态】content 非字节 / 解码抛错 / 解码为空 → 落失败态（不炸整个预览壳）。
// ─────────────────────────────────────────────────────────────────────────────
import { useMemo } from 'react'
import { tokens } from '@ark-mate/ui'
import type { PreviewBodyProps } from '../routes'

/** 把 content 规整成 HTML 字符串（契约给 Uint8Array；防御性兼容 string）。 */
function toHtml(content: PreviewBodyProps['content']): string {
  if (typeof content === 'string') return content
  // 用 ArrayBuffer.isView 而非 instanceof Uint8Array：跨 realm（jsdom/worker）instanceof 会失效。
  if (ArrayBuffer.isView(content)) {
    return new TextDecoder('utf-8').decode(content as ArrayBufferView as Uint8Array)
  }
  // 契约外（如 { url }）不猜测，返回空串走失败态。
  return ''
}

/** 失败态（保持与 TextRenderer 一致的文案/配色）。 */
function Failure({ detail }: { detail: string }) {
  return (
    <div style={{ padding: 24, color: tokens.error, fontSize: 13 }}>
      预览加载失败：{detail}
    </div>
  )
}

export default function HtmlRenderer({ content }: PreviewBodyProps) {
  // 解码放 useMemo：大文件（如 14KB+ 的 HTML）每次 render 不必重复解码；content 变化才重算。
  const decoded = useMemo(() => {
    try {
      return { html: toHtml(content), error: null as string | null }
    } catch (err) {
      return { html: '', error: err instanceof Error ? err.message : String(err) }
    }
  }, [content])

  if (decoded.error) return <Failure detail={decoded.error} />
  if (!decoded.html.trim()) return <Failure detail="HTML 内容为空或不是有效字节" />

  return (
    <iframe
      // srcDoc：把解码后的完整文档作为浏览上下文内容；受控、不发起外部 document 导航。
      srcDoc={decoded.html}
      // 最严格沙箱：不允许脚本 / 同源 / 表单 / 嵌套框架 / top 导航 / 弹窗。
      // 这是「只渲染、不执行」的安全边界（浏览器强制，无需正则清洗）。
      sandbox=""
      // 阻止 referrer 外泄（即便产物含外链资源）。
      referrerPolicy="no-referrer"
      title="HTML 预览"
      style={{
        display: 'block',
        width: '100%',
        height: '100%',
        minHeight: 240,
        border: 'none',
        background: tokens.surface,
      }}
    />
  )
}
