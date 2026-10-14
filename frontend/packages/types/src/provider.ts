/**
 * 供应商面（`/providers`）。
 *
 * 为什么单独一个文件而不是继续挂在 `agent.ts` 里：v19 把 agent 概念整个并入 runtime
 * （底座构建期固定、用户不可自定义），那个文件连同 `/agents/*` 一起撤掉了；供应商是**规格完全不受影响**
 * 的相邻读面，随文件一起删掉等于顺手删掉用户可见的功能。所以只搬位置、不改形状。
 *
 * 与之对照，本文件里**没有**任何 agent 形状的字段（原 `ProviderRefs.agents` /
 * `ProviderEffect.applies_to_agents` / `ProviderDeleteResp.referenced_by_agents`）：
 * 它们的事实来源就是被撤掉的 agents 表，撤掉之后这三个键再也填不出值，
 * 留着只会让读的人以为"还有一份 agent 引用面"。
 */

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
  /** 只读派生的引用面（哪些 runtime 在用它）。 */
  refs: ProviderRefs
  /** 逐**已注册** runtime 的可达性（刻意不给合并结论）。 */
  applicability?: Applicability[]
  /** 写面回执（只在写面响应里出现）。 */
  effect?: ProviderEffect
}

/**
 * provider 的引用面（只读派生，无写入方）。
 *
 * 只有 runtime 维度：v19 之前这里还有 `agents[]`（"哪些 agent 绑定它"），
 * 而 agent 已并入构建期固定的 runtime——"谁在用它"这个问题现在只有一个答案维度。
 */
export interface ProviderRefs {
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
  /** next_run（下一次轮次边界换代际）| none（只改了不驱动行为的字段，如 name/note）。 */
  effect: string
  affected_sessions: number
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
  /** 被拒时的模型引用名单（先删掉这些模型再删）。 */
  referenced_by_models?: string[]
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
