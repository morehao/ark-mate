import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { isDanger, PERM_ROWS, permLabel } from '../src/console/permissionView.ts'

/**
 * 权限 chip 展示映射（v4 · design §3.3/§3.4）：
 * 只读回显、三档 + 跟随默认、危险档标记。纯函数，不碰组件。
 */

describe('permLabel', () => {
  it('空值/缺省 → 跟随默认', () => {
    assert.equal(permLabel(''), '跟随默认')
    assert.equal(permLabel(undefined), '跟随默认')
    assert.equal(permLabel(null), '跟随默认')
  })

  it('三档对齐 DSH 文案', () => {
    assert.equal(permLabel('read-only'), '仅可查看')
    assert.equal(permLabel('workspace-write'), '工作区内修改')
    assert.equal(permLabel('danger-full-access'), '完全权限')
  })

  it('未知值原样回落，不吞不猜', () => {
    assert.equal(permLabel('custom-x'), 'custom-x')
  })
})

describe('isDanger', () => {
  it('只有完全权限是危险档', () => {
    assert.equal(isDanger('danger-full-access'), true)
    assert.equal(isDanger('workspace-write'), false)
    assert.equal(isDanger('read-only'), false)
    assert.equal(isDanger(''), false)
  })
})

describe('PERM_ROWS', () => {
  it('四行：跟随默认 + 三档，顺序固定', () => {
    assert.deepEqual(
      PERM_ROWS.map((r) => r.mode),
      ['', 'read-only', 'workspace-write', 'danger-full-access'],
    )
  })

  it('只有完全权限带 risk 标记', () => {
    const risky = PERM_ROWS.filter((r) => r.risk).map((r) => r.mode)
    assert.deepEqual(risky, ['danger-full-access'])
  })
})
