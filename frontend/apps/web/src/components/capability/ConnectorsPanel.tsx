import { Alert, Button, Empty, Input, Modal, Space, Spin, Tabs, Tag, Tooltip } from 'antd'
import { PlayCircleOutlined, ReloadOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import { toolSourceLabel } from '../../console/capabilityView'
import { SOURCE_COLOR, TRANSPORT_COLOR, mono, SectionHeader, SectionEmpty, Row, Card, promptText, CapabilityEditorModal } from './shared'
import { useCapabilityData } from './useCapabilityData'

/** MCP server 与它们承载的工具：传输、密钥引用（只显示键名）、live 探测与 tools/call 试调——定义来自 ~/.ark-mate/capabilities/{mcp,tools}。 */
export function ConnectorsPanel() {
  const {
    mcps,
    tools,
    edit,
    setEdit,
    editBusy,
    editErr,
    busy,
    callTarget,
    setCallTarget,
    callArgs,
    setCallArgs,
    callBusy,
    callResult,
    callErr,
    liveServer,
    setLiveServer,
    liveTab,
    setLiveTab,
    liveBusy,
    liveErr,
    setLiveErr,
    resResult,
    resRead,
    promptResult,
    promptArgs,
    setPromptArgs,
    promptGot,
    loadAll,
    refresh,
    globalMcpRows,
    probeButton,
    openCreate,
    openEditMcp,
    submitEdit,
    openCall,
    doCall,
    loadLive,
    doReadResource,
    doGetPrompt,
    liveButton,
    probeStatus,
    secretTags,
    writeButtons,
  } = useCapabilityData('connectors')

  return (
    <PageContainer
      title="连接器"
      description="MCP server 与它们承载的工具：传输、密钥引用（只显示键名）、live 探测与 tools/call 试调——定义来自 ~/.ark-mate/capabilities/{mcp,tools}"
      extra={
        <Space>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void loadAll()}>
            刷新
          </Button>
        </Space>
      }
    >
      <div style={{ padding: 18, display: 'flex', flexDirection: 'column', gap: 16 }}>
        {/* ——— 全局：MCP Servers（含 live 探测） ——— */}
        <Card>
          <SectionHeader
            title="MCP Servers"
            hint="注册表条目（§13.3 M1/M6：只回明文与密钥的**键名**，值与引用一律不读回）；「探测」= live tools/list 写入 Tool 目录；删除会**一并摘除归属它的工具条目**（留着等于让你看见一个调不通的能力）"
            count={globalMcpRows.length}
            loading={busy === 'mcps'}
            onRefresh={() => refresh('mcps')}
            extra={
              <Button size="small" onClick={() => openCreate('mcp')}>
                新建 MCP server
              </Button>
            }
          />
          <div style={{ maxHeight: 340, overflowY: 'auto' }}>
            {globalMcpRows.length === 0 ? (
              <SectionEmpty text="暂无 MCP server（~/.ark-mate/capabilities/mcp/*.json）" />
            ) : (
              globalMcpRows.map((m) => (
                <Row key={m.id}>
                  <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{m.name}</b>
                  <Tag color={TRANSPORT_COLOR[m.transport] ?? 'default'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                    {m.transport}
                  </Tag>
                  {secretTags(m)}
                  {probeStatus(m)}
                  <span style={{ flex: 1 }}>
                    <span style={mono}>{m.endpoint}</span>
                  </span>
                  {writeButtons('mcp', m.id, m.name, () => {
                    const full = mcps.find((x) => x.id === m.id)
                    if (full) openEditMcp(full)
                  })}
                  {probeButton(m.id, m.probeable)}
                  {liveButton(m)}
                </Row>
              ))
            )}
          </div>
        </Card>
        {/* ——— 全局：Tools ——— */}
        <Card>
          <SectionHeader
            title="Tools"
            hint="平台/静态 + MCP tools/list live 探测条目（带归属 server）"
            count={tools.length}
            loading={busy === 'tools'}
            onRefresh={() => refresh('tools')}
          />
          <div style={{ maxHeight: 340, overflowY: 'auto' }}>
            {tools.length === 0 ? (
              <SectionEmpty text="暂无 tool（~/.ark-mate/capabilities/tools/*.json、代码注入或 MCP 探测）" />
            ) : (
              tools.map((t) => (
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
                  {t.mcp_server && (
                    <Tooltip title={`调用 ${t.mcp_server}.${t.name}（tools/call，独立会话）`}>
                      <Button size="small" icon={<PlayCircleOutlined />} onClick={() => openCall(t.mcp_server!, t)}>
                        调用
                      </Button>
                    </Tooltip>
                  )}
                </Row>
              ))
            )}
          </div>
        </Card>
        <Modal
          title={liveServer ? `${liveServer.name} · 资源与提示词` : '资源与提示词'}
          open={liveServer !== null}
          onCancel={() => setLiveServer(null)}
          footer={null}
          width={760}
        >
          <div style={{ fontSize: 12.5, color: tokens.textSecondary, lineHeight: 1.8, marginBottom: 8 }}>
            实时向该 MCP server 拉取（<code>resources/list</code> + <code>resources/templates/list</code> /
            <code> prompts/list</code>）：**不落盘、不目录化**——资源与提示词随 server 权限/状态变化，
            每次打开现拉最诚实。
          </div>
          <Tabs
            activeKey={liveTab}
            onChange={(k) => {
              const tab = k as 'resources' | 'prompts'
              setLiveTab(tab)
              setLiveErr(null)
              if (liveServer) void loadLive(liveServer.id, tab)
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
                      <Empty description="该 server 未暴露资源（或未实现 resources/list）" />
                    ) : (
                      <div style={{ maxHeight: 420, overflowY: 'auto' }}>
                        {resResult.templates_error && (
                          <Alert
                            type="info"
                            showIcon
                            style={{ marginBottom: 8 }}
                            message="该 server 未实现 resources/templates/list（降级为空）"
                            description={resResult.templates_error}
                          />
                        )}
                        {(resResult.templates ?? []).map((t) => (
                          <Row key={`tpl-${t.uri_template}`}>
                            <Tag color="purple" style={{ marginInlineEnd: 0, fontSize: 10.5 }}>
                              模板
                            </Tag>
                            <span style={{ ...mono, flex: 1 }}>{t.uri_template}</span>
                            <span style={{ fontSize: 11.5, color: tokens.textSecondary }}>{t.name}</span>
                          </Row>
                        ))}
                        {resResult.resources.map((r) => (
                          <div key={r.uri}>
                            <Row>
                              <span style={{ ...mono, flex: 1 }}>{r.uri}</span>
                              <span style={{ fontSize: 11.5, color: tokens.textSecondary }}>
                                {r.name}
                                {r.mime_type ? ` · ${r.mime_type}` : ''}
                                {typeof r.size === 'number' ? ` · ${r.size}B` : ''}
                              </span>
                              <Button size="small" onClick={() => void doReadResource(liveServer!.id, r.uri)}>
                                读取
                              </Button>
                            </Row>
                            {resRead?.uri === r.uri && (
                              <div style={{ padding: '0 16px 8px' }}>
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
                                        maxHeight: 200,
                                        overflow: 'auto',
                                        background: tokens.softFill,
                                        borderRadius: 8,
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
                      <Empty description="该 server 未暴露提示词（或未实现 prompts/list）" />
                    ) : (
                      <div style={{ maxHeight: 420, overflowY: 'auto' }}>
                        {promptResult.prompts.map((p) => (
                          <div key={p.name}>
                            <Row>
                              <span style={{ ...mono, minWidth: 140 }}>{p.name}</span>
                              <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>{p.description}</span>
                              <Button size="small" onClick={() => void doGetPrompt(liveServer!.id, p)}>
                                取用
                              </Button>
                            </Row>
                            {(p.arguments ?? []).length > 0 && (
                              <div style={{ padding: '0 16px 8px', display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                                {(p.arguments ?? []).map((a) => (
                                  <Input
                                    key={a.name}
                                    size="small"
                                    style={{ width: 180 }}
                                    placeholder={`${a.name}${a.required ? '（必填）' : ''}`}
                                    value={promptArgs[a.name] ?? ''}
                                    onChange={(e) => setPromptArgs((cur) => ({ ...cur, [a.name]: e.target.value }))}
                                  />
                                ))}
                              </div>
                            )}
                            {promptGot?.name === p.name && (
                              <div style={{ padding: '0 16px 8px' }}>
                                {promptGot.err ? (
                                  <Alert type="error" showIcon message="取用失败" description={promptGot.err} />
                                ) : (
                                  <pre
                                    style={{
                                      ...mono,
                                      margin: 0,
                                      padding: 8,
                                      maxHeight: 200,
                                      overflow: 'auto',
                                      background: tokens.softFill,
                                      borderRadius: 8,
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
        </Modal>
        <Modal
          title={callTarget ? `调用工具 ${callTarget.server}.${callTarget.tool}` : '调用工具'}
          open={callTarget !== null}
          onCancel={() => setCallTarget(null)}
          onOk={() => void doCall()}
          okText="调用"
          confirmLoading={callBusy}
          width={680}
        >
          <div style={{ fontSize: 12.5, color: tokens.textSecondary, lineHeight: 1.8, marginBottom: 8 }}>
            参数为 JSON 对象（`arguments`）；后端每次调用建独立 MCP 会话（不做连接池）。
            工具级失败（服务端 <code>isError</code>）会以 <code>is_error=true</code> 正常返回，
            只有传输/协议/超时才报错。
          </div>
          <Input.TextArea
            value={callArgs}
            onChange={(e) => setCallArgs(e.target.value)}
            rows={5}
            spellCheck={false}
            style={{ fontFamily: tokens.fontMono, fontSize: 12.5 }}
            placeholder='{"path": "/tmp/a"}'
          />
          {callErr && (
            <Alert type="error" showIcon style={{ marginTop: 10 }} message="调用失败" description={callErr} />
          )}
          {callResult && (
            <div style={{ marginTop: 10 }}>
              <Space size={8} wrap style={{ marginBottom: 6 }}>
                <Tag color={callResult.is_error ? 'red' : 'green'} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                  {callResult.is_error ? 'is_error=true（工具级失败）' : 'ok'}
                </Tag>
                <span style={{ fontSize: 11.5, color: tokens.textSecondary, fontFamily: tokens.fontMono }}>
                  {callResult.duration_ms}ms
                  {callResult.server_name ? ` · ${callResult.server_name}` : ''}
                  {callResult.server_version ? ` ${callResult.server_version}` : ''}
                </span>
              </Space>
              <pre
                style={{
                  ...mono,
                  margin: 0,
                  padding: 10,
                  maxHeight: 260,
                  overflow: 'auto',
                  background: tokens.softFill,
                  borderRadius: 8,
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
        </Modal>
      <CapabilityEditorModal
        edit={edit}
        setEdit={setEdit}
        submitEdit={submitEdit}
        editBusy={editBusy}
        editErr={editErr}
      />
      </div>
    </PageContainer>
  )
}
