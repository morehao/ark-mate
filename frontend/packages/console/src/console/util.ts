import type { SessionSummary, StreamEntry } from '@ark-mate/types'
import type { SessionView } from '../store'

/**
 * 会话标题：服务端已推导（sm.title，可被 PATCH 覆盖）优先；其次首条用户消息/正文；
 * 最后「新会话 · <底座名>」。
 *
 * `runtimeName` 只影响最后那层兜底（v19 前传的是 agent 列表，用来拼「新会话 · <agent 名>」）：
 * 一个还没发过言的会话唯一确定的事实就是"它归在哪个底座上"。取不到底座事实时退回短 id，
 * 不直接摊 36 位平台 uuid（对外 id 展示策略，P3 F4）。
 *
 * `fallback`（v21）决定**兜底那一层**在哪种面上用：默认 `id` 让左栏、重命名草稿与导出文件名
 * 能区分"这条是哪条"；问答面板的标题用 `plain`——底座信息不进对话框页面（不再拼「新会话 · dsh」，
 * 也不摊一串 id 冒充名字，那种还没命名的会话就叫「新会话」）。
 */
export function titleFor(
  sm: SessionSummary | undefined,
  view: SessionView | undefined,
  runtimeName?: string,
  fallback: 'id' | 'plain' = 'id',
): string {
  if (sm && sm.title) return sm.title
  if (view) {
    const user = view.entries.find((e) => e.kind === 'user')
    if (user?.text) return user.text
    const assistant = view.entries.find((e) => e.kind === 'assistant')
    if (assistant?.text) return assistant.text
  }
  if (sm) {
    if (fallback === 'plain') return '新会话'
    if (runtimeName) return `新会话 · ${runtimeName}`
    return `新会话 · ${shortId(sm.id)}`
  }
  return '新会话'
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
