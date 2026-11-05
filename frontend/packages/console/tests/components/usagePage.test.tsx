import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'

// mock 掉 api：usageSummary 返回一份含 llm 的固定数据。
const SUMMARY = {
  group_by: 'day',
  source: 'sqlite',
  context_semantics: 'end_of_window_snapshot_sum',
  cost_semantics: 'session_cumulative_end_of_window',
  buckets_additive: false,
  totals: {
    key: 'total', runs: 128, completed: 121, failed: 5, cancelled: 2, running: 0, sessions: 23,
    duration_ms: 23_000_000, context_used: 1_900_000, context_size: 4_000_000,
    usage_reported: 23, cost_reported: 23, cost_amount: 3.82, cost_currency: 'USD',
  },
  buckets: [
    { key: '2026-11-02', runs: 24, completed: 22, failed: 0, cancelled: 0, running: 0, sessions: 5, duration_ms: 4_300_000, context_used: 39_300, context_size: 200_000, usage_reported: 5, cost_reported: 5, cost_amount: 0.68 },
  ],
  llm: {
    calls: 642, failed: 6, degraded: 12, session_calls: 32,
    tokens: { input_tokens: 3_100_000, output_tokens: 600_000, cached_read_tokens: 900_000, cached_write_tokens: 0, thought_tokens: 30_000, total_tokens: 3_700_000 },
    by_purpose: [
      { purpose: 'summary', calls: 310, failed: 2, degraded: 8, tokens: { input_tokens: 1_700_000, output_tokens: 300_000, cached_read_tokens: 500_000, cached_write_tokens: 0, thought_tokens: 0, total_tokens: 2_000_000 }, cost_amount: 1.86, cost_currency: 'USD' },
      { purpose: 'title', calls: 210, failed: 0, degraded: 4, tokens: { input_tokens: 600_000, output_tokens: 80_000, cached_read_tokens: 300_000, cached_write_tokens: 0, thought_tokens: 0, total_tokens: 700_000 }, cost_amount: 0.42 },
    ],
  },
}

vi.mock('@ark-mate/api', () => ({
  usageSummary: vi.fn(async () => SUMMARY),
  toErrorMessage: (e: unknown) => String(e),
}))

// store 的 showToast：给一个最小 stub。
vi.mock('../../src/store', () => ({
  useMate: (sel: (s: { showToast: () => void }) => unknown) => sel({ showToast: () => {} }),
}))

import { UsagePage } from '../../src/pages/UsagePage'

describe('UsagePage · 方向 C（总览 + 分节）', () => {
  beforeEach(() => vi.clearAllMocks())

  it('渲染总览四卡、模型调用分节、失败/降级率与按用途明细', async () => {
    render(<UsagePage />)
    // 等数据加载落定
    await waitFor(() => expect(screen.getByText('模型调用')).toBeInTheDocument())

    // 总览卡存在
    expect(screen.getByText('会话轮次')).toBeInTheDocument()
    expect(screen.getAllByText('Token 合计').length).toBeGreaterThan(0)
    // 模型调用分节标题 + 失败率/降级率标签
    expect(screen.getAllByText('失败率').length).toBeGreaterThan(0)
    expect(screen.getAllByText('降级率').length).toBeGreaterThan(0)
    // 用途行（中文标签映射）
    expect(screen.getByText('摘要')).toBeInTheDocument()
    expect(screen.getByText('标题')).toBeInTheDocument()
    // token 缩写渲染
    expect(screen.getAllByText(/3\.70M/).length).toBeGreaterThan(0)
  })
})
