import { Ev } from '@ark/types'
import type { BusEvent, QueuedInput, QueuedInputResp } from '@ark/types'

// ——— §6 busy input 队列视图模型（纯函数，便于 Node 侧校验） ———
//
// 队列是**事件的投影**，不是本地状态机：`session.input_queued` 建项，`session.input_delivered`
// / `session.input_cancelled` 收敛。这样刷新、重连、换标签页都靠历史回放重建出同一份队列——
// 代价是需要服务端为每一种离队都发事件（进程重启作废也补发 cancelled reason=stale，否则
// 会留下永远投不出、也撤不掉的幻影队项）。

/** 可在输入区选择的投递模式（`reject` 不是「选择」而是「不排队直接报错」的历史行为，不在选项里）。 */
export type DeliveryChoice = 'after_turn' | 'interrupt_then_prompt'

/** 投递模式 → 短文案（按钮/标签用）。 */
export const DELIVERY_LABEL: Record<string, string> = {
  after_turn: '排队',
  interrupt_then_prompt: '打断并发送',
  reject: '直接报错',
}

/** 投递模式 → 一句话解释（下拉选项副标题/tooltip 用）。 */
export const DELIVERY_HINT: Record<string, string> = {
  after_turn: '当前轮跑完后，作为新的一轮自动发出',
  interrupt_then_prompt: '先取消当前轮，再立刻发出这条',
  reject: '不做队列，会话忙时直接报错（历史行为）',
}

export function deliveryLabel(delivery: string): string {
  return DELIVERY_LABEL[delivery] ?? delivery
}

export function deliveryHint(delivery: string): string {
  return DELIVERY_HINT[delivery] ?? ''
}

/** 离队原因 → 文案（服务端 ReasonUserCancel / ReasonDeliverFail / ReasonStale）。 */
export function cancelReasonLabel(reason: string): string {
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
export interface QueueTransition {
  queue: QueuedInput[]
  removed: QueuedInput | null
}

/** transitionQueue：出队事件顺带交出被移除的队项（文本只存在于投影里，事件载荷不带文本）。 */
export function transitionQueue(queue: QueuedInput[], e: BusEvent): QueueTransition {
  if (e.type !== Ev.InputQueued && e.type !== Ev.InputDelivered && e.type !== Ev.InputCancelled) {
    return { queue, removed: null }
  }
  const p = e.payload ?? {}
  const inputId = p.input_id === undefined || p.input_id === null ? '' : String(p.input_id)
  if (!inputId) return { queue, removed: null }

  if (e.type !== Ev.InputQueued) {
    const removed = queue.find((q) => q.inputId === inputId) ?? null
    if (!removed) return { queue, removed: null }
    return { queue: queue.filter((q) => q.inputId !== inputId), removed }
  }
  return { queue: applyQueuedEvent(queue, e, inputId), removed: null }
}

/** 服务端当前态（GET /inputs）→ 队列视图（冷挂载重建用）。 */
export function queuedFromResp(r: QueuedInputResp): QueuedInput {
  const q: QueuedInput = { inputId: r.input_id, text: r.text, delivery: r.delivery }
  if (r.target_turn_id) q.targetTurnId = r.target_turn_id
  if (typeof r.generation === 'number') q.generation = r.generation
  return q
}

/**
 * 队列归并（纯函数）：queued 建项、delivered/cancelled 出队，其余事件原样透传。
 *
 * 幂等：同一 input_id 重复入队（重连重放）只按最新载荷更新，不叠加成两条；
 * 出队事件对不存在的 id 是 no-op——**队项可能先于本页建立**（入队发生在别的标签页/刷新前），
 * 这不值得报错，队列本来就只是投影。
 */
export function applyQueue(queue: QueuedInput[], e: BusEvent): QueuedInput[] {
  return transitionQueue(queue, e).queue
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
  if (p.target_turn_id !== undefined && p.target_turn_id !== null && String(p.target_turn_id) !== '') {
    next.targetTurnId = String(p.target_turn_id)
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
