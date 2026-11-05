import { useMemo, useState } from 'react'
import { Alert, Button, Input, Skeleton } from 'antd'
import { SearchOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import type { Expert } from '@ark-mate/types'
import { expertModelShort, missingExpertRefs } from '../../console/expertView'
import { CapabilityEmpty, SectionHeader } from './shared'
import { ExpertCard } from './ExpertCard'
import { ExpertDialog } from './ExpertDialog'
import { useExpertData } from './useExpertData'

/** 专家管理页：卡片画廊 + 居中弹窗编辑器，管理本机专家定义（列表/创建/编辑/启停/删除 + 角色设定）。 */
export function ExpertsPanel() {
  const d = useExpertData()
  const [query, setQuery] = useState('')

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
    <PageContainer
      title="专家"
      description="每位专家是一组推荐能力 + 一段角色设定。点卡片编辑；角色设定会在对话开始时自动生效，不显示在对话里。"
      extra={
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <Input
            size="small"
            allowClear
            prefix={<SearchOutlined style={{ color: tokens.textPlaceholder }} />}
            style={{ width: 210 }}
            placeholder="搜索专家"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {/* 2026-11-05 反馈：新建入口只在页头这一颗，空态里不再重复一颗（同一页两个「新建」）。 */}
          <Button type="primary" size="small" onClick={d.openNew}>
            新建
          </Button>
        </div>
      }
    >
      <SectionHeader
        title="我的专家"
        hint="本机创建 · 启用后新会话可选；停用不影响历史会话。"
        count={d.experts.length}
        loading={d.loading}
        onRefresh={d.refresh}
      />

      <div style={{ padding: 16 }}>
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
          <div style={galleryStyle}>
            {Array.from({ length: 4 }).map((_, i) => (
              <div
                key={i}
                style={{ border: `1px solid ${tokens.border}`, borderRadius: tokens.radiusMd, padding: 16, minHeight: 150 }}
              >
                <Skeleton active avatar paragraph={{ rows: 3 }} />
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
                  <div>专家是一组推荐能力，不会限制你手动选择。</div>
                </>
              }
            />
          )
        ) : visible.length === 0 ? (
          <CapabilityEmpty text={`没有找到匹配「${query}」的专家，试试其他关键词。`} />
        ) : (
          <div style={galleryStyle}>
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
    </PageContainer>
  )
}

const galleryStyle = {
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fill, minmax(300px, 1fr))',
  gap: 14,
  alignItems: 'start',
} as const

function filterExperts(experts: Expert[], query: string): Expert[] {
  const q = query.trim().toLowerCase()
  if (!q) return experts
  return experts.filter((e) =>
    [e.name, e.id, e.description ?? '', ...(e.tags ?? [])].join(' ').toLowerCase().includes(q),
  )
}
