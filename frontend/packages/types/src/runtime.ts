/**
 * 底座（runtime）面：**构建期固定**，用户没有任何选择面。
 *
 * v19 之前这里的每个事实都挂在 agent 上（`GET /agents`、`/agents/:id/diagnostics`、
 * `/agents/:id/capabilities`），而 agent 的每个字段要么是派生事实、要么本来就是平台事实——
 * 于是"哪个底座在跑"在仓里有两个答案指向同一个东西。收成一个之后，本文件是前端**唯一**的
 * 底座身份来源：`GET /meta` 的 `runtime{id,name,version}`（身份）与
 * `GET /runtime/diagnostics`（运行时事实）。
 */

/**
 * 底座的构建期身份（`GET /meta` 的 `runtime`）。
 *
 * 三件事各自的性质不同，别混着用：
 *  - `id`：构建期注入的那个 id（默认 `dsh`），是**唯一的**身份键（任何按底座分派的地方都只认它）；
 *  - `name`：给人看的展示名（界面上凡是要说"当前用的是哪个底座"就用它，不要摊 id）；
 *  - `version`：核心侧声明的底座版本（**不是**本机装的那份的版本——那份只有 `payloads[]` 答得出）。
 */
export interface RuntimeInfo {
  id: string
  name: string
  version: string
}

/** 随包/命中载荷的一条（`GET /meta` 的 `payloads[]`）。 */
export interface MetaPayload {
  name: string
  version: string
  digest: string
  /** bundled（随包）| runtime-env（显式覆盖）| path（PATH 上找到）。 */
  source: string
}

/**
 * `GET /meta` 的响应：这份部署的装配事实（版本 + 资产 + 数据目录 + 底座身份）。
 *
 * 字段性质分三类，读的时候要分清：
 *  - **恒有值**：`coreVersion` / `webBuildId` / `apiContract` / `home` / `runtime`；
 *  - **可空字段**（缺值时是显式 `null`，不是缺字段——两者会在消费方分出两条判断分支）：
 *    `webPath` / `webDigest`；
 *  - **恒为数组**（无载荷时是 `[]` 而不是 `null`）：`payloads`。
 */
export interface MetaResp {
  /** 核心版本（发布构建由 -ldflags 注入；未注入时是 dev）。 */
  coreVersion: string
  /** 前端产物的构建 id（版本闸门据此发现"dist 被换过"）。 */
  webBuildId: string
  /** API 契约代数（与核心版本解耦）。 */
  apiContract: string
  /** 资产目录的绝对路径；未接资产层时为 null。 */
  webPath: string | null
  /** 逐文件 sha256 的确定性合成；未接资产层时为 null。 */
  webDigest: string | null
  /** 数据目录（默认 ~/.ark-mate）。 */
  home: string
  payloads: MetaPayload[]
  /** 构建期固定的那个底座的身份（v19 新增）。 */
  runtime: RuntimeInfo
}

/** 底座进程状态（诊断面的 `state`；与界面上"运行中/已停止"的语义色同源）。 */
export type RuntimeState = 'running' | 'stopped'

/** 底座最近一次失败的诊断快照（A3）。 */
export interface RuntimeLastError {
  at: string
  session_id?: string
  code: string
  class: string
  details: string
  stderr_tail?: string[]
}

/**
 * 底座诊断快照（`GET /runtime/diagnostics`）：连接态 + stderr 诊断尾巴 + 最近失败。
 *
 * 形状与 v19 之前的 `/agents/:id/diagnostics` 逐字段一致，**只少了 agent 身份**：
 * 没有 `agent_id`（底座只有一个），也没有原来那个 `name`（那是 agent 的展示名，
 * 现在这个位置由 `GET /meta` 的 `runtime.name` 回答，诊断面不重复第二份展示名）。
 * 所以本对象里所有"名字/身份"都出自 `runtime` / `runtime_name` 两个事实字段。
 */
export interface RuntimeDiagnostics {
  state: RuntimeState
  /** 构建期固定的底座 id。 */
  runtime: string
  /** 底座的展示名（空 = 取不到，界面回落用 `runtime`）。 */
  runtime_name?: string
  /** 实际拉起的启动命令（空 = 还没起过进程）。 */
  command?: string
  connected: boolean
  pid?: number
  /** 尾巴来源：live（当前子进程实时）| last_error（上次失败采集）| none。 */
  stderr_source: 'live' | 'last_error' | 'none' | string
  stderr_tail: string[]
  last_error?: RuntimeLastError
  /**
   * 可执行修复步骤（服务端按事实推导：runtime 未注册 / 启动命令不可执行 / provider 档案悬空或无
   * 密钥 / 底座自报的鉴权方式 / 底座安装步骤）。健康快照不带该字段。
   */
  remediation?: string[]
  checked_at: string
}
