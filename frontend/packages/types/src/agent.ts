export type AgentState = 'running' | 'stopped'

export interface Agent {
  id: string
  name: string
  runtime: string
  state: AgentState
}
