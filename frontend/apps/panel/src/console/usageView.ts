import type { UsageBucket, UsageSummary } from '@ark/types'

// ——— 用量视图模型（M3 用量统计；纯函数，便于 Node 侧校验） ———

/** 毫秒 → 人类可读（0 → `0s`；<1s → `123ms`；<60s → `12.3s`；<60m → `3m04s`；否则 `2h05m`）。 */
export function fmtDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0s'
  if (ms < 1000) return `${Math.round(ms)}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)}s`
  const total = Math.round(s)
  const m = Math.floor(total / 60)
  const sec = total % 60
  if (m < 60) return `${m}m${String(sec).padStart(2, '0')}s`
  const h = Math.floor(m / 60)
  return `${h}h${String(m % 60).padStart(2, '0')}m`
}

/** 成本展示：无成本（0 且无币种）→ `—`；有币种 → `$1.2345` 形态（保留 4 位有效小数）。 */
export function fmtCost(amount: number, currency?: string): string {
  if (!amount) return '—'
  const sym = currency === 'USD' || !currency ? '$' : `${currency} `
  const digits = Math.abs(amount) < 1 ? 4 : 2
  return `${sym}${amount.toFixed(digits)}`
}

/** 上下文占用展示：`9.4k / 1.0M`；size 未知（0）→ 只显示 used。 */
export function fmtContext(used: number, size: number): string {
  const k = (n: number): string => (n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)}M` : n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n))
  if (!size) return k(used)
  return `${k(used)} / ${k(size)}`
}

/** 成功率（completed / (completed+failed+cancelled)）；无终态轮次 → null（显示 `—`）。 */
export function successRate(b: UsageBucket): number | null {
  const done = b.completed + b.failed + b.cancelled
  if (done <= 0) return null
  return b.completed / done
}

export interface UsageRow {
  key: string
  /** 展示名（agent_id 或日期）。 */
  label: string
  bucket: UsageBucket
}

/** 把汇总桶转成行模型：agent 分组按 runs 降序（同量按 key 升序），day 分组按日期升序。 */
export function usageRows(summary: UsageSummary | null): UsageRow[] {
  if (!summary) return []
  const rows = summary.buckets.map((b) => ({
    key: b.key,
    label: b.agent_id || b.key,
    bucket: b,
  }))
  if (summary.group_by === 'day') return rows.sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0))
  return rows.sort((a, b) => b.bucket.runs - a.bucket.runs || (a.key < b.key ? -1 : 1))
}
