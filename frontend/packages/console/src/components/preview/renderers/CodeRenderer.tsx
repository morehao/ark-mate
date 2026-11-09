// ─────────────────────────────────────────────────────────────────────────────
// renderers/CodeRenderer.tsx —— 代码正文（shiki 高亮 + 行号）
//
// 关键约束（见 task-7）：
//   1. shiki 是重库：**动态 import** 才把它拉进来，保证主 chunk 不含 shiki，
//      高亮器随本文件所在的路由懒 chunk 走。
//   2. 高亮器按需构建：只用 `shiki` 主入口的 `createHighlighter`，传当前文件命中的
//      单一 lang + 单一 theme；shiki 内部对 lang/theme 走懒加载（runtime 才拉对应语法）。
//   3. 语言由 fileName 后缀推断；推断不出（或不在白名单）→ 回落纯文本，**不报错**。
//   4. 带行号：把 shiki 的 tokens 渲染成「行号列 + 代码列」的两栏结构。
//   5. 善用 async 初始化：加载中显示占位；初始化抛错时落失败态（仍可读纯文本兜底）。
// ─────────────────────────────────────────────────────────────────────────────
import { useEffect, useMemo, useRef, useState } from 'react'
import { tokens as themeTokens } from '@ark-mate/ui'
import type { BundledLanguage, BundledTheme } from 'shiki'
import type { PreviewBodyProps } from '../routes'

/** 后缀 → shiki 语言 id（白名单：只映射这些，其余回落纯文本）。 */
const EXT_TO_LANG: Record<string, string> = {
  ts: 'typescript',
  mts: 'typescript',
  cts: 'typescript',
  tsx: 'tsx',
  js: 'javascript',
  mjs: 'javascript',
  cjs: 'javascript',
  jsx: 'jsx',
  go: 'go',
  py: 'python',
  rb: 'ruby',
  rs: 'rust',
  java: 'java',
  kt: 'kotlin',
  c: 'c',
  h: 'c',
  cc: 'cpp',
  cpp: 'cpp',
  cxx: 'cpp',
  hpp: 'cpp',
  hh: 'cpp',
  cs: 'csharp',
  php: 'php',
  swift: 'swift',
  dart: 'dart',
  lua: 'lua',
  sh: 'shellscript',
  bash: 'shellscript',
  zsh: 'shellscript',
  sql: 'sql',
  json: 'json',
  jsonl: 'json',
  yaml: 'yaml',
  yml: 'yaml',
  toml: 'toml',
  ini: 'ini',
  xml: 'xml',
  css: 'css',
  scss: 'scss',
  less: 'less',
  html: 'html',
  htm: 'html',
  vue: 'vue',
}

/** 白色主题：与「精致冷白」令牌风格一致。 */
const SHIKI_THEME: BundledTheme = 'github-light'

/** 取小写后缀（无点）。 */
function extOf(fileName: string): string {
  const normalized = fileName.split('\\').join('/').toLowerCase()
  const base = normalized.slice(normalized.lastIndexOf('/') + 1)
  const dot = base.lastIndexOf('.')
  return dot > 0 ? base.slice(dot + 1) : ''
}

/** 由文件名推断 shiki 语言 id；未知返回 undefined（→ 纯文本）。 */
function langOf(fileName: string): string | undefined {
  return EXT_TO_LANG[extOf(fileName)]
}

/** 把 content 规整为字符串（text 类给定 string，仍兼容 bytes 兜底）。 */
function asText(content: PreviewBodyProps['content']): string {
  if (typeof content === 'string') return content
  if (content instanceof Uint8Array) return new TextDecoder().decode(content)
  return ''
}

type Status = 'loading' | 'ready' | 'error'

/** 高亮后的一行：纯文本（行号用）+ 已转义的 token HTML。 */
type Line = { text: string; html: string }

const monoStyle = {
  fontFamily: themeTokens.fontMono,
  fontSize: 12.5,
  lineHeight: 1.6,
} as const

/** 行号 + 代码两栏的表格化渲染（行号不参与选中复制，用 user-select:none）。 */
function CodeTable({ lines }: { lines: Line[] }) {
  const gutterWidth = Math.max(1, String(lines.length).length)
  return (
    <div style={{ ...monoStyle, color: themeTokens.text, paddingBlock: 8 }}>
      <table style={{ borderCollapse: 'collapse', width: '100%', tabSize: 2 }}>
        <tbody>
          {lines.map((line, i) => (
            <tr key={i}>
              <td
                aria-hidden="true"
                style={{
                  width: 1,
                  paddingInline: '12px 14px',
                  textAlign: 'right',
                  verticalAlign: 'top',
                  color: themeTokens.textPlaceholder,
                  userSelect: 'none',
                  whiteSpace: 'nowrap',
                  borderRight: `1px solid ${themeTokens.border}`,
                  fontVariantNumeric: 'tabular-nums',
                  minWidth: `${gutterWidth}ch`,
                }}
              >
                {i + 1}
              </td>
              <td
                style={{ paddingInline: '14px 16px', verticalAlign: 'top', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}
                // 有高亮 HTML 时直出 shiki 受控 token HTML（已 <、>、& 转义）；无高亮（纯文本兜底）
                // 时直接渲染文本——之前写成 `line.html || '&nbsp;'` 会让纯文本行全部变空白（2026-11-09 实机
                // 发现 .py/.json 预览一片空白），这里改为兜底渲染 line.text（同样转义）。
                {...(line.html
                  ? { dangerouslySetInnerHTML: { __html: line.html } }
                  : { children: line.text })}
              />
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** 纯文本兜底（无高亮，保留行号）—— 未知语言 / 高亮失败 / 高亮完成前的统一形态。 */
function PlainCode({ text }: { text: string }) {
  const lines = useMemo<Line[]>(() => {
    const arr = text.split('\n')
    // 末尾换行产生的空串不再单列一行（视觉上就是文件结尾）。
    if (arr.length > 1 && arr[arr.length - 1] === '') arr.pop()
    return arr.map((t) => ({ text: t, html: '' }))
  }, [text])
  return <CodeTable lines={lines} />
}

/** 状态提示条（加载中 / 失败）。 */
function StatusBar({ status }: { status: Status }) {
  if (status === 'ready') return null
  const message = status === 'loading' ? '正在高亮…' : '该语言高亮不可用，已按纯文本展示'
  return (
    <div
      style={{
        ...monoStyle,
        paddingInline: 16,
        paddingBlock: 6,
        fontSize: 11,
        color: status === 'error' ? themeTokens.warning : themeTokens.textPlaceholder,
        background: status === 'error' ? themeTokens.warningBg : themeTokens.codeBg,
        borderBottom: `1px solid ${themeTokens.border}`,
      }}
    >
      {message}
    </div>
  )
}

export default function CodeRenderer({ content, fileName }: PreviewBodyProps) {
  const text = useMemo(() => asText(content), [content])
  const lang = useMemo(() => langOf(fileName), [fileName])

  const [lines, setLines] = useState<Line[] | null>(null)
  const [status, setStatus] = useState<Status>(() => (lang ? 'loading' : 'ready'))
  // 防竞态：内容/语言变化后，过期的异步结果直接丢弃。
  const runId = useRef(0)

  useEffect(() => {
    if (!lang) {
      setLines(null)
      setStatus('ready')
      return
    }
    const current = ++runId.current
    setStatus('loading')
    setLines(null)

    let cancelled = false

    void (async () => {
      try {
        // 仅当真正渲染代码时才把 shiki 拉进来（重库不进主 chunk）。
        // 用主入口 createHighlighter：lang/theme 传字符串，shiki 内部懒加载对应语法/主题。
        const { createHighlighter } = await import('shiki')

        if (cancelled || current !== runId.current) return

        const highlighter = await createHighlighter({
          themes: [SHIKI_THEME],
          langs: [lang as BundledLanguage],
        })

        if (cancelled || current !== runId.current) return

        const { tokens } = await highlighter.codeToTokens(text, { lang: lang as BundledLanguage, theme: SHIKI_THEME })
        const built: Line[] = tokens.map((row) => {
          const html = row
            .map((token) =>
              token.color
                ? `<span style="color:${token.color}">${escapeHtml(token.content)}</span>`
                : escapeHtml(token.content),
            )
            .join('')
          const plain = row.map((token) => token.content).join('')
          return { text: plain, html }
        })
        // codeToTokens 已按 \n 切行；行号即数组下标。
        setLines(built)
        setStatus('ready')
      } catch {
        if (cancelled || current !== runId.current) return
        // 高亮失败不阻断阅读：回落纯文本（仍带行号）。
        setLines(null)
        setStatus('error')
      }
    })()

    return () => {
      cancelled = true
    }
  }, [lang, text])

  if (status === 'ready' && lines) {
    return <CodeTable lines={lines} />
  }
  return (
    <div>
      <StatusBar status={status} />
      <PlainCode text={text} />
    </div>
  )
}

/** 最小 HTML 转义（shiki token 只含文本，但仍防 <、>、&）。 */
function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}
