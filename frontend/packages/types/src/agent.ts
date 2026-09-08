export type AgentState = 'running' | 'stopped'

export interface Agent {
  id: string
  name: string
  runtime: string
  /** provider 档案 id（P2 注入面）；空 = 默认。 */
  provider?: string
  state: AgentState
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
