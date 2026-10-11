import type { ReactNode } from 'react'
import { tokens } from '@ark-mate/ui'

/** 设置弹窗内容卡的表头（标题 + 一句状态说明 + 右侧可选操作），与卡片同宽、下压一条 hairline。 */
export function CardHeader({ title, hint, extra }: { title: string; hint: string; extra?: ReactNode }) {
  return (
    <header style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '10px 16px', borderBottom: `1px solid ${tokens.border}` }}>
      <b style={{ fontSize: 13.5, fontWeight: 600 }}>{title}</b>
      <span style={{ flex: 1, fontSize: 11.5, color: tokens.textPlaceholder }}>{hint}</span>
      {extra}
    </header>
  )
}
