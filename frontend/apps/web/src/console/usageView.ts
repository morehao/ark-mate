import type { ConversationRun, RunUsage, UsageBucket, UsageSummary } from '@ark-mate/types'

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

/** 多币种哨兵（后端 store.CostCurrencyMixed）：桶内出现多种币种时 amount 置零、币种为此值。 */
export const COST_CURRENCY_MIXED = 'MIXED'

/** 币种符号：`USD`/空 → `$`；其他 → `币种 ` 前缀（与后端「空币种归一 USD」口径一致）。 */
function costSymbol(currency?: string): string {
  return currency === 'USD' || !currency ? '$' : `${currency} `
}

/** 金额精度：<1 → 4 位；否则 2 位。 */
function costDigits(amount: number): number {
  return Math.abs(amount) < 1 ? 4 : 2
}

/**
 * 成本展示：无成本 → `—`；单币种 → `$1.2345`；多币种（MIXED + 明细）→ `$0.50 · ¥2.00`
 * （分币种并列，绝不把不同币种相加）。
 */
export function fmtCost(
  amount: number,
  currency?: string,
  byCurrency?: Record<string, number>,
): string {
  if (currency === COST_CURRENCY_MIXED && byCurrency && Object.keys(byCurrency).length > 0) {
    return Object.keys(byCurrency)
      .sort()
      .map((c) => `${costSymbol(c)}${byCurrency[c].toFixed(costDigits(byCurrency[c]))}`)
      .join(' · ')
  }
  if (!amount) return '—'
  return `${costSymbol(currency)}${amount.toFixed(costDigits(amount))}`
}

/** 上报覆盖率文案（用于上下文/成本列 tooltip）：`上报 3/5 会话` / `无上报` / `无会话`。 */
export function reportedLabel(reported: number, conversations: number): string {
  if (conversations <= 0) return '无会话'
  if (reported <= 0) return '无上报'
  return `上报 ${reported}/${conversations} 会话`
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

/** 上下文占用文案；无上报 → `—`。 */
export function fmtRunUsage(run: RunUsage | null | undefined): string {
  if (!run || (!run.used && !run.size)) return '—'
  return `上下文 ${fmtContext(run.used, run.size)}`
}

/**
 * 本轮上下文占用相对上一轮的增量（Δ = 本轮 used − 上一轮 used）。
 * 说明：used 是上下文窗口占用快照（会话累计）。Δ>0 表示上下文增长；压缩后可能 Δ≤0，
 * 因此这一差值**不是**「本轮消耗 token」，只是「上下文占用变化」——展示时如实标注。
 */
export function runUsageDelta(curr: RunUsage | null | undefined, prev?: RunUsage | null | undefined): number | null {
  if (!curr || !prev || !curr.used || !prev.used) return null
  return curr.used - prev.used
}

/** 增量文案：`Δ +3.2k` / `Δ −1.0k` / `Δ 0`；无对比 → ''。 */
export function fmtRunUsageDelta(curr: RunUsage | null | undefined, prev?: RunUsage | null | undefined): string {
  const d = runUsageDelta(curr, prev)
  if (d == null) return ''
  const sign = d > 0 ? '+' : ''
  const k = (n: number): string => (n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)}M` : n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n))
  return `Δ ${sign}${k(d)}`
}

/** 单轮用量展示行：`上下文 30.0k / 128.0k`[` · Δ +3.2k`][` · $0.5`]；无上报 → `—`。 */
export function fmtRunUsageLine(run: RunUsage | null | undefined, prev?: RunUsage | null | undefined): string {
  const base = fmtRunUsage(run)
  if (base === '—') return base
  const parts = [base]
  const delta = fmtRunUsageDelta(run, prev)
  if (delta) parts.push(delta)
  if (run?.cost_amount) parts.push(fmtCost(run.cost_amount, run.cost_currency))
  return parts.join(' · ')
}

// ——— goose 风格单轮用量（真实数据；不估算 token） ———

/** 单轮真实可展示的用量视图模型：计时真实来自 run 时间戳，上下文/成本来自快照。 */
export interface RunUsageInfo {
  used: number
  size: number
  costAmount: number
  costCurrency?: string
  /** 首令牌耗时（ms；0 = 未记录/无输出）。 */
  firstTokenMS: number
  /** 总耗时（ms；0 = 未知）。 */
  durationMS: number
}

/** 从 run 时间戳计算总耗时（ended−started，ms）；未结束/无效 → 0。 */
export function runDurationMS(run: ConversationRun): number {
  if (!run?.started_at) return 0
  const s = Date.parse(run.started_at)
  const e = run.ended_at ? Date.parse(run.ended_at) : 0
  if (!Number.isFinite(s)) return 0
  if (!Number.isFinite(e) || e <= s) return 0
  return e - s
}

/** 把 ConversationRun 归一为展示模型；无任何可展示数据 → null。 */
export function toRunUsageInfo(run: ConversationRun | null | undefined): RunUsageInfo | null {
  if (!run) return null
  const u = run.usage
  const first = run.first_token_ms ?? 0
  const dur = runDurationMS(run)
  const hasCost = !!u?.cost_amount
  if (!u?.used && !u?.size && !hasCost && first <= 0 && dur <= 0) return null
  return {
    used: u?.used ?? 0,
    size: u?.size ?? 0,
    costAmount: u?.cost_amount ?? 0,
    costCurrency: u?.cost_currency,
    firstTokenMS: first,
    durationMS: dur,
  }
}

/** popover 里的统计行（header/“——” 占位；全部真实数据）。 */
export interface RunStatRow {
  label: string
  value: string
}

/** 单轮用量的明细行：上下文占用（快照）/首令牌/总耗时/成本。 */
export function runStatRows(info: RunUsageInfo | null | undefined): RunStatRow[] {
  if (!info) return []
  const rows: RunStatRow[] = []
  if (info.used || info.size) rows.push({ label: '上下文占用', value: fmtContext(info.used, info.size) })
  rows.push({ label: '首令牌', value: info.firstTokenMS > 0 ? fmtDuration(info.firstTokenMS) : '—' })
  rows.push({ label: '总耗时', value: info.durationMS > 0 ? fmtDuration(info.durationMS) : '—' })
  rows.push({ label: '成本', value: info.costAmount ? fmtCost(info.costAmount, info.costCurrency) : '—' })
  return rows
}

/** composer 下方常驻汇总行：`首令牌 714ms · 总耗时 1.5s · 上下文 30k/128k · $0.5`；无数据 → ''。 */
export function fmtRunDockLine(info: RunUsageInfo | null | undefined): string {
  if (!info) return ''
  const parts: string[] = []
  if (info.firstTokenMS > 0) parts.push(`首令牌 ${fmtDuration(info.firstTokenMS)}`)
  if (info.durationMS > 0) parts.push(`总耗时 ${fmtDuration(info.durationMS)}`)
  if (info.used || info.size) parts.push(`上下文 ${fmtContext(info.used, info.size)}`)
  if (info.costAmount) parts.push(fmtCost(info.costAmount, info.costCurrency))
  return parts.join(' · ')
}
