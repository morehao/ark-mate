import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Empty, Input, Modal, Space, Spin, Tabs, Tag, Tooltip } from 'antd'
import { CopyOutlined, PlayCircleOutlined, ReloadOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import {
  getMcpPrompt,
  listMcpPrompts,
  listMcpResources,
  readMcpResource,
} from '@ark-mate/api'
import type {
  McpPrompt,
  McpPromptGetResult,
  McpPromptsResult,
  McpResourceReadResult,
  McpResourcesResult,
  McpServer,
  McpToolCallResult,
  Tool,
} from '@ark-mate/types'
import {
  mcpConnState,
  transportLongLabel,
  transportLabel,
  type McpRow,
} from '../../console/capabilityView'
import { fmtProbeAt } from '../../console/time'
import { useMate } from '../../store'
import { CapabilityEmpty, DetailGrid, SectionLabel, StatusTag, mono, promptText, schemaSkeleton, smallTag } from './shared'

const TABS = [
  { key: 'overview', label: '概览' },
  { key: 'tools', label: '工具' },
  { key: 'live', label: '资源与提示词' },
  { key: 'config', label: '配置' },
]

/**
 * MCP 详情（780）：概览 / 工具 / 资源与提示词 / 配置。
 *
 * 工具、资源、提示词**只在这里出现**（主页面不再单独成块）：它们随 server 变化，而「这个
 * server 现在到底能做什么」是一个需要一起看的问题。资源与提示词每次现拉、不落盘。
 */
export function McpDetailModal({
  server,
  row,
  tools,
  open,
  tab,
  onTabChange,
  onClose,
  onEdit,
  onProbe,
  probing,
  onCallTool,
}: {
  server: McpServer | null
  row: McpRow | null
  tools: Tool[]
  open: boolean
  tab: string
  onTabChange: (tab: string) => void
  onClose: () => void
  onEdit: () => void
  onProbe: () => void
  probing: boolean
  onCallTool: (server: string, tool: string, argsText: string) => Promise<{ result?: McpToolCallResult; err?: string }>
}) {
  const showToast = useMate((s) => s.showToast)

  // ——— 工具试调用 ———
  const [selectedTool, setSelectedTool] = useState<Tool | null>(null)
  const [argsText, setArgsText] = useState('{}')
  const [callBusy, setCallBusy] = useState(false)
  const [callResult, setCallResult] = useState<McpToolCallResult | null>(null)
  const [callErr, setCallErr] = useState<string | null>(null)

  // ——— 资源与提示词（live，不落盘） ———
  const [liveTab, setLiveTab] = useState<'resources' | 'prompts'>('resources')
  const [liveBusy, setLiveBusy] = useState(false)
  const [liveErr, setLiveErr] = useState<string | null>(null)
  const [resResult, setResResult] = useState<McpResourcesResult | null>(null)
  const [resRead, setResRead] = useState<{ uri: string; data?: McpResourceReadResult; err?: string } | null>(null)
  const [promptResult, setPromptResult] = useState<McpPromptsResult | null>(null)
  const [promptArgs, setPromptArgs] = useState<Record<string, string>>({})
  const [promptGot, setPromptGot] = useState<{ name: string; data?: McpPromptGetResult; err?: string } | null>(null)

  const loadLive = useCallback(
    async (id: string, which: 'resources' | 'prompts'): Promise<void> => {
      setLiveBusy(true)
      setLiveErr(null)
      try {
        if (which === 'resources') setResResult(await listMcpResources(id))
        else setPromptResult(await listMcpPrompts(id))
      } catch (err) {
        setLiveErr(err instanceof Error ? err.message : String(err))
      } finally {
        setLiveBusy(false)
      }
    },
    [],
  )

  // 换 server / 打开 live tab 时重置并现拉：上一次 server 的资源不能留给下一个。
  useEffect(() => {
    if (!open || !server) return
    if (tab !== 'live') return
    setResResult(null)
    setResRead(null)
    setPromptResult(null)
    setPromptGot(null)
    setPromptArgs({})
    setLiveErr(null)
    void loadLive(server.id, liveTab)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, server?.id, tab, liveTab])

  useEffect(() => {
    setSelectedTool(null)
    setCallResult(null)
    setCallErr(null)
  }, [server?.id])

  if (!server || !row) return null

  const state = mcpConnState(row)
  const serverTools = tools.filter((t) => t.mcp_server === server.id)
  const endpoint = row.endpoint
  const unresolved = new Set(row.unresolvedSecretKeys)

  const copy = async (text: string, label: string): Promise<void> => {
    try {
      await navigator.clipboard.writeText(text)
      showToast(`已复制${label}`, 'success')
    } catch {
      showToast('复制失败：请手动选中复制')
    }
  }

  const runCall = async (): Promise<void> => {
    if (!selectedTool) return
    setCallBusy(true)
    setCallErr(null)
    setCallResult(null)
    const res = await onCallTool(server.id, selectedTool.name, argsText)
    if (res.err) setCallErr(res.err)
    else if (res.result) setCallResult(res.result)
    setCallBusy(false)
  }

  const readResource = async (uri: string): Promise<void> => {
    setResRead({ uri })
    try {
      setResRead({ uri, data: await readMcpResource(server.id, uri) })
    } catch (err) {
      setResRead({ uri, err: err instanceof Error ? err.message : String(err) })
    }
  }

  const getPrompt = async (p: McpPrompt): Promise<void> => {
    setPromptGot({ name: p.name })
    try {
      setPromptGot({ name: p.name, data: await getMcpPrompt(server.id, p.name, promptArgs) })
    } catch (err) {
      setPromptGot({ name: p.name, err: err instanceof Error ? err.message : String(err) })
    }
  }

  const configJSON = {
    id: server.id,
    name: server.name,
    transport: server.transport,
    ...(server.transport === 'stdio'
      ? { command: server.command, args: server.args ?? [] }
      : { url: server.url }),
    enabled: server.enabled ?? true,
    ...(server.env_keys && server.env_keys.length > 0
      ? { env: Object.fromEntries(server.env_keys.map((k) => [k, '<已存值不回读>'])) }
      : {}),
    ...(server.secret_env_keys && server.secret_env_keys.length > 0
      ? { secret_env: Object.fromEntries(server.secret_env_keys.map((k) => [k, 'store:<secret>'])) }
      : {}),
    ...(server.secret_header_keys && server.secret_header_keys.length > 0
      ? { secret_headers: Object.fromEntries(server.secret_header_keys.map((k) => [k, 'store:<secret>'])) }
      : {}),
  }

  return (
    <Modal
      open={open}
      onCancel={onClose}
      width={780}
      destroyOnHidden
      title={
        <div style={{ paddingRight: 24 }}>
          <div style={{ fontSize: 15, fontWeight: 600, lineHeight: 1.35 }}>{server.name || server.id}</div>
          <div style={{ marginTop: 2, fontSize: 11, color: tokens.textPlaceholder, fontFamily: tokens.fontMono }}>
            {transportLongLabel(server.transport)} · {endpoint || '未声明端点'}
          </div>
        </div>
      }
      footer={
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
          <Button onClick={onClose}>关闭</Button>
          <Space size={8}>
            <Button onClick={onEdit}>编辑</Button>
            <Button type="primary" icon={<ReloadOutlined />} loading={probing} onClick={onProbe}>
              探测
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
          label: t.key === 'tools' ? `工具 ${serverTools.length}` : t.label,
          children:
            t.key === 'overview' ? (
              <div>
                {state.key === 'error' ? (
                  <Alert
                    type="error"
                    showIcon
                    message={`探测失败：${row.lastProbeError || '原因未知'}`}
                    description="先确认命令能在终端独立运行，再回来点「获取工具」。密钥未就绪时会被跳过——见下方「密钥与请求头」。"
                  />
                ) : state.key === 'needs_setup' ? (
                  <Alert
                    type="warning"
                    showIcon
                    message={`缺少 ${row.unresolvedSecretKeys.join('、')}`}
                    description="密钥已绑定但当前取不到值；使用时会跳过这个连接器。请检查对应的密钥或环境变量是否已设置。"
                  />
                ) : state.key === 'disabled' ? (
                  <Alert type="warning" showIcon message="已停用" description="工具不会进入模型上下文；配置与密钥保留。" />
                ) : state.key === 'pending' ? (
                  <Alert
                    type="info"
                    showIcon
                    message="尚未获取工具"
                    description="保存后点「获取工具」，即可同步它提供的工具。"
                  />
                ) : (
                  <Alert
                    type="success"
                    showIcon
                    message="已连接"
                    description={serverTools.length > 0 ? '工具已就绪，可随会话使用。' : '已连接，但它没有提供工具。'}
                  />
                )}

                <SectionLabel>端点</SectionLabel>
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
                  <span style={{ ...mono, flex: 1 }}>{endpoint || '（未声明）'}</span>
                  {endpoint && (
                    <Tooltip title="复制端点">
                      <Button
                        size="small"
                        type="text"
                        icon={<CopyOutlined />}
                        onClick={() => void copy(endpoint, '端点')}
                      />
                    </Tooltip>
                  )}
                </div>

                <SectionLabel>概要</SectionLabel>
                <DetailGrid
                  items={[
                    { label: '传输', value: transportLongLabel(server.transport) },
                    { label: '工具', value: serverTools.length },
                    { label: '最近更新', value: server.last_probe_at ? fmtProbeAt(server.last_probe_at) : '—' },
                    { label: '启用', value: server.enabled === false ? '否' : '是' },
                  ]}
                />

                <SectionLabel>密钥与请求头</SectionLabel>
                {row.secretKeys.length === 0 ? (
                  <div style={{ fontSize: 11.5, color: tokens.textPlaceholder }}>没有声明密钥或请求头。</div>
                ) : (
                  <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
                    {row.secretKeys.map((k) => (
                      <Tooltip
                        key={k}
                        title={
                          unresolved.has(k)
                            ? '引用已绑定但当前解析不出值：注入时会被跳过'
                            : '密钥型绑定（只有键名；值只在注入时解析）'
                        }
                      >
                        <Tag color={unresolved.has(k) ? 'red' : 'geekblue'} style={smallTag}>
                          {unresolved.has(k) ? `${k} 未就绪` : `${k} 已就绪`}
                        </Tag>
                      </Tooltip>
                    ))}
                  </div>
                )}
              </div>
            ) : t.key === 'tools' ? (
              <div>
                <div style={{ fontSize: 11.5, color: tokens.textSecondary, lineHeight: 1.7, marginBottom: 8 }}>
                  这里显示这个连接器提供的工具；没有条目时先点「获取工具」。
                  试运行工具时，写操作会先请求确认。
                </div>
                {serverTools.length === 0 ? (
                  <CapabilityEmpty
                    text={
                      row.lastProbeError
                        ? '获取工具失败——先在概览里看排查建议。'
                        : '还没有工具条目。'
                    }
                    action={
                      <Button size="small" icon={<ReloadOutlined />} loading={probing} onClick={onProbe}>
                        获取工具
                      </Button>
                    }
                  />
                ) : (
                  <div style={{ maxHeight: 300, overflowY: 'auto' }}>
                    {serverTools.map((tool) => (
                      <div
                        key={tool.name}
                        style={{
                          padding: '9px 2px',
                          borderBottom: `1px solid ${tokens.border}`,
                          display: 'flex',
                          alignItems: 'flex-start',
                          gap: 8,
                        }}
                      >
                        <span style={{ ...mono, fontWeight: 600, minWidth: 150 }}>{tool.name}</span>
                        <span style={{ flex: 1, fontSize: 11.5, color: tokens.textSecondary }}>
                          {tool.description || (tool.schema ? '（含参数 Schema）' : '')}
                        </span>
                        <Button
                          size="small"
                          icon={<PlayCircleOutlined />}
                          onClick={() => {
                            setSelectedTool(tool)
                            setArgsText(schemaSkeleton(tool.schema))
                            setCallResult(null)
                            setCallErr(null)
                          }}
                        >
                          试调用
                        </Button>
                      </div>
                    ))}
                  </div>
                )}

                {selectedTool && (
                  <div style={{ marginTop: 12 }}>
                    <SectionLabel>调用 {server.id}.{selectedTool.name}</SectionLabel>
                    <Input.TextArea
                      value={argsText}
                      onChange={(e) => setArgsText(e.target.value)}
                      rows={4}
                      spellCheck={false}
                      style={{ fontFamily: tokens.fontMono, fontSize: 12.5 }}
                      placeholder='{"path": "/tmp/a"}'
                    />
                    <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 6 }}>
                      <Button size="small" type="primary" loading={callBusy} onClick={() => void runCall()}>
                        调用
                      </Button>
                    </div>
                    {callErr && <Alert type="error" showIcon style={{ marginTop: 8 }} message="调用失败" description={callErr} />}
                    {callResult && (
                      <div style={{ marginTop: 8 }}>
                        <Space size={8} wrap style={{ marginBottom: 6 }}>
                          <Tag color={callResult.is_error ? 'red' : 'green'} style={smallTag}>
                            {callResult.is_error ? 'is_error=true（工具级失败）' : 'ok'}
                          </Tag>
                          <span style={{ fontSize: 11, color: tokens.textSecondary, fontFamily: tokens.fontMono }}>
                            {callResult.duration_ms}ms
                          </span>
                        </Space>
                        <pre
                          style={{
                            ...mono,
                            margin: 0,
                            padding: 10,
                            maxHeight: 220,
                            overflow: 'auto',
                            background: tokens.codeBg,
                            border: `1px solid ${tokens.border}`,
                            borderRadius: tokens.radiusSm,
                            whiteSpace: 'pre-wrap',
                          }}
                        >
                          {callResult.text || JSON.stringify(callResult.content, null, 2)}
                          {callResult.structured_content
                            ? `\n\nstructuredContent:\n${JSON.stringify(callResult.structured_content, null, 2)}`
                            : ''}
                        </pre>
                      </div>
                    )}
                  </div>
                )}
              </div>
            ) : t.key === 'live' ? (
              <div>
                <div style={{ fontSize: 11.5, color: tokens.textSecondary, lineHeight: 1.7, marginBottom: 8 }}>
                  这里的资源和提示词每次打开时实时获取，不会保存；它们会随连接器状态变化。
                </div>
                <Tabs
                  size="small"
                  activeKey={liveTab}
                  onChange={(k) => {
                    setLiveTab(k as 'resources' | 'prompts')
                    setLiveErr(null)
                  }}
                  items={[
                    {
                      key: 'resources',
                      label: `资源${resResult ? ` (${resResult.resources.length})` : ''}`,
                      children: (
                        <Spin spinning={liveBusy}>
                          {liveErr && liveTab === 'resources' ? (
                            <Alert type="error" showIcon message="资源拉取失败" description={liveErr} />
                          ) : !resResult || resResult.resources.length === 0 ? (
                            <Empty description="该连接器没有提供资源" />
                          ) : (
                            <div style={{ maxHeight: 360, overflowY: 'auto' }}>
                              {resResult.templates_error && (
                                <Alert
                                  type="info"
                                  showIcon
                                  style={{ marginBottom: 8 }}
                                  message="该连接器没有提供资源模板"
                                  description={resResult.templates_error}
                                />
                              )}
                              {(resResult.templates ?? []).map((tpl) => (
                                <div
                                  key={`tpl-${tpl.uri_template}`}
                                  style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '6px 0' }}
                                >
                                  <Tag color="purple" style={smallTag}>
                                    模板
                                  </Tag>
                                  <span style={{ ...mono, flex: 1 }}>{tpl.uri_template}</span>
                                  <span style={{ fontSize: 11, color: tokens.textSecondary }}>{tpl.name}</span>
                                </div>
                              ))}
                              {resResult.resources.map((r) => (
                                <div key={r.uri}>
                                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '6px 0' }}>
                                    <span style={{ ...mono, flex: 1 }}>{r.uri}</span>
                                    <span style={{ fontSize: 11, color: tokens.textSecondary }}>
                                      {r.name}
                                      {r.mime_type ? ` · ${r.mime_type}` : ''}
                                      {typeof r.size === 'number' ? ` · ${r.size}B` : ''}
                                    </span>
                                    <Button size="small" onClick={() => void readResource(r.uri)}>
                                      读取
                                    </Button>
                                  </div>
                                  {resRead?.uri === r.uri && (
                                    <div style={{ paddingBottom: 8 }}>
                                      {resRead.err ? (
                                        <Alert type="error" showIcon message="读取失败" description={resRead.err} />
                                      ) : (
                                        (resRead.data?.contents ?? []).map((c, i) => (
                                          <pre
                                            key={i}
                                            style={{
                                              ...mono,
                                              margin: '0 0 6px',
                                              padding: 8,
                                              maxHeight: 180,
                                              overflow: 'auto',
                                              background: tokens.codeBg,
                                              border: `1px solid ${tokens.border}`,
                                              borderRadius: tokens.radiusSm,
                                              whiteSpace: 'pre-wrap',
                                            }}
                                          >
                                            {c.text ?? `（二进制 blob，base64 ${c.blob?.length ?? 0} 字符，前端不自动解码）`}
                                          </pre>
                                        ))
                                      )}
                                    </div>
                                  )}
                                </div>
                              ))}
                            </div>
                          )}
                        </Spin>
                      ),
                    },
                    {
                      key: 'prompts',
                      label: `提示词${promptResult ? ` (${promptResult.prompts.length})` : ''}`,
                      children: (
                        <Spin spinning={liveBusy}>
                          {liveErr && liveTab === 'prompts' ? (
                            <Alert type="error" showIcon message="提示词拉取失败" description={liveErr} />
                          ) : !promptResult || promptResult.prompts.length === 0 ? (
                            <Empty description="该连接器没有提供提示词" />
                          ) : (
                            <div style={{ maxHeight: 360, overflowY: 'auto' }}>
                              {promptResult.prompts.map((p) => (
                                <div key={p.name}>
                                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '6px 0' }}>
                                    <span style={{ ...mono, minWidth: 140 }}>{p.name}</span>
                                    <span style={{ flex: 1, fontSize: 11.5, color: tokens.textSecondary }}>
                                      {p.description}
                                    </span>
                                    <Button size="small" onClick={() => void getPrompt(p)}>
                                      取用
                                    </Button>
                                  </div>
                                  {(p.arguments ?? []).length > 0 && (
                                    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', paddingBottom: 6 }}>
                                      {(p.arguments ?? []).map((a) => (
                                        <Input
                                          key={a.name}
                                          size="small"
                                          style={{ width: 180 }}
                                          placeholder={`${a.name}${a.required ? '（必填）' : ''}`}
                                          value={promptArgs[a.name] ?? ''}
                                          onChange={(e) =>
                                            setPromptArgs((cur) => ({ ...cur, [a.name]: e.target.value }))
                                          }
                                        />
                                      ))}
                                    </div>
                                  )}
                                  {promptGot?.name === p.name && (
                                    <div style={{ paddingBottom: 8 }}>
                                      {promptGot.err ? (
                                        <Alert type="error" showIcon message="取用失败" description={promptGot.err} />
                                      ) : (
                                        <pre
                                          style={{
                                            ...mono,
                                            margin: 0,
                                            padding: 8,
                                            maxHeight: 180,
                                            overflow: 'auto',
                                            background: tokens.codeBg,
                                            border: `1px solid ${tokens.border}`,
                                            borderRadius: tokens.radiusSm,
                                            whiteSpace: 'pre-wrap',
                                          }}
                                        >
                                          {(promptGot.data?.messages ?? [])
                                            .map((m) => `${m.role}: ${promptText(m.content)}`)
                                            .join('\n')}
                                        </pre>
                                      )}
                                    </div>
                                  )}
                                </div>
                              ))}
                            </div>
                          )}
                        </Spin>
                      ),
                    },
                  ]}
                />
              </div>
            ) : (
              <div>
                <Alert
                  type="info"
                  showIcon
                  style={{ marginBottom: 10, fontSize: 11.5 }}
                  message="安全边界"
                  description="明文 env 的值后端从不读回；密钥型绑定只接受 store:/env: 引用（值在库或进程环境里）。这里展示的是读面能给的全部事实。"
                />
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
                  {JSON.stringify(configJSON, null, 2)}
                </pre>
                <div style={{ marginTop: 6, fontSize: 10.5, color: tokens.textPlaceholder }}>
                  传输 tag 只显示 {transportLabel(server.transport)}；完整名称是 {transportLongLabel(server.transport)}。
                  状态：<StatusTag label={state.label} tone={state.tone} />
                </div>
              </div>
            ),
        }))}
      />
    </Modal>
  )
}
