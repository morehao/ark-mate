import { http } from '../request'
import type {
  Agent,
  CancelSessionResp,
  CreateSessionReq,
  CreateSessionResp,
  PermissionResp,
  PromptReq,
  PromptResp,
  ResolvePermissionReq,
  ResolvePermissionResp,
  Runtime,
  SessionDetail,
  SessionPatch,
  SessionSummary,
} from '@ark/types'

// ——— forge 控制面 REST（/v1/forge，见 backend/internal/server/handler） ———

export const listAgents = (): Promise<Agent[]> => http.get<Agent[]>('/forge/agents')

export const listRuntimes = (): Promise<Runtime[]> => http.get<Runtime[]>('/forge/runtimes')

/** 列出未归档会话（`archived=true` 列出已归档）。 */
export const listSessions = (archived = false): Promise<SessionSummary[]> =>
  http.get<SessionSummary[]>(`/forge/sessions${archived ? '?archived=true' : ''}`)

/** 单会话详情（元数据 + 挂起权限）。 */
export const getSession = (id: string): Promise<SessionDetail> =>
  http.get<SessionDetail>(`/forge/sessions/${encodeURIComponent(id)}`)

/** 更新会话元数据（title/pinned/archived 子集，nil 不更新）。 */
export const updateSession = (id: string, patch: SessionPatch): Promise<SessionSummary> =>
  http.patch<SessionSummary>(`/forge/sessions/${encodeURIComponent(id)}`, patch)

/** 新建会话：指定 agent + 首轮 prompt，后台即起真实会话；返回 attach 续传水位。 */
export const createSession = (req: CreateSessionReq): Promise<CreateSessionResp> =>
  http.post<CreateSessionResp>('/forge/sessions', req)

export const promptSession = (id: string, req: PromptReq): Promise<PromptResp> =>
  http.post<PromptResp>(`/forge/sessions/${encodeURIComponent(id)}/prompt`, req)

export const cancelSession = (id: string): Promise<CancelSessionResp> =>
  http.post<CancelSessionResp>(`/forge/sessions/${encodeURIComponent(id)}/cancel`)

/** 列出会话的挂起权限请求。 */
export const listPendingPermissions = (id: string): Promise<PermissionResp[]> =>
  http.get<PermissionResp[]>(`/forge/sessions/${encodeURIComponent(id)}/permissions`)

/** 对挂起权限请求决策（allow/reject × once/always）。 */
export const resolvePermission = (id: string, req: ResolvePermissionReq): Promise<ResolvePermissionResp> =>
  http.post<ResolvePermissionResp>(`/forge/sessions/${encodeURIComponent(id)}/permission`, req)
