import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Empty, Input, Segmented, Select, Space, Spin, Statistic, Tag, Tooltip } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import { toErrorMessage, usageSummary } from '@ark-mate/api'
import type { Agent, UsageBucket, UsageSummary } from '@ark-mate/types'
import { useMate } from '../store'
import { fmtContext, fmtCost, fmtDuration, reportedLabel, successRate, usageRows, type UsageRow } from '../console/usageView'

// ——— 用量视图（M3 GET /usage/summary；sqlite 模式才有数据） ———
// 维度：agent（默认，按轮次降序）/ day（按日期升序）；筛选：时间区间（半开）+ agent。
// 诚实降级：内存模式（MATE_STORE=memory）后端返回 40501，此处显示后端原文，不臆造数字。
// 口径（后端回显 context_semantics/cost_semantics/buckets_additive）：上下文与成本都是
// **区间末快照**（各会话取区间内最后一次上报后求和），不是区间增量；day 分组不可相加。

/** 时间范围预设：近 7/30 天、自定义（起止日期）、全部。 */
type RangeMode = '7d' | '30d' | 'custom' | 'all'

const RANGES: { label: string; value: RangeMode }[] = [
  { label: '近 7 天', value: '7d' },
  { label: '近 30 天', value: '30d' },
  { label: '自定义', value: 'custom' },
  { label: '全部', value: 'all' },
]

/** 本地日期 → `YYYY-MM-DD`（DatePicker 不参与，避免额外依赖）。 */
function isoDay(d: Date): string {
  const p = (n: number): string => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

/** 起止（含当天）→ 后端半开区间 [from, to)：`to` 取次日。 */
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

/** `YYYY-MM-DD` → 展示用 `MM-DD`。 */
function fmtDay(v?: string): string {
  return v ? v.slice(5) : '不限'
}

function fmtPct(v: number | null): string {
  return v == null ? '—' : `${(v * 100).toFixed(0)}%`
}

function BucketCells({ b }: { b: UsageBucket }) {
  const rate = successRate(b)
  return (
    <>
      <span style={{ fontFamily: tokens.fontMono }}>{b.runs}</span>
      <span style={{ color: tokens.success }}>{b.completed}</span>
      <span style={{ color: b.failed > 0 ? tokens.error : tokens.textPlaceholder }}>{b.failed}</span>
      <span style={{ color: b.cancelled > 0 ? tokens.warning : tokens.textPlaceholder }}>{b.cancelled}</span>
      <span style={{ color: rate == null ? tokens.textPlaceholder : tokens.text }}>{fmtPct(rate)}</span>
      <span style={{ fontFamily: tokens.fontMono }}>{fmtDuration(b.duration_ms)}</span>
      <span style={{ fontFamily: tokens.fontMono }} title={`上下文为区间末快照 · ${reportedLabel(b.usage_reported, b.conversations)}`}>
        {fmtContext(b.context_used, b.context_size)}
      </span>
      <span style={{ fontFamily: tokens.fontMono }} title={`会话累计费用快照 · ${reportedLabel(b.cost_reported, b.conversations)}`}>
        {fmtCost(b.cost_amount, b.cost_currency, b.cost_by_currency)}
      </span>
    </>
  )
}

const CELL: React.CSSProperties = { fontSize: 12.5, textAlign: 'right', whiteSpace: 'nowrap' }

/** 用量页（M3 首项的可视化面）。 */
export function UsagePage() {
  const agents = useMate((s) => s.agents)
  const refreshAgents = useMate((s) => s.refreshAgents)
  const showToast = useMate((s) => s.showToast)

  const [mode, setMode] = useState<RangeMode>('7d')
  const [fromDay, setFromDay] = useState<string>(isoDay(new Date(Date.now() - 7 * 86_400_000)))
  const [toDay, setToDay] = useState<string>(isoDay(new Date()))
  const [agentId, setAgentId] = useState<string>('')
  const [groupBy, setGroupBy] = useState<'agent' | 'day'>('agent')
  const [data, setData] = useState<UsageSummary | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setErr(null)
    try {
      const { from, to } = rangeToQuery(mode, fromDay, toDay)
      setData(await usageSummary({ from, to, agent_id: agentId || undefined, group_by: groupBy }))
    } catch (e) {
      setData(null)
      setErr(toErrorMessage(e))
      showToast(toErrorMessage(e))
    } finally {
      setLoading(false)
    }
  }, [mode, fromDay, toDay, agentId, groupBy, showToast])

  useEffect(() => {
    void refreshAgents()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const rows = usageRows(data)
  const agentName = (id?: string): string => (id ? (agents.find((a: Agent) => a.id === id)?.name ?? id) : '')

  const head = (label: string, key: string) => (
    <th key={key} style={{ ...CELL, color: tokens.textSecondary, fontWeight: 500, padding: '8px 10px', borderBottom: `1px solid ${tokens.border}` }}>
      {label}
    </th>
  )
  return (
    <PageContainer
      title="用量"
      description="按 agent / 日期聚合的轮次、时长、上下文与成本（数据来自 SQLite 读模型；内存模式无统计）。上下文与成本为区间末快照，非区间增量。"
      extra={
        <Space>
          <Segmented
            size="small"
            value={groupBy}
            onChange={(v) => setGroupBy(v as 'agent' | 'day')}
            options={[
              { label: '按 agent', value: 'agent' },
              { label: '按日期', value: 'day' },
            ]}
          />
          <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={() => void load()}>
            刷新
          </Button>
        </Space>
      }
    >
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8, padding: '12px 16px', borderBottom: `1px solid ${tokens.border}` }}>
        <Segmented
          size="small"
          value={mode}
          onChange={(v) => setMode(v as RangeMode)}
          options={RANGES}
        />
        {mode === 'custom' && (
          <Space size={6}>
            <Input size="small" type="date" value={fromDay} onChange={(e) => setFromDay(e.target.value)} style={{ width: 140 }} />
            <span style={{ color: tokens.textSecondary, fontSize: 12 }}>至</span>
            <Input size="small" type="date" value={toDay} onChange={(e) => setToDay(e.target.value)} style={{ width: 140 }} />
            <span style={{ color: tokens.textPlaceholder, fontSize: 11.5 }}>（含当天）</span>
          </Space>
        )}
        <Select
          size="small"
          allowClear
          placeholder="全部 agent"
          value={agentId || undefined}
          onChange={(v) => setAgentId(v ?? '')}
          style={{ width: 180 }}
          options={agents.map((a) => ({ label: a.name, value: a.id }))}
        />
        {data && (
          <span style={{ marginLeft: 'auto', fontSize: 12, color: tokens.textSecondary }}>
            区间 {fmtDay(data.from)} ~ {data.to ? fmtDay(data.to) : '至今'}
            <Tag style={{ marginInlineStart: 8, fontSize: 11 }} color={data.source === 'sqlite' ? 'blue' : 'default'}>
              {data.source}
            </Tag>
          </span>
        )}
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
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 28, padding: '14px 16px', borderBottom: `1px solid ${tokens.border}` }}>
            <Statistic title="轮次" value={data.totals.runs} valueStyle={{ fontSize: 20 }} />
            <Statistic
              title="完成 / 失败"
              value={`${data.totals.completed} / ${data.totals.failed}`}
              valueStyle={{ fontSize: 20 }}
            />
            <Statistic title="总时长" value={fmtDuration(data.totals.duration_ms)} valueStyle={{ fontSize: 20 }} />
            <Tooltip title="区间末各会话累计费用之和（快照，非区间增量）">
              <Statistic
                title="成本"
                value={fmtCost(data.totals.cost_amount, data.totals.cost_currency, data.totals.cost_by_currency)}
                valueStyle={{ fontSize: 20 }}
              />
            </Tooltip>
            <Tooltip title="区间末各会话上下文占用之和（快照，非峰值、非区间增量）">
              <Statistic
                title="上下文占用（区间末）"
                value={fmtContext(data.totals.context_used, data.totals.context_size)}
                valueStyle={{ fontSize: 20 }}
              />
            </Tooltip>
            <Statistic title="会话数" value={data.totals.conversations} valueStyle={{ fontSize: 20 }} />
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
                    <th style={{ ...CELL, textAlign: 'left', color: tokens.textSecondary, fontWeight: 500, padding: '8px 10px', borderBottom: `1px solid ${tokens.border}` }}>
                      {data.group_by === 'day' ? '日期' : 'Agent'}
                    </th>
                    {['轮次', '完成', '失败', '取消', '成功率', '时长', '上下文', '成本'].map((h) => head(h, h))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r: UsageRow) => (
                    <tr key={r.key}>
                      <td style={{ ...CELL, textAlign: 'left', padding: '8px 10px', borderBottom: `1px solid ${tokens.border}` }}>
                        {data.group_by === 'day' ? (
                          r.label
                        ) : (
                          <Tooltip title={r.bucket.agent_id || r.key}>
                            <span>{agentName(r.bucket.agent_id) || r.label}</span>
                          </Tooltip>
                        )}
                      </td>
                      <BucketCells b={r.bucket} />
                    </tr>
                  ))}
                  <tr style={{ background: tokens.bg }}>
                    <td style={{ ...CELL, textAlign: 'left', padding: '8px 10px', fontWeight: 600 }}>
                      {data.buckets_additive ? '合计' : '区间末'}
                    </td>
                    <td colSpan={8} style={{ ...CELL, textAlign: 'left', padding: '8px 10px' }}>
                      <span style={{ display: 'inline-flex', gap: 14, flexWrap: 'wrap', fontFamily: tokens.fontMono, fontWeight: 600 }}>
                        <span>{data.totals.runs} 轮</span>
                        <span style={{ color: tokens.success }}>{data.totals.completed} 完成</span>
                        <span style={{ color: tokens.error }}>{data.totals.failed} 失败</span>
                        <span>{fmtDuration(data.totals.duration_ms)}</span>
                        <span>{fmtCost(data.totals.cost_amount, data.totals.cost_currency, data.totals.cost_by_currency)}</span>
                      </span>
                      {!data.buckets_additive && (
                        <span style={{ marginLeft: 12, color: tokens.textSecondary, fontSize: 11.5 }}>
                          上下文/成本为快照，不可与上方各行相加（跨天会话会重复计入）
                        </span>
                      )}
                    </td>
                  </tr>
                </tbody>
              </table>
              {!data.buckets_additive && (
                <div style={{ padding: '10px 12px', color: tokens.textSecondary, fontSize: 11.5 }}>
                  按日期分组时，每一行是「当日末各会话的累计快照」；同一会话跨天会在多天各出现一次，
                  因此成本/上下文相加会重复。要看区间总量请切到「按 agent」。
                </div>
              )}
            </div>
          )}
        </>
      )}
    </PageContainer>
  )
}
