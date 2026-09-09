export type AgentState = 'running' | 'stopped'

/** 权限动作（平台拦截面）。 */
export type PermissionAction = 'ask' | 'allow' | 'deny'

/** 原生权限模式（B5；作用于底座进程级开关）。 */
export type NativePermissionMode = 'auto' | 'off' | 'read-only' | 'workspace-write' | 'danger-full-access'

/**
 * 每 agent 的默认权限策略（M2 #7 拦截面 + B5 原生映射）。
 * default/tools 决定 ACP 权限请求的自动裁决；native_mode 决定底座原生权限开关的注入。
 */
export interface PermissionPolicy {
  /** 未匹配到具体工具时的动作（缺省 ask）。 */
  default?: PermissionAction
  /** 工具粒度动作：键 = 工具 title 或 ACP kind。 */
  tools?: Record<string, PermissionAction>
  /** 原生权限模式：auto（按 default 推导）| off | read-only | workspace-write | danger-full-access。 */
  native_mode?: NativePermissionMode
}

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
  /** 候选 runtime 顺序（ResolveRuntime FirstAvailable 按此试探）；用于判定模型作用域。 */
  runtimes?: string[]
  /** 默认权限策略（只读面；未声明 = 全 ask）。 */
  permission_policy?: PermissionPolicy
}

export interface Provider {
  id: string
  type: string
  model?: string
  /** 密钥只写不读回：仅表示是否已存有密钥。 */
  has_key: boolean
  /** 该 provider 档案的可选模型目录（live 优先，兜底快照；表单目录兜底用）。 */
  models?: import('./model').ModelOption[]
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
