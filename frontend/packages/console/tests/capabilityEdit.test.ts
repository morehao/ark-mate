import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import {
  capabilityChips,
  capabilityToastText,
  choiceFromCapabilities,
  expertSwitchPatch,
  mergeCapabilityPatch,
  selectionPatchFor,
} from '../src/console/capabilityEdit.ts'

// 这一组钉的是**请求语义**（三态补丁），不是像素：
//  - 缺键 = 不改这一格（整体替换会让"只换技能"顺手清掉专家）；
//  - `null` 与 `[]` 在服务端是两个代际，界面上也不能混；
//  - chip 只在"声明过"的那一格出现（未声明 = 跟随默认，不加任何标记）。
// 这些分支出错时界面照样能点，只有服务端收到的东西悄悄变了样。

describe('choiceFromCapabilities', () => {
  it('整个对象缺失 = 三格都未声明', () => {
    assert.deepEqual(choiceFromCapabilities(null), { expert: null, skills: null, mcps: null, apps: null })
    assert.deepEqual(choiceFromCapabilities(undefined), { expert: null, skills: null, mcps: null, apps: null })
  })

  it('缺键与 null 同义；空数组与空串原样保留', () => {
    assert.deepEqual(choiceFromCapabilities({}), { expert: null, skills: null, mcps: null, apps: null })
    assert.deepEqual(choiceFromCapabilities({ skills: [], expert: '' }), {
      expert: '',
      skills: [],
      mcps: null,
      apps: null,
    })
  })

  it('白名单原样带出（顺序不在这里重排：服务端会规范化）', () => {
    assert.deepEqual(choiceFromCapabilities({ skills: ['b', 'a'], mcps: ['m'], expert: 'x' }), {
      expert: 'x',
      skills: ['b', 'a'],
      mcps: ['m'],
      apps: null,
    })
  })
})

describe('selectionPatchFor', () => {
  it('只带被改的那一格（另一格不出现 = 服务端不改它）', () => {
    assert.deepEqual(selectionPatchFor('skills', ['a']), { skills: ['a'] })
    assert.deepEqual(selectionPatchFor('mcps', []), { mcps: [] })
  })

  it('回「跟随默认」用 null，不是空数组', () => {
    assert.deepEqual(selectionPatchFor('skills', null), { skills: null })
    assert.notDeepEqual(selectionPatchFor('skills', null), { skills: [] })
  })

  it('应用这一格同构：白名单 / 空数组 / null 都只带 apps 一个键', () => {
    assert.deepEqual(selectionPatchFor('apps', ['weather-query']), { apps: ['weather-query'] })
    assert.deepEqual(selectionPatchFor('apps', []), { apps: [] })
    assert.deepEqual(selectionPatchFor('apps', null), { apps: null })
    // 只带 apps，不误带 skills/mcps/expert
    assert.deepEqual(Object.keys(selectionPatchFor('apps', [])), ['apps'])
  })
})

describe('expertSwitchPatch', () => {
  it('换专家 = 换成它的预设：显式列表交回未声明', () => {
    assert.deepEqual(expertSwitchPatch('data-analyst'), {
      expert: 'data-analyst',
      skills: null,
      mcps: null,
    })
    // 换专家**不动 apps 这一格**（apps 与专家预设正交，见 capacityEdit 的 expertSwitchPatch 注释）。
    assert.equal('apps' in expertSwitchPatch('data-analyst'), false)
  })

  it('「跟随默认」只动专家这一格（不顺手清掉手选的技能）', () => {
    assert.deepEqual(expertSwitchPatch(null), { expert: null })
  })

  it('「通用」是空串（明确不用），与 null 不是一回事', () => {
    assert.deepEqual(expertSwitchPatch(''), { expert: '', skills: null, mcps: null })
  })
})

describe('mergeCapabilityPatch', () => {
  it('只改补丁里出现的格子，另一格原样保留（创建页的本地草稿就靠这条）', () => {
    const cur = { expert: 'data-analyst', skills: ['a'], mcps: null, apps: null }
    assert.deepEqual(mergeCapabilityPatch(cur, { skills: ['b'] }), {
      expert: 'data-analyst',
      skills: ['b'],
      mcps: null,
      apps: null,
    })
  })

  it('null 是一个要落下去的值（回未声明），不是"没改"', () => {
    const cur = { expert: 'x', skills: ['a'], mcps: ['m'], apps: null }
    assert.deepEqual(mergeCapabilityPatch(cur, { skills: null, expert: null }), {
      expert: null,
      skills: null,
      mcps: ['m'],
      apps: null,
    })
  })

  it('空数组与 null 合并后仍是两个不同的状态', () => {
    const cur = { expert: null, skills: null, mcps: null, apps: null }
    assert.deepEqual(mergeCapabilityPatch(cur, { skills: [] }).skills, [])
    assert.deepEqual(mergeCapabilityPatch(cur, { skills: null }).skills, null)
  })

  it('与 expertSwitchPatch 组合：换成某个专家后本地就是"专家 + 两格未声明"', () => {
    const cur = { expert: null, skills: ['a'], mcps: ['m'], apps: null }
    assert.deepEqual(mergeCapabilityPatch(cur, expertSwitchPatch('data-analyst')), {
      expert: 'data-analyst',
      skills: null,
      mcps: null,
      apps: null,
    })
  })
})

describe('capabilityChips', () => {
  it('三格都未声明 → 没有 chip（输入区与默认态逐字一致）', () => {
    assert.deepEqual(capabilityChips({ expert: null, skills: null, mcps: null, apps: null }), [])
  })

  it('声明过的格子才出现，带数量与头像字', () => {
    const chips = capabilityChips(
      { expert: 'data-analyst', skills: ['a', 'b'], mcps: [], apps: null },
      '数据分析师',
    )
    assert.deepEqual(
      chips.map((c) => [c.kind, c.label, c.count, c.avatar]),
      [
        ['expert', '数据分析师', undefined, '数'],
        ['skills', '技能', 2, undefined],
        ['mcps', '连接器', 0, undefined],
      ],
    )
  })

  it('「明确不用专家」（空串）也出 chip，显示成「通用」', () => {
    const chips = capabilityChips({ expert: '', skills: null, mcps: null, apps: null })
    assert.equal(chips.length, 1)
    assert.equal(chips[0].label, '通用')
    assert.match(chips[0].title, /明确不用/)
  })

  it('专家 id 没有名字时用 id 兜底', () => {
    const chips = capabilityChips({ expert: 'ghost', skills: null, mcps: null, apps: null })
    assert.equal(chips[0].label, 'ghost')
    assert.equal(chips[0].avatar, 'g')
  })

  it('应用声明的才出 chip，带数量', () => {
    const chips = capabilityChips({ expert: null, skills: null, mcps: null, apps: ['weather-query'] })
    assert.equal(chips.length, 1)
    assert.equal(chips[0].kind, 'apps')
    assert.equal(chips[0].label, '应用')
    assert.equal(chips[0].count, 1)
    assert.match(chips[0].title, /启用这 1 个/)
  })

  it('应用"一个都不要"（空数组）也出 chip，与"未声明"不同', () => {
    const chips = capabilityChips({ expert: null, skills: null, mcps: null, apps: [] })
    assert.equal(chips.length, 1)
    assert.equal(chips[0].count, 0)
    assert.match(chips[0].title, /不用任何应用/)
  })
})

describe('capabilityToastText', () => {
  it('无告警 → 只说「下一条消息生效」', () => {
    assert.equal(capabilityToastText(null), '已更新能力 · 下一条消息生效')
    assert.equal(capabilityToastText([]), '已更新能力 · 下一条消息生效')
  })

  it('有告警 → 缀在后面（不另开提示，也不阻断）', () => {
    const text = capabilityToastText([
      { kind: 'skill', id: 'x', reason: '注册表里没有这个 skill，已跳过' },
      { kind: 'expert', id: 'y', reason: '人格不重注入' },
    ])
    assert.match(text, /^已更新能力 · 下一条消息生效（/)
    assert.match(text, /已跳过/)
    assert.match(text, /人格不重注入/)
  })

  it('空理由被丢掉（不产出「（）」这种残句）', () => {
    const text = capabilityToastText([{ kind: 'skill', id: 'x', reason: '  ' }])
    assert.equal(text, '已更新能力 · 下一条消息生效')
  })
})
