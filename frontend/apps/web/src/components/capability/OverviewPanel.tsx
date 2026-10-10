import { type ReactNode } from 'react'
import { Alert, Button, Input, Select, Space, Tag, Tooltip } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import type { LibraryItem } from '@ark-mate/types'
import {
  libraryKindColor,
  libraryKindLabel,
  libraryOriginView,
  libraryStatusView,
  toolSourceLabel,
} from '../../console/capabilityView'
import { SOURCE_COLOR, TRANSPORT_COLOR, mono, smallTag, SectionHeader, SectionEmpty, Row, Card, SkillActivationTags, SkillRuntimeTags } from './shared'
import { useCapabilityData } from './useCapabilityData'

/** 能力总览：四类同构的统一清单（skill / MCP / 工具 / Agent），以及按 Agent 裁剪后的可见能力（skill 源裁剪 / MCP 启用与可承载底座 / 工具归属）。 */
export function OverviewPanel() {
  const {
    agentId,
    setAgentId,
    caps,
    capsErr,
    library,
    libKind,
    setLibKind,
    libQ,
    setLibQ,
    busy,
    agents,
    loadLibrary,
    loadAll,
    refresh,
    agentMcpRows,
    probeButton,
    liveButton,
    probeStatus,
    secretTags,
  } = useCapabilityData('overview')

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
          {it.scope === 'workspace' && (
            <Tooltip title="作用域 workspace：只投放到工作区内的落点（待实测的形态会标「未实测」）">
              <Tag color="purple" style={smallTag}>
                workspace
              </Tag>
            </Tooltip>
          )}
          {!it.enabled && (
            <Tooltip title="已禁用（定义与引用都保留，可随时恢复）">
              <Tag color="red" style={smallTag}>
                已禁用
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
            {it.source_file && <span style={{ ...mono, fontSize: 11, color: tokens.textPlaceholder }}> · {it.source_file}</span>}
          </span>
          {it.applicability.length === 0 ? (
            <Tooltip title="没有任何候选 runtime（如归属 server 已消失的工具）">
              <Tag style={{ ...smallTag, color: tokens.textPlaceholder }}>无候选底座</Tag>
            </Tooltip>
          ) : (
            it.applicability.map((a) => {
              const v = libraryStatusView(a.status)
              return (
                <Tooltip
                  key={a.runtime}
                  title={
                    <span style={{ whiteSpace: 'pre-line' }}>
                      {a.runtime} · {v.text}
                      {a.reason ? `\n${a.reason}` : ''}
                    </span>
                  }
                >
                  <Tag color={v.color} style={smallTag}>
                    {a.runtime}:{v.text}
                  </Tag>
                </Tooltip>
              )
            })
          )}
        </Row>
      )
    }

  return (
    <PageContainer
      title="扩展"
      description="能力总览：四类同构的统一清单（skill / MCP / 工具 / Agent），以及按 Agent 裁剪后的可见能力（skill 源裁剪 / MCP 启用与可承载底座 / 工具归属）"
      extra={
        <Space>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void loadAll()}>
            刷新
          </Button>
        </Space>
      }
    >
      <div style={{ padding: 18, display: 'flex', flexDirection: 'column', gap: 16 }}>
        {/* ——— M2-4 Library：四类同构的统一能力清单（只读聚合） ——— */}
        <Card>
          <SectionHeader
            title="能力清单（Library）"
            hint="skill / MCP / Agent / 工具四类同构的只读聚合视图；每条给**逐 runtime 的可达性**（不做「整体可用吗」的合并结论——对 A 可用、对 B 不可用是有用信息）"
            count={library.length}
            loading={busy === 'library'}
            onRefresh={() => refresh('library')}
            extra={
              <Space size={6}>
                <Input
                  size="small"
                  allowClear
                  style={{ width: 160 }}
                  placeholder="搜索 id / 名称 / 描述"
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
                    { value: 'mcp', label: 'MCP' },
                    { value: 'agent', label: 'Agent' },
                    { value: 'tool', label: '工具' },
                  ]}
                />
              </Space>
            }
          />
          <div style={{ maxHeight: 380, overflowY: 'auto' }}>
            {library.length === 0 ? (
              <SectionEmpty text="没有匹配的能力（能力定义在 ~/.ark-mate/capabilities/{skills,mcp,tools}）" />
            ) : (
              library.map(libraryRow)
            )}
          </div>
        </Card>
        {/* ——— Agent 视角统一能力目录（§8.1） ——— */}
        <Card>
          <SectionHeader
            title="Agent 视角"
            hint="该 Agent 可见的能力面（未注册底座/禁用 MCP 会在下方提示）"
            count={(caps?.skills.length ?? 0) + (caps?.mcp_servers.length ?? 0) + (caps?.tools.length ?? 0)}
            loading={busy === 'caps'}
            onRefresh={() => refresh('caps')}
            extra={
              <Select
                size="small"
                style={{ minWidth: 180 }}
                placeholder="选择 Agent"
                value={agentId || undefined}
                onChange={(v) => setAgentId(v)}
                options={agents.map((a) => ({ value: a.id, label: a.name ? `${a.name}（${a.id}）` : a.id }))}
              />
            }
          />
          {capsErr ? (
            <SectionEmpty text={`读取失败：${capsErr}`} />
          ) : !caps ? (
            <SectionEmpty text="选择 Agent 查看其统一能力目录" />
          ) : (
            <div>
              {caps.notes && caps.notes.length > 0 && (
                <div style={{ padding: '10px 16px 0' }}>
                  <Alert
                    type="warning"
                    showIcon
                    style={{ fontSize: 12 }}
                    message={caps.notes.join('；')}
                  />
                </div>
              )}
              <div style={{ padding: '10px 16px 0', display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                <span style={{ fontSize: 11.5, color: tokens.textPlaceholder }}>候选底座</span>
                {caps.runtimes.length === 0 ? (
                  <span style={{ fontSize: 11.5, color: tokens.textPlaceholder }}>—</span>
                ) : (
                  caps.runtimes.map((r) => (
                    <Tag key={r} style={{ fontSize: 10.5, marginInlineEnd: 0 }}>
                      {r}
                    </Tag>
                  ))
                )}
              </div>

              <div style={{ padding: '10px 16px 0' }}>
                <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 6 }}>
                  Skills <Tag style={{ fontSize: 10.5 }}>{caps.skills.length}</Tag>
                </div>
                {caps.skills.length === 0 ? (
                  <SectionEmpty text="该 Agent 无可见 skill（没有候选底座用得上它们，或本进程没解析出 skill 投影）" />
                ) : (
                  caps.skills.map((s) => (
                    <Row key={s.id}>
                      <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{s.name}</b>
                      <Tag style={{ marginInlineEnd: 0, fontSize: 11 }}>{s.id}</Tag>
                      {/* §13.4 S5：不适用也列出来并说明原因（不再静默过滤） */}
                      <SkillActivationTags skill={s} />
                      <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>{s.description}</span>
                      <SkillRuntimeTags runtimes={s.runtimes} />
                    </Row>
                  ))
                )}
              </div>

              <div style={{ padding: '14px 16px 0' }}>
                <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 6 }}>
                  MCP Servers <Tag style={{ fontSize: 10.5 }}>{agentMcpRows.length}</Tag>
                </div>
                {agentMcpRows.length === 0 ? (
                  <SectionEmpty text="注册表为空" />
                ) : (
                  agentMcpRows.map((m) => (
                    <Row key={m.id}>
                      <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{m.name}</b>
                      <Tag color={TRANSPORT_COLOR[m.transport] ?? 'default'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                        {m.transport}
                      </Tag>
                      <Tag color={m.enabled ? 'green' : 'default'} style={{ marginInlineEnd: 0, fontSize: 10.5 }}>
                        {m.enabled ? '已启用' : '未启用'}
                      </Tag>
                      {secretTags(m)}
                      {probeStatus(m)}
                      <span style={{ flex: 1, display: 'flex', gap: 5, flexWrap: 'wrap' }}>
                        {m.runtimes.length === 0 ? (
                          <span style={{ fontSize: 11.5, color: tokens.textPlaceholder }}>无候选底座可承载</span>
                        ) : (
                          m.runtimes.map((r) => (
                            <Tag key={r} style={{ fontSize: 10.5, marginInlineEnd: 0 }} color="cyan">
                              {r}
                            </Tag>
                          ))
                        )}
                      </span>
                      {probeButton(m.id, m.probeable)}
                      {liveButton(m)}
                    </Row>
                  ))
                )}
              </div>

              <div style={{ padding: '14px 16px 16px' }}>
                <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 6 }}>
                  Tools <Tag style={{ fontSize: 10.5 }}>{caps.tools.length}</Tag>
                </div>
                {caps.tools.length === 0 ? (
                  <SectionEmpty text="该 Agent 无可见工具" />
                ) : (
                  caps.tools.map((t) => (
                    <Row key={t.name}>
                      <span style={{ ...mono, fontWeight: 600, minWidth: 150 }}>{t.name}</span>
                      <Tag color={SOURCE_COLOR[t.source] ?? 'default'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                        {toolSourceLabel(t)}
                      </Tag>
                      <Tooltip title={t.schema ? JSON.stringify(t.schema) : undefined}>
                        <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>
                          {t.description || (t.schema ? '（含参数 Schema）' : '')}
                        </span>
                      </Tooltip>
                    </Row>
                  ))
                )}
              </div>
            </div>
          )}
        </Card>
      </div>
    </PageContainer>
  )
}
