/** 模型目录数据来源（三段式降级 live → 磁盘缓存 → go:embed 快照）。 */
export type ModelSource = 'live' | 'cache' | 'snapshot' | string

/** 目录中的一个可选模型条目（对齐后端 handler.ModelOptionResp；Provider 为 provider 类型）。 */
export interface ModelOption {
  /** provider 类型（openai/anthropic/deepseek/openrouter/groq），非档案 id。 */
  provider: string
  /** 模型 id（如 gpt-5 / deepseek-reasoner）。 */
  id: string
  /** 展示名。 */
  title: string
  description?: string
  /** 行为/能力标签（dsh 可能含推理档位；opencode/echo 通常无），仅展示。 */
  behavior?: string[]
  /** 推荐星标。 */
  recommended?: boolean
  is_free?: boolean
}

/** 模型目录响应（source 标数据来源，降级可观测）。 */
export interface ModelCatalogResp {
  source: ModelSource
  models: ModelOption[]
  note?: string
}

/** 模型引用：创建/切换会话时选定的 provider 档案 id + 模型 id +（可选）会话模型档位。 */
export interface ModelRef {
  /** provider 档案 id（会话级模型选择的 provider 面；创建时后端用 agent.provider 决定）。 */
  providerID?: string
  modelID: string
  /** effort/reasoning 档位（如 low/medium/high；仅 dsh，空 = 交给后端默认）。 */
  effort?: string
}
