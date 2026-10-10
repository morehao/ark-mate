import { http } from '../request'
import type {
  Agent,
  AgentCapabilities,
  AgentDeleteResp,
  Automation,
  AutomationDeleteResp,
  AutomationPreviewResp,
  AutomationRunsResp,
  AutomationRunResp,
  AutomationWriteBody,
  AgentDiagnostics,
  PermissionPolicy,
  Provider,
  ProviderPatch,
  BusEvent,
  CancelConversationResp,
  CapabilityWriteBody,
  CancelQueuedInputResp,
  QueuedInputResp,
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
  McpWriteResp,
  McpDeleteResp,
  ModelApplyResp,
  ModelCatalogResp,
  ModelCreateReq,
  ModelDeleteResp,
  ModelPatchReq,
  ModelRow,
  ModelsResp,
  ProviderCreateReq,
  ProviderDeleteResp,
  ProviderProbeResp,
  Runtime,
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
  SkillWriteResp,
  SkillDeleteResp,
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
} from '@ark-mate/types'

// ——— mate 控制面 REST（/v1/mate，见 backend/internal/router/routes_*.go） ———

export const listAgents = (): Promise<Agent[]> => http.get<Agent[]>('/mate/agents')

// ——— automation（定时任务，M4）：定义的事实源是文件，接口是编辑器与观察窗 ———

/** 列出全部 automation（**含装载失败的那些**：坏定义带 `error` 出现在列表里，不静默消失）。 */
export const listAutomations = (): Promise<Automation[]> => http.get<Automation[]>('/mate/automations')

/** 取一条（不存在 → 40401）。 */
export const getAutomation = (id: string): Promise<Automation> =>
  http.get<Automation>(`/mate/automations/${encodeURIComponent(id)}`)

/** 新建定义（`id` 必填 = 文件名；已存在 → 40901，绝不覆盖）。 */
export const createAutomation = (body: AutomationWriteBody): Promise<Automation> =>
  http.post<Automation>('/mate/automations', body)

/** 覆盖写：**只改 body 里出现过的键**（未出现的保留原值，`null` 删键）。 */
export const updateAutomation = (id: string, body: AutomationWriteBody): Promise<Automation> =>
  http.put<Automation>(`/mate/automations/${encodeURIComponent(id)}`, body)

/** 删除（幂等：本来就不存在 → `deleted:false` 且不报错）。 */
export const deleteAutomation = (id: string): Promise<AutomationDeleteResp> =>
  http.delete<AutomationDeleteResp>(`/mate/automations/${encodeURIComponent(id)}`)

/**
 * 预览未来若干次到期时刻（**不落盘、不建会话**，也不要求定义已存在）。
 * 面板里还没保存的 schedule 也能先看到"到底什么时候跑"。
 */
export const previewAutomation = (body: {
  /** 原文对象（`null` = 删键）：面板把"还没保存的表单"直接发过来，服务端按同一套规则解出。 */
  schedule: Record<string, unknown>
  count?: number
  from?: string
}): Promise<AutomationPreviewResp> => http.post<AutomationPreviewResp>('/mate/automations/preview', body)

/** 运行史（最新 20 条，含 `overlap`/`duplicate`/`skipped`）。 */
export const listAutomationRuns = (id: string): Promise<AutomationRunsResp> =>
  http.get<AutomationRunsResp>(`/mate/automations/${encodeURIComponent(id)}/runs`)

/**
 * 手动触发一次（走同一认领判据：上一次还在跑 → `overlap`；**不推进认领游标**，
 * 手动试跑不该静默吃掉当晚的排期）。失败会同时抛错并已留痕。
 */
export const runAutomationNow = (id: string): Promise<AutomationRunResp> =>
  http.post<AutomationRunResp>(`/mate/automations/${encodeURIComponent(id)}/run`)

/** agent 诊断快照（A3：连接态 + stderr 诊断尾巴 + 最近失败）。 */
export const agentDiagnostics = (id: string): Promise<AgentDiagnostics> =>
  http.get<AgentDiagnostics>(`/mate/agents/${encodeURIComponent(id)}/diagnostics`)

/** 列出未归档会话（`archived=true` 列出已归档；`cwd` 非空 = 只列工作空间精确等于该路径的会话）。 */
export const listConversations = (archived = false, cwd?: string): Promise<ConversationSummary[]> => {
  const s = new URLSearchParams()
  if (archived) s.set('archived', 'true')
  if (cwd) s.set('cwd', cwd)
  const q = s.toString()
  return http.get<ConversationSummary[]>(`/mate/conversations${q ? `?${q}` : ''}`)
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
  return http.get<WorkspaceBrowseResp>(`/mate/workspaces/browse${q ? `?${q}` : ''}`)
}

/**
 * 资料库列表（工作区根下的一级子目录，服务端零新状态）。
 * 未配 `MATE_WORKSPACE_ROOTS` 时返回空数组。
 */
export const workspaceLibraries = (): Promise<WorkspaceLibrary[]> =>
  http.get<WorkspaceLibrary[]>('/mate/workspaces/libraries')

/**
 * 列目录**内容**（文件 + 子目录，带大小/修改时间）——"看资料"面。
 * 与 `browseWorkspaces` 分工：那个用于选工作空间（只列目录），这个用于看内容；
 * 这里的 `entries[].path` **不可**作为建会话 cwd。
 */
export const workspaceFiles = (path: string, hidden = false): Promise<WorkspaceFilesResp> => {
  const s = new URLSearchParams({ path })
  if (hidden) s.set('hidden', 'true')
  return http.get<WorkspaceFilesResp>(`/mate/workspaces/files?${s.toString()}`)
}

/**
 * 工作区根白名单快照（`GET /settings/workspace`）。
 *
 * `configured` 是可编辑的显式名单，`roots` 是含平台代建根的**生效**名单；
 * `writable=false`（被 env/flag 锁定或无配置目录）时面板必须转只读展示，不要给一个必然失败的保存按钮。
 */
export const getWorkspaceSettings = (): Promise<WorkspaceSettings> =>
  http.get<WorkspaceSettings>('/mate/settings/workspace')

/**
 * **全量替换**工作区根白名单（`PUT /settings/workspace`）。
 * 空数组 = 恢复"不限制"；服务端逐项要求绝对路径 / 存在 / 是目录，落盘后对新会话即时生效。
 */
export const updateWorkspaceSettings = (roots: string[]): Promise<WorkspaceSettings> =>
  http.put<WorkspaceSettings>('/mate/settings/workspace', { roots })

/** 最近使用的工作空间（服务端从既有会话派生；排除平台代建的临时空间）。 */
export const recentWorkspaces = (limit = 10): Promise<WorkspaceRecent[]> =>
  http.get<WorkspaceRecent[]>(`/mate/workspaces/recent?limit=${limit}`)

/** 搜索会话（标题/Agent/用户消息正文；含归档；服务端 updated 降序）。 */
export const conversationSearch = (q: string, limit = 30): Promise<ConversationSearchHit[]> => {
  const s = new URLSearchParams({ q, limit: String(limit) })
  return http.get<ConversationSearchHit[]>(`/mate/conversations/search?${s.toString()}`)
}

/** 跨会话消息级检索（M3 ②：返回 seq 锚点，用于跳转定位）。 */
export const messageSearch = (q: string, limit = 30): Promise<MessageSearchResp> => {
  const s = new URLSearchParams({ q, limit: String(limit) })
  return http.get<MessageSearchResp>(`/mate/search/messages?${s.toString()}`)
}

/** 批量导出会话（markdown|text|json；ids 上限 50）。 */
export const exportConversations = (ids: string[], format: 'markdown' | 'text' | 'json' = 'markdown'): Promise<ExportResp> =>
  http.post<ExportResp>('/mate/conversations/export', { ids, format })

/** 单会话详情（元数据 + 挂起权限）。 */
export const getConversation = (id: string): Promise<ConversationDetail> =>
  http.get<ConversationDetail>(`/mate/conversations/${encodeURIComponent(id)}`)

/** 某会话的轮次明细（runs 表读模型；含每轮用量快照。内存模式返回空数组）。 */
export const conversationRuns = (id: string): Promise<{ id: string; runs: ConversationRun[] }> =>
  http.get<{ id: string; runs: ConversationRun[] }>(`/mate/conversations/${encodeURIComponent(id)}/runs`)

/** 更新会话元数据（title/pinned/archived 子集，nil 不更新）。 */
export const updateConversation = (id: string, patch: ConversationPatch): Promise<ConversationSummary> =>
  http.patch<ConversationSummary>(`/mate/conversations/${encodeURIComponent(id)}`, patch)

/** 新建会话：指定 agent + 首轮 prompt，后台即起真实会话；返回 attach 续传水位。 */
export const createConversation = (req: CreateConversationReq): Promise<CreateConversationResp> =>
  http.post<CreateConversationResp>('/mate/conversations', req)

export const promptConversation = (id: string, req: PromptReq): Promise<ConversationPromptResp> =>
  http.post<ConversationPromptResp>(`/mate/conversations/${encodeURIComponent(id)}/prompt`, req)

export const cancelConversation = (id: string): Promise<CancelConversationResp> =>
  http.post<CancelConversationResp>(`/mate/conversations/${encodeURIComponent(id)}/cancel`)

/** 列出会话的挂起权限请求。 */
export const listPendingPermissions = (id: string): Promise<PermissionResp[]> =>
  http.get<PermissionResp[]>(`/mate/conversations/${encodeURIComponent(id)}/permissions`)

/** 对挂起权限请求决策（allow/reject × once/always）。 */
export const resolvePermission = (id: string, req: ResolvePermissionReq): Promise<ResolvePermissionResp> =>
  http.post<ResolvePermissionResp>(`/mate/conversations/${encodeURIComponent(id)}/permission`, req)

/** 撤销会话级权限记忆（始终允许/始终拒绝；title+kind 均空 = 一键撤销全部）。 */
export const revokeMemory = (id: string, req: RevokeMemoryReq = {}): Promise<{ title: string; kind: string }> =>
  http.post<{ title: string; kind: string }>(
    `/mate/conversations/${encodeURIComponent(id)}/permissions/memories/revoke`,
    req,
  )

/** 列出会话的挂起结构化提问（elicitation 表单/URL 卡）。 */
export const listPendingElicitations = (id: string): Promise<ElicitationResp[]> =>
  http.get<ElicitationResp[]>(`/mate/conversations/${encodeURIComponent(id)}/elicitations`)

/** 对挂起结构化提问决策（accept/decline/cancel；accept 可带 content）。 */
export const resolveElicitation = (id: string, req: ResolveElicitationReq): Promise<ResolveElicitationResp> =>
  http.post<ResolveElicitationResp>(`/mate/conversations/${encodeURIComponent(id)}/elicitation`, req)

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
  return http.get<ConversationHistoryPage>(`/mate/conversations/${encodeURIComponent(id)}/history${s ? `?${s}` : ''}`)
}

/**
 * §6：列出某会话**待投递**的排队输入（当前态）。
 *
 * 与事件的分工：`session.input_queued` 等三条事件讲「过程」，本接口给「此刻还在队里」。
 * 冷挂载（刷新/直达）时 WS 只发 snapshot 不回放历史，没有它队列会在刷新后凭空消失。
 */
export const listQueuedInputs = (conversationId: string): Promise<QueuedInputResp[]> =>
  http.get<QueuedInputResp[]>(`/mate/conversations/${encodeURIComponent(conversationId)}/inputs`)

/**
 * §6：撤下一条**尚未投递**的排队输入。
 *
 * 已投递/已作废是台账行、撤不动（回 `deleted:false`，幂等不报错）；真正的收敛以
 * `session.input_cancelled` 事件为准——这里返回的 `deleted` 只说明「确实从队列里拿掉了」。
 */
export const cancelQueuedInput = (conversationId: string, inputId: string): Promise<CancelQueuedInputResp> =>
  http.delete<CancelQueuedInputResp>(
    `/mate/conversations/${encodeURIComponent(conversationId)}/inputs/${encodeURIComponent(inputId)}`,
  )

// ——— P1 能力数据面（Skill/MCP/Tool 目录，REST 列出能力） ———

export const listSkills = (): Promise<Skill[]> => http.get<Skill[]>('/mate/skills')

export const listMcpServers = (): Promise<McpServer[]> => http.get<McpServer[]>('/mate/mcps')

export const listTools = (): Promise<Tool[]> => http.get<Tool[]>('/mate/tools')

/**
 * §13.4 S4：启停一个 skill（写回定义文件的 enabled 并立刻重校物化——启用写文件、禁用撤回副本）。
 *
 * 定义文件写成功即成功；物化失败在 `materialize_errors` 里回（不回滚定义，禁用本身是安全方向）。
 */
export const setSkillEnabled = (id: string, enabled: boolean): Promise<SkillToggleResp> =>
  http.patch<SkillToggleResp>(`/mate/skills/${encodeURIComponent(id)}`, { enabled })

/**
 * §13.4 S2：skill 的物化审计（**只报不修**）。
 *
 * 每个物化目标给出状态与「期望摘要 vs 磁盘实测摘要」。修复不走这里：重校（再次启用/刷新）会
 * 按平台版本覆盖，用户自有目录（user_owned）平台不碰。
 */
export const getSkillMaterialization = (id: string): Promise<SkillMaterializationResp> =>
  http.get<SkillMaterializationResp>(`/mate/skills/${encodeURIComponent(id)}/materialization`)

/**
 * M2 写面：新建一个 skill 定义（body = skill 定义原文，`id` 必填）。
 *
 * **闸门在写盘之前**：正文扫描（critical 直接拒）与定义校验不过 → 40001 `definition_invalid`
 * 且**零字节落盘**；id 已存在 → 40901 `skill_exists`。
 */
export const createSkill = (body: CapabilityWriteBody): Promise<SkillWriteResp> =>
  http.post<SkillWriteResp>('/mate/skills', body)

/**
 * M2 写面：覆盖写一个 skill 定义——**只改请求体里出现过的键**。
 *
 * 未出现的键保留原值（含用户手写的未知键）、`null` 删键、`""`/`[]` 显式清空。目标不存在 →
 * 40401 `skill_not_found`（**不会顺便新建**）；读写窗口内被外部改动 → 40901 `definition_changed`。
 */
export const updateSkill = (id: string, body: CapabilityWriteBody): Promise<SkillWriteResp> =>
  http.put<SkillWriteResp>(`/mate/skills/${encodeURIComponent(id)}`, body)

/** M2 写面：删定义文件 + 撤回已物化副本（幂等：不存在 → `deleted:false` 且不报错）。 */
export const deleteSkill = (id: string): Promise<SkillDeleteResp> =>
  http.delete<SkillDeleteResp>(`/mate/skills/${encodeURIComponent(id)}`)

/** M2 写面：新建一个 MCP server 定义（闸门比 skill 多：transport/绝对路径/引用词法…）。 */
export const createMcpServer = (body: CapabilityWriteBody): Promise<McpWriteResp> =>
  http.post<McpWriteResp>('/mate/mcps', body)

/** M2 写面：覆盖写一个 MCP server 定义（键的出现与否有语义，同 skill）。 */
export const updateMcpServer = (id: string, body: CapabilityWriteBody): Promise<McpWriteResp> =>
  http.put<McpWriteResp>(`/mate/mcps/${encodeURIComponent(id)}`, body)

/** M2 写面：删 MCP server 定义 + 摘除归属它的工具条目（幂等）。 */
export const deleteMcpServer = (id: string): Promise<McpDeleteResp> =>
  http.delete<McpDeleteResp>(`/mate/mcps/${encodeURIComponent(id)}`)

// ——— P2 §8.1/§5.2：Agent 统一能力目录 + MCP live 探测 ———

/** 某 Agent 的统一能力目录（skills 已按候选 runtime 裁剪；MCP 带 enabled/runtimes 标注）。 */
export const getAgentCapabilities = (agentId: string): Promise<AgentCapabilities> =>
  http.get<AgentCapabilities>(`/mate/agents/${encodeURIComponent(agentId)}/capabilities`)

/** 对某 MCP server 做一次 live tools/list 探测（结果写 Tool 目录，返回本轮增删）。 */
export const probeMcpServer = (id: string): Promise<McpProbeResult> =>
  http.post<McpProbeResult>(`/mate/mcps/${encodeURIComponent(id)}/probe`)

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
    `/mate/mcps/${encodeURIComponent(serverId)}/tools/${encodeURIComponent(tool)}/call`,
    req,
  )

/** 实时拉取某 MCP server 的资源列表（B2：resources/list + resources/templates/list）。 */
export const listMcpResources = (id: string): Promise<McpResourcesResult> =>
  http.get<McpResourcesResult>(`/mate/mcps/${encodeURIComponent(id)}/resources`)

/** 读取一个 MCP 资源（B2：resources/read）。 */
export const readMcpResource = (id: string, uri: string): Promise<McpResourceReadResult> =>
  http.post<McpResourceReadResult>(`/mate/mcps/${encodeURIComponent(id)}/resources/read`, { uri })

/** 实时拉取某 MCP server 的提示词模板（B2：prompts/list）。 */
export const listMcpPrompts = (id: string): Promise<McpPromptsResult> =>
  http.get<McpPromptsResult>(`/mate/mcps/${encodeURIComponent(id)}/prompts`)

/** 取用一条 MCP 提示词（B2：prompts/get；参数值必须是字符串）。 */
export const getMcpPrompt = (
  id: string,
  name: string,
  args: Record<string, string> = {},
): Promise<McpPromptGetResult> =>
  http.post<McpPromptGetResult>(`/mate/mcps/${encodeURIComponent(id)}/prompts/${encodeURIComponent(name)}/get`, {
    arguments: args,
  })

// ——— P2 Agent 管理面（definitions 持久化 + 启停 + 热更新 + 删除） ———

/** 注册自定义 agent（校验 runtimes 已注册即「可启动预检」；provider 为档案 id，可选）。 */
export const createAgent = (req: {
  id: string
  name?: string
  runtimes: string[]
  provider?: string
  /** MCP 白名单：省略 = 未声明（全量）；`[]` = 禁用全部；非空 = 白名单。 */
  mcps?: string[]
}): Promise<Agent> => http.post<Agent>('/mate/agents', req)

/**
 * 热更新 agent 元数据（name/provider/runtimes/mcps 子集；种子 runtimes 只读）。
 *
 * `mcps` 三态：**省略键** = 不改；`null` = 清空白名单（回落"未声明" = 全量）；`[]` = 禁用全部；
 * 非空 = 白名单。改白名单会让该 agent 的**空闲**会话句柄作废——下一轮按当前定义重建并按新
 * 白名单注入（有在途轮次或未应答权限/提问的会话顺延到下一次重建）。所以类型是
 * `string[] | null`（不能只写 `string[]`，否则"清空"表达不出来）。
 */
export const patchAgent = (
  id: string,
  patch: {
    name?: string
    provider?: string
    runtimes?: string[]
    mcps?: string[] | null
    permission_policy?: PermissionPolicy
  },
): Promise<Agent> => http.patch<Agent>(`/mate/agents/${encodeURIComponent(id)}`, patch)

/** 删除自定义 agent（种子 agent-* 返回只读错误）：返回影响面（绑定会话数 / 摘除的活跃会话数 / 是否当前 Agent）。 */
export const deleteAgent = (id: string): Promise<AgentDeleteResp> =>
  http.delete<AgentDeleteResp>(`/mate/agents/${encodeURIComponent(id)}`)

/**
 * 切换「当前 Agent」（全局默认单例：新建会话、模型目录缺省都取它；幂等）。
 * 返回切换后的 agent（`is_default=true`）；选择落 `<AgentDir>/default.json`，重启后保持。
 */
export const setDefaultAgent = (id: string): Promise<Agent> =>
  http.post<Agent>(`/mate/agents/${encodeURIComponent(id)}/default`)

/** 启动 agent 对应 runtime 连接（真实进程；已运行幂等）。 */
export const startAgent = (id: string): Promise<{ id: string; state: string }> =>
  http.post<{ id: string; state: string }>(`/mate/agents/${encodeURIComponent(id)}/start`)

/** 停止 agent 对应 runtime 进程（有在途 turn 报 agent_busy）。 */
export const stopAgent = (id: string): Promise<{ id: string; state: string }> =>
  http.post<{ id: string; state: string }>(`/mate/agents/${encodeURIComponent(id)}/stop`)

// ——— P2 provider 注入面（密钥只写不读回） ———

export const listProviders = (): Promise<Provider[]> => http.get<Provider[]>('/mate/providers')

/**
 * 底座列表（含每个底座认的运行时参数 schema）。
 *
 * 它是「每个底座的运行时参数」那张表单的**唯一数据源**：加一个底座不需要改前端。
 */
export const listRuntimes = (): Promise<Runtime[]> => http.get<Runtime[]>('/mate/runtimes')

/** 新建一条供应商凭据（id 已存在 → 40901 `provider_exists`，绝不覆盖）。 */
export const createProvider = (req: ProviderCreateReq): Promise<Provider> =>
  http.post<Provider>('/mate/providers', req)

/**
 * 更新一条**已存在**的供应商凭据（v11 起 PATCH 不再 upsert；不存在 → 40401 `provider_not_found`）。
 * `key` 传空串清除密钥。响应带 `effect`（生效面回执：改 `type`/`key` 才是"下一轮生效"）。
 */
export const upsertProvider = (id: string, patch: ProviderPatch): Promise<Provider> =>
  http.patch<Provider>(`/mate/providers/${encodeURIComponent(id)}`, patch)

/** 删除一条凭据（幂等；被 agent 或模型条目引用 → 40901 `provider_in_use` + 引用名单）。 */
export const deleteProvider = (id: string): Promise<ProviderDeleteResp> =>
  http.delete<ProviderDeleteResp>(`/mate/providers/${encodeURIComponent(id)}`)

/**
 * 连接自检两段：**本地段无条件且零出网**；上游段仅当 `upstream: true`。
 * 上游段会把用户密钥发给第三方，所以绝不能被页面加载顺手触发。
 */
export const probeProvider = (id: string, upstream = false): Promise<ProviderProbeResp> =>
  http.post<ProviderProbeResp>(`/mate/providers/${encodeURIComponent(id)}/probe`, { upstream })

// ——— 模型面（平台清单 + 运行时事实的对账 + 清单写面） ———

/**
 * 模型读面：`{runtime, declared, advertised, diff, config}`（平台清单 + 运行时事实 + 差集 + 物化状态）。
 * `?runtime=` 缺省取该 agent 的首选 runtime；`?refresh=1` 跳过 5 分钟目录缓存重新取样。
 */
export const listModels = (
  agentId?: string,
  runtimeId?: string,
  refresh = false,
): Promise<ModelsResp | ModelCatalogResp> => {
  const p = new URLSearchParams()
  if (agentId) p.set('agent', agentId)
  if (runtimeId) p.set('runtime', runtimeId)
  if (refresh) p.set('refresh', '1')
  const qs = p.toString()
  return http.get<ModelsResp>(`/mate/models${qs ? `?${qs}` : ''}`)
}

/** 新建一条模型条目（同 `(runtime, provider_id, model_id)` 已存在 → 40901 `model_exists`）。 */
export const createModel = (req: ModelCreateReq): Promise<ModelRow> =>
  http.post<ModelRow>('/mate/models', req)

/** 更新一条模型条目（指针更新；`is_default:true` 同时清掉同 runtime 的旧默认）。 */
export const patchModel = (id: string, patch: ModelPatchReq): Promise<ModelRow> =>
  http.patch<ModelRow>(`/mate/models/${encodeURIComponent(id)}`, patch)

/** 删除一条模型条目（幂等；它是该 runtime 的默认模型 → 40501 `model_is_default`）。 */
export const deleteModel = (id: string): Promise<ModelDeleteResp> =>
  http.delete<ModelDeleteResp>(`/mate/models/${encodeURIComponent(id)}`)

/**
 * 强制重新物化某 runtime（缺省全部已声明落点的 runtime）的模型配置文件。
 *
 * 成功路径**只回 `pending:true`**（此刻拿不到 advertised）——界面不许把它说成"已生效"。
 */
export const applyModels = (runtime?: string): Promise<ModelApplyResp> =>
  http.post<ModelApplyResp>('/mate/models/apply', runtime ? { runtime } : {})

// ——— M3 用量统计 ———

/** 用量汇总（GET /usage/summary）：from/to 半开区间、agent_id、group_by=agent|day。 */
export const usageSummary = (q: UsageQuery = {}): Promise<UsageSummary> => {
  const p = new URLSearchParams()
  if (q.from) p.set('from', q.from)
  if (q.to) p.set('to', q.to)
  if (q.agent_id) p.set('agent_id', q.agent_id)
  if (q.group_by) p.set('group_by', q.group_by)
  const qs = p.toString()
  return http.get<UsageSummary>(`/mate/usage/summary${qs ? `?${qs}` : ''}`)
}
