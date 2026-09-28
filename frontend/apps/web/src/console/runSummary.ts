import type { BusEvent } from '@ark/types'
import { Ev } from '@ark/types'

/** 一条失败 turn 的摘要（供详情面板/聊天流「重试该轮」）。 */
export interface FailedTurn {
  seq: number // 终态 turn.error 事件 seq（稳定锚点）
  text: string // 该轮用户输入（无则空串；重试按钮据此禁用）
  code: string
  details: string
}

/** 会话运行统计（由历史事件推导，前端只读聚合）。 */
export interface RunSummary {
  completed: number // turn.ended
  failed: number // turn.error
  cancelled: number // run.cancelled
  tools: number // session.tool_call（工具启动即计一次）
  failedTurns: FailedTurn[]
}

const textOf = (p: Record<string, unknown> | undefined): string => (typeof p?.text === 'string' ? p.text : '')

/**
 * 从按 seq 升序的历史事件推导运行统计：轮次/失败/取消/工具调用数 + 失败轮列表
 * （每条关联其所属轮的用户输入，供「一键重试该轮」）。
 */
export function summarizeRun(events: BusEvent[]): RunSummary {
  const sum: RunSummary = { completed: 0, failed: 0, cancelled: 0, tools: 0, failedTurns: [] }
  let lastUser = ''
  for (const e of events) {
    switch (e.type) {
      case Ev.UserMessage:
        lastUser = textOf(e.payload)
        break
      case Ev.ToolCall:
        sum.tools++
        break
      case Ev.TurnEnded:
        sum.completed++
        break
      case Ev.RunCancelled:
        sum.cancelled++
        break
      case Ev.TurnError:
        sum.failed++
        sum.failedTurns.push({
          seq: e.seq,
          text: lastUser,
          code: typeof e.payload?.code === 'string' ? e.payload.code : '',
          details: typeof e.payload?.details === 'string' ? e.payload.details : '',
        })
        lastUser = '' // 下一轮 user_message 到来前清空，避免跨轮串扰
        break
      default:
        break
    }
  }
  return sum
}
