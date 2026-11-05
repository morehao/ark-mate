/** 用量统计（M3 `GET /usage/summary`；sqlite 模式才有数据，内存模式返回 40501）。 */

/** 查询参数：from/to 半开区间 [from, to)（RFC3339 或 YYYY-MM-DD，空 = 不限该侧）。 */
export interface UsageQuery {
  from?: string
  to?: string
  /**
   * 客户端相对 UTC 的偏移分钟数（东为正，`-new Date().getTimezoneOffset()`；缺省 0/UTC）。
   *
   * 日期形式的 `from`/`to` 与 day 桶的键都按它解释：日期是**用户日历上的**那一天，
   * 不是 UTC 的那一天。不传它 = 按 UTC 分桶——UTC+8 的用户会看到每天 00:00–08:00 的
   * 轮次算进前一天，而界面上一句话都不会说。
   */
  tz_offset?: number
  /**
   * 分组维度：只有 day（键 = `YYYY-MM-DD`，日期属于 `tz_offset` 时区）。
   *
   * v19 起没有 agent 分组（连同 `?agent_id=` 过滤一起撤掉）：底座由构建期固定，
   * 按它分组只会得到"一个桶装着全部"——那不是分组，是给同一个答案多画一列。
   */
  group_by?: 'day'
}

/** 一行用量聚合（key=YYYY-MM-DD）。 */
export interface UsageBucket {
  key: string
  /** 桶归属的底座 id（构建期固定的那个；作为事实回带，不作分组维度）。 */
  runtime_id?: string
  runs: number
  completed: number
  failed: number
  cancelled: number
  running: number
  sessions: number
  duration_ms: number
  context_used: number
  context_size: number
  /** 上报过上下文的会话数（与 cost_reported 分开，二者不可互相替代）。 */
  usage_reported: number
  /** 上报过费用的会话数。 */
  cost_reported: number
  cost_amount: number
  /** `USD` 等；多币种为 `MIXED`（此时 cost_amount=0，明细见 cost_by_currency）。 */
  cost_currency?: string
  /** 多币种时的分币种金额；单币种为空。 */
  cost_by_currency?: Record<string, number>
}

/** 用量汇总：总计 + 分组桶 + 生效条件回显。 */
export interface UsageSummary {
  from?: string
  to?: string
  runtime_id?: string
  group_by: string
  /** 生效的客户端时区偏移分钟数（东为正；0 = UTC）。day 桶与日期参数按它解释。 */
  tz_offset?: number
  /** sqlite | memory（memory 模式无统计）。 */
  source: string
  /** 上下文口径：`end_of_window_snapshot_sum`（区间末快照，非增量）。 */
  context_semantics: string
  /** 费用口径：`session_cumulative_end_of_window`（会话累计快照，非增量）。 */
  cost_semantics: string
  /** 桶之间能否相加：day 分组 false（跨天会话的累计快照会重复出现）。 */
  buckets_additive: boolean
  totals: UsageBucket
  buckets: UsageBucket[]
  /**
   * 内部模型调用流水（llm_calls）的 token 明细汇总。**与上面 totals/buckets 的会话快照口径分开**：
   * 这一块是流水（一次调用一行、可跨调用相加），覆盖含"无会话"的旁路调用。
   */
  llm: UsageLLM
}

/** 累加型 token 明细。 */
export interface UsageTokens {
  input_tokens: number
  output_tokens: number
  cached_read_tokens: number
  cached_write_tokens: number
  thought_tokens: number
  total_tokens: number
}

/** 一个用途维度的调用聚合。 */
export interface UsagePurpose {
  purpose: string
  calls: number
  /** 失败（error/timeout/cancelled）与降级（degraded）调用数。 */
  failed: number
  degraded: number
  tokens: UsageTokens
  cost_amount: number
  cost_currency?: string
}

/** 内部模型调用流水汇总（数据源 llm_calls）。 */
export interface UsageLLM {
  calls: number
  failed: number
  degraded: number
  /** 带 session_id 的调用数；calls - session_calls 即旁路调用数。 */
  session_calls: number
  tokens: UsageTokens
  /** 按用途分组（按调用数降序）。 */
  by_purpose: UsagePurpose[]
}
