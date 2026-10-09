import { useEffect, useState } from 'react'
import { Checkbox, Popover, Spin, Tooltip } from 'antd'
import { ApiOutlined, ExperimentOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { getAgentCapabilities, listMcpServers } from '@ark-mate/api'
import type { AgentCapabilities, McpServer } from '@ark-mate/types'
import { useMate } from '../store'
import { Label } from './WorkspaceBrowser'
import {
  mcpOptionView,
  mcpRows,
  selectionHint,
  selectionIsDefault,
  selectionPillText,
  skillOptionView,
  type CapabilityChoice,
} from '../console/capabilityView'

interface Props {
  /** 当前 Agent（新建会话固定用它；能力清单也按它的视角取）。 */
  agentId?: string
  value: CapabilityChoice
  onChange: (value: CapabilityChoice) => void
  disabled?: boolean
}

type Kind = 'skill' | 'mcp'

/** 选定列表的一行：两个 kind 合流后的形态（渲染只认它）。 */
interface OptionRow {
  id: string
  name?: string
  /** 能不能勾（与服务端校验同源；不能勾时 note 是原因）。 */
  selectable: boolean
  /** 勾了要提醒什么（空 = 无需提醒）。 */
  note: string
  /** 是否属于"缺省路径本来就会投"的集合——切到自定义时用它预填。 */
  inDefault: boolean
}

/**
 * 会话能力选择器（新建会话时勾选「这个会话用哪些技能 / 哪些 MCP」）。
 *
 * ## 为什么必须有它
 * 能力集**按进程绑定**（ADR-14）：同一个进程里的所有会话共享一套能力。所以"只让这个会话用某几个"
 * 唯一能表达的途径就是**另起一个进程**——平台据此把会话钉到一个"代际"上（M1 的指纹）。这个选择器
 * 就是把这条机制暴露给用户的那一层：勾了子集，会话就投这个子集，且与别的会话互不影响。
 *
 * ## 三态，不糊成两态
 * 每个 kind 先是「跟随默认 / 自定义」二选一，**不是**"勾选框全都不勾 = 不要"：
 *  - 跟随默认 → 请求体里那个键**不出现**（走 agent 白名单 / 技能自己的 runtimes 声明）；
 *  - 自定义 → 那个键出现，勾了哪些就是哪些（一个都不勾 = 明确一个都不要）。
 * 这两种"空"在服务端是两个不同的代际，UI 上就不能画成同一个样子。
 *
 * 切到「自定义」时**用当前默认集预填**：用户的常见动作是"减掉几个"，预填能让他只做减法，且减完
 * 若恰好等于默认集，服务端算出的指纹与"跟随默认"相同 → 不会白起一个进程。
 *
 * ## 不可达的项：列出来并说明原因，不隐藏
 * 列出来但不可勾 + 就地给原因（被禁用 / runtimes 没放行 / 没有投影落点 / 底座不承载该传输）。
 * 判据与服务端校验同源（都取 agent 视角的适用性），前端先挡只是省一次往返、把原因写清；
 * **权威判定仍在服务端**——清单可能是旧快照（定义刚被改/删），那时服务端会拒并给出原因。
 */
export function CapabilityPicker({ agentId, value, onChange, disabled }: Props) {
  const showToast = useMate((s) => s.showToast)
  const [open, setOpen] = useState(false)
  const [caps, setCaps] = useState<AgentCapabilities | null>(null)
  const [servers, setServers] = useState<McpServer[]>([])
  const [loading, setLoading] = useState(false)

  // 展开时装载（每次打开都取最新：能力清单是"现在有什么"，缓存一份旧快照只会让原因文案过期）。
  //
  // 两个请求都必要，且与能力页同款组合：`agent/capabilities` 给「这个 Agent 的候选底座 +
  // 白名单 + 技能适用性」，`/mcps` 给**定义级**开关（关掉的 MCP 谁都拿不到，而 agent 视角里
  // 看不到这个字段）。少取一个就会把"服务端一定会拒"的选项画成可勾选。
  useEffect(() => {
    if (!open || !agentId) return
    let alive = true
    setLoading(true)
    Promise.all([getAgentCapabilities(agentId), listMcpServers()])
      .then(([c, ss]) => {
        if (!alive) return
        setCaps(c)
        setServers(ss)
      })
      .catch((err) => {
        if (!alive) return
        setCaps(null)
        setServers([])
        showToast(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [open, agentId, showToast])

  const skills = caps?.skills ?? []
  const mcps = mcpRows(caps, servers)
  // 两个 kind 的行模型在这里合流（下面渲染只有一套）：能不能勾 + 原因 + 是否属于"缺省就该投"的集合。
  // `inDefault` = 与服务端**缺省路径**的投放集合一致（MCP 还要过 agent 白名单这一层）。
  const optionRows = (kind: Kind): OptionRow[] =>
    kind === 'skill'
      ? skills.map((s) => ({ id: s.id, name: s.name, inDefault: s.applicable === true, ...skillOptionView(s) }))
      : mcps.map((m) => {
          const view = mcpOptionView(m)
          return { id: m.id, name: m.name, inDefault: view.selectable && m.enabled, ...view }
        })

  const setKind = (kind: Kind, v: string[] | null) => {
    onChange(kind === 'skill' ? { ...value, skills: v } : { ...value, mcps: v })
  }

  const toggle = (kind: Kind, id: string, checked: boolean) => {
    const cur = (kind === 'skill' ? value.skills : value.mcps) ?? []
    const next = checked ? [...cur, id] : cur.filter((x) => x !== id)
    // 保持稳定顺序（按清单顺序），服务端也会规范化，但请求体可读性好一点。
    const order = optionRows(kind).map((r) => r.id)
    next.sort((a, b) => order.indexOf(a) - order.indexOf(b))
    setKind(kind, next)
  }

  const section = (kind: Kind) => {
    const list = optionRows(kind)
    const def = list.filter((r) => r.inDefault).map((r) => r.id)
    const selected = kind === 'skill' ? value.skills : value.mcps
    const custom = selected !== null
    const title = kind === 'skill' ? '技能' : 'MCP'
    return (
      <div key={kind}>
        <div className="fg-cap-head">
          <Label>{title}</Label>
          <span className="fg-cap-mode">
            <button
              type="button"
              className={`fg-cap-mode-btn${custom ? '' : ' fg-on'}`}
              onClick={() => setKind(kind, null)}
            >
              跟随默认
            </button>
            <button
              type="button"
              className={`fg-cap-mode-btn${custom ? ' fg-on' : ''}`}
              onClick={() => setKind(kind, def)}
            >
              自定义
            </button>
          </span>
        </div>
        {!custom ? (
          <div className="fg-cap-note">
            {def.length === 0 ? `当前 Agent 没有可用的${title}` : `按 Agent 声明投放（当前 ${def.length} 个）`}
          </div>
        ) : (
          <>
            <div className="fg-cap-note">
              <span>
                只投勾选的 {selected.length} 个{selected.length === 0 ? '（一个都不要）' : ''}
              </span>
              <span>
                <button type="button" className="fg-cap-mini" onClick={() => setKind(kind, [...def])}>
                  全选
                </button>
                <button type="button" className="fg-cap-mini" onClick={() => setKind(kind, [])}>
                  清空
                </button>
              </span>
            </div>
            <div className="fg-cap-list">
              {list.length === 0 && <div className="fg-cap-note">清单为空</div>}
              {list.map((row) => {
                const checked = (selected ?? []).includes(row.id)
                return (
                  <div key={row.id} className={`fg-cap-opt${row.selectable ? '' : ' fg-off'}`}>
                    <Checkbox
                      checked={checked}
                      disabled={!row.selectable}
                      onChange={(e) => toggle(kind, row.id, e.target.checked)}
                    >
                      <span className="fg-cap-opt-name">{row.name || row.id}</span>
                      <span className="fg-cap-opt-id">{row.id}</span>
                    </Checkbox>
                    {row.note && (
                      // 不可勾的项：原因就在 tooltip 里（与服务端拒它的那句同源）；
                      // 可勾但有提醒的项（不在白名单）也走这里，用户 hover 就能看到代价。
                      <Tooltip title={row.note}>
                        <span className="fg-cap-opt-why">{row.selectable ? '不在白名单' : '不可选'}</span>
                      </Tooltip>
                    )}
                  </div>
                )
              })}
            </div>
          </>
        )}
      </div>
    )
  }

  const menu = (
    <div className="fg-cap-pop">
      {loading && !caps ? (
        <div className="fg-cap-loading">
          <Spin size="small" /> 正在取能力清单…
        </div>
      ) : (
        <>
          {section('skill')}
          <div className="fg-ws-sep" />
          {section('mcp')}
          <div className="fg-ws-sep" />
          {/* 机制说明放在最后：用户先看见"能选什么"，再看"选了会怎样"。
              不写"代际/指纹"这类内部术语，只写用户能观察到的后果（各起一个进程、下一轮生效）。 */}
          <div className="fg-cap-note">
            <ThunderboltOutlined /> 不同的选定会各自起一个底座进程（互不干扰）；会话中途改能力要等下一轮生效。
          </div>
          {caps && caps.runtimes.length > 0 && (
            <div className="fg-cap-note">
              <ApiOutlined /> 可用性按当前 Agent 的底座判定：{caps.runtimes.join('、')}
            </div>
          )}
          {!caps && <div className="fg-cap-note">取不到能力清单（服务端不可达）：只能按默认投放。</div>}
        </>
      )}
    </div>
  )

  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      trigger="click"
      placement="topLeft"
      content={menu}
      arrow={false}
      styles={{ content: { padding: 6 } }}
    >
      <Tooltip title={selectionHint(value)} placement="top">
        <button
          type="button"
          className={`fg-ws-pill${selectionIsDefault(value) ? '' : ' fg-cap-on'}`}
          disabled={disabled}
        >
          <ExperimentOutlined style={{ fontSize: 12, color: tokens.textPlaceholder }} />
          <span className="fg-ws-pill-name">{selectionPillText(value)}</span>
        </button>
      </Tooltip>
    </Popover>
  )
}
