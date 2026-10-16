import type { CSSProperties, ReactNode } from 'react'
import { Select, Tag } from 'antd'
import type { SelectProps } from 'antd'
import { tokens } from '@ark-mate/ui'
import type { ConfigOptionValue } from '@ark-mate/types'

interface ModelSelectProps {
  /** 候选值（来自运行时目录 category=model 的 values；空 = 运行时没有可选项）。 */
  values: ConfigOptionValue[]
  /**
   * 选中值 = **平台模型行 id**（`mdl_…`；null = 跟随运行时默认）。
   *
   * v26 起受控值与回调都换成了平台 id（此前是不透明串）：候选 `value` 是**底座**的词，
   * 而会话存/收的是**平台**的词——两层词汇在这里汇合，转换只此一处（按候选的 `model_id` 注解）。
   * 没有注解的候选渲染得出来但**选不了**（`disabled`）：平台没声明过它，提交必被服务端拒。
   */
  value: string | null
  onChange: (value: string | null) => void
  disabled?: boolean
  placeholder?: string
  allowClear?: boolean
  size?: 'small' | 'middle' | 'large'
  style?: CSSProperties
  /** value=null 时关闭态展示的默认文案（如「跟随默认」）。 */
  defaultLabel?: string
  /** 目录为空时的下拉说明（如登录引导，替代原「浏览全部」弹窗的空态提示）。 */
  emptyNote?: ReactNode
}

/** 下拉项展示：名称 +（描述副文案，第二行）+（标签：不可用）。
 *  两行紧凑行（而不是名称与描述并排一行）——并排会把浮层撑到 550px+，相对 130px 的药丸太重。 */
function optionLabel(v: ConfigOptionValue): ReactNode {
  // 变暗 = 这个候选此刻不该被当成可选项：或者底座拒过它（动态事实），或者平台没有声明过它
  // （静态事实 —— v27 起这种候选由调用方收窄掉，见下面的组件注释；真出现就只有"补当前模型"那一行）。
  const dim = v.unavailable || !v.model_id ? tokens.textPlaceholder : tokens.text
  return (
    <span style={{ display: 'flex', flexDirection: 'column', gap: 1, minWidth: 0, maxWidth: 300 }}>
      <span style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
        <span
          style={{
            color: dim,
            fontSize: 12.5,
            lineHeight: 1.5,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {v.name || v.value}
        </span>
        {v.unavailable && (
          <Tag color="warning" style={{ marginInlineEnd: 0, fontSize: 10, lineHeight: '16px' }}>
            不可用
          </Tag>
        )}
      </span>
      {v.description && (
        <span
          title={v.description}
          style={{
            maxWidth: 300,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            color: tokens.textPlaceholder,
            fontSize: 11,
            lineHeight: 1.5,
            whiteSpace: 'nowrap',
          }}
        >
          {v.description}
        </span>
      )}
    </span>
  )
}

/**
 * 模型选择器（会话级模型：值取自运行时目录，经 session/set_config_option 应用）。
 *
 *  - 候选由调用方给，且**只该是平台清单里声明过的那些**（v27：`modelValues` = 运行时广告 ∩
 *    平台清单）。运行时广告里对不上平台行的候选（`model_id` 为空）**不渲染**——用户要的是
 *    "设置里配过的模型"，而"底座广告了、平台没声明"是诊断事实（`GET /models` 的 `sync[]`）。
 *    组件这一层仍按 `disabled: !v.model_id` 兜底（纵深防御：调用方补的"当前模型"那一行就是这种）；
 *  - 按底座下发的分组（values[].group/group_name）分组展示，未分组则扁平；
 *  - 受控值与回调是**平台模型行 id**（v26），候选是不透明串：两层词汇的转换只在本文件里做
 *    （按候选的 `model_id` 注解），别处不许再转一次；
 *  - 候选只展示 name，**绝不解析**它的结构（那是底座词表）；
 *  - null = 跟随运行时默认；allowClear 可清回默认；
 *  - **关闭态只显示名称**：antd 把 option.label 原样渲染进选择器，若把描述塞进 label，
 *    收起的选择器会宽达 500+px 成为不可压缩的 flex item，把所在行挤塌（见 StartPanel
 *    底栏）。描述与「不可用」标记一律走 optionRender，只在下拉面板出现。
 * 交互态 CSS 收口 console.css.ts；颜色/圆角走 @ark-mate/ui tokens，不硬编码。
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
  emptyNote,
}: ModelSelectProps) {
  // 分组键：底座未下发分组时统一落到空串组（antd 的 options 平铺 + OptGroup 混用需分开处理）。
  const groups = new Map<string, { name: string; items: ConfigOptionValue[] }>()
  for (const v of values) {
    const key = v.group ?? ''
    const g = groups.get(key) ?? { name: v.group_name ?? '', items: [] }
    g.items.push(v)
    groups.set(key, g)
  }
  // 候选 → 平台模型 id 的映射（antd 的 option value 用**运行时的不透明串**：它是候选自身的标识；
  // 而受控值与回调走平台 id）。两边的转换只在这里做一次。
  const modelIDByValue = new Map<string, string>()
  for (const v of values) {
    if (v.model_id) modelIDByValue.set(v.value, v.model_id)
  }
  // 关闭态显示什么：受控值是平台 id，所以要先从候选里反查出它对应的**运行时值**再交给 antd 匹配。
  // 反查不到（会话引用的那条模型已不在清单里）就把 id 原样交给 antd——它按原始串显示，这正是
  // "如实显示不可用"（显示成空会让人以为会话没选模型）。
  const valueOfModelID = new Map<string, string>()
  for (const [v, id] of modelIDByValue) valueOfModelID.set(id, v)
  const rawValue = value === null ? undefined : (valueOfModelID.get(value) ?? value)

  const toOption = (v: ConfigOptionValue) => ({
    value: v.value,
    // 关闭态回落到这个 label：只放名称（放描述会让选择器带上几百像素的固有宽度）。
    label: v.name || v.value,
    // 过滤用检索串（label 是 JSX，antd 不能直接 search）——只含展示字段，不含 value。
    search: `${v.name} ${v.description ?? ''}`.toLowerCase(),
    // **平台没声明过它** ⇒ 不可选（服务端会以 40401 拒；界面这里只是别让用户白点一次）。
    // v27 起正常候选都带注解（调用方已收窄），这条只剩"调用方补的当前模型那一行"会命中。
    disabled: !v.model_id,
    // 下拉项要靠它还原完整展示（optionRender 拿到的只有这条 option 本身）。
    model: v,
  })
  const grouped = [...groups.entries()].some(([k]) => k !== '')
  // 分组/扁平两种形态二选一（antd options 联合类型不接受混用）。
  const options: SelectProps['options'] = grouped
    ? [...groups.entries()].map(([key, g]) => ({ label: g.name || key, options: g.items.map(toOption) }))
    : [...groups.values()].flatMap((g) => g.items.map(toOption))

  return (
    <Select
      size={size}
      variant="borderless"
      disabled={disabled}
      allowClear={allowClear}
      value={rawValue}
      placeholder={placeholder ?? `模型 · ${defaultLabel}`}
      // `v` 是候选的**运行时值**：折回平台模型 id 再交给调用方（没有注解的候选不可选，所以这里
      // 理论上拿不到"没有 id"的选中值；真拿到就按 null 处置 = 不下发，不猜）。
      onChange={(v: string | undefined) => {
        if (typeof v !== 'string') {
          onChange(null)
          return
        }
        onChange(modelIDByValue.get(v) ?? null)
      }}
      options={options}
      showSearch
      popupMatchSelectWidth={false}
      // 关闭态是「药丸」而不是表单框：皮肤（hairline 边 / 全圆角 / hover / focus 环）收口在
      // console.css.ts 的 .fg-model-pill（纯 token，无硬编码色值）；这里只覆盖内层排版。
      classNames={{
        root: 'fg-model-pill',
        popup: { root: 'fg-model-pop', listItem: 'fg-model-pop-item' },
      }}
      styles={{
        content: { fontFamily: tokens.fontMono, fontSize: 11.5, lineHeight: '16px', color: tokens.text },
        placeholder: { color: tokens.textPlaceholder },
        suffix: { color: tokens.textPlaceholder, fontSize: 10 },
        popup: { root: { borderRadius: 12, padding: 4 } },
      }}
      // 下拉项：名称 + 描述（关闭态只有名称，完整信息在这里）。
      optionRender={(opt) => {
        const raw = (opt.data as { model?: ConfigOptionValue } | undefined)?.model
        return raw ? optionLabel(raw) : opt.label
      }}
      // 空目录（运行时没给候选）时把说明放进下拉：原来的「浏览全部」弹窗承担这个提示。
      notFoundContent={
        values.length === 0 ? (
          <div
            style={{
              maxWidth: 320,
              padding: '4px 2px',
              color: tokens.textPlaceholder,
              fontSize: 12,
              lineHeight: 1.7,
              whiteSpace: 'normal',
            }}
          >
            {emptyNote ?? '运行时未提供模型目录'}
          </div>
        ) : (
          '无匹配模型'
        )
      }
      filterOption={(input, opt) =>
        String((opt as { search?: string } | undefined)?.search ?? '').includes(input.toLowerCase())
      }
      style={style}
    />
  )
}
