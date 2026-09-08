import { http } from '../request'
import type {
  Agent,
  Provider,
  ProviderPatch,
  BusEvent,
  CancelConversationResp,
  CreateConversationReq,
  CreateConversationResp,
  ElicitationResp,
  PermissionResp,
  PromptReq,
  ConversationPromptResp,
  ResolveElicitationReq,
  ResolveElicitationResp,
  ResolvePermissionReq,
  ResolvePermissionResp,
  RevokeMemoryReq,
  ConversationDetail,
  ConversationPatch,
  ConversationSearchHit,
  ConversationSummary,
} from '@ark/types'

// ——— forge 控制面 REST（/v1/forge，见 backend/internal/server/handler） ———

export const listAgents = (): Promise<Agent[]> => http.get<Agent[]>('/forge/agents')

/** 列出未归档会话（`archived=true` 列出已归档）。 */
export const listConversations = (archived = false): Promise<ConversationSummary[]> =>
  http.get<ConversationSummary[]>(`/forge/conversations${archived ? '?archived=true' : ''}`)

/** 搜索会话（标题/Agent/用户消息正文；含归档；服务端 updated 降序）。 */
export const conversationSearch = (q: string, limit = 30): Promise<ConversationSearchHit[]> => {
  const s = new URLSearchParams({ q, limit: String(limit) })
  return http.get<ConversationSearchHit[]>(`/forge/conversations/search?${s.toString()}`)
}

/** 单会话详情（元数据 + 挂起权限）。 */
export const getConversation = (id: string): Promise<ConversationDetail> =>
  http.get<ConversationDetail>(`/forge/conversations/${encodeURIComponent(id)}`)

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
  has_more: boolean
}

/** 事件回放（`after` 为开区间游标；默认 200/上限 1000）。 */
export const conversationHistory = (
  id: string,
  opts: { after?: number; limit?: number } = {},
): Promise<ConversationHistoryPage> => {
  const q = new URLSearchParams()
  if (opts.after != null) q.set('after', String(opts.after))
  if (opts.limit != null) q.set('limit', String(opts.limit))
  const s = q.toString()
  return http.get<ConversationHistoryPage>(`/forge/conversations/${encodeURIComponent(id)}/history${s ? `?${s}` : ''}`)
}

// ——— P2 Agent 管理面（definitions 持久化 + 启停 + 热更新 + 删除） ———

/** 注册自定义 agent（校验 runtimes 已注册即「可启动预检」；provider 为档案 id，可选）。 */
export const createAgent = (req: { id: string; name?: string; runtimes: string[]; provider?: string }): Promise<Agent> =>
  http.post<Agent>('/forge/agents', req)

/** 热更新 agent 元数据（name/provider/runtimes 子集；种子 runtimes 只读）。 */
export const patchAgent = (id: string, patch: { name?: string; provider?: string; runtimes?: string[] }): Promise<Agent> =>
  http.patch<Agent>(`/forge/agents/${encodeURIComponent(id)}`, patch)

/** 删除自定义 agent（种子 agent-* 返回只读错误）。 */
export const deleteAgent = (id: string): Promise<{ deleted: string }> =>
  http.delete<{ deleted: string }>(`/forge/agents/${encodeURIComponent(id)}`)

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
