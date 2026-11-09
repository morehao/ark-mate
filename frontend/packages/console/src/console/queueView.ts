import { Ev } from '@ark-mate/types'
import type { BusEvent, QueuedInput, QueuedInputResp } from '@ark-mate/types'
import { applyInputUpdated, readAttachments } from './queueUpdate'

// ——— §6 busy input 队列视图模型（纯函数，便于 Node 侧校验） ———
//
// 队列是**事件的投影**，不是本地状态机：`session.input_queued` 建项，`session.input_delivered`
// / `session.input_cancelled` 收敛。这样刷新、重连、换标签页都靠历史回放重建出同一份队列——
// 代价是需要服务端为每一种离队都发事件（进程重启作废也补发 cancelled reason=stale，否则
// 会留下永远投不出、也撤不掉的幻影队项）。

/** 可在输入区选择的投递模式（`reject` 不是「选择」而是「不排队直接报错」的历史行为，不在选项里）。 */
export type DeliveryChoice = 'after_run' | 'interrupt_then_prompt'

/** 投递模式 → 短文案（按钮/标签用）。 */
export const DELIVERY_LABEL: Record<string, string> = {
  after_run: '排队',
  interrupt_then_prompt: '打断并发送',
  reject: '直接报错',
}

/** 投递模式 → 一句话解释（下拉选项副标题/tooltip 用）。 */
export const DELIVERY_HINT: Record<string, string> = {
  after_run: '当前轮跑完后，作为新的一轮自动发出',
  interrupt_then_prompt: '先取消当前轮，再立刻发出这条',
  reject: '不做队列，会话忙时直接报错（历史行为）',
}

export function deliveryLabel(delivery: string): string {
  return DELIVERY_LABEL[delivery] ?? delivery
}

/** 离队原因 → 文案（服务端 ReasonUserCancel / ReasonDeliverFail / ReasonStale）。 */
function cancelReasonLabel(reason: string): string {
  switch (reason) {
    case 'user_cancel':
      return '已撤下'
    case 'deliver_failed':
      return '投递失败'
    case 'stale':
      return '已作废'
    default:
      return reason || '已离队'
  }
}

/** 队项离队时的可读说明（撤下/失败/作废各自给出路，不一句「已取消」了事）。 */
export function cancelNotice(reason: string, text?: string): string {
  const excerpt = queueExcerpt(text)
  const head = excerpt ? `「${excerpt}」` : '排队输入'
  switch (reason) {
    case 'user_cancel':
      return `${head} 已从队列撤下`
    case 'deliver_failed':
      return `${head} 投递失败（文本仍留在台账里）——请重发`
    case 'stale':
      return `${head} 已作废（进程重启或会话代际已变）——请重发`
    default:
      return `${head} 已离队（${cancelReasonLabel(reason)}）`
  }
}

/** 队项文本摘要（系统提示里只用一小段，全文在队列行/台账里）。 */
export function queueExcerpt(text?: string, max = 40): string {
  const t = (text ?? '').replace(/\s+/g, ' ').trim()
  if (t.length <= max) return t
  return `${t.slice(0, max)}…`
}

/** 离队原因 → 系统提示的语气（失败要显眼，撤下/作废是中性事实）。 */
export function cancelTone(reason: string): 'err' | 'cancel' | 'info' {
  if (reason === 'deliver_failed') return 'err'
  if (reason === 'user_cancel') return 'cancel'
  return 'info'
}

/** 一次队列事件的结果：新队列 + 被移除的队项（未移除 → null；用于写系统提示）。 */
interface QueueTransition {
  queue: QueuedInput[]
  removed: QueuedInput | null
}

/** transitionQueue：出队事件顺带交出被移除的队项（文本只存在于投影里，事件载荷不带文本）。 */
export function transitionQueue(queue: QueuedInput[], e: BusEvent): QueueTransition {
  if (
    e.type !== Ev.InputQueued &&
    e.type !== Ev.InputDelivered &&
    e.type !== Ev.InputCancelled &&
    e.type !== Ev.InputUpdated
  ) {
    return { queue, removed: null }
  }
  const p = e.payload ?? {}
  const inputId = p.input_id === undefined || p.input_id === null ? '' : String(p.input_id)
  if (!inputId) return { queue, removed: null }

  if (e.type === Ev.InputQueued) {
    return { queue: applyQueuedEvent(queue, e, inputId), removed: null }
  }
  if (e.type === Ev.InputUpdated) {
    // 行内编辑：就地替换该队项的正文/附件，**不出队、不改顺序**（逻辑在 queueUpdate.applyInputUpdated）。
    return { queue: applyInputUpdated(queue, inputId, String(p.text ?? ''), readAttachments(p.attachments)), removed: null }
  }
  // delivered / cancelled → 出队并交出被移除项（文本只存在于投影里，事件载荷不带文本）。
  const removed = queue.find((q) => q.inputId === inputId) ?? null
  if (!removed) return { queue, removed: null }
  return { queue: queue.filter((q) => q.inputId !== inputId), removed }
}

/** 服务端当前态（GET /inputs）→ 队列视图（冷挂载重建用）。 */
export function queuedFromResp(r: QueuedInputResp): QueuedInput {
  const q: QueuedInput = { inputId: r.input_id, text: r.text, delivery: r.delivery }
  if (r.attachments && r.attachments.length > 0) q.attachments = r.attachments
  if (r.target_run_id) q.targetRunId = r.target_run_id
  if (typeof r.generation === 'number') q.generation = r.generation
  return q
}

/** applyQueuedEvent：入队/重放（同 id 覆盖而不叠加）。 */
function applyQueuedEvent(queue: QueuedInput[], e: BusEvent, inputId: string): QueuedInput[] {
  const p = e.payload ?? {}
  const next: QueuedInput = {
    inputId,
    text: String(p.text ?? ''),
    delivery: String(p.delivery ?? ''),
    seq: e.seq,
  }
  const atts = readAttachments(p.attachments)
  if (atts && atts.length > 0) next.attachments = atts
  if (p.target_run_id !== undefined && p.target_run_id !== null && String(p.target_run_id) !== '') {
    next.targetRunId = String(p.target_run_id)
  }
  if (typeof p.generation === 'number') next.generation = p.generation

  const idx = queue.findIndex((q) => q.inputId === inputId)
  if (idx < 0) return [...queue, next]
  const out = [...queue]
  out[idx] = next
  return out
}

/** 队列一行摘要（标题栏）：空队列 → 空串（调用方据此不渲染）。 */
export function queueSummary(queue: QueuedInput[]): string {
  if (queue.length === 0) return ''
  const interrupt = queue.filter((q) => q.delivery === 'interrupt_then_prompt').length
  const parts = [`${queue.length} 条待投递`]
  if (interrupt > 0) parts.push(`其中 ${interrupt} 条要打断当前轮`)
  return parts.join(' · ')
}
