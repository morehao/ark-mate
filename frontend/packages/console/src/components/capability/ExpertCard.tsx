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
import { CapabilityCard, IconAction } from './shared'

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
  return <span style={iconStyle}>{icon}</span>
}

const tagBase = { fontSize: 10.5, lineHeight: '17px', padding: '0 7px', borderRadius: 5 } as const
const sepStyle = { width: 1, height: 9, background: tokens.borderStrong, flex: 'none' } as const

/**
 * 专家卡片（2026-11-06 由行列表改卡片网格）。
 *
 * 头像 · 名称 + 标签（角色设定 / 引用缺失 / 已停用） / 描述 / 底栏：状态点 · 技能·连接器 · 模型，
 * 右端常显动作（编辑 ✎ / 删除 🗑——删除统一危险图标）。整卡可点打开弹窗。
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
  const enabled = expert.enabled
  const skillCount = expert.skills?.length ?? 0
  const mcpCount = expert.mcps?.length ?? 0
  const name = expert.name || expert.id

  const tags = (
    <>
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
    </>
  )

  const meta = (
    <>
      <span
        style={{ width: 6, height: 6, borderRadius: '50%', flex: 'none', background: enabled ? tokens.success : tokens.textPlaceholder }}
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
    </>
  )

  const actions = (
    <>
      <Tooltip title="编辑">
        <IconAction icon={<EditOutlined />} title={`编辑 ${name}`} onClick={() => onOpen()} />
      </Tooltip>
      <Popconfirm
        title={`删除「${name}」？`}
        description="删除后不可恢复；已建会话不受影响。"
        okText="删除"
        okButtonProps={{ danger: true }}
        cancelText="取消"
        onConfirm={onDelete}
      >
        <span onClick={(e) => e.stopPropagation()}>
          <IconAction icon={<DeleteOutlined />} title={`删除 ${name}`} danger disabled={delBusy} />
        </span>
      </Popconfirm>
    </>
  )

  return (
    <div
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
      style={{ cursor: 'pointer' }}
    >
      <CapabilityCard
        avatar={<ExpertAvatar avatar={expert.avatar} size={36} />}
        title={name}
        tags={tags}
        description={expert.description || '（无描述）'}
        meta={meta}
        actions={actions}
        dim={!enabled}
      />
    </div>
  )
}
