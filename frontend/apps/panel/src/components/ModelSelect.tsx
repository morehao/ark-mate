import type { CSSProperties, ReactNode } from 'react'
import { Select, Tag } from 'antd'
import type { SelectProps } from 'antd'
import { tokens } from '@ark/ui'
import type { ConfigOptionValue } from '@ark/types'

interface ModelSelectProps {
  /** 候选值（来自运行时目录 category=model 的 values；空 = 运行时没有可选项）。 */
  values: ConfigOptionValue[]
  /** 选中值（null = 跟随运行时默认）。值是不透明串，原样回传，不解析。 */
  value: string | null
  onChange: (value: string | null) => void
  disabled?: boolean
  placeholder?: string
  allowClear?: boolean
  size?: 'small' | 'middle' | 'large'
  style?: CSSProperties
  /** value=null 时关闭态展示的默认文案（如「跟随默认」）。 */
  defaultLabel?: string
}

/** 选项展示：名称 +（描述副文案）+（被运行时拒绝过的值标灰提示）。 */
function optionLabel(v: ConfigOptionValue): ReactNode {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
      <span style={{ color: v.unavailable ? tokens.textPlaceholder : tokens.text, whiteSpace: 'nowrap' }}>
        {v.name || v.value}
      </span>
      {v.unavailable && (
        <Tag color="warning" style={{ marginInlineEnd: 0, fontSize: 10, lineHeight: '16px' }}>
          不可用
        </Tag>
      )}
      {v.description && (
        <span style={{ color: tokens.textPlaceholder, fontSize: 11, whiteSpace: 'nowrap' }}>{v.description}</span>
      )}
    </span>
  )
}

/**
 * 模型选择器（会话级模型：值取自运行时目录，经 session/set_config_option 应用）。
 *
 *  - 全部候选来自运行时（agent 自报的 config option），平台不内置任何清单；
 *  - 按 agent 下发的分组（values[].group/group_name）分组展示，未分组则扁平；
 *  - value 是不透明串：只展示 name，绝不解析内部结构；
 *  - null = 跟随运行时默认；allowClear 可清回默认。
 * 交互态 CSS 收口 console.css.ts；颜色/圆角走 @ark/ui tokens，不硬编码。
 */
export function ModelSelect({
  values,
  value,
  onChange,
  disabled,
  placeholder,
  allowClear,
  size = 'small',
  style,
  defaultLabel = '跟随默认',
}: ModelSelectProps) {
  // 分组键：agent 未下发分组时统一落到空串组（antd 的 options 平铺 + OptGroup 混用需分开处理）。
  const groups = new Map<string, { name: string; items: ConfigOptionValue[] }>()
  for (const v of values) {
    const key = v.group ?? ''
    const g = groups.get(key) ?? { name: v.group_name ?? '', items: [] }
    g.items.push(v)
    groups.set(key, g)
  }
  const toOption = (v: ConfigOptionValue) => ({
    value: v.value,
    label: optionLabel(v),
    // 过滤用检索串（label 是 JSX，antd 不能直接 search）——只含展示字段，不含 value。
    search: `${v.name} ${v.description ?? ''}`.toLowerCase(),
  })
  const grouped = [...groups.entries()].some(([k]) => k !== '')
  // 分组/扁平两种形态二选一（antd options 联合类型不接受混用）。
  const options: SelectProps['options'] = grouped
    ? [...groups.entries()].map(([key, g]) => ({ label: g.name || key, options: g.items.map(toOption) }))
    : [...groups.values()].flatMap((g) => g.items.map(toOption))

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
