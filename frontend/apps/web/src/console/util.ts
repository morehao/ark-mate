import type { Agent, ConversationSummary, StreamEntry } from '@ark/types'
import type { ConversationView } from '../store'

/** 会话标题：服务端已推导（sm.title，可被 PATCH 覆盖）优先；其次首条用户消息/正文；最后「新会话 · <agent>」。 */
export function titleFor(
  sm: ConversationSummary | undefined,
  view: ConversationView | undefined,
  agents: Agent[],
): string {
  if (sm && sm.title) return sm.title
  if (view) {
    const user = view.entries.find((e) => e.kind === 'user')
    if (user?.text) return user.text
    const assistant = view.entries.find((e) => e.kind === 'assistant')
    if (assistant?.text) return assistant.text
  }
  if (sm) {
    const a = agents.find((x) => x.id === sm.agent_id)
    if (a) return `新会话 · ${a.name}`
    // agent 已被删除等兜底：不直接摊出 36 位平台 uuid（对外 id 展示策略，P3 F4）。
    return `新会话 · ${shortId(sm.id)}`
  }
  return '新会话'
}

export function agentOf(agents: Agent[], id: string | undefined): Agent | undefined {
  return agents.find((a) => a.id === id)
}

export function agentNameOf(agents: Agent[], id: string | undefined): string {
  return agentOf(agents, id)?.name ?? id ?? '未知 agent'
}

/** 由名称派生的短标识（新建会话选项卡的角标）：deepseek→DS / opencode→OC，其余取词首字母。 */
export function shortFor(label: string): string {
  const lower = label.toLowerCase()
  if (lower.includes('deepseek') || lower.includes('dsh')) return 'DS'
  if (lower.includes('opencode') || lower.includes('open')) return 'OC'
  const words = label.split(/[-_\s]+/).filter(Boolean)
  const letters = words
    .slice(0, 2)
    .map((w) => w[0] ?? '')
    .join('')
  return (letters || label.slice(0, 2)).toUpperCase()
}

export function findUserText(entries: StreamEntry[] | undefined): string | undefined {
  return entries?.find((e) => e.kind === 'user')?.text
}

/**
 * 会话 id 展示策略（P3 F4）：对外 id 是平台 uuid（底座 id 形状不再暴露），36 位全量在详情里
 * 噪声大——默认只显示前 8 位（hover 看全量，可一键复制完整 id 供排障/API 调用）。
 */
export function shortId(id: string): string {
  return id.length > 8 ? `${id.slice(0, 8)}…` : id
}
