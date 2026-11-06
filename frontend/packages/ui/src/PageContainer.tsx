import type { ReactNode } from 'react'
import { tokens } from './theme'

interface Props {
  title: ReactNode
  description?: ReactNode
  extra?: ReactNode
  children?: ReactNode
}

/** 管理页骨架：标题 + 描述 + 右上操作区，下方白色内容卡（hairline 分层，无阴影）。 */
export function PageContainer({ title, description, extra, children }: Props) {
  return (
    <div
      style={{
        maxWidth: 960,
        margin: '0 auto',
        padding: '20px 24px 48px',
        boxSizing: 'border-box',
        width: '100%',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 16 }}>
        <div style={{ minWidth: 0 }}>
          {/* 行高用整数字面值：`lineHeight:1.3×18=23.4px` 这类小数会层层传给子元素，
              使内容区起点落在半像素、控件文字发虚（全站亚像素根因之一）。 */}
          <div style={{ fontSize: 18, fontWeight: 650, letterSpacing: '-0.01em', lineHeight: '24px' }}>{title}</div>
          {description != null && (
            <div style={{ marginTop: 6, fontSize: 13, color: tokens.textSecondary, lineHeight: '21px' }}>{description}</div>
          )}
        </div>
        {extra != null && (
          <div style={{ flex: 'none', display: 'flex', alignItems: 'center', gap: 8 }}>{extra}</div>
        )}
      </div>
      {children != null && (
        <div
          style={{
            marginTop: 16,
            border: `1px solid ${tokens.border}`,
            borderRadius: tokens.radiusMd,
            background: tokens.surface,
            overflow: 'hidden',
          }}
        >
          {children}
        </div>
      )}
    </div>
  )
}
