import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import {
  describeSchedule,
  everySecondsText,
  formFromAutomation,
  scheduleFromForm,
  writeBody,
} from '../src/console/automationView.ts'
import type { Automation } from '@ark-mate/types'

// v35：interval / monthly 两种新 kind + delivery 的纯函数覆盖。

describe('describeSchedule', () => {
  it('interval 说人话（秒/分/时归一）', () => {
    assert.equal(everySecondsText(3600), '1 小时')
    assert.equal(everySecondsText(90), '90 秒')
    assert.equal(everySecondsText(1800), '30 分钟')
    assert.match(describeSchedule({ kind: 'interval', timezone: 'UTC', every_seconds: 7200 }), /每 2 小时/)
  })
  it('monthly 带日号与墙钟', () => {
    const s = describeSchedule({ kind: 'monthly', timezone: 'Asia/Shanghai', hour: 9, minute: 0, day: 15 })
    assert.match(s, /每月 15 日 09:00/)
  })
})

describe('scheduleFromForm', () => {
  it('interval 落 every_seconds 且 ≥60，清掉其它字段', () => {
    const out = scheduleFromForm({ kind: 'interval', timezone: 'UTC', every_seconds: 30 })
    assert.equal(out.every_seconds, 60) // 兜底到 60
    assert.equal(out.hour, null)
    assert.equal(out.day, null)
  })
  it('monthly 落 day，越界夹到 1–31', () => {
    const out = scheduleFromForm({ kind: 'monthly', timezone: 'UTC', hour: 9, minute: 0, day: 99 })
    assert.equal(out.day, 31)
    assert.equal(out.every_seconds, null)
  })
})

describe('formFromAutomation + writeBody（delivery）', () => {
  const base: Automation = {
    id: 'a1',
    name: 'n',
    enabled: true,
    state: 'active',
    schedule: { kind: 'daily', timezone: 'UTC', hour: 9, minute: 0 },
    target: { cwd: '', prompt: 'p', delivery: 'session', session_id: 'sess_1' },
  }
  it('空缺省 delivery 归一 session', () => {
    const f = formFromAutomation({ ...base, target: { cwd: '/x', prompt: 'p' } })
    assert.equal(f.delivery, 'session')
  })
  it('session 投递：target 带 session_id、不带 cwd', () => {
    const f = formFromAutomation(base)
    const body = writeBody(f, { create: true })
    const t = body.target as Record<string, unknown>
    assert.equal(t.delivery, 'session')
    assert.equal(t.session_id, 'sess_1')
    assert.equal(t.cwd, undefined)
  })
  it('new_session 投递：target 带 cwd、不带 session_id', () => {
    const f = { ...formFromAutomation(base), delivery: 'new_session' as const, cwd: '/abs/ws' }
    const body = writeBody(f, { create: false })
    const t = body.target as Record<string, unknown>
    assert.equal(t.delivery, 'new_session')
    assert.equal(t.cwd, '/abs/ws')
    assert.equal(t.session_id, undefined)
  })
})
