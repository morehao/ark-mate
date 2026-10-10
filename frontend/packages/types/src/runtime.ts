export interface CapabilityKV {
  key: string
  value: boolean
}

/** 一条可声明的「运行时参数」（H1/H3）：平台按 adapter 的声明派生，前端只渲染、不硬编码字段。 */
export interface ProviderProjectionField {
  name: string
  /** string | object（object 在表单里按 JSON 文本编辑）。 */
  type: string
  required: boolean
  note?: string
}

/** 某 runtime 认的运行时参数 schema（`GET /runtimes` 的 `provider_projection`）。 */
export interface ProviderProjectionSchema {
  runtime: string
  fields: ProviderProjectionField[]
  note?: string
}

export interface Runtime {
  name: string
  id: string
  version: string
  capabilities: CapabilityKV[] // 静态能力基线（read-only）
  /**
   * 该底座认的**逐 runtime 运行时参数** schema（`projections.<runtime>` 里能写哪些键）。
   * 缺席 = 该底座不承载平台模型清单（对它写运行时参数没有意义）。
   */
  provider_projection?: ProviderProjectionSchema
}
