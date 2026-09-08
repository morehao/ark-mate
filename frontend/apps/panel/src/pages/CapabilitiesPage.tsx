import { useCallback, useEffect, useState, type CSSProperties, type ReactNode } from 'react'
import { Button, Space, Tag, Tooltip } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark/ui'
import { listMcpServers, listSkills, listTools, toErrorMessage } from '@ark/api'
import type { McpServer, Skill, Tool } from '@ark/types'
import { useForge } from '../store'

// ——— P1 能力目录页（§8.3 能力卡）：Skills / MCP / Tools 只读列表 ———
// 数据来自 GET /v1/forge/skills|mcps|tools（平台唯一事实源；数据源 ~/.forge/capabilities）。

const SOURCE_COLOR: Record<string, string> = { builtin: 'blue', mcp: 'green', observed: 'gold' }
const TRANSPORT_COLOR: Record<string, string> = { stdio: 'default', sse: 'blue', http: 'green', streamable_http: 'purple' }

const mono: CSSProperties = { fontFamily: tokens.fontMono, fontSize: 12.5, color: tokens.text, overflowWrap: 'anywhere' }

function SectionHeader({
  title,
  hint,
  count,
  loading,
  onRefresh,
}: {
  title: string
  hint: string
  count: number
  loading: boolean
  onRefresh: () => void
}) {
  return (
    <header
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 10,
        padding: '10px 16px',
        borderBottom: `1px solid ${tokens.border}`,
      }}
    >
      <b style={{ fontSize: 13.5, fontWeight: 600 }}>{title}</b>
      <Tag style={{ marginInlineEnd: 0, fontSize: 11 }}>{count}</Tag>
      <span style={{ flex: 1, fontSize: 11.5, color: tokens.textPlaceholder }}>{hint}</span>
      <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={onRefresh}>
        刷新
      </Button>
    </header>
  )
}

function SectionEmpty({ text }: { text: string }) {
  return <div style={{ padding: '28px 20px', textAlign: 'center', color: tokens.textPlaceholder, fontSize: 12.5 }}>{text}</div>
}

function Row({ children }: { children: ReactNode }) {
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8, padding: '10px 16px', borderBottom: `1px solid ${tokens.border}` }}>
      {children}
    </div>
  )
}

/** 能力目录管理页（P1 只读；编辑/注入在 P2）。 */
export function CapabilitiesPage() {
  const showToast = useForge((s) => s.showToast)
  const [skills, setSkills] = useState<Skill[]>([])
  const [mcps, setMcps] = useState<McpServer[]>([])
  const [tools, setTools] = useState<Tool[]>([])
  const [busy, setBusy] = useState<string | null>(null)

  const loadSkills = async (): Promise<void> => {
    try {
      setSkills(await listSkills())
    } catch (err) {
      showToast(toErrorMessage(err))
    }
  }
  const loadMcps = async (): Promise<void> => {
    try {
      setMcps(await listMcpServers())
    } catch (err) {
      showToast(toErrorMessage(err))
    }
  }
  const loadTools = async (): Promise<void> => {
    try {
      setTools(await listTools())
    } catch (err) {
      showToast(toErrorMessage(err))
    }
  }

  const loadAll = useCallback(async (): Promise<void> => {
    await Promise.all([loadSkills(), loadMcps(), loadTools()])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    void loadAll()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const refresh = (key: 'skills' | 'mcps' | 'tools'): void => {
    setBusy(key)
    const p = key === 'skills' ? loadSkills() : key === 'mcps' ? loadMcps() : loadTools()
    void p.finally(() => setBusy(null))
  }

  return (
    <PageContainer
      title="能力"
      description="Skill 目录 / MCP 注册表 / Tool 目录（平台唯一事实源，目录文件 ~/.forge/capabilities/{skills,mcp,tools}）。本页只读；注入与物化在 P2 接线。"
      extra={
        <Space>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void loadAll()}>
            全部刷新
          </Button>
        </Space>
      }
    >
      <div style={{ padding: 18, display: 'flex', flexDirection: 'column', gap: 16 }}>
        <section style={{ border: `1px solid ${tokens.border}`, borderRadius: 12, background: tokens.surface, overflow: 'hidden' }}>
          <SectionHeader title="Skills" hint="目录条目（body 为 SKILL.md 内容；物化到各 runtime 属 P2）" count={skills.length} loading={busy === 'skills'} onRefresh={() => refresh('skills')} />
          <div style={{ maxHeight: 340, overflowY: 'auto' }}>
            {skills.length === 0 ? (
              <SectionEmpty text="暂无 skill（~/.forge/capabilities/skills/*.json）" />
            ) : (
              skills.map((s) => (
                <Row key={s.id}>
                  <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{s.name}</b>
                  <Tag style={{ marginInlineEnd: 0, fontSize: 11 }}>{s.id}</Tag>
                  <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>{s.description}</span>
                  {s.sources?.map((src, i) => (
                    <Tag key={i} style={{ fontSize: 10, marginInlineEnd: 0 }}>
                      {src.runtime}·{src.format}
                    </Tag>
                  ))}
                </Row>
              ))
            )}
          </div>
        </section>

        <section style={{ border: `1px solid ${tokens.border}`, borderRadius: 12, background: tokens.surface, overflow: 'hidden' }}>
          <SectionHeader title="MCP Servers" hint="注册表条目（headers/凭证后端不读回）" count={mcps.length} loading={busy === 'mcps'} onRefresh={() => refresh('mcps')} />
          <div style={{ maxHeight: 340, overflowY: 'auto' }}>
            {mcps.length === 0 ? (
              <SectionEmpty text="暂无 MCP server（~/.forge/capabilities/mcp/*.json）" />
            ) : (
              mcps.map((m) => (
                <Row key={m.id}>
                  <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{m.name || m.id}</b>
                  <Tag color={TRANSPORT_COLOR[m.transport] ?? 'default'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                    {m.transport}
                  </Tag>
                  <span style={{ flex: 1 }}>
                    <span style={mono}>{m.command ? [m.command, ...(m.args ?? [])].join(' ') : m.url}</span>
                  </span>
                </Row>
              ))
            )}
          </div>
        </section>

        <section style={{ border: `1px solid ${tokens.border}`, borderRadius: 12, background: tokens.surface, overflow: 'hidden' }}>
          <SectionHeader title="Tools" hint="平台/静态与观察兜底条目；MCP tools/list live 属 P2" count={tools.length} loading={busy === 'tools'} onRefresh={() => refresh('tools')} />
          <div style={{ maxHeight: 340, overflowY: 'auto' }}>
            {tools.length === 0 ? (
              <SectionEmpty text="暂无 tool（~/.forge/capabilities/tools/*.json 或代码注入）" />
            ) : (
              tools.map((t) => (
                <Row key={t.name}>
                  <span style={{ ...mono, fontWeight: 600, minWidth: 150 }}>{t.name}</span>
                  <Tag color={SOURCE_COLOR[t.source] ?? 'default'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                    {t.source}
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
        </section>
      </div>
    </PageContainer>
  )
}
