import { useCallback, useEffect, useMemo, useState, type CSSProperties, type ReactNode } from 'react'
import { Alert, Button, Empty, Input, Modal, Select, Space, Spin, Tabs, Tag, Tooltip } from 'antd'
import { ExperimentOutlined, FileTextOutlined, PlayCircleOutlined, ReloadOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark/ui'
import {
  callMcpTool,
  getAgentCapabilities,
  getMcpPrompt,
  listMcpPrompts,
  listMcpResources,
  listMcpServers,
  listSkills,
  listTools,
  probeMcpServer,
  readMcpResource,
  toErrorMessage,
} from '@ark/api'
import type {
  AgentCapabilities,
  McpPrompt,
  McpPromptsResult,
  McpPromptGetResult,
  McpResourceReadResult,
  McpResourcesResult,
  McpServer,
  McpToolCallResult,
  Skill,
  Tool,
} from '@ark/types'
import { useForge } from '../store'
import { fmtProbeAt, mcpRows, probeSummary, toolSourceLabel, type McpRow } from '../console/capabilityView'

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
  // B1 工具调用：目标（server+tool）+ 入参文本 + 结果/错误。
  const [callTarget, setCallTarget] = useState<{ server: string; tool: string } | null>(null)
  const [callArgs, setCallArgs] = useState('{}')
  const [callBusy, setCallBusy] = useState(false)
  const [callResult, setCallResult] = useState<McpToolCallResult | null>(null)
  const [callErr, setCallErr] = useState<string | null>(null)

  // B2 资源/提示词弹窗：按需 live 拉取（打开时加载，不缓存）。
  const [liveServer, setLiveServer] = useState<{ id: string; name: string } | null>(null)
  const [liveTab, setLiveTab] = useState<'resources' | 'prompts'>('resources')
  const [liveBusy, setLiveBusy] = useState(false)
  const [liveErr, setLiveErr] = useState<string | null>(null)
  const [resResult, setResResult] = useState<McpResourcesResult | null>(null)
  const [resRead, setResRead] = useState<{ uri: string; data?: McpResourceReadResult; err?: string } | null>(null)
  const [promptResult, setPromptResult] = useState<McpPromptsResult | null>(null)
  const [promptArgs, setPromptArgs] = useState<Record<string, string>>({})
  const [promptGot, setPromptGot] = useState<{ name: string; data?: McpPromptGetResult; err?: string } | null>(null)

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

  // openCall 打开调用弹窗（默认参数取工具 schema 的 properties 骨架，纯便利、非校验）。
  const openCall = (server: string, tool: Tool): void => {
    setCallTarget({ server, tool: tool.name })
    setCallResult(null)
    setCallErr(null)
    setCallArgs(schemaSkeleton(tool.schema))
  }

  const doCall = async (): Promise<void> => {
    if (!callTarget) return
    let args: unknown
    try {
      args = callArgs.trim() ? JSON.parse(callArgs) : undefined
    } catch (err) {
      setCallErr(`arguments 不是合法 JSON：${toErrorMessage(err)}`)
      return
    }
    setCallBusy(true)
    setCallErr(null)
    try {
      const res = await callMcpTool(callTarget.server, callTarget.tool, args === undefined ? {} : { arguments: args })
      setCallResult(res)
      showToast(res.is_error ? `${callTarget.tool} 返回工具级失败（is_error=true）` : `${callTarget.tool} 调用完成（${res.duration_ms}ms）`)
    } catch (err) {
      setCallResult(null)
      setCallErr(toErrorMessage(err))
    } finally {
      setCallBusy(false)
    }
  }

  // openLive 打开资源/提示词弹窗（按当前 tab 立即 live 拉取）。
  const openLive = (m: McpRow, tab: 'resources' | 'prompts'): void => {
    setLiveServer({ id: m.id, name: m.name })
    setLiveTab(tab)
    setLiveErr(null)
    setResResult(null)
    setResRead(null)
    setPromptResult(null)
    setPromptGot(null)
    setPromptArgs({})
    void loadLive(m.id, tab)
  }

  const loadLive = async (id: string, tab: 'resources' | 'prompts'): Promise<void> => {
    setLiveBusy(true)
    setLiveErr(null)
    try {
      if (tab === 'resources') {
        setResResult(await listMcpResources(id))
      } else {
        setPromptResult(await listMcpPrompts(id))
      }
    } catch (err) {
      setLiveErr(toErrorMessage(err))
    } finally {
      setLiveBusy(false)
    }
  }

  const doReadResource = async (id: string, uri: string): Promise<void> => {
    setResRead({ uri })
    try {
      setResRead({ uri, data: await readMcpResource(id, uri) })
    } catch (err) {
      setResRead({ uri, err: toErrorMessage(err) })
    }
  }

  const doGetPrompt = async (id: string, p: McpPrompt): Promise<void> => {
    setPromptGot({ name: p.name })
    try {
      setPromptGot({ name: p.name, data: await getMcpPrompt(id, p.name, promptArgs) })
    } catch (err) {
      setPromptGot({ name: p.name, err: toErrorMessage(err) })
    }
  }

  // liveButton：MCP server 行的「资源/提示词」入口（sse 传输同样不可用，与探测一致）。
  const liveButton = (m: McpRow): ReactNode => (
    <Tooltip title={m.transport === 'sse' ? 'sse 传输暂不支持（后端明确降级）' : '实时拉取该 server 的 resources/prompts（不落盘）'}>
      <Button
        size="small"
        icon={<FileTextOutlined />}
        disabled={m.transport === 'sse'}
        onClick={() => openLive(m, 'resources')}
      >
        资源/提示词
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
                      {liveButton(m)}
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
                  {liveButton(m)}
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
                  {t.mcp_server && (
                    <Tooltip title={`调用 ${t.mcp_server}.${t.name}（tools/call，独立会话）`}>
                      <Button size="small" icon={<PlayCircleOutlined />} onClick={() => openCall(t.mcp_server!, t)}>
                        调用
                      </Button>
                    </Tooltip>
                  )}
                </Row>
              ))
            )}
          </div>
        </Card>

        {/* ——— B2：MCP 资源 / 提示词（live 拉取） ——— */}
        <Modal
          title={liveServer ? `${liveServer.name} · 资源与提示词` : '资源与提示词'}
          open={liveServer !== null}
          onCancel={() => setLiveServer(null)}
          footer={null}
          width={760}
        >
          <div style={{ fontSize: 12.5, color: tokens.textSecondary, lineHeight: 1.8, marginBottom: 8 }}>
            实时向该 MCP server 拉取（<code>resources/list</code> + <code>resources/templates/list</code> /
            <code> prompts/list</code>）：**不落盘、不目录化**——资源与提示词随 server 权限/状态变化，
            每次打开现拉最诚实。
          </div>
          <Tabs
            activeKey={liveTab}
            onChange={(k) => {
              const tab = k as 'resources' | 'prompts'
              setLiveTab(tab)
              setLiveErr(null)
              if (liveServer) void loadLive(liveServer.id, tab)
            }}
            items={[
              {
                key: 'resources',
                label: `资源${resResult ? ` (${resResult.resources.length})` : ''}`,
                children: (
                  <Spin spinning={liveBusy}>
                    {liveErr && liveTab === 'resources' ? (
                      <Alert type="error" showIcon message="资源拉取失败" description={liveErr} />
                    ) : !resResult || resResult.resources.length === 0 ? (
                      <Empty description="该 server 未暴露资源（或未实现 resources/list）" />
                    ) : (
                      <div style={{ maxHeight: 420, overflowY: 'auto' }}>
                        {resResult.templates_error && (
                          <Alert
                            type="info"
                            showIcon
                            style={{ marginBottom: 8 }}
                            message="该 server 未实现 resources/templates/list（降级为空）"
                            description={resResult.templates_error}
                          />
                        )}
                        {(resResult.templates ?? []).map((t) => (
                          <Row key={`tpl-${t.uri_template}`}>
                            <Tag color="purple" style={{ marginInlineEnd: 0, fontSize: 10.5 }}>
                              模板
                            </Tag>
                            <span style={{ ...mono, flex: 1 }}>{t.uri_template}</span>
                            <span style={{ fontSize: 11.5, color: tokens.textSecondary }}>{t.name}</span>
                          </Row>
                        ))}
                        {resResult.resources.map((r) => (
                          <div key={r.uri}>
                            <Row>
                              <span style={{ ...mono, flex: 1 }}>{r.uri}</span>
                              <span style={{ fontSize: 11.5, color: tokens.textSecondary }}>
                                {r.name}
                                {r.mime_type ? ` · ${r.mime_type}` : ''}
                                {typeof r.size === 'number' ? ` · ${r.size}B` : ''}
                              </span>
                              <Button size="small" onClick={() => void doReadResource(liveServer!.id, r.uri)}>
                                读取
                              </Button>
                            </Row>
                            {resRead?.uri === r.uri && (
                              <div style={{ padding: '0 16px 8px' }}>
                                {resRead.err ? (
                                  <Alert type="error" showIcon message="读取失败" description={resRead.err} />
                                ) : (
                                  (resRead.data?.contents ?? []).map((c, i) => (
                                    <pre
                                      key={i}
                                      style={{
                                        ...mono,
                                        margin: '0 0 6px',
                                        padding: 8,
                                        maxHeight: 200,
                                        overflow: 'auto',
                                        background: tokens.softFill,
                                        borderRadius: 8,
                                        whiteSpace: 'pre-wrap',
                                      }}
                                    >
                                      {c.text ?? `（二进制 blob，base64 ${c.blob?.length ?? 0} 字符，前端不自动解码）`}
                                    </pre>
                                  ))
                                )}
                              </div>
                            )}
                          </div>
                        ))}
                      </div>
                    )}
                  </Spin>
                ),
              },
              {
                key: 'prompts',
                label: `提示词${promptResult ? ` (${promptResult.prompts.length})` : ''}`,
                children: (
                  <Spin spinning={liveBusy}>
                    {liveErr && liveTab === 'prompts' ? (
                      <Alert type="error" showIcon message="提示词拉取失败" description={liveErr} />
                    ) : !promptResult || promptResult.prompts.length === 0 ? (
                      <Empty description="该 server 未暴露提示词（或未实现 prompts/list）" />
                    ) : (
                      <div style={{ maxHeight: 420, overflowY: 'auto' }}>
                        {promptResult.prompts.map((p) => (
                          <div key={p.name}>
                            <Row>
                              <span style={{ ...mono, minWidth: 140 }}>{p.name}</span>
                              <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>{p.description}</span>
                              <Button size="small" onClick={() => void doGetPrompt(liveServer!.id, p)}>
                                取用
                              </Button>
                            </Row>
                            {(p.arguments ?? []).length > 0 && (
                              <div style={{ padding: '0 16px 8px', display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                                {(p.arguments ?? []).map((a) => (
                                  <Input
                                    key={a.name}
                                    size="small"
                                    style={{ width: 180 }}
                                    placeholder={`${a.name}${a.required ? '（必填）' : ''}`}
                                    value={promptArgs[a.name] ?? ''}
                                    onChange={(e) => setPromptArgs((cur) => ({ ...cur, [a.name]: e.target.value }))}
                                  />
                                ))}
                              </div>
                            )}
                            {promptGot?.name === p.name && (
                              <div style={{ padding: '0 16px 8px' }}>
                                {promptGot.err ? (
                                  <Alert type="error" showIcon message="取用失败" description={promptGot.err} />
                                ) : (
                                  <pre
                                    style={{
                                      ...mono,
                                      margin: 0,
                                      padding: 8,
                                      maxHeight: 200,
                                      overflow: 'auto',
                                      background: tokens.softFill,
                                      borderRadius: 8,
                                      whiteSpace: 'pre-wrap',
                                    }}
                                  >
                                    {(promptGot.data?.messages ?? [])
                                      .map((m) => `${m.role}: ${promptText(m.content)}`)
                                      .join('\n')}
                                  </pre>
                                )}
                              </div>
                            )}
                          </div>
                        ))}
                      </div>
                    )}
                  </Spin>
                ),
              },
            ]}
          />
        </Modal>

        {/* ——— B1：MCP 工具调用（tools/call） ——— */}
        <Modal
          title={callTarget ? `调用工具 ${callTarget.server}.${callTarget.tool}` : '调用工具'}
          open={callTarget !== null}
          onCancel={() => setCallTarget(null)}
          onOk={() => void doCall()}
          okText="调用"
          confirmLoading={callBusy}
          width={680}
        >
          <div style={{ fontSize: 12.5, color: tokens.textSecondary, lineHeight: 1.8, marginBottom: 8 }}>
            参数为 JSON 对象（`arguments`）；后端每次调用建独立 MCP 会话（不做连接池）。
            工具级失败（服务端 <code>isError</code>）会以 <code>is_error=true</code> 正常返回，
            只有传输/协议/超时才报错。
          </div>
          <Input.TextArea
            value={callArgs}
            onChange={(e) => setCallArgs(e.target.value)}
            rows={5}
            spellCheck={false}
            style={{ fontFamily: tokens.fontMono, fontSize: 12.5 }}
            placeholder='{"path": "/tmp/a"}'
          />
          {callErr && (
            <Alert type="error" showIcon style={{ marginTop: 10 }} message="调用失败" description={callErr} />
          )}
          {callResult && (
            <div style={{ marginTop: 10 }}>
              <Space size={8} wrap style={{ marginBottom: 6 }}>
                <Tag color={callResult.is_error ? 'red' : 'green'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                  {callResult.is_error ? 'is_error=true（工具级失败）' : 'ok'}
                </Tag>
                <span style={{ fontSize: 11.5, color: tokens.textSecondary, fontFamily: tokens.fontMono }}>
                  {callResult.duration_ms}ms
                  {callResult.server_name ? ` · ${callResult.server_name}` : ''}
                  {callResult.server_version ? ` ${callResult.server_version}` : ''}
                </span>
              </Space>
              <pre
                style={{
                  ...mono,
                  margin: 0,
                  padding: 10,
                  maxHeight: 260,
                  overflow: 'auto',
                  background: tokens.softFill,
                  borderRadius: 8,
                  whiteSpace: 'pre-wrap',
                }}
              >
                {callResult.text || JSON.stringify(callResult.content, null, 2)}
                {callResult.structured_content
                  ? `\n\nstructuredContent:\n${JSON.stringify(callResult.structured_content, null, 2)}`
                  : ''}
              </pre>
            </div>
          )}
        </Modal>
      </div>
    </PageContainer>
  )
}

// promptText 把 prompts/get 的 content（MCP 联合类型）取成可读文本：
// 文本块取 text，其余类型原样 JSON 展示（不猜语义）。
function promptText(content: unknown): string {
  if (content && typeof content === 'object') {
    const blk = content as { type?: string; text?: string }
    if (blk.type === 'text' && typeof blk.text === 'string') return blk.text
  }
  try {
    return JSON.stringify(content)
  } catch {
    return String(content)
  }
}

// schemaSkeleton 由工具 inputSchema 生成参数骨架（properties 键 + 类型占位；无 schema → "{}"）。
// 仅作输入便利：不做校验（服务端才是权威），也不猜默认值。
function schemaSkeleton(schema: unknown): string {
  if (!schema || typeof schema !== 'object') return '{}'
  const props = (schema as { properties?: Record<string, { type?: string; default?: unknown }> }).properties
  if (!props || typeof props !== 'object') return '{}'
  const out: Record<string, unknown> = {}
  for (const [key, def] of Object.entries(props)) {
    if (def && typeof def === 'object' && 'default' in def) {
      out[key] = def.default
      continue
    }
    const type = def && typeof def === 'object' ? def.type : undefined
    out[key] = type === 'number' || type === 'integer' ? 0 : type === 'boolean' ? false : type === 'array' ? [] : type === 'object' ? {} : ''
  }
  return JSON.stringify(out, null, 2)
}
