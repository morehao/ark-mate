/**
 * 会话配置项（ACP session config options）的直投影。
 *
 * 数据源 = **运行时**（dsh / opencode 的 ACP server），不是平台内置清单：
 * 平台已删除 go:embed 快照 + 上游 live + 磁盘缓存的三段式目录，可选项完全由运行时自报。
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
 * 模型目录响应（**旧形态**）：`options` 为运行时配置项全量投影。
 *
 * v11 起它只对**未声明模型配置落点**的 runtime 回落出现（`GET /models` 的 `options` 字段）；
 * 声明了落点的 runtime（opencode / dsh）走 `ModelsResp` 的四段形态。
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

/** 平台声明的一条模型条目（`GET /models` 的 `declared[]`）。 */
export interface ModelRow {
  id: string
  runtime: string
  provider_id: string
  /** 平台写出去给底座的模型 id（**不是**运行时的 value）。 */
  model_id: string
  /** 平台侧显示名（改不动 ACP 候选项里的路由前缀）。 */
  name?: string
  extra?: Record<string, unknown>
  /** 平台侧隐藏：**不下发**（底座的目录里仍会出现它——隐藏不是"删掉它"）。 */
  hidden: boolean
  /** 平台侧置顶（只改平台列表顺序，不下发）。 */
  pinned: boolean
  is_default: boolean
  /** 平台按该 runtime 规则**生成**的不透明值（会话级下发与对账都用它）。 */
  value: string
  revision: number
  updated_at?: string
  /** 写面回执（只在写面响应里出现）。 */
  effect?: ModelEffect
}

/** 模型写面的生效面回执：两条通道 + 最早的生效时刻。 */
export interface ModelEffect {
  /** config_write（新进程读配置）+ 改了默认时的 session_config（新会话创建时下发）。 */
  channels: string[]
  /** next_session（下一次建会话时下发）| next_process（新 runtime 进程读配置）。 */
  effect: string
  applies_to_runtimes: string[]
  affected_conversations: number
  deferred: number
  note?: string
}

/** 运行时实际广告的一个模型候选（`GET /models` 的 `advertised[]`）。 */
export interface AdvertisedModel {
  value: string
  name?: string
  group?: string
  group_name?: string
  unavailable?: boolean
  unavailable_reason?: string
}

/** 差集里的一条：`severity` ∈ error（选了就报错）/ degrade（静默降级）/ info（只是事实）。 */
export interface ModelDiffEntry {
  model_id?: string
  value: string
  provider_id?: string
  route?: string
  severity: 'error' | 'degrade' | 'info' | string
  reason: string
}

/** 清单（意图）与运行时广告（事实）的两个差集。差集为空 = 已生效。 */
export interface ModelDiff {
  declared_not_advertised: ModelDiffEntry[]
  advertised_not_declared: ModelDiffEntry[]
}

/** 某 runtime 的模型配置物化状态（T5）。`pending:true` 就是在说"还没生效"。 */
export interface RuntimeConfigResp {
  runtime: string
  declared: boolean
  /** 落点是否经真实底座实测；false = 只读（生成候选但不落盘）。 */
  tested: boolean
  writable: boolean
  channel?: string
  path?: string
  digest?: string
  generated_at?: string
  changed: boolean
  pending: boolean
  model_count: number
  note?: string
}

/** `GET /models` 的响应：平台清单 + 运行时事实 + 差集 + 物化状态（v11 起的主形态）。 */
export interface ModelsResp {
  runtime: string
  agent?: string
  declared: ModelRow[]
  advertised: AdvertisedModel[]
  diff: ModelDiff
  config: RuntimeConfigResp
  /** 旧形态回落：仅当该 runtime **未声明** ConfigProjection 时出现。 */
  options?: ConfigOption[]
  source?: ModelCatalogSource
  fetched_at?: string
  auth_guidance?: AuthGuidance[]
  note?: string
}

/** 写面的请求体（与后端 DTO 逐字对应）。 */
export interface ModelCreateReq {
  runtime: string
  provider_id: string
  model_id: string
  name?: string
  extra?: Record<string, unknown>
  hidden?: boolean
  pinned?: boolean
  is_default?: boolean
}

export interface ModelPatchReq {
  name?: string
  extra?: Record<string, unknown>
  hidden?: boolean
  pinned?: boolean
  is_default?: boolean
}

export interface ModelDeleteResp {
  deleted: boolean
  next_action?: string
}

export interface ModelApplyResp {
  applied: RuntimeConfigResp[]
}

/** 按 category 取配置项（语义化取值的唯一入口）。 */
export const configOptionByCategory = (
  options: ConfigOption[] | undefined,
  category: ConfigOptionCategory,
): ConfigOption | undefined => (options ?? []).find((o) => o.category === category)

/**
 * 模型项候选值（无模型项 → 空数组：运行时没有可切换的模型）。
 *
 * **两种响应都能喂**（`ModelSelect` 因此零改动：它只消费 `ConfigOptionValue[]` 与 `value`）：
 *  - v11 的四段形态：候选取 `advertised[]`（运行时事实），并**在这里**把平台侧 `hidden` 的条目
 *    过滤掉——隐藏只影响平台选择器，不影响会话与物化（"隐藏不是删掉它"）；
 *  - 旧形态：直投影 `options[category=model].values`。
 */
export const modelValues = (resp?: ModelCatalogResp | ModelsResp): ConfigOptionValue[] => {
  if (!resp) return []
  if ('advertised' in resp) {
    const hidden = new Set((resp.declared ?? []).filter((d) => d.hidden).map((d) => d.value))
    return (resp.advertised ?? [])
      .filter((a) => !hidden.has(a.value))
      .map((a) => ({
        value: a.value,
        name: a.name || a.value,
        group: a.group,
        group_name: a.group_name,
        unavailable: a.unavailable,
        unavailable_reason: a.unavailable_reason,
      }))
  }
  return configOptionByCategory(resp.options, 'model')?.values ?? []
}

/** 模型引用：创建/切换会话时选定的模型（运行时目录里的不透明 value）。 */
export interface ModelRef {
  /** 运行时目录里的模型 value（原样回传；空 = 跟随运行时默认）。 */
  modelID?: string
}
