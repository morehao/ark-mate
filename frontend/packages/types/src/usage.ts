/** 用量统计（M3 `GET /usage/summary`；sqlite 模式才有数据，内存模式返回 40501）。 */

/** 查询参数：from/to 半开区间 [from, to)（RFC3339 或 YYYY-MM-DD，空 = 不限该侧）。 */
export interface UsageQuery {
  from?: string
  to?: string
  agent_id?: string
  /** agent（默认）| day（UTC 日期）。 */
  group_by?: 'agent' | 'day'
}

/** 一行用量聚合（group_by=agent → key=agent_id；=day → key=YYYY-MM-DD）。 */
export interface UsageBucket {
  key: string
  agent_id?: string
  runs: number
  completed: number
  failed: number
  cancelled: number
  running: number
  conversations: number
  duration_ms: number
  context_used: number
  context_size: number
  usage_reported: number
  cost_amount: number
  cost_currency?: string
}

/** 用量汇总：总计 + 分组桶 + 生效条件回显。 */
export interface UsageSummary {
  from?: string
  to?: string
  agent_id?: string
  group_by: string
  /** sqlite | memory（memory 模式无统计）。 */
  source: string
  totals: UsageBucket
  buckets: UsageBucket[]
}
