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
  /**
   * 「当前 Agent」（**全局默认单例**）：新建会话与模型目录缺省都用它。
   * 服务端权威计算（回落链：显式选择 > 内置种子 dsh > id 升序首个），在 Agents 页可切换
   * （`POST /agents/:id/default`）。会话一经建立即绑定其 agent，切换不影响既有会话。
   */
  is_default?: boolean
  /** 种子 agent（内置播种/保留前缀）：只读（不可删、runtime 不可改）；由服务端权威判定。 */
  seed?: boolean
  /** 候选 runtime 顺序（ResolveRuntime FirstAvailable 按此试探）；用于判定模型作用域。 */
  runtimes?: string[]
  /** 默认权限策略（只读面；未声明 = 全 ask）。 */
  permission_policy?: PermissionPolicy
  /**
   * 绑定该 agent 的会话数（删除影响面）：删除后这些会话历史仍可读，但**无法继续提问**
   * （归属不可变：平台不自动改绑；用同一个 id 重新注册可复活绑定）。
   */
  conversation_count?: number
}

/** 删除 agent 的结果与影响面（`DELETE /agents/:id`）。 */
export interface AgentDeleteResp {
  deleted: string
  /** 绑定该 agent 的会话总数（含归档）。 */
  affected_conversations: number
  /** 本次被摘除的活跃会话数（内存里仍连着的会话立即失效）。 */
  detached_conversations: number
  /** 被删的是否为「当前 Agent」（是则回落内置默认）。 */
  was_default: boolean
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
  /**
   * 可执行修复步骤（服务端按事实推导：runtime 未注册 / 启动命令不可执行 / provider 档案悬空或无
   * 密钥 / 底座自报的鉴权方式 / 底座安装步骤）。健康快照不带该字段。
   */
  remediation?: string[]
  checked_at: string
}
