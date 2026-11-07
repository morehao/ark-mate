import type { QueuedInput } from '@ark-mate/types'

/**
 * 行内编辑的**就地替换**（v4；纯函数、无事件类型依赖，便于 Node 侧单测）。
 *
 * `input_updated` 的语义：改一条**仍在队列里**的队项的正文/附件，不出队、不改顺序。
 * 队项不在（已投递/已作废）→ 原样返回（no-op）：编辑一条已离队的项没有意义，也不该复活它。
 *
 * 附件：载荷给了（数组）就替换，缺省（undefined）保留原值——「只改文本、不动附件」与
 * 「带附件一起改」两种写法都可表达。
 */
export function applyInputUpdated(queue: QueuedInput[], inputId: string, text: string, attachments?: string[]): QueuedInput[] {
  const idx = queue.findIndex((q) => q.inputId === inputId)
  if (idx < 0) return queue
  const out = [...queue]
  const next: QueuedInput = { ...out[idx], text: text !== '' ? text : out[idx].text }
  if (attachments) next.attachments = attachments
  out[idx] = next
  return out
}

/** 从事件载荷读附件 id 列表（非数组 → undefined，不改写既有值）。 */
export function readAttachments(v: unknown): string[] | undefined {
  if (!Array.isArray(v)) return undefined
  return v.map((x) => String(x))
}
