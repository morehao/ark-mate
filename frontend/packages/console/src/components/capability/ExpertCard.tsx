import { useState } from 'react'
import { Popconfirm, Tooltip } from 'antd'
import {
  BarChartOutlined,
  CodeOutlined,
  ContainerOutlined,
  DeleteOutlined,
  EditOutlined,
  SearchOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { Expert } from '@ark-mate/types'

/** 内置头像键 → 图标（对齐后端 ValidExpertAvatars 的六个键）。 */
export function ExpertAvatar({ avatar, size = 40 }: { avatar?: string; size?: number }) {
  const iconStyle = { fontSize: size * 0.48, color: tokens.textSecondary }
  const icon =
    avatar === 'chart' ? (
      <BarChartOutlined style={iconStyle} />
    ) : avatar === 'code' ? (
      <CodeOutlined style={iconStyle} />
    ) : avatar === 'pen' ? (
      <EditOutlined style={iconStyle} />
    ) : avatar === 'search' ? (
      <SearchOutlined style={iconStyle} />
    ) : avatar === 'briefcase' ? (
      <ContainerOutlined style={iconStyle} />
    ) : (
      <ThunderboltOutlined style={iconStyle} />
    )
  return (
    <span
      style={{
        width: size,
        height: size,
        flex: 'none',
        borderRadius: tokens.radiusSm,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        background: tokens.softFill,
        border: `1px solid ${tokens.border}`,
      }}
    >
      {icon}
    </span>
  )
}

const tagBase = { fontSize: 10.5, lineHeight: '17px', padding: '0 7px', borderRadius: 5 } as const
const sepStyle = { width: 1, height: 9, background: tokens.borderStrong, flex: 'none' } as const

/**
 * 单行专家条目（精致冷白 · 行列表形态）。
 *
 * 一行：头像 · 名称+标签 / 描述 / 元信息（状态点 · 技能数 · 模型 · id）+ hover 动作。
 * 列表接口不带 instructions 正文，故「角色设定」只用标签示意「已设置」，正文在弹窗里看。
 * 整行可点打开弹窗；键盘 Enter / Space 同效。
 */
export function ExpertCard({
  expert,
  modelLabel,
  missing,
  onOpen,
  onDelete,
  delBusy,
}: {
  expert: Expert
  modelLabel: string
  missing: boolean
  onOpen: () => void
  onDelete: () => void
  delBusy: boolean
}) {
  const [hover, setHover] = useState(false)
  const enabled = expert.enabled
  const skillCount = expert.skills?.length ?? 0
  const mcpCount = expert.mcps?.length ?? 0
  const name = expert.name || expert.id

  return (
    <article
      role="button"
      tabIndex={0}
      aria-label={`编辑 ${name}`}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onOpen()
        }
      }}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      onFocus={() => setHover(true)}
      onBlur={() => setHover(false)}
      style={{
        display: 'flex',
        alignItems: 'flex-start',
        gap: 14,
        padding: '16px 4px',
        borderBottom: `1px solid ${tokens.border}`,
        cursor: 'pointer',
        opacity: enabled ? 1 : 0.62,
        background: hover ? tokens.bg : undefined,
        transition: 'background .12s ease',
      }}
    >
      <ExpertAvatar avatar={expert.avatar} size={36} />

      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <span style={{ fontSize: 13.5, fontWeight: 600, color: tokens.text }}>{name}</span>
          {expert.has_instructions && (
            <span style={{ ...tagBase, background: tokens.accentSoft, color: tokens.accent }}>角色设定</span>
          )}
          {missing && (
            <Tooltip title="推荐技能/连接器指向不存在的项：保存仍成功；使用该专家时这些项被跳过并提示。">
              <span style={{ ...tagBase, background: tokens.warningBg, color: tokens.warning, border: `1px solid ${tokens.warningBorder}` }}>
                引用缺失
              </span>
            </Tooltip>
          )}
          {!enabled && (
            <span style={{ ...tagBase, background: tokens.softFill, color: tokens.textPlaceholder }}>已停用</span>
          )}
        </div>

        <div
          title={expert.description || undefined}
          style={{
            fontSize: 12.5,
            color: tokens.textSecondary,
            lineHeight: 1.55,
            marginTop: 3,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {expert.description || '（无描述）'}
        </div>

        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 7,
            marginTop: 6,
            fontSize: 11,
            color: tokens.textPlaceholder,
            flexWrap: 'wrap',
            fontVariantNumeric: 'tabular-nums',
          }}
        >
          <span
            style={{
              width: 6,
              height: 6,
              borderRadius: '50%',
              flex: 'none',
              background: enabled ? tokens.success : tokens.textPlaceholder,
            }}
          />
          <span>
            {skillCount} 技能 · {mcpCount} 连接器
          </span>
          {modelLabel && (
            <>
              <span style={sepStyle} />
              <span>{modelLabel}</span>
            </>
          )}
          <span style={sepStyle} />
          <span style={{ fontFamily: tokens.fontMono }}>{expert.id}</span>
        </div>
      </div>

      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 2,
          flex: 'none',
          opacity: hover ? 1 : 0,
          transition: 'opacity .12s ease',
          paddingTop: 1,
        }}
      >
        <Tooltip title="编辑">
          <button type="button" aria-label={`编辑 ${name}`} onClick={(e) => { e.stopPropagation(); onOpen() }} style={iconBtnStyle}>
            <EditOutlined />
          </button>
        </Tooltip>
        <Popconfirm
          title={`删除「${name}」？`}
          description="删除后不可恢复；已建会话不受影响。"
          okText="删除"
          okButtonProps={{ danger: true }}
          cancelText="取消"
          onConfirm={onDelete}
        >
          <button
            type="button"
            aria-label={`删除 ${name}`}
            disabled={delBusy}
            onClick={(e) => e.stopPropagation()}
            style={{ ...iconBtnStyle, color: tokens.error }}
          >
            <DeleteOutlined />
          </button>
        </Popconfirm>
      </div>
    </article>
  )
}

const iconBtnStyle = {
  width: 26,
  height: 26,
  border: 0,
  background: 'transparent',
  borderRadius: 6,
  color: tokens.textPlaceholder,
  cursor: 'pointer',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
} as const
