import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import type { StreamEntry } from '@ark-mate/types'

import { toolDetail } from '../src/console/transcriptView.ts'

const tool = (id: string, title: string, extra: Partial<StreamEntry> = {}): StreamEntry => ({
  kind: 'tool',
  id,
  title,
  ...extra,
})

describe('todo_write 投影', () => {
  it('合法 todos：trim、去空、去重并保留三态状态', () => {
    const detail = toolDetail(tool('t1', 'todo_write', {
      status: 'completed',
      input: {
        todos: [
          { content: '  写计划  ', status: 'completed' },
          { content: '写代码', status: 'in_progress' },
          { content: '  ', status: 'pending' },
          { content: '写计划', status: 'pending' },
          { content: '跑测试', status: 'pending' },
        ],
      },
    }))
    assert.equal(detail?.kind, 'todo')
    assert.deepEqual(detail?.todos, [
      { content: '写计划', status: 'completed' },
      { content: '写代码', status: 'in_progress' },
      { content: '跑测试', status: 'pending' },
    ])
    assert.ok(!detail?.notes.some((note) => note.includes('状态')))
  })

  it('status 非法归 pending 并推 notes', () => {
    const detail = toolDetail(tool('t2', 'todo_write', {
      status: 'completed',
      input: {
        todos: [
          { content: '甲', status: 'doing' },
          { content: '乙', status: 'done' },
          { content: '丙', status: 'in_progress' },
        ],
      },
    }))
    assert.equal(detail?.kind, 'todo')
    assert.deepEqual(detail?.todos?.map((t) => t.status), ['pending', 'pending', 'in_progress'])
    assert.ok(detail?.notes.some((note) => note.includes('状态')))
  })

  it('标题大小写不敏感（TodoWrite / TODO_WRITE）', () => {
    const upper = toolDetail(tool('t3', 'TODO_WRITE', {
      status: 'completed',
      input: { todos: [{ content: '项', status: 'pending' }] },
    }))
    assert.equal(upper?.kind, 'todo')
    const camel = toolDetail(tool('t4', 'TodoWrite', {
      status: 'completed',
      input: { todos: [{ content: '项', status: 'pending' }] },
    }))
    assert.equal(camel?.kind, 'todo')
  })

  it('空 / 畸形 input → null（回落通用卡）', () => {
    assert.equal(toolDetail(tool('t5', 'todo_write', { status: 'completed' })), null)
    assert.equal(toolDetail(tool('t6', 'todo_write', { status: 'completed', input: {} })), null)
    assert.equal(toolDetail(tool('t7', 'todo_write', { status: 'completed', input: { todos: [] } })), null)
    // 全是空内容 → 解析不出任何项 → null
    assert.equal(
      toolDetail(tool('t8', 'todo_write', { status: 'completed', input: { todos: [{ content: '  ' }] } })),
      null,
    )
  })
})

describe('exit_plan_mode 投影', () => {
  it('plan 正文多键容错：plan / content / markdown / text 都能取', () => {
    for (const key of ['plan', 'content', 'markdown', 'text']) {
      const detail = toolDetail(tool(`p-${key}`, 'exit_plan_mode', {
        status: 'completed',
        input: { [key]: `# 计划\n- 步骤 via ${key}` },
      }))
      assert.equal(detail?.kind, 'plan', `key=${key}`)
      assert.equal(detail?.planMD, `# 计划\n- 步骤 via ${key}`)
    }
  })

  it('approved 从结果透传（true / false）', () => {
    const approved = toolDetail(tool('p1', 'exit_plan_mode', {
      status: 'completed',
      input: { plan: '# 计划' },
      output: { approved: true },
    }))
    assert.equal(approved?.approved, true)

    const rejected = toolDetail(tool('p2', 'exit_plan_mode', {
      status: 'completed',
      input: { plan: '# 计划' },
      output: { approved: false },
    }))
    assert.equal(rejected?.approved, false)
  })

  it('approved 缺省则不设该字段（不猜）', () => {
    const detail = toolDetail(tool('p3', 'exit_plan_mode', {
      status: 'completed',
      input: { plan: '# 计划' },
    }))
    assert.equal(detail?.kind, 'plan')
    assert.equal(detail?.approved, undefined)
  })

  it('正文取不到 → null（回落通用卡）', () => {
    assert.equal(toolDetail(tool('p4', 'exit_plan_mode', { status: 'completed' })), null)
    assert.equal(toolDetail(tool('p5', 'exit_plan_mode', { status: 'completed', input: {} })), null)
  })
})
