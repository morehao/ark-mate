import { useEffect, useMemo, useState } from 'react'
import { Button, Input, Popconfirm, Space, Switch, Tag, Tooltip } from 'antd'
import { SearchOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import { fmtProbeAt, mcpConnState, mcpRowDescription } from '../../console/capabilityView'
import { AddCapabilityMenu, type CapabilityAddAction } from './AddCapabilityMenu'
import { McpDetailModal } from './McpDetailModal'
import { McpEditorModal } from './McpEditorModal'
import { McpJsonImportModal } from './McpJsonImportModal'
import {
  CapabilityEditorModal,
  CapabilityEmpty,
  CapabilityRow,
  SectionHeader,
  TransportTag,
  mono,
  smallTag,
} from './shared'
import { useCapabilityData } from './useCapabilityData'

/** 连接器页：MCP server、探测与启停；工具/资源/提示词全部收进详情弹窗。 */
export function ConnectorsPanel() {
  const d = useCapabilityData('connectors')
  const [query, setQuery] = useState('')

  useEffect(() => {
    if (d.detail && !d.globalMcpRows.some((r) => r.id === d.detail!.id)) d.closeDetail()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [d.detail, d.globalMcpRows])

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return d.globalMcpRows
    return d.globalMcpRows.filter((row) => {
      const toolNames = d.tools
        .filter((t) => t.mcp_server === row.id)
        .map((t) => t.name)
        .join(' ')
      return [row.id, row.name, row.endpoint, row.transport, row.lastProbeError ?? '', toolNames]
        .join(' ')
        .toLowerCase()
        .includes(q)
    })
  }, [d.globalMcpRows, d.tools, query])

  const detailRow = d.detail?.kind === 'mcp' ? (d.globalMcpRows.find((r) => r.id === d.detail!.id) ?? null) : null
  const detailServer = d.detail?.kind === 'mcp' ? (d.mcps.find((m) => m.id === d.detail!.id) ?? null) : null

  const onAdd = (action: CapabilityAddAction): void => {
    if (action === 'mcp-manual') d.openMcpForm(null)
    if (action === 'mcp-json') d.setImportOpen(true)
  }

  return (
    <PageContainer
      title="连接器"
      description="管理 MCP server、探测工具，并查看它们承载的资源与提示词。"
      extra={
        <Space size={8}>
          <Input
            size="small"
            allowClear
            prefix={<SearchOutlined style={{ color: tokens.textPlaceholder }} />}
            style={{ width: 210 }}
            placeholder="搜索 MCP / 工具"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          <AddCapabilityMenu kind="mcp" onPick={onAdd} />
        </Space>
      }
    >
      <SectionHeader
        title="MCP Servers"
        hint="传输、密钥引用与 live 探测结果；工具与资源在服务详情里查看，密钥只显示键名。删除 server 会一并摘除归属它的工具。"
        count={visible.length}
        loading={d.busy === 'mcps'}
        onRefresh={() => d.refresh('mcps')}
      />
      <div style={{ maxHeight: 560, overflowY: 'auto' }}>
        {visible.length === 0 ? (
          <CapabilityEmpty
            text={
              d.mcps.length === 0
                ? '暂无 MCP server。新建一个后，探测结果会写入工具目录。'
                : `没有找到匹配「${query}」的 MCP server。`
            }
            action={
              d.mcps.length === 0 ? (
                <AddCapabilityMenu kind="mcp" onPick={onAdd} label="添加 MCP server" type="primary" />
              ) : undefined
            }
          />
        ) : (
          visible.map((row) => {
            const state = mcpConnState(row)
            const desc = mcpRowDescription(row, state, row.lastProbeError)
            const full = d.mcps.find((m) => m.id === row.id)
            const descColor =
              state.tone === 'error' ? tokens.error : state.tone === 'warning' ? tokens.warning : tokens.textSecondary
            const unresolved = row.unresolvedSecretKeys.length > 0
            return (
              <CapabilityRow
                key={row.id}
                title={
                  <>
                    <span
                      role="button"
                      tabIndex={0}
                      onClick={() => d.openDetail('mcp', row.id)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault()
                          d.openDetail('mcp', row.id)
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
                      {row.name}
                    </span>
                    <TransportTag transport={row.transport} />
                    <Tooltip title={state.key === 'error' ? row.lastProbeError : undefined}>
                      <Tag color={state.color} style={smallTag}>
                        {state.label}
                      </Tag>
                    </Tooltip>
                    {unresolved ? (
                      <Tooltip title={`引用已绑定但当前解析不了：${row.unresolvedSecretKeys.join('、')}。注入时会被跳过。`}>
                        <Tag color="red" style={smallTag}>
                          {row.unresolvedSecretKeys.length === 1
                            ? `${row.unresolvedSecretKeys[0]} 未就绪`
                            : `${row.unresolvedSecretKeys.length} 个密钥未就绪`}
                        </Tag>
                      </Tooltip>
                    ) : (
                      row.secretKeys.length > 0 && (
                        <Tooltip title={`密钥型绑定（值只在注入时解析，界面不回）：${row.secretKeys.join('、')}`}>
                          <Tag color="geekblue" style={smallTag}>
                            密钥 {row.secretKeys.length}
                          </Tag>
                        </Tooltip>
                      )
                    )}
                  </>
                }
                description={<span style={{ color: descColor }}>{desc}</span>}
                meta={
                  <>
                    <span
                      title={row.endpoint}
                      style={{ ...mono, fontSize: 10, color: tokens.textSecondary, maxWidth: 420, overflow: 'hidden', textOverflow: 'ellipsis' }}
                    >
                      {row.endpoint || '（未声明端点）'}
                    </span>
                    <span style={{ opacity: 0.65 }}>·</span>
                    <span>{row.toolCount} tools</span>
                    {row.lastProbeAt && (
                      <>
                        <span style={{ opacity: 0.65 }}>·</span>
                        <span>探测 {fmtProbeAt(row.lastProbeAt)}</span>
                      </>
                    )}
                  </>
                }
                actions={
                  <>
                    {state.key === 'error' ? (
                      <Button size="small" onClick={() => d.openDetail('mcp', row.id)}>
                        诊断
                      </Button>
                    ) : (
                      <Tooltip title={row.probeable ? '拉取该 server 的 tools/list 并写入 Tool 目录' : '该传输无法探测（缺少端点声明）'}>
                        <Button
                          size="small"
                          disabled={!row.probeable}
                          loading={d.probing === row.id}
                          onClick={() => void d.doProbe(row.id)}
                        >
                          探测
                        </Button>
                      </Tooltip>
                    )}
                    <Button size="small" onClick={() => full && d.openMcpForm(full)}>
                      编辑
                    </Button>
                    <Popconfirm
                      title={`删除 ${row.name}？`}
                      description="定义文件会被删除，归属它的工具条目会一并摘除。此操作不可撤销。"
                      okText="删除"
                      okButtonProps={{ danger: true }}
                      cancelText="取消"
                      onConfirm={() => void d.doDelete('mcp', row.id)}
                    >
                      <Button size="small" danger loading={d.delBusy === row.id}>
                        删除
                      </Button>
                    </Popconfirm>
                    <Tooltip title={row.defEnabled ? '停用：工具不再进入模型上下文（配置与密钥保留）' : '启用：下次会话/拉起时注入'}>
                      <Switch
                        size="small"
                        checked={row.defEnabled}
                        loading={d.toggleBusy === row.id}
                        onChange={(next) => void d.doToggleMcp(row, next)}
                      />
                    </Tooltip>
                  </>
                }
              />
            )
          })
        )}
      </div>

      <McpDetailModal
        server={detailServer}
        row={detailRow}
        tools={d.tools}
        open={detailServer !== null && detailRow !== null}
        tab={d.detail?.tab ?? 'overview'}
        onTabChange={d.setDetailTab}
        onClose={d.closeDetail}
        onEdit={() => {
          if (!detailServer) return
          d.closeDetail()
          d.openMcpForm(detailServer)
        }}
        onProbe={() => {
          if (detailServer) void d.doProbe(detailServer.id)
        }}
        probing={detailServer !== null && d.probing === detailServer.id}
        onCallTool={d.doCallTool}
      />
      <McpEditorModal
        open={d.mcpForm !== null}
        server={d.mcpForm?.server ?? null}
        busy={d.formBusy}
        error={d.formErr}
        onCancel={d.closeMcpForm}
        onSubmit={d.submitMcpForm}
        onAdvanced={d.openMcpAdvanced}
      />
      <McpJsonImportModal
        open={d.importOpen}
        onClose={() => d.setImportOpen(false)}
        existingIds={d.mcps.map((m) => m.id)}
        onImport={d.doImportMcpServers}
      />
      <CapabilityEditorModal
        edit={d.edit}
        setEdit={d.setEdit}
        submitEdit={d.submitEdit}
        editBusy={d.editBusy}
        editErr={d.editErr}
      />
    </PageContainer>
  )
}
