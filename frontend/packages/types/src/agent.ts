export type AgentState = 'running' | 'stopped'

export interface Agent {
  id: string
  name: string
  /** provider 档案 id（P2 注入面）；空 = 默认。 */
  provider?: string
  state: AgentState
  /** 默认播种 Agent（前端预选 + 徽标）。 */
  is_default?: boolean
  /** 种子 agent（内置播种/保留前缀）：只读（不可删、runtime 不可改）；由服务端权威判定。 */
  seed?: boolean
}

export interface Provider {
  id: string
  type: string
  model?: string
  /** 密钥只写不读回：仅表示是否已存有密钥。 */
  has_key: boolean
}

export interface ProviderPatch {
  type?: string
  model?: string
  /** 非 nil 即覆盖/清除（空串 = 清除）。 */
  key?: string
}

/** 某 agent 最近一次失败的诊断快照（A3）。 */
export interface AgentLastError {
  at: string
  conversation_id?: string
  code: string
  class: string
  details: string
  stderr_tail?: string[]
}

/** agent 诊断快照（A3：连接态 + stderr 诊断尾巴 + 最近失败）。 */
export interface AgentDiagnostics {
  agent_id: string
  name: string
  state: AgentState
  runtime: string
  runtime_name?: string
  command?: string
  connected: boolean
  pid?: number
  /** 尾巴来源：live（当前子进程实时）| last_error（上次失败采集）| none。 */
  stderr_source: 'live' | 'last_error' | 'none' | string
  stderr_tail: string[]
  last_error?: AgentLastError
  checked_at: string
}
