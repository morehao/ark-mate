import type { CSSProperties, ReactNode } from 'react'
import { CheckOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'

/**
 * 轻量消息 Markdown（无第三方依赖）：块级 = 标题/无序/有序/任务列表/表格/引用/分隔线/段落，
 * 行内 = `行内码` / **粗体** / *斜体* / [链接](url)。流式容错：未闭合记号按纯文本退化；
 * ``` 围栏代码块由上层 parseMsg 先行切走（本组件只处理文本段）。
 *
 * 样式纪律对齐 tokens：hairline 边框 + 中性灰阶；链接/勾选用靛蓝 success 语义色。
 */

type MdBlock =
  | { type: 'p'; text: string }
  | { type: 'h'; level: number; text: string }
  | { type: 'hr' }
  | { type: 'quote'; lines: string[] }
  | { type: 'ul'; items: string[] }
  | { type: 'ol'; items: string[] }
  | { type: 'task'; items: { checked: boolean; text: string }[] }
  | { type: 'table'; head: string[]; rows: string[][]; aligns: string[] }

const TASK_RE = /^\s{0,3}[-*+]\s+\[([ xX])\]\s+/
const UL_RE = /^\s{0,3}[-*+]\s+/
const OL_RE = /^\s{0,3}\d{1,3}[.)]\s+/
const HEAD_RE = /^(#{1,6})\s+(.*)$/
const QUOTE_RE = /^>\s?(.*)$/
const HR_RE = /^\s{0,3}(?:-{3,}|\*{3,}|_{3,})\s*$/

function isTableSep(line: string): boolean {
  const t = line.trim()
  if (!t.includes('-')) return false
  const body = t.replace(/^\|/, '').replace(/\|$/, '').trim()
  return /^:?-+:?(\s*\|\s*:?-+:?)*$/.test(body)
}

function toBlocks(text: string): MdBlock[] {
  const lines = text.split('\n')
  const blocks: MdBlock[] = []
  let i = 0
  const pushP = (buf: string[]) => {
    if (buf.length === 0) return
    blocks.push({ type: 'p', text: buf.join('\n') })
  }
  while (i < lines.length) {
    const line = lines[i]
    const trimmed = line.trim()
    if (trimmed === '') {
      i++
      continue
    }
    const hm = line.match(HEAD_RE)
    if (hm) {
      blocks.push({ type: 'h', level: hm[1].length, text: hm[2].trim() })
      i++
      continue
    }
    if (HR_RE.test(trimmed) && !line.includes('|')) {
      blocks.push({ type: 'hr' })
      i++
      continue
    }
    // 引用
    if (QUOTE_RE.test(line)) {
      const q: string[] = []
      while (i < lines.length && QUOTE_RE.test(lines[i])) {
        q.push(lines[i].replace(QUOTE_RE, '$1'))
        i++
      }
      blocks.push({ type: 'quote', lines: q })
      continue
    }
    // 任务列表
    if (TASK_RE.test(line)) {
      const items: { checked: boolean; text: string }[] = []
      while (i < lines.length && TASK_RE.test(lines[i])) {
        const m = lines[i].match(TASK_RE)!
        items.push({ checked: m[1].toLowerCase() === 'x', text: lines[i].slice(m[0].length) })
        i++
      }
      blocks.push({ type: 'task', items })
      continue
    }
    // 无序/有序列表
    const isUL = UL_RE.test(line)
    const isOL = OL_RE.test(line)
    if (isUL || isOL) {
      const items: string[] = []
      const re = isUL ? UL_RE : OL_RE
      while (i < lines.length) {
        const l = lines[i]
        if (re.test(l)) {
          items.push(l.slice(l.match(re)![0].length))
          i++
        } else if (l.trim() === '' && i + 1 < lines.length && (UL_RE.test(lines[i + 1]) || OL_RE.test(lines[i + 1]) || TASK_RE.test(lines[i + 1]))) {
          i++ // 列表内空行（容错）
        } else {
          break
        }
      }
      blocks.push({ type: isUL ? 'ul' : 'ol', items })
      continue
    }
    // 表格：表头行 + 紧随分隔行
    if (i + 1 < lines.length && line.includes('|') && isTableSep(lines[i + 1])) {
      const raw: string[] = [line]
      i++
      const sepLine = lines[i]
      const aligns = parseAligns(sepLine)
      i++
      while (i < lines.length && lines[i].includes('|') && !HR_RE.test(lines[i].trim())) {
        if (lines[i].trim() === '') break
        raw.push(lines[i])
        i++
      }
      const parsed = parseTable(raw, aligns)
      if (parsed) {
        blocks.push({ type: 'table', ...parsed })
        continue
      }
    }
    // 段落：直到空行或下一个块起始
    const buf: string[] = []
    while (i < lines.length) {
      const l = lines[i]
      if (l.trim() === '') break
      if (HEAD_RE.test(l) || HR_RE.test(l.trim()) || QUOTE_RE.test(l) || TASK_RE.test(l) || UL_RE.test(l) || OL_RE.test(l)) break
      buf.push(l)
      i++
    }
    pushP(buf)
  }
  return blocks
}

function parseAligns(sep: string): string[] {
  const cells = sep.split('|').filter((s) => s.trim() !== '')
  return cells.map((c) => {
    const t = c.trim()
    if (t.startsWith(':') && t.endsWith(':')) return 'center'
    if (t.endsWith(':')) return 'right'
    return 'left'
  })
}

function parseTable(raw: string[], aligns: string[]): { head: string[]; rows: string[][]; aligns: string[] } | null {
  const cells = (row: string) =>
    row
      .replace(/^\s*\|/, '')
      .replace(/\|\s*$/, '')
      .split('|')
      .map((s) => s.trim())
  const head = cells(raw[0])
  const width = head.length
  const rows = raw.slice(1).map(cells).filter((r) => r.some(Boolean))
  if (width === 0) return null
  return { head, rows, aligns }
}

// ——— 行内解析 ———
const INLINE_RE = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*\s][^*]*\*)|(\[[^\]\n]+\]\([^)\s]+\))/g

function splitInlines(text: string): ReactNode[] {
  const out: ReactNode[] = []
  let last = 0
  let m: RegExpExecArray | null
  INLINE_RE.lastIndex = 0
  let key = 0
  while ((m = INLINE_RE.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index))
    const [full] = m
    if (m[1]) out.push(<code key={key++} className="md-code" style={inlineCodeStyle}>{m[1].slice(1, -1)}</code>)
    else if (m[2]) out.push(<strong key={key++} className="md-strong">{m[2].slice(2, -2)}</strong>)
    else if (m[3]) out.push(<em key={key++} className="md-em">{m[3].slice(1, -1)}</em>)
    else if (m[4]) {
      const idx = full.indexOf('](')
      const label = full.slice(1, idx)
      const href = full.slice(idx + 2, -1)
      out.push(
        <a key={key++} className="md-link" href={href} target="_blank" rel="noreferrer" onClick={(e) => e.stopPropagation()}>
          {label}
        </a>,
      )
    }
    last = m.index + full.length
  }
  if (last < text.length) out.push(text.slice(last))
  return out
}

function Inline({ text }: { text: string }) {
  return <>{splitInlines(text)}</>
}

const tableStyle: CSSProperties = {
  borderCollapse: 'collapse',
  width: '100%',
  margin: '2px 0',
  fontSize: 13.5,
  lineHeight: 1.6,
}

const thStyle: CSSProperties = {
  textAlign: 'left',
  border: `1px solid ${tokens.border}`,
  background: tokens.softFill,
  fontWeight: 640,
  padding: '5px 10px',
}

const tdStyle: CSSProperties = {
  border: `1px solid ${tokens.border}`,
  padding: '5px 10px',
}

function alignOf(a: string): CSSProperties {
  if (a === 'center') return { textAlign: 'center' }
  if (a === 'right') return { textAlign: 'right' }
  return {}
}

export function Md({ text }: { text: string }) {
  const blocks = toBlocks(text)
  return (
    <>
      {blocks.map((b, i) => {
        const key = `${b.type}-${i}`
        switch (b.type) {
          case 'p':
            return (
              <p key={key} className="md-p" style={{ margin: '1px 0 7px', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
                <Inline text={b.text} />
              </p>
            )
          case 'h': {
            const size = b.level === 1 ? 16.5 : b.level === 2 ? 15.5 : 15
            return (
              <div key={key} className={`md-h md-h${b.level}`} style={{ fontSize: size, fontWeight: 720, letterSpacing: '-0.01em', color: tokens.text, margin: '10px 0 6px', lineHeight: 1.5 }}>
                <Inline text={b.text} />
              </div>
            )
          }
          case 'hr':
            return <div key={key} className="md-hr" style={{ height: 1, background: tokens.border, margin: '10px 0' }} />
          case 'quote':
            return (
              <blockquote
                key={key}
                className="md-quote"
                style={{
                  margin: '4px 0',
                  padding: '2px 0 2px 12px',
                  borderLeft: `3px solid ${tokens.borderStrong}`,
                  color: tokens.textSecondary,
                  whiteSpace: 'pre-wrap',
                }}
              >
                {b.lines.map((l, j) => (
                  <span key={j} style={{ display: 'block' }}>
                    <Inline text={l} />
                  </span>
                ))}
              </blockquote>
            )
          case 'ul':
          case 'ol':
            return (
              <div key={key} className={b.type === 'ul' ? 'md-ul' : 'md-ol'} style={{ margin: '2px 0 7px', display: 'flex', flexDirection: 'column', gap: 2 }}>
                {b.items.map((it, j) => (
                  <div key={j} className="md-li" style={{ display: 'flex', gap: 8, lineHeight: 1.6 }}>
                    <span style={{ flex: 'none', color: tokens.textPlaceholder, fontFamily: tokens.fontMono, fontSize: 12.5 }}>
                      {b.type === 'ol' ? `${j + 1}.` : '·'}
                    </span>
                    <span style={{ minWidth: 0, flex: 1, overflowWrap: 'anywhere' }}>
                      <Inline text={it} />
                    </span>
                  </div>
                ))}
              </div>
            )
          case 'task':
            return (
              <div key={key} className="md-task" style={{ margin: '2px 0 7px', display: 'flex', flexDirection: 'column', gap: 2 }}>
                {b.items.map((it, j) => (
                  <div key={j} className="md-task-item" style={{ display: 'flex', gap: 8, lineHeight: 1.6 }}>
                    <span
                      className={it.checked ? 'md-check md-checked' : 'md-check'}
                      style={{
                        flex: 'none',
                        display: 'inline-flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                        width: 15,
                        height: 15,
                        marginTop: 4,
                        borderRadius: 4,
                        border: `1.5px solid ${it.checked ? tokens.success : tokens.borderStrong}`,
                        background: it.checked ? tokens.success : 'transparent',
                        color: '#fff',
                        fontSize: 9,
                      }}
                    >
                      {it.checked && <CheckOutlined />}
                    </span>
                    <span style={{ minWidth: 0, flex: 1, overflowWrap: 'anywhere', color: it.checked ? tokens.textSecondary : tokens.text }}>
                      <Inline text={it.text} />
                    </span>
                  </div>
                ))}
              </div>
            )
          case 'table': {
            const colAlign = (c: number) => alignOf(b.aligns[c] ?? 'left')
            return (
              <div key={key} className="md-table-wrap" style={{ overflowX: 'auto', maxWidth: '100%', margin: '4px 0 8px' }}>
                <table className="md-table" style={tableStyle}>
                  <thead>
                    <tr>
                      {b.head.map((c, j) => (
                        <th key={j} style={{ ...thStyle, ...colAlign(j) }}>
                          <Inline text={c} />
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {b.rows.map((row, ri) => (
                      <tr key={ri}>
                        {b.head.map((_, ci) => (
                          <td key={ci} style={{ ...tdStyle, ...colAlign(ci) }}>
                            <Inline text={row[ci] ?? ''} />
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }
        }
      })}
    </>
  )
}

/** 渲染样式：行内码。 */
export const inlineCodeStyle: CSSProperties = {
  fontFamily: tokens.fontMono,
  fontSize: '0.86em',
  background: tokens.codeBg,
  border: `1px solid ${tokens.border}`,
  borderRadius: 5,
  padding: '0.5px 5px',
  color: tokens.text,
}
