import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { matchSkills, skillHintQuery } from '../src/console/skillHints.ts'

/**
 * composer `/` 技能选择器的纯逻辑（v4 P1）：触发上下文 + 前缀筛选。
 * 与命令补全分开：技能是平台内置、一次性注入。
 */

const SKILLS = [
  { id: 'plan-first', name: '先给方案', desc: '先探索再出方案' },
  { id: 'weekly-report', name: '周报生成', desc: '整理周报' },
]

describe('skillHintQuery', () => {
  it('`/` 开头且无空白才算上下文', () => {
    assert.equal(skillHintQuery('/'), '')
    assert.equal(skillHintQuery('/pl'), 'pl')
    assert.equal(skillHintQuery('/PL'), 'pl')
  })
  it('非 `/` 开头或已进参数区 → null', () => {
    assert.equal(skillHintQuery('hi'), null)
    assert.equal(skillHintQuery('/plan foo'), null)
    assert.equal(skillHintQuery(' /'), null)
  })
})

describe('matchSkills', () => {
  it('按 id 前缀筛（大小写不敏感）', () => {
    assert.deepEqual(matchSkills(SKILLS, 'pl').map((s) => s.id), ['plan-first'])
  })
  it('按名称前缀也能命中（中文）', () => {
    assert.deepEqual(matchSkills(SKILLS, '周').map((s) => s.id), ['weekly-report'])
  })
  it('空前缀返回全部（保留顺序）', () => {
    assert.deepEqual(matchSkills(SKILLS, '').map((s) => s.id), ['plan-first', 'weekly-report'])
  })
  it('无技能或空数组 → 空', () => {
    assert.deepEqual(matchSkills(undefined, 'x'), [])
    assert.deepEqual(matchSkills([], ''), [])
  })
})
