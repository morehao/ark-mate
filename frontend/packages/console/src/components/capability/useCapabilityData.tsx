import { useCallback, useEffect, useMemo, useState } from 'react'
import { useMate } from '../../store'
import {
  callMcpTool,
  createMcpServer,
  createSkill,
  deleteMcpServer,
  deleteSkill,
  importSkillPackage,
  listMcpServers,
  listSkills,
  listTools,
  probeMcpServer,
  setSkillEnabled,
  toErrorMessage,
  updateMcpServer,
  updateSkill,
} from '@ark-mate/api'
import type {
  CapabilityWriteBody,
  McpServer,
  McpToolCallResult,
  McpWriteResp,
  Skill,
  SkillWriteResp,
  Tool,
} from '@ark-mate/types'
import {
  isEmptyPatch,
  mcpCreateBody,
  mcpFormPatch,
  mcpFormValues,
  mcpRows,
  mcpTransportView,
  probeSummary,
  skillCreateBody,
  skillFormPatch,
  skillFormValues,
  writeErrorText,
  type McpFormValues,
  type McpImportOutcome,
  type McpJsonServer,
  type McpRow,
  type SkillFormValues,
} from '../../console/capabilityView'
import { type CapabilityEditState } from './shared'

/**
 * 能力数据的唯一加载 / 变更入口（设计 §模块划分：把能力页拆成「数据 + 面板」）。
 *
 * 按 scope 只拉本页要用的数据源，避免「打开技能页先发 5 个请求」。
 * 契约零改动：这里调的仍是被拆页原有的那些 REST 接口。
 *
 * 2026-10-18 的两页改造把「写面」从原文 JSON 编辑器搬进了表单，但**写面的语义没变**：
 * 编辑只提交变化键（`capabilityView.ts` 的 patch 纯函数给出），成功后重新拉投影、
 * 不用本地乐观状态覆盖服务端。原文 JSON 仍保留为「高级」入口（未知键/精确 null/[] 的出路）。
 */
type CapScope = 'skills' | 'connectors'

/** 详情弹窗的目标与当前 tab（tab 的取值由两个面板各自解释）。 */
interface CapabilityDetailState {
  kind: 'skill' | 'mcp'
  id: string
  tab: string
}

export function useCapabilityData(scope: CapScope) {
  const showToast = useMate((s) => s.showToast)
  const [skills, setSkills] = useState<Skill[]>([])
  const [mcps, setMcps] = useState<McpServer[]>([])
  const [tools, setTools] = useState<Tool[]>([])
  const [busy, setBusy] = useState<string | null>(null)
  const [probing, setProbing] = useState<string | null>(null)
  const [toggleBusy, setToggleBusy] = useState<string | null>(null)
  const [delBusy, setDelBusy] = useState<string | null>(null)

  // ——— 弹窗状态（两个页面共用一套编排，避免「哪一页能开哪个弹窗」变成两份状态机） ———
  const [detail, setDetail] = useState<CapabilityDetailState | null>(null)
  const [skillForm, setSkillForm] = useState<{ skill: Skill | null } | null>(null)
  const [mcpForm, setMcpForm] = useState<{ server: McpServer | null } | null>(null)
  const [formBusy, setFormBusy] = useState(false)
  const [formErr, setFormErr] = useState<string | null>(null)
  const [chatOpen, setChatOpen] = useState(false)
  const [uploadOpen, setUploadOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)

  // 原文 JSON 编辑器（高级入口；skill 与 MCP 共用）。
  const [edit, setEdit] = useState<CapabilityEditState | null>(null)
  const [editBusy, setEditBusy] = useState(false)
  const [editErr, setEditErr] = useState<string | null>(null)

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
  /** 按页加载：只拉本页真正要用的数据源。 */
  const loadAll = useCallback(async (): Promise<void> => {
    if (scope === 'skills') {
      await loadSkills()
      return
    }
    await Promise.all([loadMcps(), loadTools()])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope])

  useEffect(() => {
    void loadAll()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope])

  const refresh = (key: 'skills' | 'mcps' | 'tools'): void => {
    setBusy(key)
    const p = key === 'skills' ? loadSkills() : key === 'mcps' ? loadMcps() : loadTools()
    void p.finally(() => setBusy((cur) => (cur === key ? null : cur)))
  }

  // MCP 行模型（合并定义级开关、逐 runtime 可达性与全局探测状态）。
  const globalMcpRows = useMemo(() => mcpRows(mcps), [mcps])

  // ——— 详情 ———
  const openDetail = (kind: 'skill' | 'mcp', id: string, tab = 'overview'): void => {
    setDetail({ kind, id, tab })
  }
  const closeDetail = (): void => setDetail(null)
  const setDetailTab = (tab: string): void => setDetail((cur) => (cur ? { ...cur, tab } : cur))

  // ——— 写面：表单 ———
  const openSkillForm = (skill: Skill | null = null): void => {
    setFormErr(null)
    setSkillForm({ skill })
  }
  const closeSkillForm = (): void => setSkillForm(null)
  const openMcpForm = (server: McpServer | null = null): void => {
    setFormErr(null)
    setMcpForm({ server })
  }
  const closeMcpForm = (): void => setMcpForm(null)

  // 原文 JSON（高级入口）：把**当前表单值**带过去，避免「切到高级就丢掉刚填的内容」。
  // 编辑态刻意只预填已知键：未知键不会被提交，也就不会因为没看见而被删掉。
  const openRawEditor = (kind: 'skill' | 'mcp', body: CapabilityWriteBody, id: string, created: boolean): void => {
    setFormErr(null)
    setEditErr(null)
    setSkillForm(null)
    setMcpForm(null)
    setEdit({ kind, id, created, text: JSON.stringify(body, null, 2) })
  }

  const openMcpAdvanced = (values: McpFormValues): void => {
    // 编辑态不预填 env/headers（值不可读；顶层键整体替换会删掉已存引用）。
    const body = mcpCreateBody(values)
    if (mcpForm?.server) {
      delete body.env
      delete body.secret_env
      delete body.headers
      delete body.secret_headers
      // 用户若在表单里改过传输，高级入口必须带着**改后的**值；没改则保留原值（旧 http 不被改型）。
      body.transport =
        mcpTransportView(values.originalTransport) === values.transport
          ? values.originalTransport || values.transport
          : values.transport
    }
    openRawEditor('mcp', body, values.id, mcpForm?.server === null)
  }

  const submitSkillForm = async (values: SkillFormValues): Promise<void> => {
    if (!skillForm) return
    const created = skillForm.skill === null
    const body = created
      ? skillCreateBody(values)
      : skillFormPatch(skillFormValues(skillForm.skill as Skill), values)
    if (!created && isEmptyPatch(body)) {
      showToast('没有改动', 'info')
      setSkillForm(null)
      return
    }
    setFormBusy(true)
    setFormErr(null)
    try {
      const res: SkillWriteResp = created
        ? await createSkill(body)
        : await updateSkill((skillForm.skill as Skill).id, body)
      showToast(`${created ? '已创建' : '已保存'}技能「${skillForm.skill?.name || res.id}」`, 'success')
      setSkillForm(null)
      await loadAll()
    } catch (err) {
      // 错误留在弹窗里：用户刚填的内容还在，改一处就能重试。
      setFormErr(writeErrorText(err))
    } finally {
      setFormBusy(false)
    }
  }

  const submitMcpForm = async (values: McpFormValues): Promise<void> => {
    if (!mcpForm) return
    const created = mcpForm.server === null
    const body = created
      ? mcpCreateBody(values)
      : mcpFormPatch(mcpFormValues(mcpForm.server as McpServer), values)
    if (!created && isEmptyPatch(body)) {
      showToast('没有改动', 'info')
      setMcpForm(null)
      return
    }
    setFormBusy(true)
    setFormErr(null)
    try {
      const res: McpWriteResp = created
        ? await createMcpServer(body)
        : await updateMcpServer((mcpForm.server as McpServer).id, body)
      const unresolved = res.unresolved_secret_keys?.length
        ? `；密钥引用当前解析不出：${res.unresolved_secret_keys.join('、')}`
        : ''
      showToast(`${created ? '已创建' : '已保存'}连接器「${res.id}」${unresolved}`, 'success')
      setMcpForm(null)
      await loadAll()
    } catch (err) {
      setFormErr(writeErrorText(err))
    } finally {
      setFormBusy(false)
    }
  }

  /** 原文 JSON 提交（高级入口）：语义与旧编辑器完全一致（只改出现过的键、null 删键）。 */
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
      setEditErr('配置必须是 JSON 对象')
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
        showToast(`${res.created ? '已创建' : '已保存'}技能「${res.id}」`, 'success')
      } else {
        const res = edit.created ? await createMcpServer(body) : await updateMcpServer(edit.id, body)
        const unresolved = res.unresolved_secret_keys?.length
          ? `；密钥引用当前解析不出：${res.unresolved_secret_keys.join('、')}`
          : ''
        showToast(`${res.created ? '已创建' : '已保存'}连接器「${res.id}」${unresolved}`, 'success')
      }
      setEdit(null)
      await loadAll()
    } catch (err) {
      setEditErr(writeErrorText(err))
    } finally {
      setEditBusy(false)
    }
  }

  // ——— 启停 / 删除 / 探测 ———
  const doToggleSkill = async (s: Skill, next: boolean): Promise<void> => {
    setToggleBusy(s.id)
    try {
      await setSkillEnabled(s.id, next)
      showToast(`${next ? '已启用' : '已停用'}技能「${s.name || s.id}」`, 'success')
      await loadSkills()
    } catch (err) {
      showToast(`切换 ${s.id} 失败：${toErrorMessage(err)}`)
    } finally {
      setToggleBusy(null)
    }
  }

  // MCP 没有单独的启停路由：`PUT` 只改出现过的键，`{enabled}` 就是最小的启停写面。
  const doToggleMcp = async (m: McpRow, next: boolean): Promise<void> => {
    setToggleBusy(m.id)
    try {
      await updateMcpServer(m.id, { enabled: next })
      showToast(`${next ? '已启用' : '已停用'}连接器「${m.name || m.id}」${next ? '' : '（配置与密钥会保留）'}`, 'success')
      await loadMcps()
    } catch (err) {
      showToast(`切换「${m.name || m.id}」失败：${writeErrorText(err)}`)
    } finally {
      setToggleBusy(null)
    }
  }

  const doDelete = async (kind: 'skill' | 'mcp', id: string): Promise<void> => {
    setDelBusy(id)
    try {
      if (kind === 'skill') {
        const res = await deleteSkill(id)
        const name = skills.find((x) => x.id === id)?.name || id
        showToast(res.deleted ? `已删除技能「${name}」` : `技能「${name}」本就不存在（已同步列表）`, 'success')
      } else {
        const res = await deleteMcpServer(id)
        showToast(
          res.deleted
            ? `已删除连接器「${id}」（一并移除 ${res.tools_removed} 个工具）`
            : `连接器「${id}」本就不存在（已同步列表）`,
          'success',
        )
      }
      if (detail && detail.kind === kind && detail.id === id) setDetail(null)
      await loadAll()
    } catch (err) {
      showToast(`删除 ${id} 失败：${writeErrorText(err)}`)
    } finally {
      setDelBusy(null)
    }
  }

  const doProbe = async (id: string): Promise<void> => {
    setProbing(id)
    try {
      const res = await probeMcpServer(id)
      showToast(`已更新「${id}」：${probeSummary(res)}`, 'success')
      await Promise.all([loadTools(), loadMcps()])
    } catch (err) {
      showToast(`更新「${id}」失败：${toErrorMessage(err)}`)
      void loadMcps() // 失败原因落 last_probe_error，刷新可读回
    } finally {
      setProbing(null)
    }
  }

  // ——— 技能包上传（D1）与 MCP JSON 导入 ———
  const doImportSkillPackage = async (file: File): Promise<SkillWriteResp> => {
    const res = await importSkillPackage(file)
    showToast(`${res.created ? '已安装' : '已更新'}技能「${res.id}」`, 'success')
    await loadAll()
    return res
  }

  /**
   * MCP JSON 批量导入：不新增后端批量接口，逐条 `POST /mcps`。
   *
   * 同名跳过（不覆盖），任一条失败不阻断其他条——逐条汇总返回给弹窗（批量操作要可复核，
   * 只 toast 会丢掉「哪几条没成功」）。
   */
  const doImportMcpServers = async (servers: McpJsonServer[]): Promise<McpImportOutcome[]> => {
    const existing = new Set(mcps.map((m) => m.id))
    const out: McpImportOutcome[] = []
    for (const s of servers) {
      if (existing.has(s.id)) {
        out.push({ id: s.id, status: 'skipped', message: '同名连接器已存在，未覆盖' })
        continue
      }
      try {
        await createMcpServer(s.body)
        out.push({ id: s.id, status: 'created', message: '已创建' })
      } catch (err) {
        out.push({ id: s.id, status: 'failed', message: writeErrorText(err) })
      }
    }
    await loadAll()
    return out
  }

  // ——— 详情里的工具试调用（弹窗内部用；不再占一个全局弹窗） ———
  const doCallTool = async (
    server: string,
    tool: string,
    argsText: string,
  ): Promise<{ result?: McpToolCallResult; err?: string }> => {
    let args: unknown
    const text = argsText.trim()
    if (text) {
      try {
        args = JSON.parse(text)
      } catch (err) {
        return { err: `arguments 不是合法 JSON：${toErrorMessage(err)}` }
      }
    }
    try {
      const res = await callMcpTool(server, tool, args === undefined ? {} : { arguments: args })
      return { result: res }
    } catch (err) {
      return { err: toErrorMessage(err) }
    }
  }

  return {
    // 数据
    skills,
    mcps,
    tools,
    busy,
    globalMcpRows,
    // 加载
    loadSkills,
    loadMcps,
    loadTools,
    loadAll,
    refresh,
    // 详情
    detail,
    openDetail,
    closeDetail,
    setDetailTab,
    // 表单
    skillForm,
    openSkillForm,
    closeSkillForm,
    mcpForm,
    openMcpForm,
    closeMcpForm,
    formBusy,
    formErr,
    submitSkillForm,
    submitMcpForm,
    openMcpAdvanced,
    // 原文 JSON（高级）
    edit,
    setEdit,
    editBusy,
    editErr,
    submitEdit,
    // 创建入口
    chatOpen,
    setChatOpen,
    uploadOpen,
    setUploadOpen,
    importOpen,
    setImportOpen,
    // 动作
    probing,
    doProbe,
    toggleBusy,
    doToggleSkill,
    doToggleMcp,
    delBusy,
    doDelete,
    doImportSkillPackage,
    doImportMcpServers,
    doCallTool,
    // 展示
    showToast,
  }
}

export type CapabilityData = ReturnType<typeof useCapabilityData>
