/**
 * 模型面（v12）：**两个读面分得很开**，改这个文件前先认清哪一个是哪一个。
 *
 *  - ① **运行时目录面**（`ModelCatalogResp`，`GET /models/catalog?agent=`）：运行时（ACP server）
 *    自报的会话配置项投影，`value` 是**不透明字符串**、必须原样回传。会话级模型选择器用它。
 *  - ② **平台清单面**（`ModelsResp`，`GET /models`）：用户登记的应用层资源（供应商下的模型），
 *    外加**聚合**生效结论。设置 · 模型 那一节用它。
 *
 * 两者相遇过（v11 把 `advertised`/`diff`/`config` 塞在 `GET /models` 里），v12 把平台面与底座面
 * 拆开：`ModelsResp` 里**没有** `advertised`/`diff`/`config`/`options`，逐 runtime 的同步明细
 * 落在 `sync[]`、标为**机器面**——设置 · 模型 那一节不得渲染它（ADR-22，A13/A14 守卫钉住）。
 */

/** 目录取样来源：session=活跃会话 / resume=恢复历史会话读取 / cache=内存缓存 / none=无来源。 */
export type ModelCatalogSource = 'session' | 'resume' | 'cache' | 'none' | string

/**
 * 配置项语义分类（协议标准，UX 提示）。
 * **只按 category 渲染，不按 id 匹配**——各家 option id 不同。
 * 平台只在 UI 上消费 `model`：运行时若还给出别的选项（如 thought_level/mode），只原样透出，不做选择面。
 */
export type ConfigOptionCategory = 'model' | 'thought_level' | 'mode' | 'model_config' | string

/** 一个可选值。value 是**不透明字符串**：原样回传（创建/切换会话），前端不得解析其内部结构。 */
export interface ConfigOptionValue {
  /** 不透明取值（dsh: JSON.stringify([provider, model])；opencode: provider/model）。 */
  value: string
  /** 展示名（唯一可展示字段）。 */
  name: string
  description?: string
  /** agent 以分组形态下发时的分组标识与标题（opencode 为扁平列表，两字段为空）。 */
  group?: string
  group_name?: string
  /** 平台观测：该值最近被运行时拒绝过（如凭证缺失）。仅提示，最终以运行时为准。 */
  unavailable?: boolean
  unavailable_reason?: string
}

/** 一个会话配置项（select 下拉 / boolean 开关）。 */
export interface ConfigOption {
  id: string
  name: string
  description?: string
  category?: ConfigOptionCategory
  type: 'select' | 'boolean' | string
  /** 运行时当前值。 */
  current_value: string
  /** 原选项是否为分组形态（分组信息在 values[].group/group_name 上）。 */
  grouped?: boolean
  values: ConfigOptionValue[]
}

/** 运行时声明的鉴权方式（空目录时给用户确切的补救路径，如 opencode 的终端登录命令）。 */
export interface AuthGuidance {
  method_id: string
  name: string
  description?: string
  terminal?: boolean
  command?: string[]
}

/**
 * 运行时目录响应（`GET /models/catalog?agent=`，v12 拆出的独立读面）。
 *
 * 它是**事实**（运行时广告什么、会话能选什么），`value` 是不透明串；平台声明的那份是 `ModelsResp`。
 */
export interface ModelCatalogResp {
  agent: string
  runtime: string
  /** 取样来源（session|resume|cache|none）。 */
  source: ModelCatalogSource
  /** 取样时间（RFC3339；cache 时为首次取样时间）。 */
  fetched_at?: string
  options: ConfigOption[]
  auth_guidance?: AuthGuidance[]
  /** 空目录/降级说明（可直接展示给用户）。 */
  note?: string
}

/** 平台声明的一条模型条目（`GET /models` 的 `models[]`）。 */
export interface ModelRow {
  /** 平台 id。 */
  id: string
  /** 挂靠的供应商 id。 */
  provider_id: string
  /** 发给上游的模型 id（**不是**运行时的 value，也不是平台 id）。 */
  model_id: string
  /** 展示名（空 = 前端回落显示 model_id）。 */
  name?: string
  /** 备注（不驱动行为）。 */
  note?: string
  /**
   * 输入模态（**总是带值**，默认 `['text']`）：`['text']` | `['text','image']`。
   *
   * 它是**平台声明**（意图），不是当前环境里那条模型本来支持什么（事实）。默认 `text` 意味
   * 「图片不会被接受」，要图片得逐条勾选——所以列表里必须让用户看得见、改得动。
   */
  input_modalities?: string[]
  /** 写面乐观锁计数（每次成功提交 +1）；**不是内容版本**（内容指纹在审计里）。 */
  revision: number
  updated_at?: string
  /** 写面回执（只在写面响应里出现；列表里为空）。 */
  effect?: ModelEffect
}

/** 模型清单写面的生效面回执（v12 只有一条通道：把整份清单交给各 adapter）。 */
export interface ModelEffect {
  /** next_session（下一次会话创建/换代际）| next_process（新进程读配置）。 */
  effect: string
  /** 受影响（可能落到任一已注册底座）的会话数。 */
  affected_conversations: number
  /** 其中本轮不能就地换代际的会话数（有在途轮次 / 有未应答权限或提问）。 */
  deferred: number
  note?: string
}

/** 某 runtime 的一次同步结论（`sync[]` 的条目）。⚠️ **机器面 / 诊断面**：设置 · 模型不得渲染它。 */
export interface RuntimeSyncResp {
  runtime: string
  /** applied | unchanged | unsupported | untested | failed。 */
  status: string
  /** 人话落点（给排障看的「同步到哪了」）。 */
  where: string
  /** immediate | next_turn | next_session | next_process（清单通道的生效时刻）。 */
  effect: string
  /** true = 平台已交付但底座还没读到（新进程才读）——**不许把它说成"已生效"**。 */
  pending: boolean
  /** 凭据交付面的结论，与清单**分开报**（两者的生效时机本就不同）。 */
  credential: CredentialSyncResp
  /** 底座事实上报的候选（尽力采样；拿不到为 null）。 */
  advertised?: string[]
  /** 声明了但没出现在 advertised 里的。 */
  missing?: string[]
  /** 可执行的一句话清单。 */
  problems: string[]
}

/** 凭据交付面的结论（机器面）。 */
export interface CredentialSyncResp {
  /** native_doc | env | none。 */
  channel: string
  /** applied | unchanged | unsupported | untested | failed。 */
  status: string
  /** 同 RuntimeSyncResp.effect 的取值域。 */
  effect: string
  /** 该通道是否已实测；false 时界面必须显示「待实测」而不是「已生效」。 */
  tested: boolean
  /** 人话落点（绝不回显密钥）。 */
  where: string
}

/**
 * `GET /models` 的响应：平台清单 + **聚合**生效结论（v12）。
 *
 * **用户面只有 `models` / `default_model` / `effective` / `effective_at` 四个字段**（ADR-22/ADR-21）：
 * `default_model` 是**单一值**（不带 runtime 维度），`effective`/`effective_at` 是所有 runtime 里
 * 最保守的聚合结论。`sync[]` 是机器面，设置 · 模型 那一节**不得渲染**它。
 */
export interface ModelsResp {
  /** 平台清单（不分页：上限 200 条）。 */
  models: ModelRow[]
  /** 唯一默认模型的**平台模型 id**（空 = 未设）。 */
  default_model: string
  /** 用户面聚合结论：applied | pending | failed。 */
  effective: string
  /** 生效时刻：now | next_session | next_launch（取保守值 = 所有 runtime 里最晚的那个）。 */
  effective_at: string
  /** ⚠️ 机器面 / 诊断面：逐 runtime 明细。**前端不得直接渲染**这里的 runtime / where / problems。 */
  sync: RuntimeSyncResp[]
  /** 人话说明（清单为空 / 没有可同步的底座等）。 */
  note?: string
}

/** `POST /models` 的请求体。**没有 `runtime`**：模型不带底座维度（带 `runtime` 会被 40001 拒）。 */
export interface ModelCreateReq {
  provider_id: string
  model_id: string
  name?: string
  note?: string
  /** 输入模态：`['text']`（默认）| `['text','image']`；非法取值会被 40501 拒。 */
  input_modalities?: string[]
  /** true = 把这条设为**唯一**默认模型。 */
  is_default?: boolean
}

/** `PATCH /models/:id`：指针更新（省略 = 不改）。 */
export interface ModelPatchReq {
  name?: string
  note?: string
  /** 输入模态：`['text']` | `['text','image']`；省略 = 不改。 */
  input_modalities?: string[]
  /** true = 设为唯一默认；false = 取消默认（幂等）。 */
  is_default?: boolean
}

/** `DELETE /models/:id` 的结果（幂等）。 */
export interface ModelDeleteResp {
  deleted: boolean
  /** 本次删除连带清空了默认模型（用户可见的副作用，必须说出来）。 */
  default_cleared?: boolean
}

/** `POST /models/sync` 的响应：逐 runtime 的同步结论（机器面，只用于判断成败）。 */
export interface ModelSyncResp {
  applied: RuntimeSyncResp[]
}

/** 按 category 取配置项（语义化取值的唯一入口）。 */
export const configOptionByCategory = (
  options: ConfigOption[] | undefined,
  category: ConfigOptionCategory,
): ConfigOption | undefined => (options ?? []).find((o) => o.category === category)

/**
 * 会话级模型候选值（无模型项 → 空数组：运行时没有可切换的模型）。
 *
 * **只吃运行时目录面**（`ModelCatalogResp`）：候选取 `options[category=model].values`，
 * 它们的 `value` 是不透明串、必须原样回传给会话接口。平台清单（`ModelsResp`）不喂这里——
 * 那是应用层资源，不是运行时候选。
 */
export const modelValues = (resp?: ModelCatalogResp | null): ConfigOptionValue[] =>
  configOptionByCategory(resp?.options, 'model')?.values ?? []

/** 模型引用：创建/切换会话时选定的模型（运行时目录里的不透明 value）。 */
export interface ModelRef {
  /** 运行时目录里的模型 value（原样回传；空 = 跟随运行时默认）。 */
  modelID?: string
}
