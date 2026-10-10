import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Button, Popconfirm, Tag, Tooltip } from 'antd'
import { ExperimentOutlined, FileSearchOutlined, FileTextOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { useMate } from '../../store'
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
} from '@ark-mate/api'
import type {
  AgentCapabilities,
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
} from '@ark-mate/types'
import {
  fmtProbeAt,
  mcpRows,
  probeSummary,
  writeErrorText,
  type McpRow,
} from '../../console/capabilityView'
import { type CapabilityEditState, smallTag, schemaSkeleton } from './shared'
/**
 * 能力数据的唯一加载 / 变更入口（设计 §模块划分：把 1468 行的能力页拆成「数据 + 三张面板」）。
 *
 * 按 scope 只拉本页要用的数据源，避免「打开技能页先发 5 个请求」。
 * 契约零改动：这里调的仍是被拆页原有的那些 REST 接口。
 */
export type CapScope = 'overview' | 'skills' | 'connectors'

export function useCapabilityData(scope: CapScope) {
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
  const [edit, setEdit] = useState<CapabilityEditState | null>(null)
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

  /**
   * 按页加载（设计 §关键逻辑）：只拉本页真正要用的数据源。
   *   总览 = Library + 该 Agent 的能力投影 + MCP 行（caps 里已含 skills/tools）；
   *   技能 = skills；连接器 = mcp + tools。
   */
  const loadAll = useCallback(async (): Promise<void> => {
    if (scope === 'skills') {
      await loadSkills()
      return
    }
    if (scope === 'connectors') {
      await Promise.all([loadMcps(), loadTools()])
      return
    }
    await Promise.all([loadLibrary(), loadCaps(agentId), loadMcps()])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope, agentId])

  useEffect(() => {
    void loadAll()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope, agentId])

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
  return {
    agentId,
    setAgentId,
    caps,
    setCaps,
    capsErr,
    setCapsErr,
    skills,
    setSkills,
    mcps,
    setMcps,
    tools,
    setTools,
    library,
    setLibrary,
    libKind,
    setLibKind,
    libQ,
    setLibQ,
    edit,
    setEdit,
    editBusy,
    setEditBusy,
    editErr,
    setEditErr,
    delBusy,
    setDelBusy,
    busy,
    setBusy,
    probing,
    setProbing,
    toggleBusy,
    setToggleBusy,
    matTarget,
    setMatTarget,
    mat,
    setMat,
    matBusy,
    setMatBusy,
    matErr,
    setMatErr,
    callTarget,
    setCallTarget,
    callArgs,
    setCallArgs,
    callBusy,
    setCallBusy,
    callResult,
    setCallResult,
    callErr,
    setCallErr,
    liveServer,
    setLiveServer,
    liveTab,
    setLiveTab,
    liveBusy,
    setLiveBusy,
    liveErr,
    setLiveErr,
    resResult,
    setResResult,
    resRead,
    setResRead,
    promptResult,
    setPromptResult,
    promptArgs,
    setPromptArgs,
    promptGot,
    setPromptGot,
    showToast,
    agents,
    loadSkills,
    loadMcps,
    loadLibrary,
    loadTools,
    loadCaps,
    loadAll,
    refresh,
    agentMcpRows,
    globalMcpRows,
    doProbe,
    probeButton,
    newTemplate,
    openCreate,
    openEditSkill,
    openEditMcp,
    parseEditBody,
    submitEdit,
    doDelete,
    doToggleSkill,
    openMaterialization,
    materializationButton,
    openCall,
    doCall,
    openLive,
    loadLive,
    doReadResource,
    doGetPrompt,
    liveButton,
    probeStatus,
    secretTags,
    writeButtons,
  }
}

export type CapabilityData = ReturnType<typeof useCapabilityData>
