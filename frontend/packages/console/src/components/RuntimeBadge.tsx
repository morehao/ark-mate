import type { ReactNode } from 'react'
import { Tooltip } from 'antd'
import { Dot, Pill, runtimeStateMeta, tokens } from '@ark-mate/ui'
import { useMate } from '../store'

interface Props {
  /** 兜底/覆盖的身份：会话头传会话自己的 `runtime_id`（历史会话可能停在别的底座上）。 */
  runtimeId?: string
  /** 前缀标签，默认「底座」——只报名字不说明身份时，读的人不知道这串是什么。 */
  prefix?: string
}

/**
 * 当前底座标识（新建会话面板 / 会话头共用，**纯展示不可点**）。
 *
 * 视觉 = 状态点 + 「底座 · 名称」mono 胶囊；详情走 hover 浮层（状态 / 版本 / id / 最近失败）。
 *
 * 这是 v19 从 `AgentBadge` 改来的：agent 并入 runtime 之后，"当前用的是哪个"这个问题只剩底座一个答案，
 * 而底座**不由用户选**（构建期固定）——所以这里只如实标出事实，不给任何切换入口
 * （原来那套"切换 Agent = 新建会话"的语义随 agent 概念一起消失）。
 *
 * 两个事实来源分开取，别混：**身份**来自 `GET /meta` 的 `runtime{}`（装配事实），
 * **进程状态**来自 `GET /runtime/diagnostics`（运行时事实）。缺任一条都照实说"未知"，
 * 不用另一条去顶替——把"没取到"画成"已停止"会让人去查一个不存在的问题。
 */
export function RuntimeBadge({ runtimeId, prefix = '底座' }: Props) {
  const runtime = useMate((s) => s.runtime)
  const diag = useMate((s) => s.runtimeDiagnostics)

  // 会话头传进来的 id 优先（它是这条会话的执行事实）；它可能与构建期固定的那个不同（历史会话），
  // 那种情况下界面必须如实摊出这个 id，而不是拿 meta 里的名字盖掉它。
  const id = runtimeId || runtime?.id
  const overridden = !!runtimeId && !!runtime?.id && runtimeId !== runtime.id
  const name = (overridden ? runtimeId : runtime?.name || runtime?.id) ?? '未知'
  const state = diag
    ? runtimeStateMeta(diag.state)
    : { label: '状态未知（诊断未取到）', color: tokens.textPlaceholder }

  const rows: Array<[string, ReactNode]> = [['状态', state.label]]
  if (runtime?.version) rows.push(['版本', runtime.version])
  if (overridden) rows.push(['构建期底座', `${runtime?.name || runtime?.id}（与本会话不同）`])
  if (diag?.command) rows.push(['启动命令', diag.command])
  if (diag?.pid) rows.push(['pid', String(diag.pid)])
  if (diag?.last_error) rows.push(['最近失败', `${diag.last_error.code}：${diag.last_error.details}`])
  if (id) rows.push(['id', id])

  return (
    <Tooltip
      title={
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2, fontSize: 11.5, lineHeight: 1.7 }}>
          {rows.map(([k, v]) => (
            <div key={k} style={{ display: 'flex', gap: 8 }}>
              <span style={{ flex: 'none', opacity: 0.6 }}>{k}</span>
              <span style={{ minWidth: 0, overflowWrap: 'anywhere' }}>{v}</span>
            </div>
          ))}
        </div>
      }
    >
      <Pill color={tokens.text}>
        <Dot color={state.color} size={6} />
        <span style={{ color: tokens.textPlaceholder }}>{prefix}</span>
        <span style={{ maxWidth: 180, overflow: 'hidden', textOverflow: 'ellipsis' }}>{name}</span>
      </Pill>
    </Tooltip>
  )
}
