import { Alert, Button, Modal, Space, Tabs, Tooltip } from 'antd'
import { CopyOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { Skill } from '@ark-mate/types'
import { skillStatusView, skillWarningsView } from '../../console/capabilityView'
import { useMate } from '../../store'
import { DetailGrid, SectionLabel, StatusTag, mono } from './shared'

const TABS = [
  { key: 'overview', label: '概览' },
  { key: 'body', label: '技能内容' },
]

/**
 * 技能详情（780）：概览 / 技能内容。
 *
 * 投放对象、作用域、物化体检属于内部实现，不进用户界面。
 */
export function SkillDetailModal({
  skill,
  open,
  tab,
  onTabChange,
  onClose,
  onEdit,
}: {
  skill: Skill | null
  open: boolean
  tab: string
  onTabChange: (tab: string) => void
  onClose: () => void
  onEdit: () => void
}) {
  const showToast = useMate((s) => s.showToast)

  if (!skill) return null

  const health = skillStatusView(skill)
  const warn = skillWarningsView(skill.warnings)

  const copyInvocation = async (): Promise<void> => {
    const text = `@${skill.id}`
    try {
      await navigator.clipboard.writeText(text)
      showToast(`已复制 ${text}`, 'success')
    } catch {
      showToast(`复制失败，请手动选中：${text}`)
    }
  }

  return (
    <Modal
      open={open}
      onCancel={onClose}
      width={780}
      destroyOnHidden
      title={
        <div style={{ paddingRight: 24 }}>
          <div style={{ fontSize: 15, fontWeight: 600, lineHeight: 1.35 }}>{skill.name || skill.id}</div>
          <div style={{ marginTop: 2, fontSize: 11, color: tokens.textPlaceholder }}>调用名 {skill.id}</div>
        </div>
      }
      footer={
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
          <Button onClick={onClose}>关闭</Button>
          <Space size={8}>
            <Button type="primary" onClick={onEdit}>
              编辑
            </Button>
          </Space>
        </div>
      }
    >
      <Tabs
        activeKey={tab}
        onChange={onTabChange}
        items={TABS.map((t) => ({
          key: t.key,
          label: t.label,
          children:
            t.key === 'overview' ? (
              <div>
                {skill.enabled === false ? (
                  <Alert
                    type="warning"
                    showIcon
                    message="已停用"
                    description="技能内容会保留；重新启用后助手会继续使用它。"
                  />
                ) : warn ? (
                  <Alert
                    type="warning"
                    showIcon
                    message={warn.text}
                    description={<span style={{ whiteSpace: 'pre-line' }}>{warn.hint}</span>}
                  />
                ) : (
                  <Alert type="success" showIcon message="已启用" description="助手会在合适的对话里自动使用这个技能。" />
                )}

                <SectionLabel>说明</SectionLabel>
                <div style={{ fontSize: 12.5, lineHeight: 1.7, color: tokens.text }}>
                  {skill.description || '（无描述）'}
                </div>

                <SectionLabel>概要</SectionLabel>
                <DetailGrid
                  items={[
                    { label: '调用名', value: <span style={mono}>{skill.id}</span> },
                    {
                      label: '状态',
                      value: <StatusTag label={health.label} tone={health.tone} hint={health.hint} />,
                    },
                  ]}
                />

                <SectionLabel>调用方式</SectionLabel>
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 8,
                    border: `1px solid ${tokens.border}`,
                    borderRadius: tokens.radiusSm,
                    background: tokens.codeBg,
                    padding: '6px 10px',
                  }}
                >
                  <span style={{ ...mono, flex: 1 }}>@{skill.id}</span>
                  <Tooltip title="复制调用方式">
                    <Button size="small" type="text" icon={<CopyOutlined />} onClick={() => void copyInvocation()} />
                  </Tooltip>
                </div>
                <div style={{ marginTop: 5, fontSize: 10.5, color: tokens.textPlaceholder }}>
                  在对话里输入 @{skill.id}，或直接描述任务让助手选用。
                </div>
              </div>
            ) : (
              <div>
                <div style={{ fontSize: 11.5, color: tokens.textSecondary, lineHeight: 1.7, marginBottom: 8 }}>
                  技能内容（保存后会立即生效）。
                </div>
                <pre
                  style={{
                    ...mono,
                    margin: 0,
                    padding: 12,
                    maxHeight: 420,
                    overflow: 'auto',
                    background: tokens.codeBg,
                    border: `1px solid ${tokens.border}`,
                    borderRadius: tokens.radiusSm,
                    whiteSpace: 'pre-wrap',
                  }}
                >
                  {skill.body || '（内容为空）'}
                </pre>
              </div>
            ),
        }))}
      />
    </Modal>
  )
}
