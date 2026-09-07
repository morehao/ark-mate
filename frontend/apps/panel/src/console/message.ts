/** 消息富文本段落：普通文本 / 围栏代码块（```lang … ```）。 */
export interface MsgPart {
  kind: 'text' | 'code'
  text: string
  lang?: string
}

const FENCE_RE = /```([^\n`]*)\n?([\s\S]*?)(?:```|$)/g

/**
 * 把消息文本切分为文本段与围栏代码块。未闭合的 ``` 视作代码块直到结尾；
 * 其余文本原样保留（换行由 pre-wrap 呈现）。
 */
export function parseMsg(text: string): MsgPart[] {
  const parts: MsgPart[] = []
  let last = 0
  let m: RegExpExecArray | null
  FENCE_RE.lastIndex = 0
  while ((m = FENCE_RE.exec(text))) {
    if (m.index > last) parts.push({ kind: 'text', text: text.slice(last, m.index) })
    const lang = (m[1] || '').trim()
    parts.push({ kind: 'code', text: m[2], lang: lang || undefined })
    last = m.index + m[0].length
  }
  if (last < text.length) parts.push({ kind: 'text', text: text.slice(last) })
  if (parts.length === 0) parts.push({ kind: 'text', text })
  return parts
}
