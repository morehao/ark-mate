import { useMemo, useState } from 'react'
import { Button, Input, Modal } from 'antd'
import { SearchOutlined } from '@ant-design/icons'
import { tokens } from '@ark/ui'
import type { ModelOption } from '@ark/types'

interface ModelPickerModalProps {
  open: boolean
  onClose: () => void
  models: ModelOption[]
  /** 选中模型 id（null = 跟随 agent 默认）。 */
  value: string | null
  onSelect: (id: string | null) => void
}

/**
 * 模型全览弹窗（Phase D：等价 openwork 全览，但不引入 org/云端供应商权限层）。
 * 搜索 + provider 分组 + 推荐星标 + 清空回默认；选择后关闭（effort 二级由内联 Select 承担）。
 * 交互态 CSS 收口 console.css.ts；颜色/圆角走 @ark/ui tokens。
 */
export function ModelPickerModal({ open, onClose, models, value, onSelect }: ModelPickerModalProps) {
  const [q, setQ] = useState('')

  const grouped = useMemo(() => {
    const lower = q.trim().toLowerCase()
    const filtered = lower
      ? models.filter((m) =>
          `${m.title} ${m.id} ${m.description ?? ''} ${(m.behavior ?? []).join(' ')}`.toLowerCase().includes(lower),
        )
      : models
    const by = new Map<string, ModelOption[]>()
    for (const m of filtered) {
      const k = m.provider || ''
      const list = by.get(k) ?? []
      list.push(m)
      by.set(k, list)
    }
    return [...by.entries()].map(([provider, list]) => ({
      provider,
      list: [...list].sort((a, b) => {
        if (!!a.recommended !== !!b.recommended) return a.recommended ? -1 : 1
        return (a.title || a.id).localeCompare(b.title || b.id)
      }),
    }))
  }, [models, q])

  return (
    <Modal open={open} onCancel={() => { setQ(''); onClose() }} title="选择模型" footer={null} width={520}>
      <Input
        size="middle"
        allowClear
        prefix={<SearchOutlined style={{ color: tokens.textPlaceholder }} />}
        placeholder="搜索模型（名称 / id / 描述）…"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        style={{ marginBottom: 10 }}
      />
      <div style={{ maxHeight: '55vh', overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 6 }}>
        {grouped.length === 0 && (
          <div style={{ color: tokens.textPlaceholder, fontSize: 12, padding: '12px 4px' }}>无匹配模型（可回内联选择器手动输入）</div>
        )}
        {grouped.map(({ provider, list }) => (
          <div key={provider} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            <div style={{ fontFamily: tokens.fontMono, fontSize: 11, color: tokens.textPlaceholder, padding: '2px 4px' }}>
              {provider || '通用'}
            </div>
            {list.map((m) => {
              const active = m.id === value
              return (
                <button
                  key={m.id}
                  type="button"
                  onClick={() => { onSelect(m.id); setQ(''); onClose() }}
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
                  <span style={{ color: tokens.text, whiteSpace: 'nowrap' }}>
                    {m.recommended ? '★ ' : ''}
                    {m.title || m.id}
                  </span>
                  {m.description && (
                    <span style={{ color: tokens.textPlaceholder, fontSize: 11, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                      {m.description}
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
