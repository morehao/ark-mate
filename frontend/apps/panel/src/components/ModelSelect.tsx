import type { CSSProperties, ReactNode } from 'react'
import { Select } from 'antd'
import { tokens } from '@ark/ui'
import type { ModelOption } from '@ark/types'

interface ModelSelectProps {
  models: ModelOption[]
  /** 选中模型 id（null = 跟随 agent 默认）。 */
  value: string | null
  onChange: (id: string | null) => void
  disabled?: boolean
  placeholder?: string
  allowClear?: boolean
  size?: 'small' | 'middle' | 'large'
  style?: CSSProperties
  /** value=null 时关闭态展示的默认文案（如「Agent 默认」）。 */
  defaultLabel?: string
}

/** 选项展示：推荐星标 + title +（description 副文案）。 */
function optionLabel(m: ModelOption): ReactNode {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
      <span style={{ color: tokens.text, whiteSpace: 'nowrap' }}>
        {m.recommended ? '★ ' : ''}
        {m.title || m.id}
      </span>
      {m.description && (
        <span style={{ color: tokens.textPlaceholder, fontSize: 11, whiteSpace: 'nowrap' }}>
          {m.description}
        </span>
      )}
    </span>
  )
}

/**
 * 模型选择器（Phase C：会话级模型）。antd Select 封装：
 *  - showSearch（按 title/id/description 子串匹配）；
 *  - 按 provider 类型分组（Select.OptGroup），推荐星标（★）；
 *  - null = 跟随 agent 默认；allowClear 可清回默认。
 * 交互态 CSS 收口 console.css.ts；颜色/圆角走 @ark/ui tokens，不硬编码。
 */
export function ModelSelect({
  models,
  value,
  onChange,
  disabled,
  placeholder,
  allowClear,
  size = 'small',
  style,
  defaultLabel = 'Agent 默认',
}: ModelSelectProps) {
  // 按 provider 类型分组，同组内推荐优先、再按 title。
  const byProvider = new Map<string, ModelOption[]>()
  for (const m of models) {
    const k = m.provider || ''
    const list = byProvider.get(k) ?? []
    list.push(m)
    byProvider.set(k, list)
  }
  const options = [...byProvider.entries()].map(([provider, list]) => {
    const sorted = [...list].sort((a, b) => {
      if (!!a.recommended !== !!b.recommended) return a.recommended ? -1 : 1
      return (a.title || a.id).localeCompare(b.title || b.id)
    })
    return {
      label: provider,
      options: sorted.map((m) => ({
        value: m.id,
        label: optionLabel(m),
        // 过滤用检索串（antd 的 label 是 JSX，不能直接 search）。
        search: `${m.title} ${m.id} ${m.description ?? ''} ${(m.behavior ?? []).join(' ')}`.toLowerCase(),
      })),
    }
  })

  return (
    <Select
      size={size}
      disabled={disabled}
      allowClear={allowClear}
      value={value ?? undefined}
      placeholder={placeholder ?? `模型 · ${defaultLabel}`}
      onChange={(v: string | undefined) => onChange(typeof v === 'string' ? v : null)}
      options={options}
      showSearch
      popupMatchSelectWidth={false}
      filterOption={(input, opt) =>
        String((opt as { search?: string } | undefined)?.search ?? '').includes(input.toLowerCase())
      }
      style={style}
    />
  )
}
