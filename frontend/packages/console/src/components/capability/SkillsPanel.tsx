import { useEffect, useMemo, useState } from 'react'
import { Button, Input, Popconfirm, Space, Switch, Tag, Tooltip } from 'antd'
import { SearchOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import type { Skill } from '@ark-mate/types'
import { skillStatusView, skillWarningsView } from '../../console/capabilityView'
import { AddCapabilityMenu, type CapabilityAddAction } from './AddCapabilityMenu'
import { SkillChatCreateModal } from './SkillChatCreateModal'
import { SkillDetailModal } from './SkillDetailModal'
import { SkillEditorModal } from './SkillEditorModal'
import { SkillUploadModal } from './SkillUploadModal'
import { CapabilityEmpty, CapabilityRow, SectionHeader, StatusTag, smallTag } from './shared'
import { useCapabilityData } from './useCapabilityData'

/** 技能页：管理技能与启用状态；添加 = 通过对话创建 / 上传技能。 */
export function SkillsPanel() {
  const d = useCapabilityData('skills')
  const [query, setQuery] = useState('')
  // 弹窗里关掉「详情 → 编辑」时，详情应保持关闭（一次只留一个主弹窗）。
  useEffect(() => {
    if (d.detail && !d.skills.some((s) => s.id === d.detail!.id)) d.closeDetail()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [d.detail, d.skills])

  const visible = useMemo(() => filterSkills(d.skills, query), [d.skills, query])
  const detailSkill = d.detail?.kind === 'skill' ? (d.skills.find((s) => s.id === d.detail!.id) ?? null) : null

  const onAdd = (action: CapabilityAddAction): void => {
    if (action === 'skill-chat') d.setChatOpen(true)
    if (action === 'skill-upload') d.setUploadOpen(true)
  }

  return (
    <PageContainer
      title="技能"
      description="管理你的技能。启用后，助手会在合适的对话里自动使用；停用后随时可以恢复。"
      extra={
        <Space size={8}>
          <Input
            size="small"
            allowClear
            prefix={<SearchOutlined style={{ color: tokens.textPlaceholder }} />}
            style={{ width: 210 }}
            placeholder="搜索技能"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          <AddCapabilityMenu kind="skill" onPick={onAdd} label="新建" type="primary" />
        </Space>
      }
    >
      <SectionHeader
        title="我的技能"
        hint="启用后助手会在合适的对话里自动使用；停用只是暂不使用，技能本身会保留。"
        count={visible.length}
        loading={d.busy === 'skills'}
        onRefresh={() => d.refresh('skills')}
      />
      <div style={{ maxHeight: 560, overflowY: 'auto' }}>
        {visible.length === 0 ? (
          <CapabilityEmpty
            text={
              d.skills.length === 0
                ? '暂无技能。你可以通过对话创建，或上传技能。'
                : `没有找到匹配「${query}」的技能。`
            }
            action={
              d.skills.length === 0 ? (
                <AddCapabilityMenu kind="skill" onPick={onAdd} label="添加技能" type="primary" />
              ) : undefined
            }
          />
        ) : (
          visible.map((s) => {
            const warn = skillWarningsView(s.warnings)
            const health = skillStatusView(s)
            const enabled = s.enabled ?? true
            return (
              <CapabilityRow
                key={s.id}
                title={
                  <>
                    <span
                      role="button"
                      tabIndex={0}
                      onClick={() => d.openDetail('skill', s.id)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault()
                          d.openDetail('skill', s.id)
                        }
                      }}
                      style={{
                        fontSize: 13,
                        fontWeight: 600,
                        color: tokens.text,
                        cursor: 'pointer',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                        minWidth: 0,
                      }}
                    >
                      {s.name || s.id}
                    </span>
                    <span style={{ fontSize: 10.5, color: tokens.textPlaceholder }}>调用名 {s.id}</span>
                    {warn && (
                      <Tooltip title={<span style={{ whiteSpace: 'pre-line' }}>{warn.hint}</span>}>
                        <Tag color={warn.color} style={smallTag}>
                          {warn.text}
                        </Tag>
                      </Tooltip>
                    )}
                  </>
                }
                description={s.description || '（无描述）'}
                meta={
                  <>
                    <StatusTag label={health.label} tone={health.tone} hint={health.hint} />
                  </>
                }
                actions={
                  <>
                    <Button size="small" onClick={() => d.openSkillForm(s)}>
                      编辑
                    </Button>
                    <Popconfirm
                      title={`删除「${s.name || s.id}」？`}
                      description="删除后不可恢复；已产生的副本会一并移除。"
                      okText="删除"
                      okButtonProps={{ danger: true }}
                      cancelText="取消"
                      onConfirm={() => void d.doDelete('skill', s.id)}
                    >
                      <Button size="small" danger loading={d.delBusy === s.id}>
                        删除
                      </Button>
                    </Popconfirm>
                    <Tooltip title={enabled ? '停用：助手暂不使用，可随时恢复' : '启用：助手会在合适的对话里自动使用'}>
                      <Switch
                        size="small"
                        checked={enabled}
                        loading={d.toggleBusy === s.id}
                        onChange={(next) => void d.doToggleSkill(s, next)}
                      />
                    </Tooltip>
                  </>
                }
              />
            )
          })
        )}
      </div>

      <SkillDetailModal
        skill={detailSkill}
        open={detailSkill !== null}
        tab={d.detail?.tab ?? 'overview'}
        onTabChange={d.setDetailTab}
        onClose={d.closeDetail}
        onEdit={() => {
          if (!detailSkill) return
          d.closeDetail()
          d.openSkillForm(detailSkill)
        }}
      />
      <SkillEditorModal
        open={d.skillForm !== null}
        skill={d.skillForm?.skill ?? null}
        busy={d.formBusy}
        error={d.formErr}
        onCancel={d.closeSkillForm}
        onSubmit={d.submitSkillForm}
      />
      <SkillChatCreateModal open={d.chatOpen} onClose={() => d.setChatOpen(false)} />
      <SkillUploadModal open={d.uploadOpen} onClose={() => d.setUploadOpen(false)} onUpload={d.doImportSkillPackage} />
    </PageContainer>
  )
}

function filterSkills(skills: Skill[], query: string) {
  const q = query.trim().toLowerCase()
  if (!q) return skills
  return skills.filter((s) =>
    [s.name, s.id, s.description ?? ''].join(' ').toLowerCase().includes(q),
  )
}
