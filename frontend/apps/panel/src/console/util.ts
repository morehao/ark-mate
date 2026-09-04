import type { Agent, SessionSummary, StreamEntry } from '@ark/types'
import type { SessionView } from '../store'

/** 会话标题：优先首条用户消息，其次首条 assistant 正文，最后退化为「新会话 · <agent>」。 */
export function titleFor(sm: SessionSummary | undefined, view: SessionView | undefined, agents: Agent[]): string {
  if (view) {
    const user = view.entries.find((e) => e.kind === 'user')
    if (user?.text) return user.text
    const assistant = view.entries.find((e) => e.kind === 'assistant')
    if (assistant?.text) return assistant.text
  }
  if (sm) {
    const a = agents.find((x) => x.id === sm.agent_id)
    if (a) return `新会话 · ${a.name}`
  }
  return sm?.id ?? '新会话'
}

export function agentOf(agents: Agent[], id: string | undefined): Agent | undefined {
  return agents.find((a) => a.id === id)
}

export function agentNameOf(agents: Agent[], id: string | undefined): string {
  return agentOf(agents, id)?.name ?? id ?? '未知 agent'
}

/** 由 runtime 派生的短标识（新建会话选项卡的角标）：opencode→OC / deepseek-harness→DS。 */
export function shortFor(runtime: string): string {
  const lower = runtime.toLowerCase()
  if (lower.includes('deepseek') || lower.includes('dsh')) return 'DS'
  if (lower.includes('opencode') || lower.includes('open')) return 'OC'
  const words = runtime.split(/[-_\s]+/).filter(Boolean)
  const letters = words
    .slice(0, 2)
    .map((w) => w[0] ?? '')
    .join('')
  return (letters || runtime.slice(0, 2)).toUpperCase()
}

export function findUserText(entries: StreamEntry[] | undefined): string | undefined {
  return entries?.find((e) => e.kind === 'user')?.text
}
