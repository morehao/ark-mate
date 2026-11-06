import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ModelSelect } from '../../src/components/ModelSelect'
import type { ConfigOptionValue } from '@ark-mate/types'

/**
 * ModelSelect 关闭态的展示口径（2026-11-06）：
 *   - 受控值（平台模型行 id）能在候选里反查到 → 显示候选名称；
 *   - 反查不到、但调用方给了 `valueLabel`（服务端解析的展示名）→ 显示它，**绝不**摊 `mdl_…` 原始 id；
 *   - 反查不到、也没给兜底名 → 保持旧行为（退化为原始值，不编名字）。
 *
 * 事故背景：会话页内联输入区的模型 chip 只喂了收窄后的候选（`modelValues`），当会话模型不在
 * 候选里（目录还没到 / 模型行被删）时，chip 直接把 `mdl_01M4…` 原始 id 显示了出来。
 */

const candidates: ConfigOptionValue[] = [
  { value: '["yygu","deepseek-flash"]', name: 'DeepSeek Flash', model_id: 'mdl_A' },
  { value: '["yygu","gpt-5"]', name: 'GPT-5', model_id: 'mdl_B' },
]

describe('ModelSelect · 关闭态展示', () => {
  it('受控值能在候选里反查到 → 显示候选名称', () => {
    render(<ModelSelect values={candidates} value="mdl_B" onChange={() => {}} />)
    expect(screen.getByText('GPT-5')).toBeInTheDocument()
    expect(screen.queryByText(/mdl_B/)).toBeNull()
  })

  it('反查不到但给了 valueLabel → 显示展示名，不摊平台 id', () => {
    render(
      <ModelSelect
        values={candidates}
        value="mdl_01M4NR8TECS8XC1JX2w3E06"
        valueLabel="DeepSeek V3.2"
        onChange={() => {}}
      />,
    )
    expect(screen.getByText('DeepSeek V3.2')).toBeInTheDocument()
    expect(screen.queryByText(/mdl_01M4/)).toBeNull()
  })

  it('候选为空（目录未到）+ valueLabel → 仍显示展示名', () => {
    render(
      <ModelSelect values={[]} value="mdl_01M4NR8TECS8XC1JX2w3E06" valueLabel="DeepSeek V3.2" onChange={() => {}} />,
    )
    expect(screen.getByText('DeepSeek V3.2')).toBeInTheDocument()
    expect(screen.queryByText(/mdl_01M4/)).toBeNull()
  })

  it('反查不到且没给 valueLabel → 保持旧行为（退化原始值）', () => {
    render(<ModelSelect values={candidates} value="mdl_orphan" onChange={() => {}} />)
    // antd 无匹配项时按原始串显示——这是历史行为，本用例把它钉住，避免误改。
    expect(screen.getByText('mdl_orphan')).toBeInTheDocument()
  })
})
