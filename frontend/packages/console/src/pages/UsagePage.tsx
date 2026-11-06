import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Empty, Input, Segmented, Space, Spin, Statistic, Tooltip } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import { toErrorMessage, usageSummary } from '@ark-mate/api'
import type { UsageBucket, UsagePurpose, UsageSummary } from '@ark-mate/types'
import { useMate } from '../store'
import {
  fmtContext,
  fmtCost,
  fmtDuration,
  fmtRate,
  fmtTokens,
  purposeShare,
  reportedLabel,
  successRate,
  usageRows,
  type UsageRow,
} from '../console/usageView'

// ——— 用量视图（方向 C：总览 + 分节；GET /usage/summary） ———
//
// 两类口径**分区呈现、不混算**：
//   - 会话用量（runs）：计数/时长按区间聚合；上下文/成本为**区间末快照**（不可跨天相加）；
//   - 模型调用（llm_calls）：流水口径，一次调用一行、可跨调用相加；含无会话的旁路调用，
//     并区分失败率与降级率。
// 顶部总览把两类口径并排给出，一眼看全；下方分行看细节。
//
// v19：`group_by` 只有 day（撤掉了 agent 维度）。诚实降级：内存模式 40501 显示后端原文。

type RangeMode = '7d' | '30d' | 'custom' | 'all'

const RANGES: { label: string; value: RangeMode }[] = [
  { label: '近 7 天', value: '7d' },
  { label: '近 30 天', value: '30d' },
  { label: '自定义', value: 'custom' },
  { label: '全部', value: 'all' },
]

function isoDay(d: Date): string {
  const p = (n: number): string => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

function rangeToQuery(mode: RangeMode, fromDay: string, toDay: string): { from?: string; to?: string } {
  if (mode === 'all') return {}
  if (mode === '7d' || mode === '30d') {
    const n = mode === '7d' ? 7 : 30
    const from = new Date(Date.now() - n * 86_400_000)
    return { from: isoDay(from) }
  }
  const q: { from?: string; to?: string } = {}
  if (fromDay) q.from = fromDay
  if (toDay) {
    const t = new Date(`${toDay}T00:00:00`)
    t.setDate(t.getDate() + 1)
    q.to = isoDay(t)
  }
  return q
}

function fmtDay(v?: string): string {
  return v ? v.slice(5) : '不限'
}

function fmtPct(v: number | null): string {
  return v == null ? '—' : `${(v * 100).toFixed(0)}%`
}

/** 成功率可视化条：绿=完成、红=失败；无终态 → 空条。 */
function RateBar({ ok, total }: { ok: number; total: number }) {
  const w = total > 0 ? Math.max(2, Math.round((ok / total) * 100)) : 0
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
      <span style={{ flex: 'none', width: 92, height: 7, background: tokens.softFill, borderRadius: 4, overflow: 'hidden', display: 'inline-flex' }}>
        <i style={{ display: 'block', height: '100%', width: `${w}%`, background: tokens.success }} />
        <i style={{ display: 'block', height: '100%', width: `${total > 0 ? Math.max(0, 100 - w) : 0}%`, background: tokens.error }} />
      </span>
      <span style={{ fontFamily: tokens.fontMono, fontSize: 12 }}>{fmtPct(total > 0 ? ok / total : null)}</span>
    </span>
  )
}

/** 会话用量行（保留原有列）。 */
function BucketCells({ b }: { b: UsageBucket }) {
  const done = b.completed + b.failed + b.cancelled
  const rate = successRate(b)
  return (
    <>
      <span style={{ fontFamily: tokens.fontMono }}>{b.runs}</span>
      <span style={{ color: tokens.success }}>{b.completed}</span>
      <span style={{ color: b.failed > 0 ? tokens.error : tokens.textPlaceholder }}>{b.failed}</span>
      <span style={{ color: b.cancelled > 0 ? tokens.warning : tokens.textPlaceholder }}>{b.cancelled}</span>
      <span title={rate == null ? '无终态轮次' : undefined}>
        <RateBar ok={b.completed} total={done} />
      </span>
      <span style={{ fontFamily: tokens.fontMono }}>{fmtDuration(b.duration_ms)}</span>
      <span style={{ fontFamily: tokens.fontMono }} title={`上下文为区间末快照 · ${reportedLabel(b.usage_reported, b.sessions)}`}>
        {fmtContext(b.context_used, b.context_size)}
      </span>
      <span style={{ fontFamily: tokens.fontMono }} title={`会话累计费用快照 · ${reportedLabel(b.cost_reported, b.sessions)}`}>
        {fmtCost(b.cost_amount, b.cost_currency, b.cost_by_currency)}
      </span>
    </>
  )
}

const CELL: React.CSSProperties = { fontSize: 12.5, textAlign: 'right', whiteSpace: 'nowrap' }
const TCellL: React.CSSProperties = { ...CELL, textAlign: 'left' }
const TH: React.CSSProperties = { ...CELL, color: tokens.textSecondary, fontWeight: 500, padding: '8px 10px', borderBottom: `1px solid ${tokens.border}` }
const THL: React.CSSProperties = { ...TH, textAlign: 'left' }
const TD: React.CSSProperties = { ...CELL, padding: '8px 10px', borderBottom: `1px solid ${tokens.border}` }
const TDL: React.CSSProperties = { ...TD, textAlign: 'left' }
void TCellL

/** 用途占比条。 */
function ShareBar({ p, total }: { p: UsagePurpose; total: number }) {
  const share = purposeShare(p.calls, total)
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
      <span style={{ width: 90, height: 7, background: tokens.softFill, borderRadius: 4, overflow: 'hidden', display: 'inline-block' }}>
        <i style={{ display: 'block', height: '100%', width: `${Math.round(share * 100)}%`, background: tokens.primary }} />
      </span>
      <span style={{ fontFamily: tokens.fontMono, fontSize: 11.5, color: tokens.textSecondary }}>{Math.round(share * 100)}%</span>
    </span>
  )
}

/** 用途展示名（purpose 常量 → 可读中文；未知原样回显，不猜）。 */
const PURPOSE_LABEL: Record<string, string> = {
  summary: '摘要',
  title: '标题',
  judge: '判别',
  extract: '抽取',
  rewrite: '改写',
  embed: '向量',
  agent_turn: '会话轮次',
}

export function UsagePage() {
  const showToast = useMate((s) => s.showToast)

  const [mode, setMode] = useState<RangeMode>('7d')
  const [fromDay, setFromDay] = useState<string>(isoDay(new Date(Date.now() - 7 * 86_400_000)))
  const [toDay, setToDay] = useState<string>(isoDay(new Date()))
  const [data, setData] = useState<UsageSummary | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setErr(null)
    try {
      const { from, to } = rangeToQuery(mode, fromDay, toDay)
      setData(
        await usageSummary({
          from,
          to,
          group_by: 'day',
          tz_offset: -new Date().getTimezoneOffset(),
        }),
      )
    } catch (e) {
      setData(null)
      setErr(toErrorMessage(e))
      showToast(toErrorMessage(e))
    } finally {
      setLoading(false)
    }
  }, [mode, fromDay, toDay, showToast])

  useEffect(() => {
    void load()
  }, [load])

  const rows = usageRows(data)
  const llm = data?.llm
  const sideCalls = llm ? Math.max(0, llm.calls - llm.session_calls) : 0

  const head = (label: string, key: string, align: 'l' | 'r' = 'r') => (
    <th key={key} style={align === 'l' ? THL : TH}>
      {label}
    </th>
  )

  return (
    <PageContainer title="用量" description="一屏总览会话与模型调用；会话用量看快照，模型调用看流水。">
      {/* —— 顶部总览四卡 —— */}
      {!err && data && (
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(4, minmax(0, 1fr))',
            gap: 1,
            background: tokens.border,
            borderBottom: `1px solid ${tokens.border}`,
          }}
        >
          <div style={{ background: tokens.surface, padding: '14px 16px' }}>
            <div style={{ fontSize: 12, color: tokens.textSecondary }}>会话轮次</div>
            <div style={{ fontSize: 24, fontWeight: 650, letterSpacing: '-0.02em', marginTop: 2 }}>{data.totals.runs}</div>
            <div style={{ fontSize: 11.5, color: tokens.textSecondary, marginTop: 3 }}>
              完成 {data.totals.completed} · <span style={{ color: tokens.error }}>失败 {data.totals.failed}</span>
            </div>
          </div>
          <div style={{ background: tokens.surface, padding: '14px 16px' }}>
            <div style={{ fontSize: 12, color: tokens.textSecondary }}>模型调用 · 流水</div>
            <div style={{ fontSize: 24, fontWeight: 650, letterSpacing: '-0.02em', marginTop: 2 }}>
              {llm?.calls ?? 0}
              <span style={{ fontSize: 12, color: tokens.textPlaceholder, fontWeight: 400, marginLeft: 3 }}>次</span>
            </div>
            <div style={{ fontSize: 11.5, color: tokens.textSecondary, marginTop: 3 }}>
              {llm?.failed ?? 0} 失败 · <span style={{ color: tokens.warning }}>{llm?.degraded ?? 0} 降级</span>
            </div>
          </div>
          <div style={{ background: tokens.surface, padding: '14px 16px' }}>
            <div style={{ fontSize: 12, color: tokens.textSecondary }}>Token 合计</div>
            <div style={{ fontSize: 24, fontWeight: 650, letterSpacing: '-0.02em', marginTop: 2 }}>
              {fmtTokens(llm?.tokens.total_tokens ?? 0)}
            </div>
            <div style={{ fontSize: 11.5, color: tokens.textSecondary, marginTop: 3 }}>
              输入 {fmtTokens(llm?.tokens.input_tokens ?? 0)} · 输出 {fmtTokens(llm?.tokens.output_tokens ?? 0)}
            </div>
          </div>
          <div style={{ background: tokens.surface, padding: '14px 16px' }}>
            <div style={{ fontSize: 12, color: tokens.textSecondary }}>
              成本 <span style={{ fontSize: 10.5, color: tokens.textPlaceholder }}>会话快照</span>
            </div>
            <div style={{ fontSize: 24, fontWeight: 650, letterSpacing: '-0.02em', marginTop: 2 }}>
              {fmtCost(data.totals.cost_amount, data.totals.cost_currency, data.totals.cost_by_currency)}
            </div>
            <div style={{ fontSize: 11.5, color: tokens.textSecondary, marginTop: 3 }}>两套口径分开看，不跨口径相加</div>
          </div>
        </div>
      )}

      {/* —— 过滤条 —— */}
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, padding: '12px 16px', borderBottom: `1px solid ${tokens.border}` }}>
        <Segmented size="small" value={mode} onChange={(v) => setMode(v as RangeMode)} options={RANGES} />
        {mode === 'custom' && (
          <Space size={6}>
            <Input size="small" type="date" value={fromDay} onChange={(e) => setFromDay(e.target.value)} style={{ width: 140 }} />
            <span style={{ color: tokens.textSecondary, fontSize: 12 }}>至</span>
            <Input size="small" type="date" value={toDay} onChange={(e) => setToDay(e.target.value)} style={{ width: 140 }} />
            <span style={{ color: tokens.textPlaceholder, fontSize: 11.5 }}>（含当天）</span>
          </Space>
        )}
        <span style={{ marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 8 }}>
          {data && (
            <span style={{ fontSize: 12, color: tokens.textSecondary }}>
              区间 {fmtDay(data.from)} ~ {data.to ? fmtDay(data.to) : '至今'}
            </span>
          )}
          {/* 刷新并入过滤条右端（不再单开一行工具栏，避免左侧留空一整条）。 */}
          <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={() => void load()}>
            刷新
          </Button>
        </span>
      </div>

      {err && (
        <div style={{ padding: 16 }}>
          <Alert type="warning" showIcon message="用量统计不可用" description={err} />
        </div>
      )}

      {!err && loading && !data && (
        <div style={{ padding: 40, textAlign: 'center' }}>
          <Spin />
        </div>
      )}

      {!err && data && (
        <>
          {/* ===== 会话用量 ===== */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '14px 16px 10px' }}>
            <span style={{ fontSize: 13.5, fontWeight: 600 }}>会话用量</span>
            <span style={{ fontSize: 10.5, padding: '1px 6px', borderRadius: 5, border: `1px solid ${tokens.border}`, color: tokens.textSecondary }}>
              快照口径 · 不可跨天相加
            </span>
          </div>
          {rows.length === 0 ? (
            <div style={{ padding: 40 }}>
              <Empty description="该区间内没有轮次记录" />
            </div>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table style={{ width: '100%', borderCollapse: 'collapse' }}>
                <thead>
                  <tr>
                    {head('日期', 'd', 'l')}
                    {['轮次', '完成', '失败', '取消', '成功率', '时长', '上下文', '成本'].map((h) => head(h, h))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r: UsageRow) => (
                    <tr key={r.key}>
                      <td style={TDL}>{r.label}</td>
                      <BucketCells b={r.bucket} />
                    </tr>
                  ))}
                  <tr style={{ background: tokens.bg }}>
                    <td style={{ ...TDL, fontWeight: 600 }}>{data.buckets_additive ? '合计' : '区间末'}</td>
                    <td colSpan={8} style={TDL}>
                      <span style={{ display: 'inline-flex', gap: 14, flexWrap: 'wrap', fontFamily: tokens.fontMono, fontWeight: 600 }}>
                        <span>{data.totals.runs} 轮</span>
                        <span style={{ color: tokens.success }}>{data.totals.completed} 完成</span>
                        <span style={{ color: tokens.error }}>{data.totals.failed} 失败</span>
                        <span>{fmtDuration(data.totals.duration_ms)}</span>
                        <span>{fmtCost(data.totals.cost_amount, data.totals.cost_currency, data.totals.cost_by_currency)}</span>
                      </span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          )}

          {/* ===== 模型调用 ===== */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '14px 16px 10px', borderTop: `1px solid ${tokens.border}` }}>
            <span style={{ fontSize: 13.5, fontWeight: 600 }}>模型调用</span>
            <span style={{ fontSize: 10.5, padding: '1px 6px', borderRadius: 5, border: `1px solid ${tokens.primarySoft}`, color: tokens.primary, background: tokens.primarySoft }}>
              llm_calls · 流水口径 · 可相加
            </span>
          </div>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 28, padding: '4px 16px 14px', borderBottom: `1px solid ${tokens.border}` }}>
            <Statistic title="调用次数" value={llm?.calls ?? 0} valueStyle={{ fontSize: 18 }} />
            <Statistic title="Token 合计" value={fmtTokens(llm?.tokens.total_tokens ?? 0)} valueStyle={{ fontSize: 18 }} />
            <Tooltip title="失败（error/timeout/cancelled）占全部调用比例">
              <Statistic title="失败率" value={fmtRate(llm?.failed ?? 0, llm?.calls ?? 0)} valueStyle={{ fontSize: 18, color: tokens.error }} />
            </Tooltip>
            <Tooltip title="降级（degraded，用兜底值返回）占全部调用比例">
              <Statistic title="降级率" value={fmtRate(llm?.degraded ?? 0, llm?.calls ?? 0)} valueStyle={{ fontSize: 18, color: tokens.warning }} />
            </Tooltip>
            <Tooltip title="旁路调用（无会话）/ 会话调用">
              <Statistic title="旁路 / 会话" value={`${sideCalls} / ${llm?.session_calls ?? 0}`} valueStyle={{ fontSize: 18 }} />
            </Tooltip>
          </div>
          {llm && llm.by_purpose.length > 0 ? (
            <div style={{ overflowX: 'auto' }}>
              <table style={{ width: '100%', borderCollapse: 'collapse' }}>
                <thead>
                  <tr>
                    {head('用途', 'p', 'l')}
                    {head('占比', 'share', 'l')}
                    {['调用', '输入', '输出', '缓存', '思考', '合计 Token', '成本'].map((h) => head(h, h))}
                  </tr>
                </thead>
                <tbody>
                  {llm.by_purpose.map((p) => (
                    <tr key={p.purpose}>
                      <td style={TDL}>
                        <span style={{ display: 'inline-block', fontSize: 10.5, padding: '1px 7px', borderRadius: 999, background: tokens.primarySoft, color: tokens.primary }}>
                          {PURPOSE_LABEL[p.purpose] ?? p.purpose}
                        </span>
                      </td>
                      <td style={TDL}>
                        <ShareBar p={p} total={llm.calls} />
                      </td>
                      <td style={{ ...TD, fontFamily: tokens.fontMono }}>
                        {p.calls}
                        {p.failed > 0 && (
                          <span style={{ color: tokens.error }} title="失败"> ·{p.failed}</span>
                        )}
                        {p.degraded > 0 && (
                          <span style={{ color: tokens.warning }} title="降级"> ·{p.degraded}</span>
                        )}
                      </td>
                      <td style={{ ...TD, fontFamily: tokens.fontMono }}>{fmtTokens(p.tokens.input_tokens)}</td>
                      <td style={{ ...TD, fontFamily: tokens.fontMono }}>{fmtTokens(p.tokens.output_tokens)}</td>
                      <td style={{ ...TD, fontFamily: tokens.fontMono }}>{fmtTokens(p.tokens.cached_read_tokens)}</td>
                      <td style={{ ...TD, fontFamily: tokens.fontMono }}>{p.tokens.thought_tokens ? fmtTokens(p.tokens.thought_tokens) : '—'}</td>
                      <td style={{ ...TD, fontFamily: tokens.fontMono }}>{fmtTokens(p.tokens.total_tokens)}</td>
                      <td style={{ ...TD, fontFamily: tokens.fontMono }}>{fmtCost(p.cost_amount, p.cost_currency)}</td>
                    </tr>
                  ))}
                  <tr style={{ background: tokens.bg }}>
                    <td style={{ ...TDL, fontWeight: 600 }}>合计</td>
                    <td style={TDL} />
                    <td style={{ ...TD, fontFamily: tokens.fontMono, fontWeight: 600 }}>{llm.calls}</td>
                    <td style={{ ...TD, fontFamily: tokens.fontMono }}>{fmtTokens(llm.tokens.input_tokens)}</td>
                    <td style={{ ...TD, fontFamily: tokens.fontMono }}>{fmtTokens(llm.tokens.output_tokens)}</td>
                    <td style={{ ...TD, fontFamily: tokens.fontMono }}>{fmtTokens(llm.tokens.cached_read_tokens)}</td>
                    <td style={{ ...TD, fontFamily: tokens.fontMono }}>{llm.tokens.thought_tokens ? fmtTokens(llm.tokens.thought_tokens) : '—'}</td>
                    <td style={{ ...TD, fontFamily: tokens.fontMono, fontWeight: 600 }}>{fmtTokens(llm.tokens.total_tokens)}</td>
                    <td style={TD} />
                  </tr>
                </tbody>
              </table>
            </div>
          ) : (
            <div style={{ padding: 32 }}>
              <Empty description="该区间内没有内部模型调用记录" />
            </div>
          )}

          <div style={{ padding: '10px 12px', color: tokens.textSecondary, fontSize: 11.5, lineHeight: 1.6 }}>
            会话用量为「区间末各会话累计快照」，跨天会话会重复计入，不可跨天相加；模型调用为流水口径（每次调用一行、可相加）。
            成本分「会话快照」与「模型流水」两套口径，分开呈现，不跨口径相加。
          </div>
        </>
      )}
    </PageContainer>
  )
}
