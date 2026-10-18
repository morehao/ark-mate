import type { ReactNode } from 'react'
import { Button, Dropdown } from 'antd'
import type { MenuProps } from 'antd'
import { CodeOutlined, FormOutlined, MessageOutlined, PlusOutlined, UploadOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'

/** 「添加」菜单的四个动作（技能两项、MCP 两项——与千问办公一致）。 */
export type CapabilityAddAction = 'skill-chat' | 'skill-upload' | 'mcp-manual' | 'mcp-json'

interface ItemCopy {
  key: CapabilityAddAction
  title: string
  sub: string
  icon: ReactNode
}

const SKILL_ITEMS: ItemCopy[] = [
  { key: 'skill-chat', title: '通过对话创建', sub: '创建个性化技能，将保存在本地使用', icon: <MessageOutlined /> },
  { key: 'skill-upload', title: '上传技能', sub: '导入技能文件，仅本地化安装', icon: <UploadOutlined /> },
]

const MCP_ITEMS: ItemCopy[] = [
  { key: 'mcp-manual', title: '手动填写配置', sub: '按字段填写 transport、命令或 URL', icon: <FormOutlined /> },
  { key: 'mcp-json', title: '粘贴 JSON 配置', sub: '兼容 mcpServers 片段，可一次导入多个', icon: <CodeOutlined /> },
]

function menuLabel(item: ItemCopy): ReactNode {
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', gap: 9, padding: '2px 0', maxWidth: 268 }}>
      <span
        style={{
          display: 'grid',
          placeItems: 'center',
          width: 28,
          height: 28,
          flex: 'none',
          border: `1px solid ${tokens.border}`,
          borderRadius: 7,
          background: tokens.surface,
          color: tokens.textSecondary,
          fontSize: 14,
        }}
      >
        {item.icon}
      </span>
      <span style={{ minWidth: 0 }}>
        <span style={{ display: 'block', fontSize: 12, fontWeight: 550, lineHeight: 1.4, color: tokens.text }}>
          {item.title}
        </span>
        <span style={{ display: 'block', marginTop: 1, fontSize: 10, lineHeight: 1.45, color: tokens.textPlaceholder }}>
          {item.sub}
        </span>
      </span>
    </div>
  )
}

/**
 * 页头「添加」菜单。
 *
 * 用 antd `Dropdown`（而不是自己画 popover）是为了键盘可操作与 Esc 关闭这两件事跟着框架走；
 * 菜单项的两行文案用自定义 label 表达，不把副文案塞进 title 的 tooltip——那样在触屏上永远看不到。
 */
export function AddCapabilityMenu({
  kind,
  onPick,
  label = '添加',
  type = 'default',
}: {
  kind: 'skill' | 'mcp'
  onPick: (action: CapabilityAddAction) => void
  label?: string
  type?: 'default' | 'primary'
}) {
  const source = kind === 'skill' ? SKILL_ITEMS : MCP_ITEMS
  const items: MenuProps['items'] = source.map((it) => ({ key: it.key, label: menuLabel(it) }))
  return (
    <Dropdown
      trigger={['click']}
      placement="bottomRight"
      menu={{
        items,
        onClick: ({ key }) => onPick(key as CapabilityAddAction),
      }}
    >
      <Button size="small" type={type} icon={<PlusOutlined />}>
        {label}
      </Button>
    </Dropdown>
  )
}
