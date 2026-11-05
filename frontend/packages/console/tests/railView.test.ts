import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { sessionBadge } from '../src/console/railView.ts'

// 会话行状态徽标（设计 §15）：口径只有这一个纯函数，测试钉住"从哪个字段读、映射到哪一态、何时不显示"。

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

  it('cancelled / interrupted / idle 不显示徽标（不把取消/中断说成已完成）', () => {
    assert.equal(sessionBadge({ status: 'cancelled' }), null)
    assert.equal(sessionBadge({ status: 'interrupted' }), null)
    assert.equal(sessionBadge({ status: 'completed', run: { status: 'idle' } })?.kind, 'done')
  })
})
