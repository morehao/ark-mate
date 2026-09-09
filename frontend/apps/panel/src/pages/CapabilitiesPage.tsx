import { useCallback, useEffect, useMemo, useState, type CSSProperties, type ReactNode } from 'react'
import { Alert, Button, Select, Space, Tag, Tooltip } from 'antd'
import { ExperimentOutlined, ReloadOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark/ui'
import { getAgentCapabilities, listMcpServers, listSkills, listTools, probeMcpServer, toErrorMessage } from '@ark/api'
import type { AgentCapabilities, McpServer, Skill, Tool } from '@ark/types'
import { useForge } from '../store'
import { fmtProbeAt, mcpRows, probeSummary, toolSourceLabel } from '../console/capabilityView'

// ——— 能力目录页（§8.3 能力卡） ———
// 上半：Agent 视角统一能力目录（GET /agents/:id/capabilities，§8.1）——按候选 runtime 裁剪/标注；
// 下半：平台三张表全局视图（GET /skills|/mcps|/tools，P1）+ MCP live 探测（POST /mcps/:id/probe，§5.2）。

const SOURCE_COLOR: Record<string, string> = { builtin: 'blue', mcp: 'green', observed: 'gold' }
const TRANSPORT_COLOR: Record<string, string> = { stdio: 'default', sse: 'blue', http: 'green', streamable_http: 'purple' }

const mono: CSSProperties = { fontFamily: tokens.fontMono, fontSize: 12.5, color: tokens.text, overflowWrap: 'anywhere' }

function SectionHeader({
  title,
  hint,
  count,
  loading,
  onRefresh,
  extra,
}: {
  title: string
  hint: string
  count: number
  loading: boolean
  onRefresh: () => void
  extra?: ReactNode
}) {
  return (
    <header
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 10,
        padding: '10px 16px',
        borderBottom: `1px solid ${tokens.border}`,
      }}
    >
      <b style={{ fontSize: 13.5, fontWeight: 600 }}>{title}</b>
      <Tag style={{ marginInlineEnd: 0, fontSize: 11 }}>{count}</Tag>
      <span style={{ flex: 1, fontSize: 11.5, color: tokens.textPlaceholder }}>{hint}</span>
      {extra}
      <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={onRefresh}>
        刷新
      </Button>
    </header>
  )
}

function SectionEmpty({ text }: { text: string }) {
  return <div style={{ padding: '28px 20px', textAlign: 'center', color: tokens.textPlaceholder, fontSize: 12.5 }}>{text}</div>
}

function Row({ children }: { children: ReactNode }) {
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8, padding: '10px 16px', borderBottom: `1px solid ${tokens.border}` }}>
      {children}
    </div>
  )
}

function Card({ children }: { children: ReactNode }) {
  return (
    <section style={{ border: `1px solid ${tokens.border}`, borderRadius: 12, background: tokens.surface, overflow: 'hidden' }}>
      {children}
    </section>
  )
}

/** 能力目录管理页（P1 全局只读 + P2 Agent 视角与 MCP live 探测）。 */
export function CapabilitiesPage() {
  const showToast = useForge((s) => s.showToast)
  const agents = useForge((s) => s.agents)
  const [agentId, setAgentId] = useState<string>('')
  const [caps, setCaps] = useState<AgentCapabilities | null>(null)
  const [capsErr, setCapsErr] = useState<string | null>(null)
  const [skills, setSkills] = useState<Skill[]>([])
  const [mcps, setMcps] = useState<McpServer[]>([])
  const [tools, setTools] = useState<Tool[]>([])
  const [busy, setBusy] = useState<string | null>(null)
  const [probing, setProbing] = useState<string | null>(null)

  // 默认选中第一个 Agent（Agent 列表由 store 统一维护）。
  useEffect(() => {
    if (!agentId && agents.length > 0) setAgentId(agents[0].id)
  }, [agents, agentId])

  const loadSkills = async (): Promise<void> => {
    try {
      setSkills(await listSkills())
    } catch (err) {
      showToast(toErrorMessage(err))
    }
  }
  const loadMcps = async (): Promise<void> => {
    try {
      setMcps(await listMcpServers())
    } catch (err) {
      showToast(toErrorMessage(err))
    }
  }
  const loadTools = async (): Promise<void> => {
    try {
      setTools(await listTools())
    } catch (err) {
      showToast(toErrorMessage(err))
    }
  }
  const loadCaps = useCallback(async (id: string): Promise<void> => {
    if (!id) {
      setCaps(null)
      return
    }
    setBusy('caps')
    setCapsErr(null)
    try {
      setCaps(await getAgentCapabilities(id))
    } catch (err) {
      setCaps(null)
      setCapsErr(toErrorMessage(err))
    } finally {
      setBusy(null)
    }
  }, [])

  const loadAll = useCallback(async (): Promise<void> => {
    await Promise.all([loadSkills(), loadMcps(), loadTools(), loadCaps(agentId)])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentId])

  useEffect(() => {
    void loadAll()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentId])

  const refresh = (key: 'skills' | 'mcps' | 'tools' | 'caps'): void => {
    setBusy(key)
    const p =
      key === 'skills'
        ? loadSkills()
        : key === 'mcps'
          ? loadMcps()
          : key === 'tools'
            ? loadTools()
            : loadCaps(agentId)
    void p.finally(() => setBusy((cur) => (cur === key ? null : cur)))
  }

  // Agent 视角 MCP 行（合并全局探测状态）；全局视图用同一套行模型（caps=null）。
  const agentMcpRows = useMemo(() => mcpRows(caps, mcps), [caps, mcps])
  const globalMcpRows = useMemo(() => mcpRows(null, mcps), [mcps])

  const doProbe = async (id: string): Promise<void> => {
    setProbing(id)
    try {
      const res = await probeMcpServer(id)
      showToast(`探测 ${id}：${probeSummary(res)}`)
      // 探测会改写 Tool 目录与 MCP 状态 → 刷新全局表与当前 Agent 视图。
      await Promise.all([loadTools(), loadMcps(), loadCaps(agentId)])
    } catch (err) {
      showToast(`探测 ${id} 失败：${toErrorMessage(err)}`)
      void loadMcps() // 失败原因落 last_probe_error，刷新可读回
    } finally {
      setProbing(null)
    }
  }

  const probeButton = (id: string, probeable: boolean): ReactNode => (
    <Tooltip title={probeable ? '拉取该 server 的 tools/list 并写入 Tool 目录' : 'sse 传输暂不支持探测（后端明确降级）'}>
      <Button
        size="small"
        icon={<ExperimentOutlined />}
        disabled={!probeable}
        loading={probing === id}
        onClick={() => void doProbe(id)}
      >
        探测
      </Button>
    </Tooltip>
  )

  const probeStatus = (row: { toolCount: number; lastProbeAt?: string; lastProbeError?: string }): ReactNode =>
    row.lastProbeError ? (
      <Tooltip title={row.lastProbeError}>
        <Tag color="red" style={{ marginInlineEnd: 0, fontSize: 10.5 }}>
          探测失败
        </Tag>
      </Tooltip>
    ) : row.lastProbeAt ? (
      <Tooltip title={`最近探测 ${fmtProbeAt(row.lastProbeAt)}`}>
        <Tag color="green" style={{ marginInlineEnd: 0, fontSize: 10.5 }}>
          {row.toolCount} 工具
        </Tag>
      </Tooltip>
    ) : (
      <Tag style={{ marginInlineEnd: 0, fontSize: 10.5, color: tokens.textPlaceholder }}>
        未探测
      </Tag>
    )

  return (
    <PageContainer
      title="能力"
      description="按 Agent 看可见能力（skill 源裁剪 / MCP 启用与可承载底座 / 工具归属），并对 MCP server 触发 live tools/list 探测。全局目录来自 ~/.forge/capabilities/{skills,mcp,tools}。"
      extra={
        <Space>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void loadAll()}>
            全部刷新
          </Button>
        </Space>
      }
    >
      <div style={{ padding: 18, display: 'flex', flexDirection: 'column', gap: 16 }}>
        {/* ——— Agent 视角统一能力目录（§8.1） ——— */}
        <Card>
          <SectionHeader
            title="Agent 视角"
            hint="该 Agent 可见的能力面（未注册底座/禁用 MCP 会在下方提示）"
            count={(caps?.skills.length ?? 0) + (caps?.mcp_servers.length ?? 0) + (caps?.tools.length ?? 0)}
            loading={busy === 'caps'}
            onRefresh={() => refresh('caps')}
            extra={
              <Select
                size="small"
                style={{ minWidth: 180 }}
                placeholder="选择 Agent"
                value={agentId || undefined}
                onChange={(v) => setAgentId(v)}
                options={agents.map((a) => ({ value: a.id, label: a.name ? `${a.name}（${a.id}）` : a.id }))}
              />
            }
          />
          {capsErr ? (
            <SectionEmpty text={`读取失败：${capsErr}`} />
          ) : !caps ? (
            <SectionEmpty text="选择 Agent 查看其统一能力目录" />
          ) : (
            <div>
              {caps.notes && caps.notes.length > 0 && (
                <div style={{ padding: '10px 16px 0' }}>
                  <Alert
                    type="warning"
                    showIcon
                    style={{ fontSize: 12 }}
                    message={caps.notes.join('；')}
                  />
                </div>
              )}
              <div style={{ padding: '10px 16px 0', display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                <span style={{ fontSize: 11.5, color: tokens.textPlaceholder }}>候选底座</span>
                {caps.runtimes.length === 0 ? (
                  <span style={{ fontSize: 11.5, color: tokens.textPlaceholder }}>—</span>
                ) : (
                  caps.runtimes.map((r) => (
                    <Tag key={r} style={{ fontSize: 10.5, marginInlineEnd: 0 }}>
                      {r}
                    </Tag>
                  ))
                )}
              </div>

              <div style={{ padding: '10px 16px 0' }}>
                <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 6 }}>
                  Skills <Tag style={{ fontSize: 10.5 }}>{caps.skills.length}</Tag>
                </div>
                {caps.skills.length === 0 ? (
                  <SectionEmpty text="该 Agent 无可见 skill（无匹配候选底座的物化源）" />
                ) : (
                  caps.skills.map((s) => (
                    <Row key={s.id}>
                      <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{s.name}</b>
                      <Tag style={{ marginInlineEnd: 0, fontSize: 11 }}>{s.id}</Tag>
                      <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>{s.description}</span>
                      {s.sources?.map((src, i) => (
                        <Tooltip key={i} title={src.path}>
                          <Tag style={{ fontSize: 10, marginInlineEnd: 0 }}>
                            {src.runtime}·{src.format}
                          </Tag>
                        </Tooltip>
                      ))}
                    </Row>
                  ))
                )}
              </div>

              <div style={{ padding: '14px 16px 0' }}>
                <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 6 }}>
                  MCP Servers <Tag style={{ fontSize: 10.5 }}>{agentMcpRows.length}</Tag>
                </div>
                {agentMcpRows.length === 0 ? (
                  <SectionEmpty text="注册表为空" />
                ) : (
                  agentMcpRows.map((m) => (
                    <Row key={m.id}>
                      <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{m.name}</b>
                      <Tag color={TRANSPORT_COLOR[m.transport] ?? 'default'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                        {m.transport}
                      </Tag>
                      <Tag color={m.enabled ? 'green' : 'default'} style={{ marginInlineEnd: 0, fontSize: 10.5 }}>
                        {m.enabled ? '已启用' : '未启用'}
                      </Tag>
                      {probeStatus(m)}
                      <span style={{ flex: 1, display: 'flex', gap: 5, flexWrap: 'wrap' }}>
                        {m.runtimes.length === 0 ? (
                          <span style={{ fontSize: 11.5, color: tokens.textPlaceholder }}>无候选底座可承载</span>
                        ) : (
                          m.runtimes.map((r) => (
                            <Tag key={r} style={{ fontSize: 10.5, marginInlineEnd: 0 }} color="cyan">
                              {r}
                            </Tag>
                          ))
                        )}
                      </span>
                      {probeButton(m.id, m.probeable)}
                    </Row>
                  ))
                )}
              </div>

              <div style={{ padding: '14px 16px 16px' }}>
                <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 6 }}>
                  Tools <Tag style={{ fontSize: 10.5 }}>{caps.tools.length}</Tag>
                </div>
                {caps.tools.length === 0 ? (
                  <SectionEmpty text="该 Agent 无可见工具" />
                ) : (
                  caps.tools.map((t) => (
                    <Row key={t.name}>
                      <span style={{ ...mono, fontWeight: 600, minWidth: 150 }}>{t.name}</span>
                      <Tag color={SOURCE_COLOR[t.source] ?? 'default'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                        {toolSourceLabel(t)}
                      </Tag>
                      <Tooltip title={t.schema ? JSON.stringify(t.schema) : undefined}>
                        <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>
                          {t.description || (t.schema ? '（含参数 Schema）' : '')}
                        </span>
                      </Tooltip>
                    </Row>
                  ))
                )}
              </div>
            </div>
          )}
        </Card>

        {/* ——— 全局：Skills ——— */}
        <Card>
          <SectionHeader title="Skills" hint="目录条目（body 为 SKILL.md 内容；物化到各 runtime 已接线）" count={skills.length} loading={busy === 'skills'} onRefresh={() => refresh('skills')} />
          <div style={{ maxHeight: 340, overflowY: 'auto' }}>
            {skills.length === 0 ? (
              <SectionEmpty text="暂无 skill（~/.forge/capabilities/skills/*.json）" />
            ) : (
              skills.map((s) => (
                <Row key={s.id}>
                  <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{s.name}</b>
                  <Tag style={{ marginInlineEnd: 0, fontSize: 11 }}>{s.id}</Tag>
                  <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>{s.description}</span>
                  {s.sources?.map((src, i) => (
                    <Tag key={i} style={{ fontSize: 10, marginInlineEnd: 0 }}>
                      {src.runtime}·{src.format}
                    </Tag>
                  ))}
                </Row>
              ))
            )}
          </div>
        </Card>

        {/* ——— 全局：MCP Servers（含 live 探测） ——— */}
        <Card>
          <SectionHeader
            title="MCP Servers"
            hint="注册表条目（headers/凭证后端不读回）；「探测」= live tools/list 写入 Tool 目录"
            count={globalMcpRows.length}
            loading={busy === 'mcps'}
            onRefresh={() => refresh('mcps')}
          />
          <div style={{ maxHeight: 340, overflowY: 'auto' }}>
            {globalMcpRows.length === 0 ? (
              <SectionEmpty text="暂无 MCP server（~/.forge/capabilities/mcp/*.json）" />
            ) : (
              globalMcpRows.map((m) => (
                <Row key={m.id}>
                  <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{m.name}</b>
                  <Tag color={TRANSPORT_COLOR[m.transport] ?? 'default'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                    {m.transport}
                  </Tag>
                  {probeStatus(m)}
                  <span style={{ flex: 1 }}>
                    <span style={mono}>{m.endpoint}</span>
                  </span>
                  {probeButton(m.id, m.probeable)}
                </Row>
              ))
            )}
          </div>
        </Card>

        {/* ——— 全局：Tools ——— */}
        <Card>
          <SectionHeader
            title="Tools"
            hint="平台/静态 + MCP tools/list live 探测条目（带归属 server）"
            count={tools.length}
            loading={busy === 'tools'}
            onRefresh={() => refresh('tools')}
          />
          <div style={{ maxHeight: 340, overflowY: 'auto' }}>
            {tools.length === 0 ? (
              <SectionEmpty text="暂无 tool（~/.forge/capabilities/tools/*.json、代码注入或 MCP 探测）" />
            ) : (
              tools.map((t) => (
                <Row key={t.name}>
                  <span style={{ ...mono, fontWeight: 600, minWidth: 150 }}>{t.name}</span>
                  <Tag color={SOURCE_COLOR[t.source] ?? 'default'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                    {toolSourceLabel(t)}
                  </Tag>
                  <Tooltip title={t.schema ? JSON.stringify(t.schema) : undefined}>
                    <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>
                      {t.description || (t.schema ? '（含参数 Schema）' : '')}
                    </span>
                  </Tooltip>
                </Row>
              ))
            )}
          </div>
        </Card>
      </div>
    </PageContainer>
  )
}
