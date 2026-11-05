import { type ReactNode, useEffect, useState } from 'react'
import { Button, Input, Select, Space, Tag, Tooltip } from 'antd'
import { ApiOutlined, ReloadOutlined, ThunderboltOutlined, UserOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { PageContainer, tokens } from '@ark-mate/ui'
import { listExperts } from '@ark-mate/api'
import type { LibraryItem } from '@ark-mate/types'
import { libraryKindColor, libraryKindLabel, libraryOriginView, libraryStatusView } from '../../console/capabilityView'
import { mono, smallTag, SectionHeader, SectionEmpty, Row, Card } from './shared'
import { useCapabilityData } from './useCapabilityData'

/**
 * 能力总览：三类同构的统一清单（skill / MCP / 工具）——**只读聚合**，逐条给逐 runtime 的可达性。
 *
 * v19：这里原来还有第二张卡「Agent 视角」（某 agent 可见的能力面 + 候选底座 + 白名单投影），
 * 随 `GET /agents/:id/capabilities` 一起删除。抽掉的原因不是"少一个入口"，而是那层视角在
 * 底座构建期固定之后退化成 Library 的抄本：候选底座只剩一个、白名单也没有上层可裁。
 * 「哪些底座投得出来、为什么投不出来」这条信息由 Library 行的逐个 applicability 标签承担。
 */
export function OverviewPanel() {
  const navigate = useNavigate()
  const { library, libKind, setLibKind, libQ, setLibQ, busy, loadLibrary, loadAll, refresh, skills, mcps } =
    useCapabilityData('overview')

  // 专家数只用于总览第三张卡（原型 §D 三卡）：取一次清单，失败退化为 0（不阻断总览）。
  const [expertCount, setExpertCount] = useState<number | null>(null)
  useEffect(() => {
    let alive = true
    listExperts()
      .then((list) => alive && setExpertCount(list.length))
      .catch(() => alive && setExpertCount(0))
    return () => {
      alive = false
    }
  }, [])

  // 三张入口卡（原型 §D）：技能 / 连接器 / 专家，各自带「N 个」副标 + 一句说明，点击进对应页。
  const overviewCards = [
    {
      key: 'skills',
      icon: <ThunderboltOutlined />,
      title: '技能',
      stat: `${skills.length} 个`,
      desc: '读文档、做表格、写代码、出图等具体本事。',
      to: '/extensions/skills',
    },
    {
      key: 'connectors',
      icon: <ApiOutlined />,
      title: '连接器',
      stat: `${mcps.length} 个已连`,
      desc: '邮箱、网盘、日历等外部服务，把数据接到助手身边。',
      to: '/extensions/connectors',
    },
    {
      key: 'experts',
      icon: <UserOutlined />,
      title: '专家',
      stat: expertCount == null ? '…' : `${expertCount} 个预设`,
      desc: '不同场景的预设：擅长什么、用哪些技能与连接器。',
      to: '/extensions/experts',
    },
  ]

  // Library 行渲染（只被本页用）
    const libraryRow = (it: LibraryItem): ReactNode => {
      const origin = libraryOriginView(it.origin)
      return (
        <Row key={`${it.kind}:${it.id}`}>
          <Tag color={libraryKindColor(it.kind)} style={{ ...smallTag, minWidth: 46, textAlign: 'center' }}>
            {libraryKindLabel(it.kind)}
          </Tag>
          <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{it.name}</b>
          <span style={{ ...mono, fontSize: 11.5, color: tokens.textSecondary }}>{it.id}</span>
          <Tooltip title={origin.hint}>
            <Tag color={origin.color} style={smallTag}>
              {origin.text}
            </Tag>
          </Tooltip>
          {!it.enabled && (
            <Tooltip title="已停用（可随时恢复）">
              <Tag color="red" style={smallTag}>
                已停用
              </Tag>
            </Tooltip>
          )}
          {it.warnings && it.warnings.length > 0 && (
            <Tooltip title={<span style={{ whiteSpace: 'pre-line' }}>{it.warnings.join('\n')}</span>}>
              <Tag color="orange" style={smallTag}>
                提醒 {it.warnings.length}
              </Tag>
            </Tooltip>
          )}
          <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>
            {it.description}
          </span>
          {it.applicability.length === 0 ? (
            <Tag style={{ ...smallTag, color: tokens.textPlaceholder }}>不可用</Tag>
          ) : (
            unifiedStatusOf(it.applicability).map((s) => (
              <Tag key={s} color={libraryStatusView(s).color} style={smallTag}>
                {libraryStatusView(s).text}
              </Tag>
            ))
          )}
        </Row>
      )
    }

  return (
    <PageContainer
      title="扩展"
      description="能力总览：技能、连接器与工具的统一清单。"
      extra={
        <Space>
          <Button size="small" onClick={() => navigate('/extensions/skills')}>
            管理技能
          </Button>
          <Button size="small" type="primary" onClick={() => navigate('/extensions/connectors')}>
            管理连接器
          </Button>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void loadAll()}>
            刷新
          </Button>
        </Space>
      }
    >
      <div style={{ padding: 18, display: 'flex', flexDirection: 'column', gap: 16 }}>
        {/* ——— 三张入口卡（原型 §D）：技能 / 连接器 / 专家，各带数量与一句说明 ——— */}
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, minmax(0, 1fr))', gap: 14 }}>
          {overviewCards.map((c) => (
            <button
              key={c.key}
              type="button"
              onClick={() => navigate(c.to)}
              style={{
                textAlign: 'left',
                cursor: 'pointer',
                border: `1px solid ${tokens.border}`,
                borderRadius: tokens.radiusMd,
                background: tokens.surface,
                padding: 16,
                font: 'inherit',
                transition: 'border-color 0.12s ease',
              }}
              onMouseEnter={(e) => (e.currentTarget.style.borderColor = tokens.borderStrong)}
              onMouseLeave={(e) => (e.currentTarget.style.borderColor = tokens.border)}
            >
              <div
                style={{
                  width: 40,
                  height: 40,
                  borderRadius: 11,
                  display: 'grid',
                  placeItems: 'center',
                  background: tokens.primarySoft,
                  color: tokens.primary,
                  fontSize: 18,
                }}
              >
                {c.icon}
              </div>
              <div style={{ fontSize: 15, fontWeight: 650, color: tokens.text, marginTop: 12 }}>{c.title}</div>
              <div style={{ fontSize: 12, fontWeight: 600, color: tokens.primary, marginTop: 2 }}>{c.stat}</div>
              <div style={{ fontSize: 12.5, color: tokens.textSecondary, marginTop: 8, lineHeight: 1.6 }}>{c.desc}</div>
            </button>
          ))}
        </div>
        {/* ——— M2-4 Library：三类同构的统一能力清单（只读聚合） ——— */}
        <Card>
          <SectionHeader
            title="能力清单"
            hint="技能、连接器与工具的统一清单；每一条给出是否可以使用的状态。"
            count={library.length}
            loading={busy === 'library'}
            onRefresh={() => refresh('library')}
            extra={
              <Space size={6}>
                <Input
                  size="small"
                  allowClear
                  style={{ width: 160 }}
                  placeholder="搜索名称 / 描述"
                  value={libQ}
                  onChange={(e) => setLibQ(e.target.value)}
                  onPressEnter={() => void loadLibrary()}
                />
                <Select
                  size="small"
                  style={{ minWidth: 120 }}
                  value={libKind || 'all'}
                  onChange={(v) => {
                    const kind = v === 'all' ? '' : v
                    setLibKind(kind)
                    void loadLibrary(kind, libQ)
                  }}
                  options={[
                    { value: 'all', label: '全部类型' },
                    { value: 'skill', label: '技能' },
                    { value: 'mcp', label: '连接器' },
                    { value: 'tool', label: '工具' },
                  ]}
                />
              </Space>
            }
          />
          <div style={{ maxHeight: 380, overflowY: 'auto' }}>
            {library.length === 0 ? (
              <SectionEmpty text="没有匹配的能力" />
            ) : (
              library.map(libraryRow)
            )}
          </div>
        </Card>
      </div>
    </PageContainer>
  )
}

/** 把逐条可达性收敛成一组去重后的状态（不暴露内部运行方名字）。 */
function unifiedStatusOf(apps: LibraryItem['applicability']): string[] {
  const order = ['disabled', 'not_configured', 'untested', 'not_applicable', 'projected']
  const seen = new Set(apps.map((a) => a.status))
  return order.filter((s) => seen.has(s))
}
