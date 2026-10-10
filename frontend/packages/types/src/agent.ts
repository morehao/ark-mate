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
  /**
   * MCP server id 白名单（M6-8 起可写，`PATCH /agents/:id {"mcps": …}`）。三态在 wire 上必须分得清：
   * 字段缺失 = 未声明（全量按 runtime transport 过滤）；`[]` = **本 agent 禁用 MCP 注入**；
   * 非空 = 白名单。写面用 `null` 表达"清空回未声明"——面板只回显，不提供编辑（见 README）。
   */
  mcps?: string[]
  /** 默认权限策略（只读面；未声明 = 全 ask）。 */
  permission_policy?: PermissionPolicy
  /**
   * 绑定该 agent 的会话数（删除影响面）：删除后这些会话历史仍可读，但**无法继续提问**
   * （归属不可变：平台不自动改绑；用同一个 id 重新注册可复活绑定）。
   */
  conversation_count?: number
  /**
   * 该 agent 在各 runtime 上的**派生只读**默认模型：`{"<runtime>": "<model_id>"}`。
   *
   * 没有写入方（`POST/PATCH /agents` 显式拒绝它）。v12 起来源是「设置 · 模型」里那个**唯一的**
   * 默认模型摊到各 runtime 上的投影，所以这里只读呈现 + 一句"去设置 · 模型 改"的跳转。
   */
  default_model?: Record<string, string>
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

/**
 * 一条供应商（`GET/POST /providers`）：**一个 OpenAI 兼容端点 + 一把密钥**（v12）。
 *
 * 与 v11 的差别（形状是资源本身，不再是"逐底座参数档案"）：
 *  - `type`（自由串）→ `protocol`（枚举，本期仅 `openai`）+ `base_url`；
 *  - `projections` 删除——底座运行时参数不再暴露给用户；
 *  - `delivery` 删除——那是静态说明表，不是这条资源的写入方。
 */
export interface Provider {
  id: string
  /** 展示名（空 = 前端回落显示 id）。 */
  name?: string
  /** 上游协议（本期恒为 `openai`）。 */
  protocol: string
  /** OpenAI 兼容端点（空 = 缺端点，界面要显式标出，且它不会下发给任何底座）。 */
  base_url: string
  /** 密钥只写不读回：仅表示是否已存有密钥。 */
  has_key: boolean
  /** 载体版本（sqlite = 行 revision；memory = 0，文件载体没有版本号）。 */
  revision: number
  updated_at?: string
  note?: string
  /** 挂靠在本供应商下的模型条数（读取时现算的派生值）。 */
  model_count: number
  /** 开会话之前就能看见的问题清单（空 = 本地段全部通过；可直接展示）。 */
  problems: string[]
  /** 只读派生的引用面（哪些 agent 在用它、这些 agent 声明了哪些 runtime）。 */
  refs: ProviderRefs
  /** 逐**已注册** runtime 的可达性（刻意不给合并结论）。 */
  applicability?: Applicability[]
  /** 写面回执（只在写面响应里出现）。 */
  effect?: ProviderEffect
}

/** provider 的引用面（只读派生，无写入方）。 */
export interface ProviderRefs {
  agents: string[]
  runtimes: string[]
}

/** 逐 runtime 的可达性（形状与 `/library` 的 applicability 保持一致）。 */
export interface Applicability {
  runtime: string
  ok: boolean
  status: string
  reason: string
}

/**
 * 供应商写面的生效面回执：只有端到端凭据面变了才说"下一轮生效"。
 *
 * `channel` 是本供应商实际走的凭据交付面（`native_doc` = 底座私有凭据文档；`env` = 子进程环境变量）。
 * 用户面**不得**渲染这两个值（A13/A14）；它只用于写面回执的一句话说明。
 */
export interface ProviderEffect {
  /** native_doc | env。 */
  channel: string
  /** next_turn（下一次轮次边界换代际）| none（只改了不驱动行为的字段，如 name/note）。 */
  effect: string
  applies_to_agents: string[]
  affected_conversations: number
  deferred: number
  note?: string
}

/** `PATCH /providers/:id` 请求体（指针语义，省略 = 不改）。 */
export interface ProviderPatch {
  name?: string
  /** 本期仅 `openai`。 */
  protocol?: string
  base_url?: string
  /** 省略 = 不动；空串 = 清除（删引用 + 删密文）；非空 = 覆盖。密钥只写不读回。 */
  api_key?: string
  note?: string
}

/** `POST /providers` 请求体（id 必填且过词法校验 `^[A-Za-z0-9._-]{1,64}$`）。 */
export interface ProviderCreateReq {
  id: string
  name?: string
  protocol: string
  base_url?: string
  api_key?: string
  note?: string
}

export interface ProviderDeleteResp {
  deleted: boolean
  /** 被拒时的 agent 引用名单（先解开这些引用再删）。 */
  referenced_by_agents?: string[]
  /** 被拒时的模型引用名单（先删掉这些模型再删）。 */
  referenced_by_models?: string[]
  /** 行已删但密文残留（如实回带，doctor 会列出孤儿密文）。 */
  vault_ciphertext_left?: string
}

export interface ProviderProbeResp {
  id: string
  upstream: boolean
  /** "没有 fail"（warn/skip/unknown 都不算失败）。 */
  ok: boolean
  checks: ProviderCheck[]
}

export interface ProviderCheck {
  /** exists | protocol | base_url | secret | refs | delivery | upstream。 */
  name: string
  /** pass | fail | warn | skip | unknown。 */
  status: string
  detail: string
  remedy?: string
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
