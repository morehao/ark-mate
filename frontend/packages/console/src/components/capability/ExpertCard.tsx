import { useState } from 'react'
import { Popconfirm, Tag, Tooltip } from 'antd'
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
  const iconStyle = { fontSize: size * 0.48, color: tokens.primary }
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
        borderRadius: size * 0.28,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        background: tokens.primarySoft,
      }}
    >
      {icon}
    </span>
  )
}

const badgeBase = { marginInlineEnd: 0, fontSize: 9.5, lineHeight: '15px', padding: '0 5px', borderRadius: 4 }

/**
 * 单张专家卡：头像 / 名 / id / 状态标记 / 描述 / 角色设定引言 / 推荐数 + hover 动作。
 *
 * 卡片只见摘要（**列表接口不带 instructions 正文**，故人格区只标「已设置」不展示正文——正文在抽屉里看）。
 * 整卡可点打开抽屉；键盘 Enter / Space 同效。
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

  return (
    <article
      role="button"
      tabIndex={0}
      aria-label={`编辑 ${expert.name || expert.id}`}
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
        position: 'relative',
        background: tokens.surface,
        border: `1px solid ${hover ? tokens.borderStrong : tokens.border}`,
        borderRadius: tokens.radiusMd,
        padding: 16,
        cursor: 'pointer',
        opacity: enabled ? 1 : 0.64,
        display: 'flex',
        flexDirection: 'column',
        transition: 'border-color .14s ease, box-shadow .14s ease, transform .14s ease',
        boxShadow: hover ? '0 6px 20px rgba(20,28,50,.06)' : 'none',
        transform: hover ? 'translateY(-1px)' : 'none',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 11, marginBottom: 10 }}>
        <ExpertAvatar avatar={expert.avatar} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', fontSize: 14.5, fontWeight: 650 }}>
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{expert.name || expert.id}</span>
            {expert.has_instructions && (
              <Tag color="blue" style={badgeBase}>
                角色设定
              </Tag>
            )}
            {missing && (
              <Tooltip title="推荐技能/连接器指向不存在的项：保存仍成功；使用该专家时这些项被跳过并提示。">
                <Tag color="warning" style={badgeBase}>
                  引用缺失
                </Tag>
              </Tooltip>
            )}
            {!enabled && (
              <Tag style={{ ...badgeBase, color: tokens.textPlaceholder }}>
                已禁用
              </Tag>
            )}
          </div>
          <div style={{ fontSize: 10.5, color: tokens.textPlaceholder, marginTop: 2 }}>
            标识：{expert.id}
          </div>
        </div>
      </div>

      <div
        style={{
          fontSize: 12,
          color: tokens.textSecondary,
          lineHeight: 1.55,
          display: '-webkit-box',
          WebkitLineClamp: 2,
          WebkitBoxOrient: 'vertical',
          overflow: 'hidden',
          minHeight: 36,
        }}
      >
        {expert.description || '（无描述）'}
      </div>

      {expert.has_instructions ? (
        <div
          style={{
            marginTop: 10,
            borderLeft: `2px solid ${tokens.primary}`,
            background: tokens.selectedBg,
            borderRadius: '0 8px 8px 0',
            padding: '7px 10px',
            fontSize: 11.5,
            color: tokens.textSecondary,
            lineHeight: 1.55,
          }}
        >
          已设置角色设定 · 对话开始时会自动生效（打开查看）
        </div>
      ) : (
        <div style={{ marginTop: 10, fontSize: 11, color: tokens.textPlaceholder }}>未设置角色设定（仅能力预设）</div>
      )}

      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          marginTop: 12,
          paddingTop: 10,
          borderTop: `1px solid ${tokens.border}`,
          fontSize: 11,
          color: tokens.textPlaceholder,
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
            <span>·</span>
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{modelLabel}</span>
          </>
        )}
        <div
          style={{
            marginLeft: 'auto',
            display: 'flex',
            gap: 2,
            opacity: hover ? 1 : 0,
            transition: 'opacity .12s ease',
          }}
        >
          <Tooltip title="编辑">
            <button
              type="button"
              aria-label={`编辑 ${expert.name || expert.id}`}
              onClick={(e) => {
                e.stopPropagation()
                onOpen()
              }}
              style={iconBtnStyle}
            >
              <EditOutlined />
            </button>
          </Tooltip>
          <Popconfirm
            title={`删除「${expert.name || expert.id}」？`}
            description="删除后不可恢复；已建会话不受影响。"
            okText="删除"
            okButtonProps={{ danger: true }}
            cancelText="取消"
            onConfirm={onDelete}
          >
            <button
              type="button"
              aria-label={`删除 ${expert.name || expert.id}`}
              disabled={delBusy}
              onClick={(e) => e.stopPropagation()}
              style={{ ...iconBtnStyle, color: tokens.error }}
            >
              <DeleteOutlined />
            </button>
          </Popconfirm>
        </div>
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
