import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Modal, Space, Spin, Tabs, Tag, Tooltip } from 'antd'
import { CopyOutlined, ReloadOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { getSkillMaterialization } from '@ark-mate/api'
import type { Skill, SkillMaterializationResp } from '@ark-mate/types'
import {
  materializationMeta,
  materializationNeedsAttention,
  materializationSummary,
  skillRuntimesView,
  skillStatusView,
  skillWarningsView,
} from '../../console/capabilityView'
import { useMate } from '../../store'
import { DetailGrid, SectionLabel, StatusTag, mono, smallTag } from './shared'

const TABS = [
  { key: 'overview', label: '概览' },
  { key: 'body', label: '正文' },
  { key: 'health', label: '投放体检' },
]

/**
 * 技能详情（780）：概览 / 正文 / 投放体检。
 *
 * 体检状态**只在打开这一 tab 时现拉**（`GET /skills/:id/materialization`）：列表不逐条请求，
 * 也不把「没查」画成「一致」——详情要回答的是「现在磁盘上到底怎么样」。
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
  const [mat, setMat] = useState<SkillMaterializationResp | null>(null)
  const [matBusy, setMatBusy] = useState(false)
  const [matErr, setMatErr] = useState<string | null>(null)

  const loadMat = useCallback(async (): Promise<void> => {
    if (!skill) return
    setMatBusy(true)
    setMatErr(null)
    try {
      setMat(await getSkillMaterialization(skill.id))
    } catch (err) {
      setMatErr(err instanceof Error ? err.message : String(err))
    } finally {
      setMatBusy(false)
    }
  }, [skill?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  // 换一条 skill / 打开健康 tab 时重新取：旧的体检结果属于上一条定义，不能拿来展示。
  useEffect(() => {
    if (!open || !skill) return
    setMat(null)
    setMatErr(null)
    if (tab === 'health') void loadMat()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, skill?.id, tab])

  if (!skill) return null

  const runtimes = skillRuntimesView(skill.runtimes, skill.scope)
  const health = skillStatusView(skill)
  const warn = skillWarningsView(skill.warnings)

  const copyInvocation = async (): Promise<void> => {
    const text = `@${skill.id}`
    try {
      await navigator.clipboard.writeText(text)
      showToast(`已复制 ${text}`)
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
          <div style={{ marginTop: 2, fontSize: 11, color: tokens.textPlaceholder, fontFamily: tokens.fontMono }}>
            {skill.id} · {runtimes.labels.join(' · ')}
          </div>
        </div>
      }
      footer={
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
          <Button onClick={onClose}>关闭</Button>
          <Space size={8}>
            <Button onClick={onEdit}>编辑</Button>
            <Button
              type="primary"
              icon={<ReloadOutlined />}
              loading={matBusy}
              onClick={() => {
                if (tab !== 'health') onTabChange('health')
                else void loadMat()
              }}
            >
              重新体检
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
                    description="定义与正文保留；打开开关即按其 runtimes 重新投放。"
                  />
                ) : warn ? (
                  <Alert
                    type="warning"
                    showIcon
                    message={`${warn.text}（critical 已在装载期拒载）`}
                    description={<span style={{ whiteSpace: 'pre-line' }}>{warn.hint}</span>}
                  />
                ) : (
                  <Alert type="success" showIcon message="已启用" description="启用即按其 runtimes 投放；禁用只撤回副本。" />
                )}

                <SectionLabel>能力说明</SectionLabel>
                <div style={{ fontSize: 12.5, lineHeight: 1.7, color: tokens.text }}>
                  {skill.description || '（无描述）'}
                </div>

                <SectionLabel>概要</SectionLabel>
                <DetailGrid
                  items={[
                    { label: '定义 id', value: <span style={mono}>{skill.id}</span> },
                    {
                      label: '投放对象',
                      value: <span title={runtimes.emptyHint}>{runtimes.labels.join(' · ')}</span>,
                    },
                    { label: '作用域', value: skill.scope || 'global' },
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
                  在对话里输入 @{skill.id}，或直接描述任务让 Agent 选择。
                </div>
              </div>
            ) : t.key === 'body' ? (
              <div>
                <div style={{ fontSize: 11.5, color: tokens.textSecondary, lineHeight: 1.7, marginBottom: 8 }}>
                  SKILL.md 正文（保存后会重新投放并做物化体检）。critical 正文在装载期即被拒，不会出现在这里。
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
                  {skill.body || '（正文为空）'}
                </pre>
              </div>
            ) : (
              <div>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
                  <span style={{ flex: 1, fontSize: 11.5, color: tokens.textSecondary }}>
                    {mat ? materializationSummary(mat) : matBusy ? '正在体检…' : '打开本 tab 时现拉一次'}
                  </span>
                  <Button size="small" icon={<ReloadOutlined />} loading={matBusy} onClick={() => void loadMat()}>
                    再体检
                  </Button>
                </div>
                {matErr ? (
                  <Alert type="error" showIcon message="体检失败" description={matErr} />
                ) : !mat ? (
                  <div style={{ padding: 24, textAlign: 'center' }}>
                    <Spin />
                  </div>
                ) : (
                  <>
                    <Alert
                      type="info"
                      showIcon
                      style={{ fontSize: 12, marginBottom: 10 }}
                      message="体检只报不修"
                      description="修复不走这里：启用/禁用 skill 会立刻重校（按平台版本覆盖回正确内容）；用户自有目录（无归属标记）平台不覆盖，需要手工处理。"
                    />
                    {mat.states.length === 0 ? (
                      <div style={{ fontSize: 12, color: tokens.textPlaceholder, padding: '12px 0' }}>
                        {materializationSummary(mat)}
                      </div>
                    ) : (
                      mat.states.map((st, i) => {
                        const meta = materializationMeta(st.status)
                        const attention = materializationNeedsAttention(st)
                        return (
                          <div
                            key={`${st.root}-${st.runtime ?? ''}-${i}`}
                            style={{
                              border: `1px solid ${attention ? tokens.borderStrong : tokens.border}`,
                              borderRadius: 8,
                              padding: '10px 12px',
                              marginBottom: 8,
                            }}
                          >
                            <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                              <Tooltip title={meta.hint}>
                                <Tag color={meta.color} style={smallTag}>
                                  {meta.label}
                                </Tag>
                              </Tooltip>
                              <Tag style={smallTag}>{st.runtime || '单根'}</Tag>
                              <span style={{ ...mono, flex: 1, fontSize: 11.5 }}>{st.dir}</span>
                            </div>
                            <div style={{ fontSize: 11.5, color: tokens.textSecondary, marginTop: 6 }}>{meta.hint}</div>
                            {st.hash && (
                              <div style={{ ...mono, fontSize: 11, color: tokens.textPlaceholder, marginTop: 4 }}>
                                期望 {st.hash.slice(0, 16)}…
                                {attention && st.actual_hash ? ` / 实测 ${st.actual_hash.slice(0, 16)}…` : ''}
                              </div>
                            )}
                            <div style={{ fontSize: 11, color: tokens.textPlaceholder, marginTop: 4 }}>
                              {st.owners && st.owners.length > 0 ? `归属：${st.owners.join('、')}` : '归属：未登记'}
                              {st.written_at ? ` · 写入 ${st.written_at}` : ''}
                            </div>
                            {st.detail && (
                              <div style={{ fontSize: 11.5, color: tokens.textSecondary, marginTop: 4 }}>{st.detail}</div>
                            )}
                          </div>
                        )
                      })
                    )}
                    {mat.notes && mat.notes.length > 0 && (
                      <Alert
                        type="warning"
                        showIcon
                        style={{ fontSize: 12, marginTop: 8 }}
                        message={mat.notes.join('；')}
                      />
                    )}
                  </>
                )}
              </div>
            ),
        }))}
      />
    </Modal>
  )
}
