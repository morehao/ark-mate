// ─────────────────────────────────────────────────────────────────────────────
// renderers/MarkdownRenderer.tsx —— Markdown 正文（react-markdown 系 + sanitize）
//
// 由 BODY-MD（task-6）定稿，覆盖 FE-SKELETON 阶段的纯占位。契约见 ../routes.ts：
// loading='text-pages' → content 是 string；本组件仍防御性兼容 Uint8Array（TextDecoder 兜底）。
//
// 管线：remark-gfm（表格/任务列表/删除线/自动链接）→ remark-math（$...$ 公式）
//       → rehype-katex（公式转 HTML）→ rehype-sanitize（**强制**，防 XSS）。
//
// ⚠ 安全红线：
//   - **必须**过 rehype-sanitize；绝不使用 dangerouslySetInnerHTML 直出不可信 md。
//   - sanitize 在 rehype-katex **之后**执行：katex 先把公式转成 HTML，再交给 sanitize 清洗，
//     保证最终进 DOM 的是清洗后的节点。
//   - react-markdown 默认的 defaultUrlTransform 也会拦 javascript: 等协议（纵深防御）。
//   - CSS 方面：katex 需要自己的样式表；本仓不使用裸 `import '*.css'`（见下方 CSS 说明）。
// ─────────────────────────────────────────────────────────────────────────────
import { Component, type ReactNode } from 'react'
import ReactMarkdown, { type Components, type Options } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize'
import { tokens } from '@ark-mate/ui'
import type { PreviewBodyProps } from '../routes'
// katex 样式表：本仓 tsconfig 未引 vite/client（`noUncheckedSideEffectImports` + 无 `*.css` 环境声明），
// 裸 `import 'katex/dist/katex.min.css'` 会报 TS2882。这里用 @ts-expect-error 让类型检查通过；
// Vite 构建时会把该 css 正常打包进产物（dev 注入 <style>、build 抽成 css 文件），运行时样式生效。
// @ts-expect-error katex 的 css 无类型声明（见上）
import 'katex/dist/katex.min.css'

// ── sanitize 白名单扩展 ────────────────────────────────────────────────────────
// defaultSchema 默认会剥掉 className 与 style，导致 katex（class + span 上的内联 style 布局）
// 与 GFM（任务列表/表格的 class）失效。这里最小化放开：
//   - className：对所有元素放开（class 本身不执行任何代码，安全）；
//   - style：**仅**对 <span> 放开（katex 只在 span 上写 height/margin 等布局内联样式）。
// 其它一律沿用 github 默认白名单（脚本类标签/事件属性/危险协议仍被剥离）。
const sanitizeSchema = {
  ...defaultSchema,
  attributes: {
    ...defaultSchema.attributes,
    '*': [...(defaultSchema.attributes?.['*'] ?? []), 'className'],
    span: [...(defaultSchema.attributes?.span ?? []), 'style'],
  },
}

// 用 Pluggable 显式标注，避免 `as const` 的 readonly tuple 与 Pluggable[] 不兼容。
type PluginList = NonNullable<Options['remarkPlugins']>
const REMARK_PLUGINS: PluginList = [remarkGfm, remarkMath]
const REHYPE_PLUGINS: NonNullable<Options['rehypePlugins']> = [rehypeKatex, [rehypeSanitize, sanitizeSchema]]

// ── 元素渲染映射：全部走 token，保持与全站「精致冷白」一致 ──────────────────────
const components: Components = {
  h1: ({ children }) => <h1 style={{ fontSize: 22, fontWeight: 700, margin: '20px 0 10px' }}>{children}</h1>,
  h2: ({ children }) => <h2 style={{ fontSize: 18, fontWeight: 700, margin: '18px 0 8px' }}>{children}</h2>,
  h3: ({ children }) => <h3 style={{ fontSize: 15.5, fontWeight: 700, margin: '16px 0 6px' }}>{children}</h3>,
  h4: ({ children }) => <h4 style={{ fontSize: 14, fontWeight: 600, margin: '14px 0 6px' }}>{children}</h4>,
  p: ({ children }) => <p style={{ margin: '0 0 10px', lineHeight: 1.72 }}>{children}</p>,
  ul: ({ children }) => <ul style={{ margin: '0 0 10px', paddingLeft: 22, lineHeight: 1.72 }}>{children}</ul>,
  ol: ({ children }) => <ol style={{ margin: '0 0 10px', paddingLeft: 22, lineHeight: 1.72 }}>{children}</ol>,
  li: ({ children }) => <li style={{ margin: '2px 0' }}>{children}</li>,
  blockquote: ({ children }) => (
    <blockquote
      style={{
        margin: '0 0 10px',
        padding: '2px 12px',
        borderLeft: `3px solid ${tokens.borderStrong}`,
        color: tokens.textSecondary,
      }}
    >
      {children}
    </blockquote>
  ),
  a: ({ children, href }) => (
    <a href={href} target="_blank" rel="noopener noreferrer" style={{ color: tokens.accent, textDecoration: 'none' }}>
      {children}
    </a>
  ),
  img: ({ src, alt }) => (
    <img
      src={typeof src === 'string' ? src : undefined}
      alt={alt ?? ''}
      style={{ maxWidth: '100%', height: 'auto', borderRadius: tokens.radiusSm, margin: '6px 0' }}
    />
  ),
  // 行内 code 与代码块（pre > code）分开处理：行内浅底、代码块等宽滚动。
  code: ({ className, children }) => {
    const isBlock = typeof className === 'string' && className.includes('language-')
    if (isBlock) {
      // 代码块的包裹与背景由下方 pre 负责，这里只管内层 code（保留 className 供未来高亮用）。
      return (
        <code className={className} style={{ fontFamily: tokens.fontMono, fontSize: 12.5 }}>
          {children}
        </code>
      )
    }
    return (
      <code
        style={{
          fontFamily: tokens.fontMono,
          fontSize: '0.9em',
          background: tokens.softFill,
          padding: '1px 5px',
          borderRadius: 4,
        }}
      >
        {children}
      </code>
    )
  },
  pre: ({ children }) => (
    <pre
      style={{
        margin: '0 0 12px',
        padding: '12px 14px',
        background: tokens.codeBg,
        border: `1px solid ${tokens.border}`,
        borderRadius: tokens.radiusSm,
        overflowX: 'auto',
        fontSize: 12.5,
        lineHeight: 1.6,
      }}
    >
      {children}
    </pre>
  ),
  table: ({ children }) => (
    <div style={{ overflowX: 'auto', margin: '0 0 12px' }}>
      <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: 13 }}>{children}</table>
    </div>
  ),
  th: ({ children }) => (
    <th
      style={{
        border: `1px solid ${tokens.border}`,
        padding: '6px 10px',
        background: tokens.softFill,
        textAlign: 'left',
        fontWeight: 600,
      }}
    >
      {children}
    </th>
  ),
  td: ({ children }) => (
    <td style={{ border: `1px solid ${tokens.border}`, padding: '6px 10px' }}>{children}</td>
  ),
  hr: () => <hr style={{ border: 'none', borderTop: `1px solid ${tokens.border}`, margin: '16px 0' }} />,
}

// ── 渲染期错误边界：react-markdown 解析/渲染抛错时落失败态，不炸整个预览壳 ──────────
interface BoundaryState {
  error: string | null
}
class RenderBoundary extends Component<{ children: ReactNode }, BoundaryState> {
  state: BoundaryState = { error: null }

  static getDerivedStateFromError(err: unknown): BoundaryState {
    return { error: err instanceof Error ? err.message : String(err) }
  }

  render() {
    if (this.state.error) {
      return <div style={{ padding: 24, color: tokens.error, fontSize: 13 }}>Markdown 渲染失败：{this.state.error}</div>
    }
    return this.props.children
  }
}

/** 把 content 规整成 string（契约给 string；防御性兼容 Uint8Array）。 */
function toText(content: PreviewBodyProps['content']): string {
  if (typeof content === 'string') return content
  // 用 ArrayBuffer.isView 而非 instanceof Uint8Array：跨 realm（jsdom/worker）instanceof 会失效。
  if (ArrayBuffer.isView(content)) return new TextDecoder().decode(content as ArrayBufferView as Uint8Array)
  return ''
}

export default function MarkdownRenderer({ content }: PreviewBodyProps) {
  let text = ''
  try {
    text = toText(content)
  } catch (err) {
    return (
      <div style={{ padding: 24, color: tokens.error, fontSize: 13 }}>
        Markdown 渲染失败：{err instanceof Error ? err.message : String(err)}
      </div>
    )
  }

  return (
    <RenderBoundary>
      <div
        style={{
          padding: '16px 20px',
          color: tokens.text,
          fontFamily: tokens.fontFamily,
          fontSize: 13.5,
          wordBreak: 'break-word',
        }}
      >
        <ReactMarkdown
          remarkPlugins={[...REMARK_PLUGINS]}
          rehypePlugins={[...REHYPE_PLUGINS]}
          components={components}
        >
          {text}
        </ReactMarkdown>
      </div>
    </RenderBoundary>
  )
}
