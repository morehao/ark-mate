import { useMemo, useState } from 'react'
import { Alert, Button, Input, Skeleton } from 'antd'
import { MessageOutlined, PlusOutlined } from '@ant-design/icons'
import { SearchOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import type { Expert } from '@ark-mate/types'
import { expertModelShort, missingExpertRefs } from '../../console/expertView'
import { CapabilityEmpty, SectionHeader } from './shared'
import { ExpertCard } from './ExpertCard'
import { ExpertDialog } from './ExpertDialog'
import { ExpertChatCreateModal } from './ExpertChatCreateModal'
import { useExpertData } from './useExpertData'

/** 专家管理页：卡片画廊 + 居中弹窗编辑器，管理本机专家定义（列表/创建/编辑/启停/删除 + 角色设定）。 */
export function ExpertsPanel() {
  const d = useExpertData()
  const [query, setQuery] = useState('')
  const [chatOpen, setChatOpen] = useState(false)

  const knownSkills = useMemo(() => new Set(d.options.skills.map((o) => o.id)), [d.options.skills])
  const knownMcps = useMemo(() => new Set(d.options.mcps.map((o) => o.id)), [d.options.mcps])
  const modelName = useMemo(() => new Map(d.options.models.map((m) => [m.id, m.name])), [d.options.models])

  const visible = useMemo(() => filterExperts(d.experts, query), [d.experts, query])
  const initialLoading = d.loading && d.experts.length === 0

  const modelLabelOf = (e: Expert): string => (e.model ? (modelName.get(e.model) ?? expertModelShort(e.model)) : '')
  const isMissing = (e: Expert): boolean => {
    const m = missingExpertRefs(e, knownSkills, knownMcps)
    return m.skills.length > 0 || m.mcps.length > 0
  }

  return (
    <PageContainer title="专家" description="不同场景的「专家」预设：擅长什么、用哪些技能与连接器。">
      {/* 工具（搜索 + 新建）下移到内容区工具行右端，与「我的专家」同一条线；页头只留标题+说明。 */}
      <SectionHeader
        title="我的专家"
        hint="本机创建 · 启用后新会话可选；停用不影响历史会话。"
        count={d.experts.length}
        loading={d.loading}
        onRefresh={d.refresh}
        tools={
          <>
            <Input
              size="small"
              allowClear
              prefix={<SearchOutlined style={{ color: tokens.textPlaceholder }} />}
              style={{ width: 200 }}
              placeholder="搜索专家"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <Button size="small" icon={<MessageOutlined />} onClick={() => setChatOpen(true)}>
              通过对话创建
            </Button>
            {/* 新建入口只此一颗（空态里不重复，避免同页两个「新建」）。 */}
            <Button type="primary" size="small" icon={<PlusOutlined />} onClick={d.openNew}>
              新建
            </Button>
          </>
        }
      />

      <div style={{ padding: '6px 0 0' }}>
        {d.listError && (
          <Alert
            type="error"
            showIcon
            style={{ marginBottom: 14 }}
            message="专家清单取不到（服务端不可达）"
            description={d.listError}
            action={
              <Button size="small" onClick={d.refresh}>
                重试
              </Button>
            }
          />
        )}

        {initialLoading ? (
          <div style={listStyle}>
            {Array.from({ length: 4 }).map((_, i) => (
              <div key={i} style={{ padding: '16px 16px', border: `1px solid ${tokens.border}`, borderRadius: tokens.radiusLg }}>
                <Skeleton active avatar paragraph={{ rows: 1 }} title={{ width: '30%' }} />
              </div>
            ))}
          </div>
        ) : d.experts.length === 0 ? (
          d.listError ? null : (
            // 空态只留说明：新建动作在页头（去掉重复入口后，空态不再自己再画一颗按钮）。
            <CapabilityEmpty
              text={
                <>
                  <div style={{ fontSize: 14, color: tokens.textSecondary, marginBottom: 4 }}>还没有专家</div>
                  <div>专家是一组推荐能力，不会限制你手动选择。可以「通过对话创建」，或点「新建」逐项填写。</div>
                </>
              }
            />
          )
        ) : visible.length === 0 ? (
          <CapabilityEmpty text={`没有找到匹配「${query}」的专家，试试其他关键词。`} />
        ) : (
          <div style={gridStyle}>
            {visible.map((e) => (
              <ExpertCard
                key={e.id}
                expert={e}
                modelLabel={modelLabelOf(e)}
                missing={isMissing(e)}
                onOpen={() => d.openEdit(e)}
                onDelete={() => void d.remove(e.id)}
                delBusy={d.delBusy === e.id}
              />
            ))}
          </div>
        )}
      </div>

      <ExpertDialog
        open={d.dialog.open}
        created={d.dialog.created}
        expert={d.dialog.detail}
        loading={d.dialog.detailLoading}
        loadError={d.dialog.detailError}
        options={d.options}
        busy={d.busy}
        error={d.formError}
        toggleBusy={d.toggleBusy === d.dialog.detail?.id}
        delBusy={d.delBusy === d.dialog.detail?.id}
        onClose={d.closeDialog}
        onRetry={d.retryDetail}
        onSave={d.save}
        onToggleEnabled={(next) => {
          const id = d.dialog.detail?.id
          return id ? d.toggle(id, next) : Promise.resolve()
        }}
        onDelete={async () => {
          const id = d.dialog.detail?.id
          if (id) await d.remove(id)
        }}
      />
      <ExpertChatCreateModal open={chatOpen} onClose={() => setChatOpen(false)} />
    </PageContainer>
  )
}

// 精致冷白：卡片网格（`<div.padding>` 内由 .fg-cap-grid 控列宽；这里只兜住 padding）。
const listStyle = {
  display: 'flex',
  flexDirection: 'column',
  gap: 12,
  padding: '18px 16px',
} as const
const gridStyle = {
  display: 'grid',
  gap: 14,
  padding: '18px 16px',
  gridTemplateColumns: 'repeat(auto-fill, minmax(300px, 1fr))',
} as const

function filterExperts(experts: Expert[], query: string): Expert[] {
  const q = query.trim().toLowerCase()
  if (!q) return experts
  return experts.filter((e) =>
    [e.name, e.id, e.description ?? '', ...(e.tags ?? [])].join(' ').toLowerCase().includes(q),
  )
}
