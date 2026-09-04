import { http } from '../request'
import type {
  Agent,
  CancelSessionResp,
  CreateSessionReq,
  CreateSessionResp,
  PromptReq,
  PromptResp,
  Runtime,
  SessionSummary,
} from '@ark/types'

// ——— forge 控制面 REST（/v1/forge，见 backend/internal/server/handler） ———

export const listAgents = (): Promise<Agent[]> => http.get<Agent[]>('/forge/agents')

export const listRuntimes = (): Promise<Runtime[]> => http.get<Runtime[]>('/forge/runtimes')

export const listSessions = (): Promise<SessionSummary[]> => http.get<SessionSummary[]>('/forge/sessions')

/** 新建会话：指定 agent + 首轮 prompt，后台即起真实会话；返回 attach 续传水位。 */
export const createSession = (req: CreateSessionReq): Promise<CreateSessionResp> =>
  http.post<CreateSessionResp>('/forge/sessions', req)

export const promptSession = (id: string, req: PromptReq): Promise<PromptResp> =>
  http.post<PromptResp>(`/forge/sessions/${encodeURIComponent(id)}/prompt`, req)

export const cancelSession = (id: string): Promise<CancelSessionResp> =>
  http.post<CancelSessionResp>(`/forge/sessions/${encodeURIComponent(id)}/cancel`)
