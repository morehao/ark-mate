import { http } from '../request'
import type {
  Agent,
  AgentCapabilities,
  AgentDeleteResp,
  AgentDiagnostics,
  PermissionPolicy,
  Provider,
  ProviderPatch,
  BusEvent,
  CancelConversationResp,
  CreateConversationReq,
  CreateConversationResp,
  ElicitationResp,
  McpServer,
  McpProbeResult,
  McpToolCallReq,
  McpToolCallResult,
  McpResourcesResult,
  McpResourceReadResult,
  McpPromptsResult,
  McpPromptGetResult,
  ModelCatalogResp,
  PermissionResp,
  PromptReq,
  ConversationPromptResp,
  ResolveElicitationReq,
  ResolveElicitationResp,
  ResolvePermissionReq,
  ResolvePermissionResp,
  RevokeMemoryReq,
  Skill,
  SkillMaterializationResp,
  SkillToggleResp,
  Tool,
  ConversationDetail,
  ConversationPatch,
  ConversationRun,
  ConversationSearchHit,
  ConversationSummary,
  MessageSearchResp,
  ExportResp,
  UsageQuery,
  UsageSummary,
  WorkspaceBrowseResp,
  WorkspaceFilesResp,
  WorkspaceLibrary,
  WorkspaceRecent,
  WorkspaceSettings,
} from '@ark/types'

// ——— forge 控制面 REST（/v1/forge，见 backend/internal/router/routes_*.go） ———

export const listAgents = (): Promise<Agent[]> => http.get<Agent[]>('/forge/agents')

/** agent 诊断快照（A3：连接态 + stderr 诊断尾巴 + 最近失败）。 */
export const agentDiagnostics = (id: string): Promise<AgentDiagnostics> =>
  http.get<AgentDiagnostics>(`/forge/agents/${encodeURIComponent(id)}/diagnostics`)

/** 列出未归档会话（`archived=true` 列出已归档；`cwd` 非空 = 只列工作空间精确等于该路径的会话）。 */
export const listConversations = (archived = false, cwd?: string): Promise<ConversationSummary[]> => {
  const s = new URLSearchParams()
  if (archived) s.set('archived', 'true')
  if (cwd) s.set('cwd', cwd)
  const q = s.toString()
  return http.get<ConversationSummary[]>(`/forge/conversations${q ? `?${q}` : ''}`)
}

/**
 * 浏览某绝对路径下的子目录（Web 无原生目录选择器，故由服务端提供只读枚举）。
 * `path` 缺省 = 服务进程家目录；只列目录，返回的 `entries[].path` 可直接作为建会话 cwd。
 */
export const browseWorkspaces = (path?: string, hidden = false): Promise<WorkspaceBrowseResp> => {
  const s = new URLSearchParams()
  if (path) s.set('path', path)
  if (hidden) s.set('hidden', 'true')
  const q = s.toString()
  return http.get<WorkspaceBrowseResp>(`/forge/workspaces/browse${q ? `?${q}` : ''}`)
}

/**
 * 资料库列表（工作区根下的一级子目录，服务端零新状态）。
 * 未配 `FORGE_WORKSPACE_ROOTS` 时返回空数组。
 */
export const workspaceLibraries = (): Promise<WorkspaceLibrary[]> =>
  http.get<WorkspaceLibrary[]>('/forge/workspaces/libraries')

/**
 * 列目录**内容**（文件 + 子目录，带大小/修改时间）——"看资料"面。
 * 与 `browseWorkspaces` 分工：那个用于选工作空间（只列目录），这个用于看内容；
 * 这里的 `entries[].path` **不可**作为建会话 cwd。
 */
export const workspaceFiles = (path: string, hidden = false): Promise<WorkspaceFilesResp> => {
  const s = new URLSearchParams({ path })
  if (hidden) s.set('hidden', 'true')
  return http.get<WorkspaceFilesResp>(`/forge/workspaces/files?${s.toString()}`)
}

/**
 * 工作区根白名单快照（`GET /settings/workspace`）。
 *
 * `configured` 是可编辑的显式名单，`roots` 是含平台代建根的**生效**名单；
 * `writable=false`（被 env/flag 锁定或无配置目录）时面板必须转只读展示，不要给一个必然失败的保存按钮。
 */
export const getWorkspaceSettings = (): Promise<WorkspaceSettings> =>
  http.get<WorkspaceSettings>('/forge/settings/workspace')

/**
 * **全量替换**工作区根白名单（`PUT /settings/workspace`）。
 * 空数组 = 恢复"不限制"；服务端逐项要求绝对路径 / 存在 / 是目录，落盘后对新会话即时生效。
 */
export const updateWorkspaceSettings = (roots: string[]): Promise<WorkspaceSettings> =>
  http.put<WorkspaceSettings>('/forge/settings/workspace', { roots })

/** 最近使用的工作空间（服务端从既有会话派生；排除平台代建的临时空间）。 */
export const recentWorkspaces = (limit = 10): Promise<WorkspaceRecent[]> =>
  http.get<WorkspaceRecent[]>(`/forge/workspaces/recent?limit=${limit}`)

/** 搜索会话（标题/Agent/用户消息正文；含归档；服务端 updated 降序）。 */
export const conversationSearch = (q: string, limit = 30): Promise<ConversationSearchHit[]> => {
  const s = new URLSearchParams({ q, limit: String(limit) })
  return http.get<ConversationSearchHit[]>(`/forge/conversations/search?${s.toString()}`)
}

/** 跨会话消息级检索（M3 ②：返回 seq 锚点，用于跳转定位）。 */
export const messageSearch = (q: string, limit = 30): Promise<MessageSearchResp> => {
  const s = new URLSearchParams({ q, limit: String(limit) })
  return http.get<MessageSearchResp>(`/forge/search/messages?${s.toString()}`)
}

/** 批量导出会话（markdown|text|json；ids 上限 50）。 */
export const exportConversations = (ids: string[], format: 'markdown' | 'text' | 'json' = 'markdown'): Promise<ExportResp> =>
  http.post<ExportResp>('/forge/conversations/export', { ids, format })

/** 单会话详情（元数据 + 挂起权限）。 */
export const getConversation = (id: string): Promise<ConversationDetail> =>
  http.get<ConversationDetail>(`/forge/conversations/${encodeURIComponent(id)}`)

/** 某会话的轮次明细（runs 表读模型；含每轮用量快照。内存模式返回空数组）。 */
export const conversationRuns = (id: string): Promise<{ id: string; runs: ConversationRun[] }> =>
  http.get<{ id: string; runs: ConversationRun[] }>(`/forge/conversations/${encodeURIComponent(id)}/runs`)

/** 更新会话元数据（title/pinned/archived 子集，nil 不更新）。 */
export const updateConversation = (id: string, patch: ConversationPatch): Promise<ConversationSummary> =>
  http.patch<ConversationSummary>(`/forge/conversations/${encodeURIComponent(id)}`, patch)

/** 新建会话：指定 agent + 首轮 prompt，后台即起真实会话；返回 attach 续传水位。 */
export const createConversation = (req: CreateConversationReq): Promise<CreateConversationResp> =>
  http.post<CreateConversationResp>('/forge/conversations', req)

export const promptConversation = (id: string, req: PromptReq): Promise<ConversationPromptResp> =>
  http.post<ConversationPromptResp>(`/forge/conversations/${encodeURIComponent(id)}/prompt`, req)

export const cancelConversation = (id: string): Promise<CancelConversationResp> =>
  http.post<CancelConversationResp>(`/forge/conversations/${encodeURIComponent(id)}/cancel`)

/** 列出会话的挂起权限请求。 */
export const listPendingPermissions = (id: string): Promise<PermissionResp[]> =>
  http.get<PermissionResp[]>(`/forge/conversations/${encodeURIComponent(id)}/permissions`)

/** 对挂起权限请求决策（allow/reject × once/always）。 */
export const resolvePermission = (id: string, req: ResolvePermissionReq): Promise<ResolvePermissionResp> =>
  http.post<ResolvePermissionResp>(`/forge/conversations/${encodeURIComponent(id)}/permission`, req)

/** 撤销会话级权限记忆（始终允许/始终拒绝；title+kind 均空 = 一键撤销全部）。 */
export const revokeMemory = (id: string, req: RevokeMemoryReq = {}): Promise<{ title: string; kind: string }> =>
  http.post<{ title: string; kind: string }>(
    `/forge/conversations/${encodeURIComponent(id)}/permissions/memories/revoke`,
    req,
  )

/** 列出会话的挂起结构化提问（elicitation 表单/URL 卡）。 */
export const listPendingElicitations = (id: string): Promise<ElicitationResp[]> =>
  http.get<ElicitationResp[]>(`/forge/conversations/${encodeURIComponent(id)}/elicitations`)

/** 对挂起结构化提问决策（accept/decline/cancel；accept 可带 content）。 */
export const resolveElicitation = (id: string, req: ResolveElicitationReq): Promise<ResolveElicitationResp> =>
  http.post<ResolveElicitationResp>(`/forge/conversations/${encodeURIComponent(id)}/elicitation`, req)

/** 会话历史页（事件按 seq 升序）。 */
export interface ConversationHistoryPage {
  id: string
  events: BusEvent[]
  /** 沿当前分页方向是否还有更多：after 分页 = 后面还有；before 分页 = 更早还有。 */
  has_more: boolean
  /** 本页首尾事件 seq（空页为 0）；继续向前/向后翻页的游标。 */
  first_seq: number
  last_seq: number
}

/**
 * 事件回放。`after` 向后（seq > after）、`before` 向前（seq < before，仍升序返回，
 * 用于「加载更早」）——两者互斥（后端 40001）；默认 200/上限 1000。
 * sqlite 模式读 events 表（可一直翻到会话第一条）；内存模式回放 eventbus。
 */
export const conversationHistory = (
  id: string,
  opts: { after?: number; before?: number; limit?: number } = {},
): Promise<ConversationHistoryPage> => {
  const q = new URLSearchParams()
  if (opts.after != null) q.set('after', String(opts.after))
  if (opts.before != null) q.set('before', String(opts.before))
  if (opts.limit != null) q.set('limit', String(opts.limit))
  const s = q.toString()
  return http.get<ConversationHistoryPage>(`/forge/conversations/${encodeURIComponent(id)}/history${s ? `?${s}` : ''}`)
}

// ——— P1 能力数据面（Skill/MCP/Tool 目录，REST 列出能力） ———

export const listSkills = (): Promise<Skill[]> => http.get<Skill[]>('/forge/skills')

export const listMcpServers = (): Promise<McpServer[]> => http.get<McpServer[]>('/forge/mcps')

export const listTools = (): Promise<Tool[]> => http.get<Tool[]>('/forge/tools')

/**
 * §13.4 S4：启停一个 skill（写回定义文件的 enabled 并立刻重校物化——启用写文件、禁用撤回副本）。
 *
 * 定义文件写成功即成功；物化失败在 `materialize_errors` 里回（不回滚定义，禁用本身是安全方向）。
 */
export const setSkillEnabled = (id: string, enabled: boolean): Promise<SkillToggleResp> =>
  http.patch<SkillToggleResp>(`/forge/skills/${encodeURIComponent(id)}`, { enabled })

/**
 * §13.4 S2：skill 的物化审计（**只报不修**）。
 *
 * 每个物化目标给出状态与「期望摘要 vs 磁盘实测摘要」。修复不走这里：重校（再次启用/刷新）会
 * 按平台版本覆盖，用户自有目录（user_owned）平台不碰。
 */
export const getSkillMaterialization = (id: string): Promise<SkillMaterializationResp> =>
  http.get<SkillMaterializationResp>(`/forge/skills/${encodeURIComponent(id)}/materialization`)

// ——— P2 §8.1/§5.2：Agent 统一能力目录 + MCP live 探测 ———

/** 某 Agent 的统一能力目录（skills 已按候选 runtime 裁剪；MCP 带 enabled/runtimes 标注）。 */
export const getAgentCapabilities = (agentId: string): Promise<AgentCapabilities> =>
  http.get<AgentCapabilities>(`/forge/agents/${encodeURIComponent(agentId)}/capabilities`)

/** 对某 MCP server 做一次 live tools/list 探测（结果写 Tool 目录，返回本轮增删）。 */
export const probeMcpServer = (id: string): Promise<McpProbeResult> =>
  http.post<McpProbeResult>(`/forge/mcps/${encodeURIComponent(id)}/probe`)

/**
 * 调用某 MCP server 的工具（B1：tools/call）。
 *
 * 工具级失败（服务端 result.isError=true）不是平台错误：返回结果的 `is_error` 为 true 且
 * 请求本身成功；只有传输/协议/超时才抛 HttpError。
 */
export const callMcpTool = (
  serverId: string,
  tool: string,
  req: McpToolCallReq = {},
): Promise<McpToolCallResult> =>
  http.post<McpToolCallResult>(
    `/forge/mcps/${encodeURIComponent(serverId)}/tools/${encodeURIComponent(tool)}/call`,
    req,
  )

/** 实时拉取某 MCP server 的资源列表（B2：resources/list + resources/templates/list）。 */
export const listMcpResources = (id: string): Promise<McpResourcesResult> =>
  http.get<McpResourcesResult>(`/forge/mcps/${encodeURIComponent(id)}/resources`)

/** 读取一个 MCP 资源（B2：resources/read）。 */
export const readMcpResource = (id: string, uri: string): Promise<McpResourceReadResult> =>
  http.post<McpResourceReadResult>(`/forge/mcps/${encodeURIComponent(id)}/resources/read`, { uri })

/** 实时拉取某 MCP server 的提示词模板（B2：prompts/list）。 */
export const listMcpPrompts = (id: string): Promise<McpPromptsResult> =>
  http.get<McpPromptsResult>(`/forge/mcps/${encodeURIComponent(id)}/prompts`)

/** 取用一条 MCP 提示词（B2：prompts/get；参数值必须是字符串）。 */
export const getMcpPrompt = (
  id: string,
  name: string,
  args: Record<string, string> = {},
): Promise<McpPromptGetResult> =>
  http.post<McpPromptGetResult>(`/forge/mcps/${encodeURIComponent(id)}/prompts/${encodeURIComponent(name)}/get`, {
    arguments: args,
  })

// ——— P2 Agent 管理面（definitions 持久化 + 启停 + 热更新 + 删除） ———

/** 注册自定义 agent（校验 runtimes 已注册即「可启动预检」；provider 为档案 id，可选）。 */
export const createAgent = (req: { id: string; name?: string; runtimes: string[]; provider?: string }): Promise<Agent> =>
  http.post<Agent>('/forge/agents', req)

/** 热更新 agent 元数据（name/provider/runtimes 子集；种子 runtimes 只读）。 */
export const patchAgent = (
  id: string,
  patch: { name?: string; provider?: string; runtimes?: string[]; permission_policy?: PermissionPolicy },
): Promise<Agent> =>
  http.patch<Agent>(`/forge/agents/${encodeURIComponent(id)}`, patch)

/** 删除自定义 agent（种子 agent-* 返回只读错误）：返回影响面（绑定会话数 / 摘除的活跃会话数 / 是否当前 Agent）。 */
export const deleteAgent = (id: string): Promise<AgentDeleteResp> =>
  http.delete<AgentDeleteResp>(`/forge/agents/${encodeURIComponent(id)}`)

/**
 * 切换「当前 Agent」（全局默认单例：新建会话、模型目录缺省都取它；幂等）。
 * 返回切换后的 agent（`is_default=true`）；选择落 `<AgentDir>/default.json`，重启后保持。
 */
export const setDefaultAgent = (id: string): Promise<Agent> =>
  http.post<Agent>(`/forge/agents/${encodeURIComponent(id)}/default`)

/** 启动 agent 对应 runtime 连接（真实进程；已运行幂等）。 */
export const startAgent = (id: string): Promise<{ id: string; state: string }> =>
  http.post<{ id: string; state: string }>(`/forge/agents/${encodeURIComponent(id)}/start`)

/** 停止 agent 对应 runtime 进程（有在途 turn 报 agent_busy）。 */
export const stopAgent = (id: string): Promise<{ id: string; state: string }> =>
  http.post<{ id: string; state: string }>(`/forge/agents/${encodeURIComponent(id)}/stop`)

// ——— P2 provider 注入面（密钥只写不读回） ———

export const listProviders = (): Promise<Provider[]> => http.get<Provider[]>('/forge/providers')

/** upsert provider 档案；key 传空串清除密钥。 */
export const upsertProvider = (id: string, patch: ProviderPatch): Promise<Provider> =>
  http.patch<Provider>(`/forge/providers/${encodeURIComponent(id)}`, patch)

// ——— 模型目录（运行时派生：ACP session config options） ———

/**
 * 模型目录：`?agent=<id>` 指定 agent（缺省取后端默认 agent）。
 * 选项全量来自运行时自报，`source` 标注取样来源（session|resume|cache|none）。
 */
export const listModels = (agentId?: string): Promise<ModelCatalogResp> =>
  http.get<ModelCatalogResp>(`/forge/models${agentId ? `?agent=${encodeURIComponent(agentId)}` : ''}`)

// ——— M3 用量统计 ———

/** 用量汇总（GET /usage/summary）：from/to 半开区间、agent_id、group_by=agent|day。 */
export const usageSummary = (q: UsageQuery = {}): Promise<UsageSummary> => {
  const p = new URLSearchParams()
  if (q.from) p.set('from', q.from)
  if (q.to) p.set('to', q.to)
  if (q.agent_id) p.set('agent_id', q.agent_id)
  if (q.group_by) p.set('group_by', q.group_by)
  const qs = p.toString()
  return http.get<UsageSummary>(`/forge/usage/summary${qs ? `?${qs}` : ''}`)
}
