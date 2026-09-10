import { useMemo, useState } from 'react'
import { Button, Input, Modal } from 'antd'
import { SearchOutlined } from '@ant-design/icons'
import { tokens } from '@ark/ui'
import type { ConfigOptionValue } from '@ark/types'

interface ModelPickerModalProps {
  open: boolean
  onClose: () => void
  /** 候选值：运行时目录 category=model 的 values（平台无内置清单）。 */
  values: ConfigOptionValue[]
  /** 选中值（null = 跟随运行时默认）。值是不透明串，原样回传。 */
  value: string | null
  onSelect: (value: string | null) => void
  /** 空目录说明（运行时未提供目录时的补救提示，如登录引导）。 */
  emptyHint?: string
}

/**
 * 模型全览弹窗：搜索 + 分组浏览（分组来自 agent 下发的 values[].group）+ 清空回默认。
 * 候选完全来自运行时（ACP session config options），平台不内置清单、不排序、不推荐。
 * 交互态 CSS 收口 console.css.ts；颜色/圆角走 @ark/ui tokens。
 */
export function ModelPickerModal({ open, onClose, values, value, onSelect, emptyHint }: ModelPickerModalProps) {
  const [q, setQ] = useState('')

  const grouped = useMemo(() => {
    const lower = q.trim().toLowerCase()
    const filtered = lower
      ? values.filter((v) => `${v.name} ${v.description ?? ''}`.toLowerCase().includes(lower))
      : values
    const by = new Map<string, { name: string; items: ConfigOptionValue[] }>()
    for (const v of filtered) {
      const k = v.group ?? ''
      const g = by.get(k) ?? { name: v.group_name ?? '', items: [] }
      g.items.push(v)
      by.set(k, g)
    }
    return [...by.entries()].map(([key, g]) => ({ key, name: g.name, items: g.items }))
  }, [values, q])

  return (
    <Modal open={open} onCancel={() => { setQ(''); onClose() }} title="选择模型" footer={null} width={520}>
      <Input
        size="middle"
        allowClear
        prefix={<SearchOutlined style={{ color: tokens.textPlaceholder }} />}
        placeholder="搜索模型（名称 / 描述）…"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        style={{ marginBottom: 10 }}
      />
      <div style={{ maxHeight: '55vh', overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 6 }}>
        {grouped.length === 0 && (
          <div style={{ color: tokens.textPlaceholder, fontSize: 12, padding: '12px 4px' }}>
            {values.length === 0 ? emptyHint ?? '运行时未提供模型目录' : '无匹配模型'}
          </div>
        )}
        {grouped.map(({ key, name, items }) => (
          <div key={key || 'default'} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            <div style={{ fontFamily: tokens.fontMono, fontSize: 11, color: tokens.textPlaceholder, padding: '2px 4px' }}>
              {name || key || '全部'}
            </div>
            {items.map((v) => {
              const active = v.value === value
              return (
                <button
                  key={v.value}
                  type="button"
                  onClick={() => { onSelect(v.value); setQ(''); onClose() }}
                  style={{
                    textAlign: 'left',
                    display: 'flex',
                    alignItems: 'center',
                    gap: 8,
                    width: '100%',
                    boxSizing: 'border-box',
                    background: active ? tokens.selectedBg : 'transparent',
                    border: `1px solid ${active ? tokens.borderStrong : 'transparent'}`,
                    borderRadius: 8,
                    padding: '7px 10px',
                    cursor: 'pointer',
                    color: tokens.text,
                    fontSize: 12.5,
                    lineHeight: 1.5,
                    fontFamily: 'inherit',
                  }}
                >
                  <span style={{ color: v.unavailable ? tokens.textPlaceholder : tokens.text, whiteSpace: 'nowrap' }}>
                    {v.name || v.value}
                  </span>
                  {v.unavailable && (
                    <span style={{ color: tokens.textPlaceholder, fontSize: 11, whiteSpace: 'nowrap' }}>
                      不可用{v.unavailable_reason ? `：${v.unavailable_reason}` : ''}
                    </span>
                  )}
                  {v.description && (
                    <span style={{ color: tokens.textPlaceholder, fontSize: 11, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                      {v.description}
                    </span>
                  )}
                </button>
              )
            })}
          </div>
        ))}
      </div>
      <div style={{ marginTop: 10, display: 'flex', justifyContent: 'space-between', gap: 8 }}>
        <Button size="small" onClick={() => { onSelect(null); setQ(''); onClose() }}>
          恢复默认
        </Button>
        <Button size="small" onClick={() => { setQ(''); onClose() }}>
          完成
        </Button>
      </div>
    </Modal>
  )
}
