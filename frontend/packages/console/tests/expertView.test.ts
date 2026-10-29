import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import type { Expert } from '@ark-mate/types'
import {
  EXPERT_INSTRUCTIONS_MAX_BYTES,
  blankExpertForm,
  expertCreateBody,
  expertFormPatch,
  expertFormValues,
  expertModelShort,
  instructionPreview,
  instructionsByteLength,
  instructionsMarkers,
  missingExpertRefs,
  parseExpertJson,
  validateExpertBody,
  validateExpertForm,
} from '../src/console/expertView.ts'

function sample(overrides: Partial<Expert> = {}): Expert {
  return {
    id: 'data-analyst',
    name: '数据分析师',
    description: '清洗、查询、可视化',
    avatar: 'chart',
    model: 'mdl_01J9Z6Q0M7X2V8K4B3N5TRW2Q1',
    skills: ['sql-analysis', 'data-cleaning'],
    mcps: ['postgres'],
    enabled: true,
    has_instructions: true,
    instructions: '你是数据分析师。',
    ...overrides,
  }
}

describe('expert form <-> wire', () => {
  it('读模型 → 表单值：列表缺席字段回落默认', () => {
    const v = expertFormValues({ id: 'x', name: 'X', enabled: false, has_instructions: false })
    assert.equal(v.avatar, 'spark')
    assert.equal(v.model, '')
    assert.deepEqual(v.skills, [])
    assert.equal(v.instructions, '')
    assert.equal(v.enabled, false)
  })

  it('新建提交完整已知字段，id/名称去空白', () => {
    const v = { ...blankExpertForm(), id: '  data-analyst ', name: '  数据分析师 ' }
    const body = expertCreateBody(v)
    assert.equal(body.id, 'data-analyst')
    assert.equal(body.name, '  数据分析师 ') // 名称不 trim（与后端 trim 各司其职，这里只保证 id 干净）
    assert.equal(body.enabled, true)
  })

  it('编辑只提交变化键（A4）：未变键不出现在 patch', () => {
    const initial = expertFormValues(sample())
    const next = { ...initial, name: '数据科学家', description: initial.description }
    const patch = expertFormPatch(initial, next)
    assert.deepEqual(Object.keys(patch), ['name'])
    assert.equal(patch.name, '数据科学家')
  })

  it('编辑：清空 description 是显式 ""，不是「没改」', () => {
    const initial = expertFormValues(sample())
    const patch = expertFormPatch(initial, { ...initial, description: '' })
    assert.equal(patch.description, '')
  })

  it('编辑：技能/标签是集合语义（顺序变不算改）', () => {
    const initial = expertFormValues(sample())
    const patch = expertFormPatch(initial, {
      ...initial,
      skills: [...initial.skills].reverse(),
      tags: ['b', 'a'],
    })
    assert.equal('skills' in patch, false)
    assert.deepEqual(patch.tags, ['b', 'a']) // initial tags 为空，增加算变化
  })
})

describe('instructions 字节 / 危险标记 / 预览（A10/A11/A12）', () => {
  it('UTF-8 字节计数（中文按 3 字节）', () => {
    assert.equal(instructionsByteLength('ab'), 2)
    assert.equal(instructionsByteLength('数据'), 6)
    assert.equal(instructionsByteLength(''), 0)
    assert.equal(instructionsByteLength(undefined), 0)
  })

  it('边界值：恰好 4096 放行，4097 拒绝', () => {
    const atLimit = { ...blankExpertForm(), id: 'x', name: 'X', instructions: 'a'.repeat(EXPERT_INSTRUCTIONS_MAX_BYTES) }
    assert.equal(validateExpertForm(atLimit, true), null)
    const over = { ...atLimit, instructions: 'a'.repeat(EXPERT_INSTRUCTIONS_MAX_BYTES + 1) }
    assert.match(validateExpertForm(over, true) ?? '', /超过 4096 字节/)
  })

  it('危险标记命中列全并拒绝保存', () => {
    assert.deepEqual(instructionsMarkers('a <!-- b {{ c'), ['<!--', '{{'])
    const bad = { ...blankExpertForm(), id: 'x', name: 'X', instructions: 'x </system-reminder> y' }
    assert.match(validateExpertForm(bad, true) ?? '', /危险标记/)
  })

  it('预览：空 → 仅能力预设；非空 → 有角色设定且保留原文', () => {
    assert.equal(instructionPreview('   ').hasPersona, false)
    const p = instructionPreview('你是分析师。')
    assert.equal(p.hasPersona, true)
    assert.equal(p.text, '你是分析师。')
  })
})

describe('校验：id / name / tags / model（A6）', () => {
  it('id 词法：大写/下划线/超长拒', () => {
    const base = blankExpertForm()
    assert.match(validateExpertForm({ ...base, id: 'Bad_Id', name: 'X' }, true) ?? '', /id 须/)
    assert.match(validateExpertForm({ ...base, id: 'a'.repeat(65), name: 'X' }, true) ?? '', /id 须/)
    assert.equal(validateExpertForm({ ...base, id: 'ok-id-1', name: 'X' }, true), null)
  })

  it('编辑态不校验 id（路径已带，不可改）', () => {
    assert.equal(validateExpertForm({ ...blankExpertForm(), id: '', name: 'X' }, false), null)
  })

  it('名称/标签上限与 model 前缀', () => {
    const base = { ...blankExpertForm(), id: 'x', name: 'X' }
    assert.match(validateExpertForm({ ...base, name: '   ' }, true) ?? '', /名称必填/)
    assert.match(validateExpertForm({ ...base, tags: Array.from({ length: 9 }, (_, i) => `t${i}`) }, true) ?? '', /最多 8/)
    assert.match(validateExpertForm({ ...base, model: 'gpt-4' }, true) ?? '', /mdl_/)
  })
})

describe('高级 JSON（A5）', () => {
  it('解析：非法 JSON / 非对象给可读错误', () => {
    assert.match(parseExpertJson('{oops').error ?? '', /不是合法 JSON/)
    assert.match(parseExpertJson('[]').error ?? '', /JSON 对象/)
  })

  it('body 校验只看出现过的已知键（未出现的不判）', () => {
    assert.equal(validateExpertBody({ name: 'X' }, false), null)
    // 编辑态只提交 instructions（A5 的典型形态）：未出现 name 不该被判「名称必填」
    assert.equal(validateExpertBody({ instructions: '新人格' }, false), null)
    assert.match(validateExpertBody({ name: 'X', instructions: 'a'.repeat(5000) }, false) ?? '', /4096/)
  })
})

describe('引用缺失 / 模型短名', () => {
  it('已知清单为空时不做「全部缺失」误报', () => {
    const miss = missingExpertRefs({ skills: ['a'], mcps: ['b'] }, new Set(), new Set())
    assert.deepEqual(miss, { skills: [], mcps: [] })
  })

  it('有已知清单时逐项比对', () => {
    const miss = missingExpertRefs(
      { skills: ['known', 'gone'], mcps: ['mcp-gone'] },
      new Set(['known']),
      new Set(['postgres']),
    )
    assert.deepEqual(miss, { skills: ['gone'], mcps: ['mcp-gone'] })
  })

  it('模型短名去掉 mdl_ 前缀', () => {
    assert.equal(expertModelShort('mdl_ABC'), 'ABC')
    assert.equal(expertModelShort(''), '')
    assert.equal(expertModelShort(undefined), '')
  })
})
