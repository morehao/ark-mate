import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Checkbox, Input, Modal, Segmented, Space, Tag, Tooltip } from 'antd'
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { McpServer } from '@ark-mate/types'
import {
  mcpCreateBody,
  mcpFormPatch,
  mcpFormValues,
  type McpBindingRow,
  type McpFormValues,
  type McpTransportValue,
} from '../../console/capabilityView'
import { mono, smallTag } from './shared'

const TRANSPORT_OPTIONS = [
  { label: 'STDIO', value: 'stdio' },
  { label: 'SSE', value: 'sse' },
  { label: 'Streamable HTTP', value: 'streamable_http' },
]

/**
 * MCP 编辑器（720）：三 transport + 命令/URL + env/Header + 定义 JSON 预览。
 *
 * 编辑态**不渲染可编辑的 env/Header 行**：读面只回键名、不回值，而 `env` 在写面上是
 * **顶层键整体替换**——凭键名重建一遍等于把已存的引用全部删掉。新增/删除引用走「高级（JSON）」，
 * 那条路本来就是给精确表达准备的。
 */
export function McpEditorModal({
  open,
  server,
  busy,
  error,
  onCancel,
  onSubmit,
  onAdvanced,
}: {
  open: boolean
  server: McpServer | null
  busy: boolean
  error: string | null
  onCancel: () => void
  onSubmit: (values: McpFormValues) => Promise<void>
  onAdvanced: (values: McpFormValues) => void
}) {
  const created = server === null
  const [values, setValues] = useState<McpFormValues>(() => emptyForm())
  const [localErr, setLocalErr] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setLocalErr(null)
    setValues(server ? mcpFormValues(server) : emptyForm())
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, server?.id])

  const patch = (next: Partial<McpFormValues>): void => setValues((cur) => ({ ...cur, ...next }))

  const preview = useMemo(() => {
    if (!server) return maskSecretPreview(mcpCreateBody(values))
    return maskSecretPreview(mcpFormPatch(mcpFormValues(server), values))
  }, [server, values])

  const updateRow = (kind: 'env' | 'headers', index: number, next: Partial<McpBindingRow>): void => {
    setValues((cur) => ({
      ...cur,
      [kind]: cur[kind].map((row, i) => (i === index ? { ...row, ...next } : row)),
    }))
  }
  const addRow = (kind: 'env' | 'headers'): void => {
    setValues((cur) => ({
      ...cur,
      [kind]: [...cur[kind], { key: '', value: '', secret: false, existed: false, removed: false }],
    }))
  }
  const removeRow = (kind: 'env' | 'headers', index: number): void => {
    setValues((cur) => ({ ...cur, [kind]: cur[kind].filter((_, i) => i !== index) }))
  }

  const submit = (): void => {
    const id = values.id.trim()
    if (created) {
      if (!id) return setLocalErr('id 必填')
      if (!/^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(id) || id.length > 64) {
        return setLocalErr('id 须字母/数字开头，只含字母/数字/._-，≤64 位')
      }
    }
    if (values.transport === 'stdio') {
      const command = values.command.trim()
      if (!command) return setLocalErr('本地命令不能为空')
      if (!command.startsWith('/')) {
        return setLocalErr('命令要填完整的可执行文件路径，例如 /usr/local/bin/npx（只写 npx 无法启动）')
      }
    } else if (!values.url.trim()) {
      return setLocalErr('远程服务必须填写地址')
    }
    setLocalErr(null)
    void onSubmit({ ...values, id })
  }

  const showBindings = created

  return (
    <Modal
      open={open}
      onCancel={onCancel}
      width={720}
      destroyOnHidden
      title={
        <div>
          <div style={{ fontSize: 15, fontWeight: 600 }}>{created ? '新建连接器' : '编辑连接器'}</div>
          <div style={{ marginTop: 2, fontSize: 11, color: tokens.textPlaceholder }}>
            {created ? '保存前会检查填写内容；不通过时不会保存。' : '只会提交改动过的内容。'}
          </div>
        </div>
      }
      footer={
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
          <Button onClick={onCancel}>取消</Button>
          <Space size={8}>
            <Button type="text" onClick={() => onAdvanced(values)}>
              高级（JSON）
            </Button>
            <Button type="primary" loading={busy} onClick={submit}>
              {created ? '新建' : '保存'}
            </Button>
          </Space>
        </div>
      }
    >
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
        <div>
          <div style={{ fontSize: 11, color: tokens.textSecondary, marginBottom: 4 }}>id *</div>
          <Input value={values.id} disabled={!created} placeholder="filesystem" onChange={(e) => patch({ id: e.target.value })} />
        </div>
        <div>
          <div style={{ fontSize: 11, color: tokens.textSecondary, marginBottom: 4 }}>名称</div>
          <Input value={values.name} placeholder="filesystem" onChange={(e) => patch({ name: e.target.value })} />
        </div>
      </div>

      <div style={{ fontSize: 11, color: tokens.textSecondary, margin: '10px 0 4px' }}>传输</div>
      <Segmented
        block
        value={values.transport}
        options={TRANSPORT_OPTIONS}
        onChange={(v) => patch({ transport: v as McpTransportValue })}
      />
      {!created && values.originalTransport === 'http' && values.transport !== 'streamable_http' && (
        <div style={{ marginTop: 4, fontSize: 10.5, color: tokens.textPlaceholder }}>
          原配置使用的是旧式 HTTP；只有你改动了连接方式才会更新，不动它就保持原样。
        </div>
      )}

      {values.transport === 'stdio' ? (
        <>
          <div style={{ fontSize: 11, color: tokens.textSecondary, margin: '10px 0 4px' }}>命令 *</div>
          <Input
            value={values.command}
            placeholder="/usr/local/bin/npx"
            onChange={(e) => patch({ command: e.target.value })}
            style={{ fontFamily: tokens.fontMono }}
          />
          <div style={{ margin: '3px 0 0', fontSize: 10.5, color: tokens.textPlaceholder }}>
            请填写完整的可执行文件路径；参数放在下一行。只写命令名无法启动。
          </div>
          <div style={{ fontSize: 11, color: tokens.textSecondary, margin: '10px 0 4px' }}>参数</div>
          <Input
            value={values.args}
            placeholder="-y @modelcontextprotocol/server-filesystem ~/Projects"
            onChange={(e) => patch({ args: e.target.value })}
            style={{ fontFamily: tokens.fontMono }}
          />
          <div style={{ marginTop: 3, fontSize: 10.5, color: tokens.textPlaceholder }}>
            按空白切分；含空格的参数用双引号包住。
          </div>
        </>
      ) : (
        <>
          <div style={{ fontSize: 11, color: tokens.textSecondary, margin: '10px 0 4px' }}>URL *</div>
          <Input
            value={values.url}
            placeholder="https://mcp.example.com/mcp"
            onChange={(e) => patch({ url: e.target.value })}
            style={{ fontFamily: tokens.fontMono }}
          />
        </>
      )}

      <div style={{ fontSize: 11, color: tokens.textSecondary, margin: '12px 0 4px' }}>环境变量</div>
      {showBindings ? (
        <>
          {values.env.map((row, i) => (
            <div key={`env-${i}`} style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6 }}>
              <Input
                size="small"
                style={{ width: 200, fontFamily: tokens.fontMono }}
                placeholder="KEY"
                value={row.key}
                onChange={(e) => updateRow('env', i, { key: e.target.value })}
              />
              <Input
                size="small"
                style={{ flex: 1, fontFamily: tokens.fontMono }}
                placeholder={row.secret ? 'store:… / env:…（留空 = 不提交）' : 'value'}
                value={row.value}
                onChange={(e) => updateRow('env', i, { value: e.target.value })}
              />
              <Tooltip title="密钥型：值只接受 store:/env: 引用，不会保存明文">
                <Checkbox checked={row.secret} onChange={(e) => updateRow('env', i, { secret: e.target.checked })}>
                  <span style={{ fontSize: 11 }}>密钥</span>
                </Checkbox>
              </Tooltip>
              <Button size="small" type="text" icon={<DeleteOutlined />} onClick={() => removeRow('env', i)} />
            </div>
          ))}
          <Button size="small" type="link" icon={<PlusOutlined />} onClick={() => addRow('env')}>
            添加变量
          </Button>
        </>
      ) : (
        <EditBindingsNotice
          keys={values.env.filter((r) => r.key).map((r) => r.key)}
        />
      )}

      {values.transport !== 'stdio' && (
        <>
          <div style={{ fontSize: 11, color: tokens.textSecondary, margin: '12px 0 4px' }}>Header</div>
          {showBindings ? (
            <>
              {values.headers.map((row, i) => (
                <div key={`h-${i}`} style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6 }}>
                  <Input
                    size="small"
                    style={{ width: 200, fontFamily: tokens.fontMono }}
                    placeholder="Authorization"
                    value={row.key}
                    onChange={(e) => updateRow('headers', i, { key: e.target.value })}
                  />
                  <Input
                    size="small"
                    style={{ flex: 1, fontFamily: tokens.fontMono }}
                    placeholder={row.secret ? 'store:… / env:…（留空 = 不提交）' : 'value'}
                    value={row.value}
                    onChange={(e) => updateRow('headers', i, { value: e.target.value })}
                  />
                  <Tooltip title="密钥型：值只接受 store:/env: 引用">
                    <Checkbox checked={row.secret} onChange={(e) => updateRow('headers', i, { secret: e.target.checked })}>
                      <span style={{ fontSize: 11 }}>密钥</span>
                    </Checkbox>
                  </Tooltip>
                  <Button size="small" type="text" icon={<DeleteOutlined />} onClick={() => removeRow('headers', i)} />
                </div>
              ))}
              <Button size="small" type="link" icon={<PlusOutlined />} onClick={() => addRow('headers')}>
                添加 Header
              </Button>
            </>
          ) : (
            <EditBindingsNotice keys={values.headers.map((r) => r.key)} />
          )}
        </>
      )}

      <div style={{ fontSize: 11, color: tokens.textSecondary, margin: '12px 0 4px' }}>
        {created ? '配置预览（JSON）' : '将提交的变更'}
      </div>
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
        {JSON.stringify(preview, null, 2)}
      </pre>
      <div style={{ marginTop: 4, fontSize: 10.5, color: tokens.textPlaceholder }}>
        保存后请在列表点「探测」拉取工具列表（探测是显式动作，不自动执行）。
      </div>

      {localErr && <Alert type="error" showIcon style={{ marginTop: 10 }} message={localErr} />}
      {error && <Alert type="error" showIcon style={{ marginTop: 10 }} message="保存失败" description={error} />}
    </Modal>
  )
}

function emptyForm(): McpFormValues {
  return {
    id: '',
    name: '',
    transport: 'stdio',
    originalTransport: '',
    command: '',
    args: '',
    url: '',
    env: [{ key: '', value: '', secret: false, existed: false, removed: false }],
    headers: [],
  }
}

/** 密钥引用在预览里统一掩码：界面上没必要回显用户刚敲的引用路径。 */
function maskSecretPreview(body: Record<string, unknown>): Record<string, unknown> {
  const clone = JSON.parse(JSON.stringify(body)) as Record<string, unknown>
  for (const key of ['secret_env', 'secret_headers']) {
    const block = clone[key]
    if (block && typeof block === 'object' && !Array.isArray(block)) {
      const out: Record<string, string> = {}
      for (const k of Object.keys(block as Record<string, unknown>)) out[k] = 'store:<secret>'
      clone[key] = out
    }
  }
  return clone
}

function EditBindingsNotice({ keys }: { keys: string[] }) {
  return (
    <>
      {keys.length > 0 ? (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
          {keys.map((k) => (
            <Tag key={k} style={smallTag}>
              {k}
            </Tag>
          ))}
        </div>
      ) : (
        <div style={{ fontSize: 11.5, color: tokens.textPlaceholder }}>没有声明环境变量。</div>
      )}
      <Alert
        type="info"
        showIcon
        style={{ marginTop: 6, fontSize: 11 }}
        message="编辑态不回读 env/Header 的值"
        description="读面只给键名；env 在写面上是顶层键整体替换，凭键名重建会删掉已存的引用。要新增/删除引用，请走「高级（JSON）」。"
      />
    </>
  )
}
