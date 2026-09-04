export type SessionStatus = 'in_progress' | 'completed' | 'cancelled' | 'failed'

export interface SessionSummary {
  id: string
  agent_id: string
  runtime: string
  status: SessionStatus
}

export interface CreateSessionReq {
  agent_id: string
  prompt: string
  cwd?: string
}

export interface CreateSessionResp {
  id: string
  agent_id: string
  runtime: string
  status: SessionStatus
  event_seq: number // 已发布事件水位：WS attach 续传起点
}

export interface PromptReq {
  prompt: string
}

export interface PromptResp {
  id: string
  status: SessionStatus
  event_seq: number
}

export interface CancelSessionResp {
  id: string
  status: SessionStatus
}
