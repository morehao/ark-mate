import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { appTaglineOf, defaultCompose, parseGuide } from '../src/components/app/appUsage.ts'

// v42：应用「使用说明」纯函数层——卡片能力句兜底、usage_guide 四段解析、开始用默认话术。

describe('appTaglineOf', () => {
  it('优先 tagline，其次 description', () => {
    assert.equal(appTaglineOf({ name: 'A', tagline: '  一句话  ', description: '描述' }), '一句话')
    assert.equal(appTaglineOf({ name: 'A', description: '描述' }), '描述')
  })

  it('都没有时从 members 派生兜底（不空转）', () => {
    const t = appTaglineOf({ name: '会议纪要', members: { experts: ['x'], skills: ['a', 'b'] } })
    assert.match(t, /会议纪要/)
    assert.match(t, /专家 1/)
    assert.match(t, /技能 2/)
  })

  it('无成员也给一句可用的兜底', () => {
    const t = appTaglineOf({ name: '空应用' })
    assert.match(t, /还没配置成员/)
  })
})

describe('parseGuide', () => {
  it('空 → null', () => {
    assert.equal(parseGuide(''), null)
    assert.equal(parseGuide(undefined), null)
    assert.equal(parseGuide('   '), null)
  })

  it('四段标签解析', () => {
    const g = parseGuide('适用场景：早报；需要提供：主题关键词；产出：markdown；边界：不核查事实')
    assert.deepEqual(g, {
      scene: '早报',
      need: '主题关键词',
      out: 'markdown',
      limit: '不核查事实',
    })
  })

  it('英文冒号也认', () => {
    const g = parseGuide('适用场景: 出行；产出: 建议')
    assert.equal(g?.scene, '出行')
    assert.equal(g?.out, '建议')
  })

  it('四段都没命中 → 整段原文回退', () => {
    const g = parseGuide('这是一段没有分段的说明文字。')
    assert.equal(g?.plain, '这是一段没有分段的说明文字。')
  })
})

describe('defaultCompose', () => {
  it('优先第一个示例', () => {
    assert.equal(defaultCompose('A', 'tag', ['示例一', '示例二']), '示例一')
  })
  it('无示例用 tagline', () => {
    assert.equal(defaultCompose('A', 'tag', []), 'tag')
  })
  it('都没有兜底', () => {
    assert.match(defaultCompose('A'), /用「A」/)
  })
})
