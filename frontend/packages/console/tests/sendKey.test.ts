import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { shouldSendKey } from '../src/console/sendKey.ts'

/**
 * 发送键的固定规则（2026-11-05 起没有档位）：
 * Enter 发送、Shift+Enter 换行、⌘/Ctrl+Enter 恒发送。
 */

const key = (over: Partial<{ key: string; shiftKey: boolean; metaKey: boolean; ctrlKey: boolean }> = {}) => ({
  key: 'Enter',
  shiftKey: false,
  metaKey: false,
  ctrlKey: false,
  ...over,
})

describe('shouldSendKey', () => {
  it('Enter 发送', () => {
    assert.equal(shouldSendKey(key()), true)
  })

  it('Shift+Enter 换行（不发送）', () => {
    assert.equal(shouldSendKey(key({ shiftKey: true })), false)
  })

  it('⌘/Ctrl + Enter 恒发送，带不带 Shift 都算发送', () => {
    assert.equal(shouldSendKey(key({ metaKey: true })), true)
    assert.equal(shouldSendKey(key({ ctrlKey: true })), true)
    assert.equal(shouldSendKey(key({ metaKey: true, shiftKey: true })), true)
    assert.equal(shouldSendKey(key({ ctrlKey: true, shiftKey: true })), true)
  })

  it('非 Enter 一律不发送', () => {
    assert.equal(shouldSendKey(key({ key: 'a' })), false)
    assert.equal(shouldSendKey(key({ key: 'Escape' })), false)
  })
})
