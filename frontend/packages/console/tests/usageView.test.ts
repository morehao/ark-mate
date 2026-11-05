import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { fmtRate, fmtTokens, purposeShare } from '../src/console/usageView.ts'

describe('用量视图 · 模型调用辅助', () => {
  it('fmtTokens：M/k 缩写与零值', () => {
    assert.equal(fmtTokens(0), '0')
    assert.equal(fmtTokens(642), '642')
    assert.equal(fmtTokens(3_700_000), '3.70M')
    assert.equal(fmtTokens(12_345_678), '12.3M')
    assert.equal(fmtTokens(9_200), '9.2k')
  })

  it('fmtRate：比率与零分母', () => {
    assert.equal(fmtRate(6, 642), '0.9%')
    assert.equal(fmtRate(0, 0), '—')
    assert.equal(fmtRate(1, 2), '50.0%')
  })

  it('purposeShare：占比与零总数', () => {
    assert.equal(purposeShare(310, 642).toFixed(2), '0.48')
    assert.equal(purposeShare(1, 0), 0)
  })
})
