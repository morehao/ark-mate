import type { Capabilities } from '@ark-mate/desktop-contract'
import { http } from '../request'
import type {
  App,
  AppInstallResp,
  AppUninstallResp,
  AppToggleResp,
  Automation,
  AutomationDeleteResp,
  AutomationPreviewResp,
  AutomationRunResp,
  AutomationRunsResp,
  AutomationWriteBody,
  BusEvent,
  CancelSessionResp,
  CancelQueuedInputResp,
  CapabilityWriteBody,
  SessionDetail,
  SessionPatch,
  SessionCapabilitiesPatch,
  SessionPromptResp,
  SessionRun,
  SessionSearchHit,
  SessionSummary,
  CreateSessionReq,
  Attachment,
  CreateSessionResp,
  ExportResp,
  McpDeleteResp,
  McpProbeResult,
  Expert,
  ExpertDeleteResp,
  ExpertToggleResp,
  ExpertWriteBody,
  ExpertWriteResp,
  McpPromptGetResult,
  McpPromptsResult,
  McpResourceReadResult,
  McpResourcesResult,
  McpServer,
  McpToolCallReq,
  McpToolCallResult,
  McpWriteResp,
  MarketApp,
  MessageSearchResp,
  MetaResp,
  ModelCatalogResp,
  ModelCreateReq,
  ModelDeleteResp,
  ModelPatchReq,
  ModelRow,
  ModelSyncResp,
  ModelsResp,
  PromptReq,
  Provider,
  ProviderCreateReq,
  ProviderDeleteResp,
  ProviderPatch,
  ProviderProbeResp,
  QueuedInputResp,
  QueuedInputEditReq,
  ResolveElicitationReq,
  ResolveElicitationResp,
  ResolvePermissionReq,
  ResolvePermissionResp,
  ComposerSkill,
  RevokeMemoryReq,
  RuntimeDiagnostics,
  Skill,
  SkillDeleteResp,
  SkillMaterializationResp,
  SkillToggleResp,
  SkillWriteResp,
  Tool,
  UsageQuery,
  UsageSummary,
  Workspace,
  WorkspaceBrowseResp,
  WorkspaceDeleteResp,
  WorkspacePickResp,
  WorkspaceDirCreateResp,
  WorkspaceFilesResp,
  WorkspaceListResp,
  WorkspaceMoveResp,
} from '@ark-mate/types'

// ——— mate 控制面 REST（/v1/mate，见 backend/internal/router/routes_*.go） ———

// ——— automation（定时任务，M4）：定义的事实源是文件，接口是编辑器与观察窗 ———

/** 列出全部 automation（**含装载失败的那些**：坏定义带 `error` 出现在列表里，不静默消失）。 */
export const listAutomations = (): Promise<Automation[]> => http.get<Automation[]>('/mate/automations')

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

/**
 * 底座诊断快照（A3：连接态 + stderr 诊断尾巴 + 最近失败）。
 *
 * v19 前这是 `/agents/:id/diagnostics`（按 agent 取），现在底座只有一个、也没有 id 可传——
 * 所以它是一条**无参**读面。响应形状逐字段不变，只少了 agent 身份（见 `RuntimeDiagnostics`）。
 */
export const getRuntimeDiagnostics = (): Promise<RuntimeDiagnostics> =>
  http.get<RuntimeDiagnostics>('/mate/runtime/diagnostics')

/** 列出未归档会话（`archived=true` 列出已归档；`cwd` 非空 = 只列工作空间精确等于该路径的会话）。 */
export const listSessions = (archived = false, cwd?: string, workspaceId?: string): Promise<SessionSummary[]> => {
  const s = new URLSearchParams()
  if (archived) s.set('archived', 'true')
  if (cwd) s.set('cwd', cwd)
  if (workspaceId) s.set('workspace_id', workspaceId)
  const q = s.toString()
  return http.get<SessionSummary[]>(`/mate/sessions${q ? `?${q}` : ''}`)
}

/**
 * 浏览某绝对路径下的子目录（Web 无原生目录选择器，故由服务端提供只读枚举）。
 * `path` 缺省 = 服务进程家目录；只列目录，返回的 `entries[].path` 可直接作为建会话 cwd。
 *
 * v14.3：响应另带 `crumbs`（文件系统根 → 当前目录的祖先链，面板据此画面包屑，**不要**自己按
 * 路径字符串切分）与 `truncated`（单层超上限时置位）。
 */
export const browseWorkspaces = (path?: string, hidden = false): Promise<WorkspaceBrowseResp> => {
  const s = new URLSearchParams()
  if (path) s.set('path', path)
  if (hidden) s.set('hidden', 'true')
  const q = s.toString()
  return http.get<WorkspaceBrowseResp>(`/mate/workspaces/browse${q ? `?${q}` : ''}`)
}

/**
 * 在某个**已存在**的目录下建一个子目录（目录浏览面的「新建文件夹」，v14.3）。
 *
 * `name` 必须是单段（服务端拒绝含 `/`、`\` 以及 `.` / `..`）；同名目录已存在 → 40901。
 * 返回的是**规范化后**的新目录绝对路径，可直接当 cwd 或加入白名单——前端不要自己拼。
 */
export const createWorkspaceDir = (parent: string, name: string): Promise<WorkspaceDirCreateResp> =>
  http.post<WorkspaceDirCreateResp>('/mate/workspaces/dirs', { parent, name })

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
 * 抬一次**宿主原生**目录选择器（`POST /workspaces/pick-directory`）。
 *
 * 面板由**核心进程**抬起（macOS osascript / Linux zenity·kdialog），所以本机浏览器入口与桌面窗口
 * 都能用到它。返回 `{cancelled:true}` 表示用户取消——**不是错误**，调用方静默收手。
 * 宿主抬不起来时服务端回 `40501 native_pick_unsupported`（调用方应据能力声明提前分流，
 * 这个错是兜底）。
 */
export const pickDirectoryHost = (): Promise<WorkspacePickResp> =>
  http.post<WorkspacePickResp>('/mate/workspaces/pick-directory')

/**
 * 能力声明（`GET /capabilities`）：两个入口**同构**（id 集合与顺序一致），只有 `available` 不同。
 *
 * 形状取自 `@ark-mate/desktop-contract`（那份契约是声明面的**唯一真源**，前端类型不另抄一份）。
 * 前端据此决定"这个部署有没有宿主原生选目录器"（`native-dialog`）——判定在宿主侧，不在客户端
 * （本机浏览器也能用系统面板）。
 */
export const getCapabilities = (): Promise<Capabilities> => http.get<Capabilities>('/mate/capabilities')

/**
 * 装配事实（`GET /meta`）：版本、资产、数据目录，以及**构建期固定的底座身份**（`runtime{}`，v19）。
 *
 * 界面上凡是要说"当前用的是哪个底座/哪个版本"的地方都读这里——不要从别处推：
 * 底座不由用户选，所以这份事实只有服务端答得出（本机装的那份版本是另一回事，见 `payloads[]`）。
 */
export const getMeta = (): Promise<MetaResp> => http.get<MetaResp>('/mate/meta')

/** 搜索会话（标题/底座/用户消息正文；含归档；服务端 updated 降序）。 */
export const sessionSearch = (q: string, limit = 30): Promise<SessionSearchHit[]> => {
  const s = new URLSearchParams({ q, limit: String(limit) })
  return http.get<SessionSearchHit[]>(`/mate/sessions/search?${s.toString()}`)
}

/** 跨会话消息级检索（M3 ②：返回 seq 锚点，用于跳转定位）。 */
export const messageSearch = (q: string, limit = 30): Promise<MessageSearchResp> => {
  const s = new URLSearchParams({ q, limit: String(limit) })
  return http.get<MessageSearchResp>(`/mate/search/messages?${s.toString()}`)
}

/** 批量导出会话（markdown|text|json；ids 上限 50）。 */
export const exportSessions = (ids: string[], format: 'markdown' | 'text' | 'json' = 'markdown'): Promise<ExportResp> =>
  http.post<ExportResp>('/mate/sessions/export', { ids, format })

/** 单会话详情（元数据 + 挂起权限）。 */
export const getSession = (id: string): Promise<SessionDetail> =>
  http.get<SessionDetail>(`/mate/sessions/${encodeURIComponent(id)}`)

/** 某会话的轮次明细（runs 表读模型；含每轮用量快照。内存模式返回空数组）。 */
export const sessionRuns = (id: string): Promise<{ id: string; runs: SessionRun[] }> =>
  http.get<{ id: string; runs: SessionRun[] }>(`/mate/sessions/${encodeURIComponent(id)}/runs`)

/** 更新会话元数据（title/pinned/archived 子集，nil 不更新）。 */
export const updateSession = (id: string, patch: SessionPatch): Promise<SessionSummary> =>
  http.patch<SessionSummary>(`/mate/sessions/${encodeURIComponent(id)}`, patch)

/**
 * 会话内改能力（技能/MCP/专家）：逐 kind 三态补丁，**下一轮边界生效**（忙时顺延，不打断当前轮）。
 *
 * 响应是改后的会话快照：`capabilities` 是**物化后**的选定（专家推荐已展开成具体 id 列表），
 * `capabilities.warnings` 带被跳过的推荐与本次编辑的边界提示（换专家不重注入人格等）。
 */
export const patchSessionCapabilities = (
  id: string,
  patch: SessionCapabilitiesPatch,
): Promise<SessionSummary> =>
  http.patch<SessionSummary>(`/mate/sessions/${encodeURIComponent(id)}/capabilities`, patch)

/** 新建会话：首轮 prompt + 可选工作空间/模型，后台即起真实会话；返回 attach 续传水位。 */
export const createSession = (req: CreateSessionReq): Promise<CreateSessionResp> =>
  http.post<CreateSessionResp>('/mate/sessions', req)

export const promptSession = (id: string, req: PromptReq): Promise<SessionPromptResp> =>
  http.post<SessionPromptResp>(`/mate/sessions/${encodeURIComponent(id)}/prompt`, req)

/** composer `/` 技能选择器的内置技能目录（v4；无会话态只读）。 */
export const listComposerSkills = (): Promise<{ items: ComposerSkill[] }> =>
  http.get<{ items: ComposerSkill[] }>('/mate/composer/skills')

export const cancelSession = (id: string): Promise<CancelSessionResp> =>
  http.post<CancelSessionResp>(`/mate/sessions/${encodeURIComponent(id)}/cancel`)

/** 对挂起权限请求决策（allow/reject × once/always）。 */
export const resolvePermission = (id: string, req: ResolvePermissionReq): Promise<ResolvePermissionResp> =>
  http.post<ResolvePermissionResp>(`/mate/sessions/${encodeURIComponent(id)}/permission`, req)

/** 撤销会话级权限记忆（始终允许/始终拒绝；title+kind 均空 = 一键撤销全部）。 */
export const revokeMemory = (id: string, req: RevokeMemoryReq = {}): Promise<{ title: string; kind: string }> =>
  http.post<{ title: string; kind: string }>(
    `/mate/sessions/${encodeURIComponent(id)}/permissions/memories/revoke`,
    req,
  )

/** 对挂起结构化提问决策（accept/decline/cancel；accept 可带 content）。 */
export const resolveElicitation = (id: string, req: ResolveElicitationReq): Promise<ResolveElicitationResp> =>
  http.post<ResolveElicitationResp>(`/mate/sessions/${encodeURIComponent(id)}/elicitation`, req)

/** 会话历史页（事件按 seq 升序）。 */
export interface SessionHistoryPage {
  id: string
  events: BusEvent[]
  /** 沿当前分页方向是否还有更多：after 分页 = 后面还有；before 分页 = 更早还有。 */
  has_more: boolean
  /** 本页首尾事件 seq（**空页为 -1**）；继续向前/向后翻页的游标。 */
  first_seq: number
  last_seq: number
}

/**
 * 事件回放。`after` 向后（seq > after）、`before` 向前（seq < before，仍升序返回，
 * 用于「加载更早」）——两者互斥（后端 40001）；默认 200/上限 1000。
 * sqlite 模式读 events 表（可一直翻到会话第一条）；内存模式回放 eventbus。
 *
 * **游标口径（v21）**：`seq` 是**会话内**位次、0 是合法位次（`session.created`），
 * 于是"没给游标"与"给了 0"是两件事：
 *   - 都不给 = 从该会话第 0 条起的第一页；
 *   - `after=0` = 第 0 条**之后**（跳过它）；`before=0` = 第 0 条之前（空页 = 已到最早）；
 *   - `before` 只接受非负整数，所以"取最新一页"要由调用方从已知水位推（`watermark + 1`）。
 */
export const sessionHistory = (
  id: string,
  opts: { after?: number; before?: number; limit?: number } = {},
): Promise<SessionHistoryPage> => {
  const q = new URLSearchParams()
  if (opts.after != null) q.set('after', String(opts.after))
  if (opts.before != null) q.set('before', String(opts.before))
  if (opts.limit != null) q.set('limit', String(opts.limit))
  const s = q.toString()
  return http.get<SessionHistoryPage>(`/mate/sessions/${encodeURIComponent(id)}/history${s ? `?${s}` : ''}`)
}

/**
 * §6：列出某会话**待投递**的排队输入（当前态）。
 *
 * 与事件的分工：`session.input_queued` 等三条事件讲「过程」，本接口给「此刻还在队里」。
 * 冷挂载（刷新/直达）时 WS 只发 snapshot 不回放历史，没有它队列会在刷新后凭空消失。
 */
export const listQueuedInputs = (sessionId: string): Promise<QueuedInputResp[]> =>
  http.get<QueuedInputResp[]>(`/mate/sessions/${encodeURIComponent(sessionId)}/inputs`)

/**
 * §6 / ADR-10：**重排**一条待投递输入（DOM insertBefore 语义，`beforeId` 缺省 = 追加到末尾）。
 * 回**重排后的完整队列**——客户端的本地顺序靠它对齐（同 `moveWorkspace` 的手法）。
 */
export const moveQueuedInput = (sessionId: string, inputId: string, beforeId?: string): Promise<QueuedInputResp[]> =>
  http.post<QueuedInputResp[]>(
    `/mate/sessions/${encodeURIComponent(sessionId)}/inputs/${encodeURIComponent(inputId)}/move`,
    { before_id: beforeId },
  )

/**
 * §6 / ADR-10：**立即发送**——让这一条成为下一轮（跳过 FIFO）。
 *
 * 会话忙时它只是把它排到队首（当前轮结束时收尾路径会先投它）；空闲时服务端会立刻起一轮。
 */
export const sendQueuedInputNow = async (sessionId: string, inputId: string): Promise<void> => {
  await http.post(
    `/mate/sessions/${encodeURIComponent(sessionId)}/inputs/${encodeURIComponent(inputId)}/send-now`,
    {},
  )
}

/**
 * §6 / ADR-10：**取出编辑**——把队项取回草稿并作废，返回正文（前端填进输入框）。
 *
 * 服务端会发一条 `input_cancelled{reason:'user_edit'}`；队列那一行的消失**只**由它驱动
 * （`user_edit` 与 `user_cancel` 是两个值，界面含义不同）。
 */
export const popQueuedInput = (sessionId: string, inputId: string): Promise<{ text: string }> =>
  http.post<{ text: string }>(
    `/mate/sessions/${encodeURIComponent(sessionId)}/inputs/${encodeURIComponent(inputId)}/pop`,
    {},
  )

/**
 * v4 行内编辑一条待投递输入：改正文/附件，**不出队**（区别于撤下 = 离队）。
 * 返回服务端重排后的当前队列（就地替换的权威读面）。
 */
export const editQueuedInput = (
  sessionId: string,
  inputId: string,
  req: QueuedInputEditReq,
): Promise<QueuedInputResp[]> =>
  http.patch<QueuedInputResp[]>(
    `/mate/sessions/${encodeURIComponent(sessionId)}/inputs/${encodeURIComponent(inputId)}`,
    req,
  )

/**
 * §6：撤下一条**尚未投递**的排队输入。
 *
 * 已投递/已作废是台账行、撤不动（回 `deleted:false`，幂等不报错）；真正的收敛以
 * `session.input_cancelled` 事件为准——这里返回的 `deleted` 只说明「确实从队列里拿掉了」。
 */
export const cancelQueuedInput = (sessionId: string, inputId: string): Promise<CancelQueuedInputResp> =>
  http.delete<CancelQueuedInputResp>(
    `/mate/sessions/${encodeURIComponent(sessionId)}/inputs/${encodeURIComponent(inputId)}`,
  )

// ——— P1 能力数据面（Skill/MCP/Tool 目录，REST 列出能力） ———

export const listSkills = (): Promise<Skill[]> => http.get<Skill[]>('/mate/skills')

export const listMcpServers = (): Promise<McpServer[]> => http.get<McpServer[]>('/mate/mcps')

// ——— 专家定义面（P0；会话级能力预设） ———

/** 专家列表（含禁用；列表**不返回 instructions 正文**，只带 has_instructions）。 */
export const listExperts = (): Promise<Expert[]> => http.get<Expert[]>('/mate/experts')

/** 专家详情（返回 instructions 全文，供预览/编辑）。 */
export const getExpert = (id: string): Promise<Expert> =>
  http.get<Expert>(`/mate/experts/${encodeURIComponent(id)}`)

/** 新建专家（body = 专家定义，id 必填）。 */
export const createExpert = (body: ExpertWriteBody): Promise<ExpertWriteResp> =>
  http.post<ExpertWriteResp>('/mate/experts', body)

/** 覆盖写专家（只改 body 里出现的键）。 */
export const updateExpert = (id: string, body: ExpertWriteBody): Promise<ExpertWriteResp> =>
  http.put<ExpertWriteResp>(`/mate/experts/${encodeURIComponent(id)}`, body)

/** 删除专家（幂等）。 */
export const deleteExpert = (id: string): Promise<ExpertDeleteResp> =>
  http.delete<ExpertDeleteResp>(`/mate/experts/${encodeURIComponent(id)}`)

/** 启停专家。 */
export const setExpertEnabled = (id: string, enabled: boolean): Promise<ExpertToggleResp> =>
  http.patch<ExpertToggleResp>(`/mate/experts/${encodeURIComponent(id)}`, { enabled })

/**
 * 列出全部应用定义（含禁用；每项带派生 `has_page`，带 `page` 的 app 会回 `page` 全量，
 * 前端据此把页面入口补登进统一注册表 `console/appRegistry.ts`）。
 */
export const listApps = (): Promise<App[]> => http.get<App[]>('/mate/apps')

/** 读单个应用定义详情（含 settings_schema / page 全量）。 */
export const getApp = (id: string): Promise<App> =>
  http.get<App>(`/mate/apps/${encodeURIComponent(id)}`)

/**
 * 应用市场清单（GET /market）：每条带派生的装态
 * （installed/enabled/updatable/installable/state，由后端 store+appsRoot 算出，不落库）。
 */
export const listMarket = (): Promise<MarketApp[]> => http.get<MarketApp[]>('/mate/market')

/** 强制刷新市场缓存（POST /market/refresh）：下次 listMarket 会重拉远端。 */
export const refreshMarket = (): Promise<void> => http.post<void>('/mate/market/refresh')

/**
 * 从本地来源目录安装一个应用包（POST /apps/install）。
 *
 * `dir` 为来源包目录（含 manifest.json 的包根）；`enable=false` 时装完仅建定义、不起进程，
 * 启用是显式动作（市场页一键安装默认 false）。
 */
export const installApp = (dir: string, enable = false): Promise<AppInstallResp> =>
  http.post<AppInstallResp>('/mate/apps/install', { dir, enable })

/** 卸载一个应用（DELETE /apps/:id/uninstall）：先停运行期+清包目录，再删定义；幂等。 */
export const uninstallApp = (id: string): Promise<AppUninstallResp> =>
  http.delete<AppUninstallResp>(`/mate/apps/${encodeURIComponent(id)}/uninstall`)

/**
 * 更新一个已安装应用（POST /apps/:id/upgrade）。
 *
 * 本期为 C8 版本治理接线点，后端可能返「更新能力接线中」的明确错误（不假装成功）。
 */
export const upgradeApp = (id: string): Promise<AppInstallResp> =>
  http.post<AppInstallResp>(`/mate/apps/${encodeURIComponent(id)}/upgrade`)

/** 启停一个应用定义（PATCH /apps/:id）：启用会起 host 子进程，停用只暂不使用、定义保留。 */
export const setAppEnabled = (id: string, enabled: boolean): Promise<AppToggleResp> =>
  http.patch<AppToggleResp>(`/mate/apps/${encodeURIComponent(id)}`, { enabled })

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

/**
 * D1 技能包导入（`POST /mate/skills/import`，multipart/form-data）。
 *
 * 包格式 v1：只接受 `.zip`（≤20 MiB），根目录或唯一一层目录内有 `SKILL.md`，且**只有它**
 * （额外的脚本/资源直接拒绝，避免「导入成功但附件没落盘」的静默丢失）。解析/校验失败 →
 * 40001 `definition_invalid` 且零字节落盘；同名 id → 40901 `skill_exists`（不覆盖）。
 */
export const importSkillPackage = (file: File): Promise<SkillWriteResp> => {
  const form = new FormData()
  form.append('file', file, file.name)
  return http.post<SkillWriteResp>('/mate/skills/import', form)
}

// ——— 文件上传（P0′ 收口：全局唯一收字节端点 POST /files）———
//
// 上传两步式：① `POST /mate/files` 收字节（ginupload 提供，需 content_hash）→ file_id；
// ② 业务模块建索引（附件 `POST /mate/attachments {file_id}` / 资料库 `POST /mate/materials/nodes`）。
// 字节落 <MATE_HOME>/attachments（filestore 内容寻址去重），不写工作区。

/** 计算文件 SHA-256（十六进制）。小文件直算；大文件分片路径留后续（当前先直算）。 */
export const sha256Hex = async (file: File): Promise<string> => {
  const buf = await file.arrayBuffer()
  const digest = await crypto.subtle.digest('SHA-256', buf)
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
}

/** `POST /files` 的响应（file_id = core_file_upload.id）。 */
export interface UploadedFile {
  file_id: string
  name: string
  mime_type: string
  status: string
}

/** 唯一收字节端点：算 hash → `POST /mate/files` → file_id。 */
export const uploadFile = async (file: File): Promise<UploadedFile> => {
  const contentHash = await sha256Hex(file)
  const form = new FormData()
  form.append('content_hash', contentHash)
  form.append('file', file, file.name)
  return http.postForm<UploadedFile>('/mate/files', form)
}

/** 从已上传文件（file_id）建附件索引（`POST /mate/attachments {file_id, name?}`）。 */
export const registerAttachment = (fileId: string, name?: string): Promise<Attachment> =>
  http.post<Attachment>('/mate/attachments', { file_id: fileId, name })

/** 上传一个附件（两步式：`POST /files` 收字节 → `POST /attachments` 建索引）。 */
export const uploadAttachment = async (file: File): Promise<Attachment> => {
  const up = await uploadFile(file)
  return registerAttachment(up.file_id, file.name)
}

/** M2 写面：新建一个 MCP server 定义（闸门比 skill 多：transport/绝对路径/引用词法…）。 */
export const createMcpServer = (body: CapabilityWriteBody): Promise<McpWriteResp> =>
  http.post<McpWriteResp>('/mate/mcps', body)

/** M2 写面：覆盖写一个 MCP server 定义（键的出现与否有语义，同 skill）。 */
export const updateMcpServer = (id: string, body: CapabilityWriteBody): Promise<McpWriteResp> =>
  http.put<McpWriteResp>(`/mate/mcps/${encodeURIComponent(id)}`, body)

/** M2 写面：删 MCP server 定义 + 摘除归属它的工具条目（幂等）。 */
export const deleteMcpServer = (id: string): Promise<McpDeleteResp> =>
  http.delete<McpDeleteResp>(`/mate/mcps/${encodeURIComponent(id)}`)

// ——— P2 §8.1/§5.2：MCP live 探测 ———
//
// v19 撤掉了「某 Agent 视角的统一能力目录」（`GET /agents/:id/capabilities`）：那张面回答的是
// "按这个 agent 裁剪之后还剩什么"，而 agent 并入构建期固定的 runtime 后退化成 `GET /library` 的抄本。
// 能力面折回既有的无参读面（`/library` + `/skills`·`/mcps`·`/tools`），这里不新增平行入口。

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

// v19：agent 管理面（注册 / 启停 / 热更新 / 删除 / 切默认）整体撤掉——
// 底座由构建期固定，用户没有任何选择面；那些动作的接收方（agents 表、`/agents/*`）已不存在。
// 进程事实改由 `runtimeDiagnostics()` 一条无参读面回答。

// ——— 供应商面（密钥只写不读回） ———

export const listProviders = (): Promise<Provider[]> => http.get<Provider[]>('/mate/providers')

/** 新建一条供应商（id 已存在 → 40901 `provider_exists`，绝不覆盖）。 */
export const createProvider = (req: ProviderCreateReq): Promise<Provider> =>
  http.post<Provider>('/mate/providers', req)

/**
 * 更新一条**已存在**的供应商（PATCH，不 upsert；不存在 → 40401 `provider_not_found`）。
 * `api_key` 传空串清除密钥。响应带 `effect`（生效面回执：改端点/密钥才是"下一轮生效"）。
 */
export const patchProvider = (id: string, patch: ProviderPatch): Promise<Provider> =>
  http.patch<Provider>(`/mate/providers/${encodeURIComponent(id)}`, patch)

/** 删除一条供应商（幂等；被模型引用 → 40901 `provider_in_use` + 引用名单）。 */
export const deleteProvider = (id: string): Promise<ProviderDeleteResp> =>
  http.delete<ProviderDeleteResp>(`/mate/providers/${encodeURIComponent(id)}`)

/**
 * 连接自检两段：**本地段无条件且零出网**；上游段仅当 `upstream: true`。
 * 上游段会把用户密钥发给第三方，所以绝不能被页面加载顺手触发。
 */
export const probeProvider = (id: string, upstream = false): Promise<ProviderProbeResp> =>
  http.post<ProviderProbeResp>(`/mate/providers/${encodeURIComponent(id)}/probe`, { upstream })

// ——— 模型面（平台清单 + 聚合生效结论 + 清单写面；运行时目录单列） ———

/**
 * 平台模型清单读面（`GET /models`）：`{models, default_model, effective, effective_at, sync[], note?}`。
 *
 * `?runtime=` 收窄同步明细的取样面（不传 = 全部已注册）；设置 · 模型 那一节**只消费前四个字段**，
 * `sync[]` 是机器面、不得渲染（ADR-22）。
 */
export const listModels = (runtimeId?: string): Promise<ModelsResp> => {
  const p = new URLSearchParams()
  if (runtimeId) p.set('runtime', runtimeId)
  const qs = p.toString()
  return http.get<ModelsResp>(`/mate/models${qs ? `?${qs}` : ''}`)
}

/**
 * 运行时目录读面（`GET /models/catalog`）：运行时自报的会话配置项投影。
 *
 * 它是**另一个屏幕**（会话级模型选择器）的数据源，`value` 必须原样回传——与平台清单不是一回事。
 *
 * v19 起**无参**：原来要带 `?agent=<id>` 指定按哪个 agent 取样，而底座只有一个，
 * 取样对象没有第二个候选。
 */
export const listModelCatalog = (): Promise<ModelCatalogResp> => http.get<ModelCatalogResp>('/mate/models/catalog')

/** 新建一条模型条目（同 `(provider_id, model)` 已存在 → 40901 `model_exists`）。 */
export const createModel = (req: ModelCreateReq): Promise<ModelRow> =>
  http.post<ModelRow>('/mate/models', req)

/** 更新一条模型条目（指针更新；`is_default:true` 同时清掉旧的唯一默认）。 */
export const patchModel = (id: string, patch: ModelPatchReq): Promise<ModelRow> =>
  http.patch<ModelRow>(`/mate/models/${encodeURIComponent(id)}`, patch)

/** 删除一条模型条目（幂等；若它正好是默认模型则连带清空默认）。 */
export const deleteModel = (id: string): Promise<ModelDeleteResp> =>
  http.delete<ModelDeleteResp>(`/mate/models/${encodeURIComponent(id)}`)

/**
 * 重新同步模型清单（`POST /models/sync`；`runtime` 空 = 全部已注册）。
 *
 * 同步失败 → 50001 `resource_sync_failed`。成功路径**只回逐 runtime 的机器面结论**——
 * 界面要说"已提交同步"，不许把它说成"已生效"（生效结论读 `GET /models` 的聚合字段）。
 */
export const syncModels = (runtime?: string): Promise<ModelSyncResp> =>
  http.post<ModelSyncResp>('/mate/models/sync', runtime ? { runtime } : {})

// ——— M3 用量统计 ———

/** 用量汇总（GET /usage/summary）：from/to 半开区间、group_by=day、tz_offset 决定日历日（v19 起没有 agent 维度）。 */
export const usageSummary = (q: UsageQuery = {}): Promise<UsageSummary> => {
  const p = new URLSearchParams()
  if (q.from) p.set('from', q.from)
  if (q.to) p.set('to', q.to)
  if (q.tz_offset != null) p.set('tz_offset', String(q.tz_offset))
  if (q.group_by) p.set('group_by', q.group_by)
  const qs = p.toString()
  return http.get<UsageSummary>(`/mate/usage/summary${qs ? `?${qs}` : ''}`)
}

// ——— v14 工作空间**登记表**（选择器与左栏分组的事实源）———
//
// 与上面 `/workspaces/browse|libraries|files|recent` 的分工：那几条是**派生的只读视图**
// （从磁盘或既有会话算出来的），这一组是**登记项**本身——有 id、可改写、可删除。

/** 列出全部工作空间登记（按展示顺序；目录状态/会话数读时现算）。 */
export const listWorkspaces = (): Promise<WorkspaceListResp> =>
  http.get<WorkspaceListResp>('/mate/workspaces')

/**
 * 登记一个目录为工作空间。
 *
 * 默认 **get-or-create**（同路径已登记 → 返回既有那一行），所以「选中后落库」这一步天然幂等，
 * 不会因为用户重复点一下就得到一个错。`strict: true` 时才把重复当冲突（40901）。
 */
export const createWorkspace = (path: string, title?: string, strict = false): Promise<Workspace> =>
  http.post<Workspace>('/mate/workspaces', { path, title, strict })

/**
 * **新建**一个工作空间（ADR-2 的 `name` 模式）：在 `create_root` 下建一个同名目录并登记，**一步**。
 *
 * 与 `createWorkspace(path)` 的区别不是"参数不同"，而是**用户意图不同**：这个是从零造一个目录，
 * 那个是登记一个已经存在的目录。服务端两个都走同一条路径校验（`ValidateWorkspaceName` /
 * `ValidateWorkspace`），所以"能新建"与"能选已有"不会分叉。
 *
 * 服务端错误码：名字非法 40001（`invalid_workspace_name`）、同名目录已存在 40901
 * （`workspace_exists`，**绝不静默复用**）、未配置落盘根 40001（`workspace_create_root_unset`）。
 */
export const createNamedWorkspace = (name: string, title?: string): Promise<Workspace> =>
  http.post<Workspace>('/mate/workspaces', { name, title })

/** 改写展示名（**不改身份**：id 与 path 都不动）。空串 = 清空，界面按末级目录名渲染。 */
export const renameWorkspace = (id: string, title: string): Promise<Workspace> =>
  http.patch<Workspace>(`/mate/workspaces/${encodeURIComponent(id)}`, { title })

/**
 * 改工作空间的**默认权限 / 默认能力**（读面见 `Workspace.default_permission_mode` /
 * `default_capability`）。
 *
 * 后端是**补丁语义**：只带你要改的键——传了才动，没传的键原样保留。因此这里把 patch 里
 * **未定义（undefined）** 的键过滤掉，`undefined` 视为「不改」；`null` 与空串按后端语义
 * （null = 不改该字段、空串 = 清掉）原样透传，不在前端另造三态。
 *
 * 与 `renameWorkspace` / `relocateWorkspace` 是**同一端点不同键**：那两条发 `{title}` / `{path}`，
 * 这条发 `{default_permission_mode?, default_capability?}`——各自独立，别合并。
 */
export const patchWorkspaceDefaults = (
  id: string,
  patch: { default_permission_mode?: string | null; default_capability?: string | null },
): Promise<Workspace> => {
  const body: Record<string, string | null> = {}
  if (patch.default_permission_mode !== undefined) body.default_permission_mode = patch.default_permission_mode
  if (patch.default_capability !== undefined) body.default_capability = patch.default_capability
  return http.patch<Workspace>(`/mate/workspaces/${encodeURIComponent(id)}`, body)
}

/**
 * **改登记路径**（W4）：把这条工作空间指向另一个目录。
 *
 * **id 不变、既有会话一个字节都不动**——它们的 `cwd` 是执行事实（ACP cwd + 沙箱边界），
 * transcript 也落在那个目录里。所以这个动作的语义是"以后在这个工作空间里新建的会话落在哪"，
 * 不是"把已有的会话搬过去"。
 *
 * 服务端按与建会话**同一条**校验处理（绝对 / 存在 / 是目录 / 规范化）；目标目录已被另一条登记
 * 占着 → 40901（**不**静默复用，那会让"改成功"与"那个目录已经是别人的"分不清楚）；
 * 默认工作空间的路径不可改 → 40001（`workspace_default_immutable`）。
 */
export const relocateWorkspace = (id: string, path: string): Promise<Workspace> =>
  http.patch<Workspace>(`/mate/workspaces/${encodeURIComponent(id)}`, { path })

/**
 * 删除一条登记**而保留数据**：目录、文件、会话历史一概不动，其会话回到默认工作空间。
 * 返回的 `released_sessions` 是给界面说清"这 N 个会话回到了默认工作空间"的事实。
 */
export const deleteWorkspace = (id: string): Promise<WorkspaceDeleteResp> =>
  http.delete<WorkspaceDeleteResp>(`/mate/workspaces/${encodeURIComponent(id)}`)

/** 重排：`beforeId` 缺省 = 追加到尾部；返回**完整**顺序（客户端据此对齐本地顺序）。 */
export const moveWorkspace = (id: string, beforeId?: string): Promise<WorkspaceMoveResp> =>
  http.post<WorkspaceMoveResp>(`/mate/workspaces/${encodeURIComponent(id)}/move`, { before_id: beforeId })
