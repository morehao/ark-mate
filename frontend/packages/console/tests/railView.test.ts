import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { sessionBadge } from '../src/console/railView.ts'
import { absTime, fullTime, sessDate, updatedLabel } from '../src/console/time.ts'

// 会话行右端（设计 §15；2026-11-05 反馈改成「状态点 + 日期」）：
// 状态口径（`sessionBadge`）与日期口径（`sessDate`）各只有一个纯函数，测试钉住读哪个字段、怎么格式化。

describe('会话行状态徽标', () => {
  it('run 优先：waiting_for_permission / waiting_for_user → 待授权', () => {
    assert.deepEqual(sessionBadge({ status: 'in_progress', run: { status: 'waiting_for_permission' } }), {
      kind: 'wait',
      label: '待授权',
    })
    assert.deepEqual(sessionBadge({ status: 'in_progress', run: { status: 'waiting_for_user' } }), {
      kind: 'wait',
      label: '待授权',
    })
  })

  it('run 忙碌阶段 → 运行中', () => {
    for (const status of [
      'preparing',
      'model_requesting',
      'model_streaming',
      'tool_executing',
      'retrying',
      'compacting',
      'cancelling',
    ] as const) {
      assert.deepEqual(sessionBadge({ status: 'completed', run: { status } }), {
        kind: 'run',
        label: '运行中',
      })
    }
  })

  it('run=idle/interrupted 时回落到快照 status（不直接下结论）', () => {
    assert.deepEqual(sessionBadge({ status: 'failed', run: { status: 'idle' } }), { kind: 'fail', label: '失败' })
    assert.deepEqual(sessionBadge({ status: 'completed', run: { status: 'interrupted' } }), {
      kind: 'done',
      label: '已完成',
    })
  })

  it('无 run 时读快照 status：failed/completed/in_progress', () => {
    assert.deepEqual(sessionBadge({ status: 'failed' }), { kind: 'fail', label: '失败' })
    assert.deepEqual(sessionBadge({ status: 'completed' }), { kind: 'done', label: '已完成' })
    assert.deepEqual(sessionBadge({ status: 'in_progress' }), { kind: 'run', label: '运行中' })
  })

  it('取消 / 中断各有自己的态（不再当成「没有状态」）', () => {
    assert.deepEqual(sessionBadge({ status: 'cancelled' }), { kind: 'cancel', label: '已取消' })
    assert.deepEqual(sessionBadge({ status: 'interrupted' }), { kind: 'interrupt', label: '已中断' })
    // run.status=interrupted 时回落到快照口径，结论一致。
    assert.deepEqual(sessionBadge({ status: 'interrupted', run: { status: 'interrupted' } }), {
      kind: 'interrupt',
      label: '已中断',
    })
  })

  it('run=idle 时不自己下结论：仍按快照 status 归类', () => {
    assert.equal(sessionBadge({ status: 'completed', run: { status: 'idle' } })?.kind, 'done')
    assert.equal(sessionBadge({ status: 'cancelled', run: { status: 'idle' } })?.kind, 'cancel')
  })

  it('六态的 kind 两两不同（颜色不会被两种状态共用）', () => {
    const kinds = (['completed', 'failed', 'cancelled', 'interrupted', 'in_progress'] as const).map(
      (status) => sessionBadge({ status })?.kind,
    )
    kinds.push(sessionBadge({ status: 'in_progress', run: { status: 'waiting_for_permission' } })?.kind)
    assert.equal(new Set(kinds).size, kinds.length)
  })
})

describe('会话行日期标签（sessDate）', () => {
  const now = new Date(2026, 10, 5, 12, 0, 0).getTime() // 2026-11-05 12:00 本地时间
  // 用本地时间构造再转毫秒：断言不依赖跑测试那台机器的时区。
  const at = (y: number, m: number, d: number): number => new Date(y, m - 1, d, 10, 0, 0).getTime()

  it('同年 → MM-DD（补零）', () => {
    assert.equal(sessDate(at(2026, 11, 5), now), '11-05')
    assert.equal(sessDate(at(2026, 1, 9), now), '01-09')
  })

  it('跨年 → YYYY-MM-DD', () => {
    assert.equal(sessDate(at(2025, 12, 31), now), '2025-12-31')
  })

  it('空 / 非法时间 → 空串（不显示噪声）', () => {
    assert.equal(sessDate(undefined, now), '')
    assert.equal(sessDate(0, now), '')
  })

  it('不用相对时间（行内口径稳定，不会随时间跳动）', () => {
    // 同一时刻下，"3 分钟前"这种口径会随 now 变；日期不会。
    const ts = at(2026, 11, 5)
    assert.equal(sessDate(ts, now), sessDate(ts, now + 3 * 60_000))
  })
})

// 2026-11-05 反馈：浮层里原来直接显示服务端下发的 RFC3339 UTC（`2026-11-05T10:42:21Z`），
// 既不是用户时区、又带协议噪声。这里钉住"本地化 + 去掉秒"的口径。
describe('会话信息浮层的时间标签（absTime / updatedLabel）', () => {
  const now = new Date(2026, 10, 5, 18, 45, 0).getTime() // 2026-11-05 18:45 本地时间
  // 用本地时间构造再转毫秒：断言不依赖跑测试那台机器的时区。
  const at = (y: number, m: number, d: number, h: number, min: number): number =>
    new Date(y, m - 1, d, h, min, 0).getTime()

  it('当天 → HH:mm；同年 → MM-DD HH:mm；跨年 → YYYY-MM-DD HH:mm', () => {
    assert.equal(absTime(at(2026, 11, 5, 9, 5), now), '09:05')
    assert.equal(absTime(at(2026, 11, 2, 9, 12), now), '11-02 09:12')
    assert.equal(absTime(at(2025, 12, 31, 23, 59), now), '2025-12-31 23:59')
  })

  it('updatedLabel：相对时间 + 本地绝对时间；相对口径已是日期时只留绝对时间', () => {
    assert.equal(updatedLabel(now - 10_000, now), '刚刚 · 18:44')
    assert.equal(updatedLabel(at(2026, 11, 5, 18, 44), now), '1 分钟前 · 18:44')
    assert.equal(updatedLabel(at(2026, 11, 2, 9, 12), now), '3 天前 · 11-02 09:12')
    // ≥7 天：relTime 已是 `MM-DD`，不再叠一遍日期。
    assert.equal(updatedLabel(at(2026, 10, 20, 9, 12), now), '10-20 09:12')
  })

  it('不出现 T/Z 协议形态；空 / 非法时间 → 空串', () => {
    const label = updatedLabel(at(2026, 11, 5, 9, 5), now)
    assert.ok(!label.includes('T') && !label.endsWith('Z'), `不应含协议噪声: ${label}`)
    assert.equal(updatedLabel(undefined, now), '')
    assert.equal(updatedLabel(0, now), '')
    assert.equal(absTime(undefined, now), '')
  })

  it('fullTime 只给 tooltip：精确到秒的本地时间', () => {
    assert.equal(fullTime(at(2026, 11, 5, 9, 5)), '2026-11-05 09:05:00')
    assert.equal(fullTime(undefined), '')
  })
})
