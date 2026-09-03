import type {
  Agent,
  CreateSessionResp,
  Envelope,
  PromptResp,
  Runtime,
  SessionSummary,
} from "./types"

const BASE = "/v1/forge"

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response
  try {
    res = await fetch(BASE + path, {
      headers: { "content-type": "application/json" },
      ...init,
    })
  } catch (err) {
    throw { code: -1, msg: `网络错误: ${(err as Error).message}` }
  }
  let env: Envelope<T>
  try {
    env = (await res.json()) as Envelope<T>
  } catch {
    throw { code: -1, msg: `非 JSON 响应 (HTTP ${res.status})` }
  }
  if (env.code !== 0) {
    const e: { code: number; msg: string } = {
      code: env.code,
      msg: env.msg || `错误码 ${env.code}`,
    }
    throw e
  }
  return env.data
}

export function listAgents(): Promise<Agent[]> {
  return request<Agent[]>("/agents")
}

export function listRuntimes(): Promise<Runtime[]> {
  return request<Runtime[]>("/runtimes")
}

export function listSessions(): Promise<SessionSummary[]> {
  return request<SessionSummary[]>("/sessions")
}

export function createSession(agentId: string, prompt: string): Promise<CreateSessionResp> {
  return request<CreateSessionResp>("/sessions", {
    method: "POST",
    body: JSON.stringify({ agent_id: agentId, prompt }),
  })
}

export function promptSession(id: string, prompt: string): Promise<PromptResp> {
  return request<PromptResp>(`/sessions/${encodeURIComponent(id)}/prompt`, {
    method: "POST",
    body: JSON.stringify({ prompt }),
  })
}

export function cancelSession(id: string): Promise<{ id: string; status: string }> {
  return request<{ id: string; status: string }>(`/sessions/${encodeURIComponent(id)}/cancel`, {
    method: "POST",
  })
}

/** 把任意异常归一为 ApiError（fetch 抛的是非标准 Error，这里显式拍平）。 */
export function asApiError(err: unknown): { code: number; msg: string } {
  if (err && typeof err === "object" && "msg" in err && "code" in err) {
    return err as { code: number; msg: string }
  }
  return { code: -1, msg: (err as Error).message ?? String(err) }
}
