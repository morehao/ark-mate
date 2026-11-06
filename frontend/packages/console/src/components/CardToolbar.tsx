import type { ReactNode } from 'react'
import { tokens } from '@ark-mate/ui'

/**
 * 内容区卡片顶部工具行（统一版式）。
 *
 * 为什么单独抽出来：`PageContainer` 的页头只该放「标题 + 描述」；搜索 / 筛选 / 新建 / 刷新
 * 这类**页面工具**属于内容区，必须贴在卡片顶部同一条基线上，否则会和模块名挤在页头那一行
 * （2026-11 版式纠偏）。扩展三页用 `capability/shared.tsx` 的 `SectionHeader`（带标题组），
 * 用量 / 定时任务 / 资料库这类没有「我的 X」小节的页面用本组件（只有右端工具）。
 *
 * 尺寸与清晰度由 `.fg-toolbar`（console.css.ts）统一：控件等高 32px、字距收紧、整像素光栅。
 */
export function CardToolbar({ left, tools }: { left?: ReactNode; tools?: ReactNode }) {
  return (
    <header
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 10,
        padding: '11px 16px',
        borderBottom: `1px solid ${tokens.border}`,
      }}
    >
      {left}
      {left && <span style={{ flex: 1, minWidth: 8 }} />}
      <div className="fg-toolbar" style={{ display: 'flex', alignItems: 'center', gap: 8, marginLeft: 'auto', flex: 'none' }}>
        {tools}
      </div>
    </header>
  )
}
