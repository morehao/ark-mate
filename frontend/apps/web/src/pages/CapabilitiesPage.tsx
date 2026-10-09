import { useCallback, useEffect, useMemo, useState, type CSSProperties, type ReactNode } from 'react'
import { Alert, Button, Empty, Input, Modal, Popconfirm, Select, Space, Spin, Switch, Tabs, Tag, Tooltip } from 'antd'
import { ExperimentOutlined, FileSearchOutlined, FileTextOutlined, PlayCircleOutlined, ReloadOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark/ui'
import {
  callMcpTool,
  createMcpServer,
  createSkill,
  deleteMcpServer,
  deleteSkill,
  getAgentCapabilities,
  getMcpPrompt,
  getSkillMaterialization,
  listMcpPrompts,
  listLibrary,
  listMcpResources,
  listMcpServers,
  listSkills,
  listTools,
  probeMcpServer,
  readMcpResource,
  setSkillEnabled,
  toErrorMessage,
  updateMcpServer,
  updateSkill,
} from '@ark/api'
import type {
  AgentCapabilities,
  AgentSkill,
  CapabilityWriteBody,
  LibraryItem,
  McpPrompt,
  McpPromptsResult,
  McpPromptGetResult,
  McpResourceReadResult,
  McpResourcesResult,
  McpServer,
  McpToolCallResult,
  Skill,
  SkillMaterializationResp,
  Tool,
} from '@ark/types'
import { useMate } from '../store'
import {
  fmtProbeAt,
  libraryKindColor,
  libraryKindLabel,
  libraryOriginView,
  libraryStatusView,
  materializationMeta,
  materializationNeedsAttention,
  materializationSummary,
  mcpRows,
  probeSummary,
  skillActivationView,
  skillRuntimesView,
  skillWarningsView,
  toolSourceLabel,
  writeErrorText,
  type McpRow,
} from '../console/capabilityView'

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

/** 行内小标签统一样式（新增代码用；既有行内样式保持不动，避免无关联改动）。 */
const smallTag: CSSProperties = { marginInlineEnd: 0, fontSize: 10.5 }

/**
 * Agent 视角 skill 的投放标签（§13.4 S5：启用 ⟂ 适用，二者正交）。
 *
 * 「已禁用」是平台侧状态（可恢复），「不适用」是这个 Agent 用不上（原因给成人话，不做静默），
 * 两者可能同时成立——所以是并列标签而不是二选一。
 */
function SkillActivationTags({ skill }: { skill: AgentSkill }) {
  const a = skillActivationView(skill)
  return (
    <>
      {!a.enabled && (
        <Tooltip title="已在平台侧禁用（定义与正文保留，可随时恢复）">
          <Tag color="default" style={smallTag}>
            已禁用
          </Tag>
        </Tooltip>
      )}
      {!a.applicable && (
        <Tooltip title={a.reasons.length > 0 ? a.reasons.join('；') : '该 Agent 的候选底座用不上这个 skill'}>
          <Tag color="orange" style={smallTag}>
            不适用
          </Tag>
        </Tooltip>
      )}
      {a.enabled && a.applicable && (
        <Tag color="green" style={smallTag}>
          可用
        </Tag>
      )}
    </>
  )
}

/**
 * 投放对象标签（M1：取代旧的 `runtime·format` 标签）。
 *
 * 空 runtimes 显示为「全部 runtime」而不是什么都不显示：后者会被读成"这个技能没有投放对象"，
 * 而真实语义正好相反（空 = 全部，是升级安全默认）。
 */
function SkillRuntimeTags({ runtimes, scope }: { runtimes?: string[]; scope?: string }) {
  const v = skillRuntimesView(runtimes, scope)
  return (
    <>
      {v.labels.map((label, i) => (
        <Tooltip key={i} title={v.emptyHint}>
          <Tag style={{ fontSize: 10, marginInlineEnd: 0 }} color={runtimes && runtimes.length > 0 ? undefined : 'default'}>
            {label}
          </Tag>
        </Tooltip>
      ))}
      {v.scopeTag && (
        <Tooltip title={v.scopeTag.hint}>
          <Tag color="blue" style={{ fontSize: 10, marginInlineEnd: 0 }}>
            {v.scopeTag.text}
          </Tag>
        </Tooltip>
      )}
    </>
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
  const showToast = useMate((s) => s.showToast)
  const agents = useMate((s) => s.agents)
  const [agentId, setAgentId] = useState<string>('')
  const [caps, setCaps] = useState<AgentCapabilities | null>(null)
  const [capsErr, setCapsErr] = useState<string | null>(null)
  const [skills, setSkills] = useState<Skill[]>([])
  const [mcps, setMcps] = useState<McpServer[]>([])
  const [tools, setTools] = useState<Tool[]>([])
  // M2-4 Library（统一读模型）：kind/q 由后端过滤（未知 kind 会报 40000，面板侧不预判）。
  const [library, setLibrary] = useState<LibraryItem[]>([])
  const [libKind, setLibKind] = useState<string>('')
  const [libQ, setLibQ] = useState<string>('')
  // M2-2/M2-3 定义写面：一个编辑器模态同时服务 skill 与 MCP（都是原文 JSON 对象）。
  const [edit, setEdit] = useState<{ kind: 'skill' | 'mcp'; id: string; text: string; created: boolean } | null>(null)
  const [editBusy, setEditBusy] = useState(false)
  const [editErr, setEditErr] = useState<string | null>(null)
  const [delBusy, setDelBusy] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [probing, setProbing] = useState<string | null>(null)
  // §13.4 S4 启停：按 skill id 记忙碌（同名行多个时只转当前那个）。
  const [toggleBusy, setToggleBusy] = useState<string | null>(null)
  // §13.4 S2 体检弹窗：目标 + 结果/错误（只报不修，故无「修复」按钮，只有再体检）。
  const [matTarget, setMatTarget] = useState<{ id: string; name: string } | null>(null)
  const [mat, setMat] = useState<SkillMaterializationResp | null>(null)
  const [matBusy, setMatBusy] = useState(false)
  const [matErr, setMatErr] = useState<string | null>(null)
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
  const loadLibrary = async (kind = libKind, q = libQ): Promise<void> => {
    try {
      setLibrary(await listLibrary({ kind: kind || undefined, q: q || undefined }))
    } catch (err) {
      showToast(writeErrorText(err))
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
    await Promise.all([loadSkills(), loadMcps(), loadTools(), loadLibrary(), loadCaps(agentId)])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentId])

  useEffect(() => {
    void loadAll()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentId])

  const refresh = (key: 'skills' | 'mcps' | 'tools' | 'caps' | 'library'): void => {
    setBusy(key)
    const p =
      key === 'skills'
        ? loadSkills()
        : key === 'mcps'
          ? loadMcps()
          : key === 'tools'
            ? loadTools()
            : key === 'library'
              ? loadLibrary()
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
    <Tooltip title={probeable ? '拉取该 server 的 tools/list 并写入 Tool 目录' : '该传输无法探测（缺少端点声明）'}>
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

  // ——— M2 定义写面（POST/PUT/DELETE /skills|/mcps） ———
  //
  // 编辑器收的是**原文 JSON 对象**而不是结构化表单：`PUT` 的语义是「只改出现过的键」，而表单
  // 分不清「没填」与「填了空串」、也表达不了 `null` 删键。给用户一个能表达全部语义的输入框，
  // 比给一组看起来很友好、但改不了未知键的表单诚实。

  /** 新建模板：只放最小可用字段（其余键由用户按需补；`id` 必填）。 */
  const newTemplate = (kind: 'skill' | 'mcp'): CapabilityWriteBody =>
    kind === 'skill'
      ? { id: '', name: '', description: '', body: '# 技能名\n\n说明与用法。', runtimes: [] }
      : { id: '', name: '', transport: 'stdio', command: '/usr/local/bin/npx', args: [] }

  const openCreate = (kind: 'skill' | 'mcp'): void => {
    setEditErr(null)
    setEdit({ kind, id: '', text: JSON.stringify(newTemplate(kind), null, 2), created: true })
  }

  /** 编辑预填：来自**读模型**（skill 的已知键齐全；MCP 的 env/密钥引用后端从不回读，故不预填）。 */
  const openEditSkill = (sk: Skill): void => {
    setEditErr(null)
    setEdit({
      kind: 'skill',
      id: sk.id,
      created: false,
      text: JSON.stringify(
        {
          id: sk.id,
          name: sk.name,
          description: sk.description ?? '',
          body: sk.body ?? '',
          runtimes: sk.runtimes ?? [],
          scope: sk.scope ?? 'global',
          enabled: sk.enabled ?? true,
        },
        null,
        2,
      ),
    })
  }

  const openEditMcp = (m: McpServer): void => {
    setEditErr(null)
    const base: CapabilityWriteBody = {
      id: m.id,
      name: m.name ?? '',
      transport: m.transport,
    }
    if (m.transport === 'stdio') {
      base.command = m.command ?? ''
      base.args = m.args ?? []
    } else {
      base.url = m.url ?? ''
    }
    setEdit({ kind: 'mcp', id: m.id, created: false, text: JSON.stringify(base, null, 2) })
  }

  /**
   * 解析编辑器文本 → 请求体。
   *
   * 必须是 JSON **对象**（数组/标量在语义上没有意义，前端先拦一次，报错文案与后端一致）；
   * 覆盖写时**去掉 id**：路径上已经是那个 id，body 里再带一份只会制造「两处 id 不一致」的
   * 40000——面板不该凭空造出这个错误。
   */
  const parseEditBody = (): CapabilityWriteBody | null => {
    if (!edit) return null
    let parsed: unknown
    try {
      parsed = JSON.parse(edit.text)
    } catch (err) {
      setEditErr(`不是合法 JSON：${err instanceof Error ? err.message : String(err)}`)
      return null
    }
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      setEditErr('定义必须是 JSON 对象（形如 {"id": "…"}）')
      return null
    }
    const body = { ...(parsed as CapabilityWriteBody) }
    if (!edit.created) delete body.id
    return body
  }

  const submitEdit = async (): Promise<void> => {
    if (!edit) return
    const body = parseEditBody()
    if (!body) return
    setEditBusy(true)
    setEditErr(null)
    try {
      if (edit.kind === 'skill') {
        const res = edit.created ? await createSkill(body) : await updateSkill(edit.id, body)
        const errs = res.materialize_errors?.length ? `；物化重校失败：${res.materialize_errors.join('；')}` : ''
        const warn = res.warnings?.length ? `（${res.warnings.length} 条告警）` : ''
        showToast(
          `${res.created ? '已新建' : '已覆盖写'} skill ${res.id}${warn}（投放 ${res.written} / 撤回 ${res.removed}）${errs}`,
        )
      } else {
        const res = edit.created ? await createMcpServer(body) : await updateMcpServer(edit.id, body)
        const unresolved = res.unresolved_secret_keys?.length
          ? `；密钥引用当前解析不出：${res.unresolved_secret_keys.join('、')}`
          : ''
        showToast(`${res.created ? '已新建' : '已覆盖写'} MCP server ${res.id}${unresolved}`)
      }
      setEdit(null)
      await Promise.all([loadSkills(), loadMcps(), loadTools(), loadLibrary(), loadCaps(agentId)])
    } catch (err) {
      // 错误留在模态里（不关窗）：用户刚写的内容还在，改一处就能重试。
      setEditErr(writeErrorText(err))
    } finally {
      setEditBusy(false)
    }
  }

  const doDelete = async (kind: 'skill' | 'mcp', id: string): Promise<void> => {
    setDelBusy(id)
    try {
      if (kind === 'skill') {
        const res = await deleteSkill(id)
        showToast(res.deleted ? `已删除 skill ${id}（撤回副本 ${res.removed}）` : `skill ${id} 本就不存在（已同步列表）`)
      } else {
        const res = await deleteMcpServer(id)
        showToast(
          res.deleted
            ? `已删除 MCP server ${id}（一并摘除 ${res.tools_removed} 条归属工具）`
            : `MCP server ${id} 本就不存在（已同步列表）`,
        )
      }
      await Promise.all([loadSkills(), loadMcps(), loadTools(), loadLibrary(), loadCaps(agentId)])
    } catch (err) {
      showToast(`删除 ${id} 失败：${writeErrorText(err)}`)
    } finally {
      setDelBusy(null)
    }
  }

  // doToggleSkill §13.4 S4：写回定义文件的 enabled 并立刻重校物化。
  // 定义文件写成功即算成功；物化失败在 materialize_errors 里回（不回滚定义——禁用是安全方向）。
  const doToggleSkill = async (s: Skill, next: boolean): Promise<void> => {
    setToggleBusy(s.id)
    try {
      const res = await setSkillEnabled(s.id, next)
      const errs = res.materialize_errors?.length ? `；重校失败：${res.materialize_errors.join('；')}` : ''
      const action = res.next_action ? ` · ${res.next_action}` : ''
      showToast(`${next ? '已启用' : '已禁用'} ${s.id}（写入 ${res.written} / 撤回 ${res.removed}）${errs}${action}`)
      await Promise.all([loadSkills(), loadCaps(agentId)])
    } catch (err) {
      showToast(`切换 ${s.id} 失败：${toErrorMessage(err)}`)
    } finally {
      setToggleBusy(null)
    }
  }

  // openMaterialization §13.4 S2：拉一次物化体检（只读，不改磁盘、不触发重校）。
  const openMaterialization = async (id: string, name: string): Promise<void> => {
    setMatTarget({ id, name })
    setMat(null)
    setMatErr(null)
    setMatBusy(true)
    try {
      setMat(await getSkillMaterialization(id))
    } catch (err) {
      setMatErr(toErrorMessage(err))
    } finally {
      setMatBusy(false)
    }
  }

  const materializationButton = (s: Skill): ReactNode => (
    <Tooltip title="看每个物化目标的摘要一致性与归属（只报不修）">
      <Button size="small" icon={<FileSearchOutlined />} onClick={() => void openMaterialization(s.id, s.name)}>
        体检
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

  // liveButton：MCP server 行的「资源/提示词」入口（B3 起 sse 同样可用）。
  const liveButton = (m: McpRow): ReactNode => (
    <Tooltip title="实时拉取该 server 的 resources/prompts（不落盘）">
      <Button size="small" icon={<FileTextOutlined />} onClick={() => openLive(m, 'resources')}>
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

  // secretTags §13.3 M6：只展示**键名**（值后端从不读回）；未就绪的引用单独标红——
  // 「绑了但解析不了」以前只能在会话启动日志里撞见，现在列表就能看出来。
  const secretTags = (m: McpRow): ReactNode =>
    m.secretKeys.length === 0 && m.unresolvedSecretKeys.length === 0 ? null : (
      <>
        {m.secretKeys.length > 0 && (
          <Tooltip title={`密钥型绑定（值只在注入时解析，界面不回）：${m.secretKeys.join('、')}`}>
            <Tag color="geekblue" style={smallTag}>
              密钥 {m.secretKeys.length}
            </Tag>
          </Tooltip>
        )}
        {m.unresolvedSecretKeys.length > 0 && (
          <Tooltip
            title={`引用已绑定但当前解析不了：${m.unresolvedSecretKeys.join('、')}。检查 vault 密文是否存在（或 vault.key 是否被换过）、env 变量是否设置——注入时解析不了这个 server 会被跳过。`}
          >
            <Tag color="red" style={smallTag}>
              密钥未就绪 {m.unresolvedSecretKeys.length}
            </Tag>
          </Tooltip>
        )}
      </>
    )

  // ——— Library 卡片（M2-4） ———
  //
  // 这是「这台机器上现在有什么能力」的**唯一视图**（四类同构）。此处刻意**不**给「整体可用吗」
  // 的合并结论：逐 runtime 报可达性 + 人话原因，用户自己决定。展示顺序按 kind 分组，
  // 因为同一个 kind 内的条目才互相可比。
  const libraryRow = (it: LibraryItem): ReactNode => {
    const origin = libraryOriginView(it.origin)
    return (
      <Row key={`${it.kind}:${it.id}`}>
        <Tag color={libraryKindColor(it.kind)} style={{ ...smallTag, minWidth: 46, textAlign: 'center' }}>
          {libraryKindLabel(it.kind)}
        </Tag>
        <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{it.name}</b>
        <span style={{ ...mono, fontSize: 11.5, color: tokens.textSecondary }}>{it.id}</span>
        <Tooltip title={origin.hint}>
          <Tag color={origin.color} style={smallTag}>
            {origin.text}
          </Tag>
        </Tooltip>
        {it.scope === 'workspace' && (
          <Tooltip title="作用域 workspace：只投放到工作区内的落点（待实测的形态会标「未实测」）">
            <Tag color="purple" style={smallTag}>
              workspace
            </Tag>
          </Tooltip>
        )}
        {!it.enabled && (
          <Tooltip title="已禁用（定义与引用都保留，可随时恢复）">
            <Tag color="red" style={smallTag}>
              已禁用
            </Tag>
          </Tooltip>
        )}
        {it.warnings && it.warnings.length > 0 && (
          <Tooltip title={<span style={{ whiteSpace: 'pre-line' }}>{it.warnings.join('\n')}</span>}>
            <Tag color="orange" style={smallTag}>
              提醒 {it.warnings.length}
            </Tag>
          </Tooltip>
        )}
        <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>
          {it.description}
          {it.source_file && <span style={{ ...mono, fontSize: 11, color: tokens.textPlaceholder }}> · {it.source_file}</span>}
        </span>
        {it.applicability.length === 0 ? (
          <Tooltip title="没有任何候选 runtime（如归属 server 已消失的工具）">
            <Tag style={{ ...smallTag, color: tokens.textPlaceholder }}>无候选底座</Tag>
          </Tooltip>
        ) : (
          it.applicability.map((a) => {
            const v = libraryStatusView(a.status)
            return (
              <Tooltip
                key={a.runtime}
                title={
                  <span style={{ whiteSpace: 'pre-line' }}>
                    {a.runtime} · {v.text}
                    {a.reason ? `\n${a.reason}` : ''}
                  </span>
                }
              >
                <Tag color={v.color} style={smallTag}>
                  {a.runtime}:{v.text}
                </Tag>
              </Tooltip>
            )
          })
        )}
      </Row>
    )
  }

  const writeButtons = (kind: 'skill' | 'mcp', id: string, name: string, onEdit: () => void): ReactNode => (
    <>
      <Tooltip title={`编辑 ${name} 的定义（只改你写进去的键，其余原样保留）`}>
        <Button size="small" onClick={onEdit}>
          编辑
        </Button>
      </Tooltip>
      <Popconfirm
        title={`删除 ${name}？`}
        description={
          kind === 'skill'
            ? '定义文件会被删除，已投放的副本会被撤回。此操作不可撤销（正文可先复制走）。'
            : '定义文件会被删除，归属它的工具条目会一并摘除。此操作不可撤销。'
        }
        okText="删除"
        okButtonProps={{ danger: true }}
        cancelText="取消"
        onConfirm={() => void doDelete(kind, id)}
      >
        <Button size="small" danger loading={delBusy === id}>
          删除
        </Button>
      </Popconfirm>
    </>
  )

  return (
    <PageContainer
      title="能力"
      description="按 Agent 看可见能力（skill 源裁剪 / MCP 启用与可承载底座 / 工具归属），并对 MCP server 触发 live tools/list 探测。全局目录来自 ~/.ark-mate/capabilities/{skills,mcp,tools}。"
      extra={
        <Space>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void loadAll()}>
            全部刷新
          </Button>
        </Space>
      }
    >
      <div style={{ padding: 18, display: 'flex', flexDirection: 'column', gap: 16 }}>
        {/* ——— M2-4 Library：四类同构的统一能力清单（只读聚合） ——— */}
        <Card>
          <SectionHeader
            title="能力清单（Library）"
            hint="skill / MCP / Agent / 工具四类同构的只读聚合视图；每条给**逐 runtime 的可达性**（不做「整体可用吗」的合并结论——对 A 可用、对 B 不可用是有用信息）"
            count={library.length}
            loading={busy === 'library'}
            onRefresh={() => refresh('library')}
            extra={
              <Space size={6}>
                <Input
                  size="small"
                  allowClear
                  style={{ width: 160 }}
                  placeholder="搜索 id / 名称 / 描述"
                  value={libQ}
                  onChange={(e) => setLibQ(e.target.value)}
                  onPressEnter={() => void loadLibrary()}
                />
                <Select
                  size="small"
                  style={{ minWidth: 120 }}
                  value={libKind || 'all'}
                  onChange={(v) => {
                    const kind = v === 'all' ? '' : v
                    setLibKind(kind)
                    void loadLibrary(kind, libQ)
                  }}
                  options={[
                    { value: 'all', label: '全部类型' },
                    { value: 'skill', label: '技能' },
                    { value: 'mcp', label: 'MCP' },
                    { value: 'agent', label: 'Agent' },
                    { value: 'tool', label: '工具' },
                  ]}
                />
              </Space>
            }
          />
          <div style={{ maxHeight: 380, overflowY: 'auto' }}>
            {library.length === 0 ? (
              <SectionEmpty text="没有匹配的能力（能力定义在 ~/.ark-mate/capabilities/{skills,mcp,tools}）" />
            ) : (
              library.map(libraryRow)
            )}
          </div>
        </Card>

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
                  <SectionEmpty text="该 Agent 无可见 skill（没有候选底座用得上它们，或本进程没解析出 skill 投影）" />
                ) : (
                  caps.skills.map((s) => (
                    <Row key={s.id}>
                      <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{s.name}</b>
                      <Tag style={{ marginInlineEnd: 0, fontSize: 11 }}>{s.id}</Tag>
                      {/* §13.4 S5：不适用也列出来并说明原因（不再静默过滤） */}
                      <SkillActivationTags skill={s} />
                      <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>{s.description}</span>
                      <SkillRuntimeTags runtimes={s.runtimes} />
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
                      {secretTags(m)}
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
          <SectionHeader
            title="Skills"
            hint="目录条目（平台唯一事实源，`~/.ark-mate/capabilities/skills/*.json`）：启用即按其 runtimes 投放（落点与格式由底座投影声明决定）、禁用即撤回副本（定义与正文保留）；「体检」看物化摘要是否被改动，并如实标注未实测/未配置的投影。新建/编辑走同一条闸门：正文扫描不过 → 40001 且**零字节落盘**"
            count={skills.length}
            loading={busy === 'skills'}
            onRefresh={() => refresh('skills')}
            extra={
              <Button size="small" onClick={() => openCreate('skill')}>
                新建 skill
              </Button>
            }
          />
          <div style={{ maxHeight: 340, overflowY: 'auto' }}>
            {skills.length === 0 ? (
              <SectionEmpty text="暂无 skill（~/.ark-mate/capabilities/skills/*.json）" />
            ) : (
              skills.map((s) => {
                const warn = skillWarningsView(s.warnings)
                const enabled = s.enabled ?? true
                return (
                  <Row key={s.id}>
                    <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{s.name}</b>
                    <Tag style={{ marginInlineEnd: 0, fontSize: 11 }}>{s.id}</Tag>
                    {/* §13.2 S1：装载期正文扫描的非 critical 告警（critical 已拒载，不会出现在这里） */}
                    {warn && (
                      <Tooltip title={<span style={{ whiteSpace: 'pre-line' }}>{warn.hint}</span>}>
                        <Tag color={warn.color} style={smallTag}>
                          {warn.text}
                        </Tag>
                      </Tooltip>
                    )}
                    <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>{s.description}</span>
                    <SkillRuntimeTags runtimes={s.runtimes} scope={s.scope} />
                    {writeButtons('skill', s.id, s.name || s.id, () => openEditSkill(s))}
                    {materializationButton(s)}
                    <Tooltip title={enabled ? '禁用：撤回已投放副本（定义与正文保留，可恢复）' : '启用：按其 runtimes 重新投放'}>
                      <Switch
                        size="small"
                        checked={enabled}
                        loading={toggleBusy === s.id}
                        onChange={(next) => void doToggleSkill(s, next)}
                      />
                    </Tooltip>
                  </Row>
                )
              })
            )}
          </div>
        </Card>

        {/* ——— 全局：MCP Servers（含 live 探测） ——— */}
        <Card>
          <SectionHeader
            title="MCP Servers"
            hint="注册表条目（§13.3 M1/M6：只回明文与密钥的**键名**，值与引用一律不读回）；「探测」= live tools/list 写入 Tool 目录；删除会**一并摘除归属它的工具条目**（留着等于让你看见一个调不通的能力）"
            count={globalMcpRows.length}
            loading={busy === 'mcps'}
            onRefresh={() => refresh('mcps')}
            extra={
              <Button size="small" onClick={() => openCreate('mcp')}>
                新建 MCP server
              </Button>
            }
          />
          <div style={{ maxHeight: 340, overflowY: 'auto' }}>
            {globalMcpRows.length === 0 ? (
              <SectionEmpty text="暂无 MCP server（~/.ark-mate/capabilities/mcp/*.json）" />
            ) : (
              globalMcpRows.map((m) => (
                <Row key={m.id}>
                  <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{m.name}</b>
                  <Tag color={TRANSPORT_COLOR[m.transport] ?? 'default'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                    {m.transport}
                  </Tag>
                  {secretTags(m)}
                  {probeStatus(m)}
                  <span style={{ flex: 1 }}>
                    <span style={mono}>{m.endpoint}</span>
                  </span>
                  {writeButtons('mcp', m.id, m.name, () => {
                    const full = mcps.find((x) => x.id === m.id)
                    if (full) openEditMcp(full)
                  })}
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
              <SectionEmpty text="暂无 tool（~/.ark-mate/capabilities/tools/*.json、代码注入或 MCP 探测）" />
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

        {/* ——— M2 定义编辑器（skill / MCP 共用；收原文 JSON 对象） ——— */}
        <Modal
          title={
            edit
              ? `${edit.created ? '新建' : '编辑'} ${edit.kind === 'skill' ? 'skill' : 'MCP server'}${edit.id ? ` · ${edit.id}` : ''}`
              : ''
          }
          open={edit !== null}
          onCancel={() => setEdit(null)}
          onOk={() => void submitEdit()}
          okText={edit?.created ? '新建' : '保存'}
          confirmLoading={editBusy}
          cancelText="取消"
          width={720}
          destroyOnClose
        >
          <div style={{ fontSize: 12, color: tokens.textSecondary, lineHeight: 1.8, marginBottom: 8 }}>
            {edit?.created ? (
              <>
                请求体就是**定义原文**（JSON 对象，<code>id</code> 必填，只能是小写字母/数字/-/_ 且 ≤64 字符）。
                <br />
              </>
            ) : (
              <>
                **只改这个框里出现过的键**：没写的键原样保留（含你自己加的字段）、写 <code>null</code> 删键、
                写 <code>""</code>/<code>[]</code> 清空。
                <br />
              </>
            )}
            {edit?.kind === 'mcp' && !edit.created && (
              <>
                MCP 的明文 env 与密钥引用**后端从不读回**（界面只知道键名），所以这里不预填——
                不动它们就别写，写了就会覆盖。密钥引用写成 <code>vault:路径</code> 或 <code>env:变量名</code>。
                <br />
              </>
            )}
            保存前先过闸门：正文扫描（critical 直接拒）、transport/路径/引用词法校验；
            <b>被拒的定义不会落盘</b>（也不会进注册表）。
          </div>
          <Input.TextArea
            value={edit?.text ?? ''}
            onChange={(e) => setEdit((cur) => (cur ? { ...cur, text: e.target.value } : cur))}
            autoSize={{ minRows: 12, maxRows: 22 }}
            spellCheck={false}
            style={{ fontFamily: tokens.fontMono, fontSize: 12.5 }}
          />
          {editErr && <Alert type="error" showIcon style={{ marginTop: 8 }} message="保存失败" description={editErr} />}
        </Modal>

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

        {/* ——— §13.4 S2：skill 物化体检（只读；修复走启停/重校，不在这里动手） ——— */}
        <Modal
          title={matTarget ? `物化体检 · ${matTarget.name}` : '物化体检'}
          open={matTarget !== null}
          onCancel={() => setMatTarget(null)}
          footer={null}
          width={780}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
            <span style={{ flex: 1, fontSize: 12.5, color: tokens.textSecondary }}>
              {mat ? materializationSummary(mat) : matBusy ? '正在体检…' : ''}
            </span>
            {mat && (
              <Tag color={mat.enabled ? 'green' : 'default'} style={smallTag}>
                {mat.enabled ? '已启用' : '已禁用'}
              </Tag>
            )}
            <Button
              size="small"
              icon={<ReloadOutlined />}
              loading={matBusy}
              disabled={!matTarget}
              onClick={() => matTarget && void openMaterialization(matTarget.id, matTarget.name)}
            >
              再体检
            </Button>
          </div>
          {matErr ? (
            <Alert type="error" showIcon message="体检失败" description={matErr} />
          ) : !mat ? (
            <div style={{ padding: 24, textAlign: 'center' }}>
              <Spin />
            </div>
          ) : (
            <>
              <Alert
                type="info"
                showIcon
                style={{ fontSize: 12, marginBottom: 10 }}
                message="体检只报不修"
                description="修复不走这里：启用/禁用 skill 会立刻重校（按平台版本覆盖回正确内容）；用户自有目录（无归属标记）平台不覆盖，需要手工处理。"
              />
              {mat.states.length === 0 ? (
                <SectionEmpty text={materializationSummary(mat)} />
              ) : (
                mat.states.map((st, i) => {
                  const meta = materializationMeta(st.status)
                  const attention = materializationNeedsAttention(st)
                  return (
                    <div
                      key={`${st.root}-${st.runtime ?? ''}-${i}`}
                      style={{
                        border: `1px solid ${attention ? tokens.borderStrong : tokens.border}`,
                        borderRadius: 8,
                        padding: '10px 12px',
                        marginBottom: 8,
                      }}
                    >
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                        <Tooltip title={meta.hint}>
                          <Tag color={meta.color} style={smallTag}>
                            {meta.label}
                          </Tag>
                        </Tooltip>
                        <Tag style={smallTag}>{st.runtime || '单根'}</Tag>
                        <span style={{ ...mono, flex: 1, fontSize: 11.5 }}>{st.dir}</span>
                      </div>
                      <div style={{ fontSize: 11.5, color: tokens.textSecondary, marginTop: 6 }}>{meta.hint}</div>
                      {st.hash && (
                        <div style={{ ...mono, fontSize: 11, color: tokens.textPlaceholder, marginTop: 4 }}>
                          期望 {st.hash.slice(0, 16)}…
                          {attention && st.actual_hash ? ` / 实测 ${st.actual_hash.slice(0, 16)}…` : ''}
                        </div>
                      )}
                      <div style={{ fontSize: 11, color: tokens.textPlaceholder, marginTop: 4 }}>
                        {st.owners && st.owners.length > 0 ? `归属：${st.owners.join('、')}` : '归属：未登记'}
                        {st.written_at ? ` · 写入 ${st.written_at}` : ''}
                      </div>
                      {st.detail && (
                        <div style={{ fontSize: 11.5, color: tokens.textSecondary, marginTop: 4 }}>{st.detail}</div>
                      )}
                    </div>
                  )
                })
              )}
              {mat.notes && mat.notes.length > 0 && (
                <Alert type="warning" showIcon style={{ fontSize: 12, marginTop: 8 }} message={mat.notes.join('；')} />
              )}
            </>
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
