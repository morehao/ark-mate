import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { applyInputUpdated, readAttachments } from '../src/console/queueUpdate.ts'
import type { QueuedInput } from '@ark-mate/types'

/**
 * v4 队列行内编辑的就地替换（纯函数）：改正文/附件，不出队、不改顺序；队项不在 → no-op。
 */

const q = (id: string, text: string, extra: Partial<QueuedInput> = {}): QueuedInput => ({
  inputId: id,
  text,
  delivery: 'after_run',
  ...extra,
})

describe('applyInputUpdated', () => {
  it('就地替换正文 + 附件，不出队、不改顺序', () => {
    const queue = [q('a', '第一条'), q('b', '第二条')]
    const out = applyInputUpdated(queue, 'a', '第一条（改）', ['att_x'])
    assert.equal(out.length, 2)
    assert.equal(out[0].inputId, 'a')
    assert.equal(out[1].inputId, 'b')
    assert.equal(out[0].text, '第一条（改）')
    assert.deepEqual(out[0].attachments, ['att_x'])
    // 不改原数组（纯函数）
    assert.equal(queue[0].text, '第一条')
  })

  it('只改文本、不动附件（attachments 缺省 → 保留原值）', () => {
    const queue = [q('a', 'x', { attachments: ['att_old'] })]
    const out = applyInputUpdated(queue, 'a', 'y')
    assert.equal(out[0].text, 'y')
    assert.deepEqual(out[0].attachments, ['att_old'])
  })

  it('队项不在 → 原样返回（no-op，不复活已离队项）', () => {
    const queue = [q('a', 'x')]
    assert.equal(applyInputUpdated(queue, 'zzz', 'y'), queue)
  })

  it('空正文不覆盖（保留原文本）', () => {
    const queue = [q('a', 'x')]
    assert.equal(applyInputUpdated(queue, 'a', '')[0].text, 'x')
  })
})

describe('readAttachments', () => {
  it('数组 → 字符串化；非数组 → undefined', () => {
    assert.deepEqual(readAttachments(['a', 'b']), ['a', 'b'])
    assert.equal(readAttachments(undefined), undefined)
    assert.equal(readAttachments('nope'), undefined)
  })
})
