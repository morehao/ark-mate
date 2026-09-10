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

/** 模型目录响应（GET /models?agent=<id>）：options 为运行时配置项全量投影。 */
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

/** 按 category 取配置项（语义化取值的唯一入口）。 */
export const configOptionByCategory = (
  options: ConfigOption[] | undefined,
  category: ConfigOptionCategory,
): ConfigOption | undefined => (options ?? []).find((o) => o.category === category)

/** 模型项候选值（无模型项 → 空数组：运行时没有可切换的模型）。 */
export const modelValues = (resp?: ModelCatalogResp): ConfigOptionValue[] =>
  configOptionByCategory(resp?.options, 'model')?.values ?? []

/** 模型引用：创建/切换会话时选定的模型（运行时目录里的不透明 value）。 */
export interface ModelRef {
  /** 运行时目录里的模型 value（原样回传；空 = 跟随运行时默认）。 */
  modelID?: string
}
