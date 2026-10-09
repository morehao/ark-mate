/**
 * 搜索结果视图逻辑（M3 ②：消息级搜索 + 锚点跳转）。
 *
 * 纯函数：便于用真实模块构建后做断言（与 console/railView、console/transcriptView 同约定）。
 */
import type { MessageSearchHit, StreamEntry } from '@ark-mate/types'

/** 命中角色标签（UI 徽标文案）。 */
export function roleLabel(role: string): string {
  return role === 'user' ? '我' : role === 'assistant' ? '助手' : role
}

/** 命中来源标签（数据面降级提示）。 */
export function sourceLabel(source: string): string {
  return source === 'transcript' ? 'transcript 回扫' : 'messages 读模型'
}

/**
 * anchorIndex 给定锚点 seq 与当前条目序列，返回应滚动到的条目下标。
 *
 * 语义：
 *   - 有 seq 完全相等的条目 → 该条目（消息级命中的 seq 即该条目的首帧 seq）；
 *   - 否则取「最后一个 seq ≤ 锚点」的条目（锚点落在被合并/截断的区间内时仍能就近定位）；
 *   - 都无（早于窗口/无 seq 条目）→ 0；空列表 → -1。
 */
export function anchorIndex(entries: StreamEntry[], seq: number): number {
  if (entries.length === 0) return -1
  let best = -1
  for (let i = 0; i < entries.length; i++) {
    const s = entries[i].seq
    if (typeof s !== 'number') continue
    if (s === seq) return i
    if (s <= seq) best = i
    else break // seq 单调递增
  }
  return best >= 0 ? best : 0
}

/** 命中列表去重（同一会话同一 seq 只保留一条；后端已去重，这里防御重复渲染）。 */
export function dedupeHits(hits: MessageSearchHit[]): MessageSearchHit[] {
  const seen = new Set<string>()
  const out: MessageSearchHit[] = []
  for (const h of hits) {
    const key = `${h.conversation_id}#${h.seq}`
    if (seen.has(key)) continue
    seen.add(key)
    out.push(h)
  }
  return out
}

/** 导出文件下载名（服务端已给安全名；空则回退会话 id）。 */
export function exportDownloadName(filename: string, conversationId: string, format: string): string {
  const name = (filename ?? '').trim()
  if (name) return name
  const ext = format === 'json' ? 'json' : format === 'text' ? 'txt' : 'md'
  return `${conversationId.slice(0, 8)}.${ext}`
}

/** 导出结果摘要文案（含 skipped 与截断提示，避免静默）。 */
export function exportSummary(exported: number, totalBytes: number, skipped: string[], truncated: number): string {
  const parts = [`已导出 ${exported} 个会话`, `${totalBytes} 字节`]
  if (truncated > 0) parts.push(`${truncated} 个截断`)
  if (skipped.length > 0) parts.push(`跳过 ${skipped.length} 个（无数据）`)
  return parts.join(' · ')
}
