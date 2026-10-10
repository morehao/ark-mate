import type { ReactNode } from 'react'
import { Tooltip } from 'antd'
import { Dot, Pill, agentStateMeta, tokens } from '@ark-mate/ui'
import type { Agent } from '@ark-mate/types'
import { agentNameOf } from '../console/util'

interface Props {
  /** 已解析的 agent（在 agents 列表里查到的那条）；缺失时退化为 id/兜底名展示。 */
  agent?: Agent
  /** agent 兜底 id（列表未加载 / agent 已删）：用于回退名与详情。 */
  agentId?: string
  /** 回退名（agent 不在列表里时直接展示，避免摊出 36 位平台 uuid）。 */
  fallbackName?: string
  /** 前缀标签，默认「Agent」——只报名字不说明身份时，读的人不知道这串是什么。 */
  prefix?: string
}

/**
 * 当前 Agent 标识（新建会话面板 / 会话头共用，**纯展示不可点**）。
 *
 * 视觉 = 状态点 + 「Agent · 名称」mono 胶囊（+ 当前标记）；详情走 hover 浮层
 * （状态 / provider / runtime 候选 / id）。会话与 Agent 是一对一绑定（切换 Agent = 新建会话），
 * 所以问答过程里只需要在会话头如实标出这一处，不用逐条消息重复标。
 */
export function AgentBadge({ agent, agentId, fallbackName, prefix = 'Agent' }: Props) {
  const id = agent?.id ?? agentId
  const name = agent?.name ?? fallbackName ?? (id ? agentNameOf([], id) : '未知 Agent')
  const state = agent ? agentStateMeta(agent.state) : { label: '状态未知（Agent 未加载）', color: tokens.textPlaceholder }

  const rows: Array<[string, ReactNode]> = [['状态', state.label]]
  if (agent?.is_default) rows.push(['当前', '新建会话与模型目录缺省都用它（可在「设置 · Agent 管理」切换）'])
  if (agent?.provider) rows.push(['provider', agent.provider])
  if (agent?.runtimes?.length) rows.push(['runtime 候选', agent.runtimes.join(' → ')])
  if (id) rows.push(['id', <span style={{ fontFamily: tokens.fontMono, overflowWrap: 'anywhere' }}>{id}</span>])

  const chip = (
    <Pill color={tokens.text}>
      <Dot color={state.color} size={6} />
      <span style={{ color: tokens.textPlaceholder }}>{prefix}</span>
      <span style={{ maxWidth: 180, overflow: 'hidden', textOverflow: 'ellipsis' }}>{name}</span>
      {agent?.is_default && (
        <span
          style={{
            borderRadius: 999,
            background: tokens.primarySoft,
            color: tokens.primary,
            fontSize: 9.5,
            fontWeight: 620,
            padding: '0 5px',
            lineHeight: 1.7,
          }}
        >
          当前
        </span>
      )}
    </Pill>
  )

  return (
    <Tooltip
      title={
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2, fontSize: 11.5, lineHeight: 1.7 }}>
          {rows.map(([k, v]) => (
            <div key={k} style={{ display: 'flex', gap: 8 }}>
              <span style={{ flex: 'none', opacity: 0.6 }}>{k}</span>
              <span style={{ minWidth: 0 }}>{v}</span>
            </div>
          ))}
        </div>
      }
    >
      {chip}
    </Tooltip>
  )
}
