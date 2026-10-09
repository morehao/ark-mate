/**
 * WS 首帧形状单测（`node --test`，零新依赖）。
 *
 * 除了语义，这里还**冻结键序与 JSON 字面量**：首帧是"客户端 → 核心"的 wire 形状，
 * 核心侧按字段名解析（Go 的 json tag），键序固定能让失败信息可逐字对照、也避免以后
 * 有人顺手改字段名却以为只是重构。
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'

import { buildAttachFrame, CSP_POLICY, STREAM_ATTACH_DEADLINE_MS } from '../src/index.ts'

test('attach：浏览器态 token=null', () => {
  const f = buildAttachFrame(null)
  assert.deepEqual(f, { action: 'attach', token: null })
  // 键序也冻结（wire 形状）：action 在前、token 在后。
  assert.deepEqual(Object.keys(f), ['action', 'token'])
  assert.equal(JSON.stringify(f), '{"action":"attach","token":null}')
})

test('attach：桌面态带 streamToken', () => {
  const f = buildAttachFrame('abc.def-GHI_123')
  assert.deepEqual(f, { action: 'attach', token: 'abc.def-GHI_123' })
  assert.equal(JSON.stringify(f), '{"action":"attach","token":"abc.def-GHI_123"}')
})

test('attach：空串令牌抛错（没有凭据必须显式传 null）', () => {
  assert.throws(() => buildAttachFrame(''), /空串令牌非法/)
})

test('握手时限常量 = 5s（与核心侧一致）', () => {
  assert.equal(STREAM_ATTACH_DEADLINE_MS, 5000)
})

test('CSP 策略：无外域、无 unsafe-eval', () => {
  assert.equal(
    CSP_POLICY,
    "default-src 'self'; connect-src 'self' ws://127.0.0.1:*; img-src 'self' data:; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
  )
  assert.ok(!CSP_POLICY.includes('unsafe-eval'), 'CSP 不得放开 unsafe-eval')
  assert.ok(!/https?:\/\/(?!127\.0\.0\.1)/.test(CSP_POLICY), 'CSP 不得列任何外域')
})
