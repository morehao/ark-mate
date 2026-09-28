import { test } from 'node:test'
import assert from 'node:assert/strict'

import { UNKNOWN_BUILD_ID, versionGate } from '../src/gate.ts'

test('一致 → 放行', () => {
  const r = versionGate('abc123', 'abc123')
  assert.equal(r.ok, true)
  assert.equal(r.mismatch, null)
  assert.equal(r.unverifiable, null)
})

test('不一致 → 拦下并带上两侧取值', () => {
  const r = versionGate('abc123', 'def456')
  assert.equal(r.ok, false)
  assert.deepEqual(r.mismatch, { web: 'abc123', core: 'def456' })
})

test('dirty 标记参与比较（脏构建不能与干净构建混搭）', () => {
  assert.equal(versionGate('abc123-dirty', 'abc123').ok, false)
  assert.equal(versionGate('abc123-dirty', 'abc123-dirty').ok, true)
})

test('核心 unknown → 放行但标记无法判定', () => {
  const r = versionGate('abc123', UNKNOWN_BUILD_ID)
  assert.equal(r.ok, true)
  assert.equal(r.mismatch, null)
  assert.match(r.unverifiable ?? '', /核心/)
})

test('前端 unknown → 放行但标记无法判定（例如没有 git 的构建容器）', () => {
  const r = versionGate(UNKNOWN_BUILD_ID, 'abc123')
  assert.equal(r.ok, true)
  assert.match(r.unverifiable ?? '', /前端/)
})

test('两侧都空 → 放行且无法判定（不把判据用成自伤）', () => {
  const r = versionGate('', '')
  assert.equal(r.ok, true)
  assert.ok(r.unverifiable)
})

test('两侧空白字符被归一', () => {
  assert.equal(versionGate(' abc123 ', 'abc123\n').ok, true)
  assert.equal(versionGate('  ', 'abc123').unverifiable !== null, true)
})
