import { useMemo, useState } from 'react'
import { Alert, Button, Input, Modal } from 'antd'
import { tokens } from '@ark-mate/ui'
import { parseMcpJson, type McpImportOutcome, type McpJsonServer } from '../../console/capabilityView'

const PLACEHOLDER = `{
  "mcpServers": {
    "github": {
      "command": "/usr/local/bin/npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "GITHUB_TOKEN": "store:github/token" }
    }
  }
}`

/**
 * 粘贴 JSON 配置（720）：解析 `mcpServers` 片段，逐条 `POST /mcps`。
 *
 * 不新增后端批量接口：同名跳过（不覆盖）、任一条失败不阻断其他条，结果**留在弹窗里逐条列出**
 * ——批量操作只发 toast 会丢掉「哪几条没成功」。
 */
export function McpJsonImportModal({
  open,
  onClose,
  existingIds,
  onImport,
}: {
  open: boolean
  onClose: () => void
  existingIds: string[]
  onImport: (servers: McpJsonServer[]) => Promise<McpImportOutcome[]>
}) {
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [outcomes, setOutcomes] = useState<McpImportOutcome[] | null>(null)
  const [err, setErr] = useState<string | null>(null)

  const parsed = useMemo(() => (text.trim() ? parseMcpJson(text) : null), [text])
  const existing = useMemo(() => new Set(existingIds), [existingIds])
  const skipPreview = (parsed?.servers ?? []).filter((s) => existing.has(s.id)).map((s) => s.id)

  const close = (): void => {
    setText('')
    setOutcomes(null)
    setErr(null)
    onClose()
  }

  const run = async (): Promise<void> => {
    if (!parsed) return
    if (parsed.errors.length > 0) return
    if (parsed.servers.length === 0) return
    setBusy(true)
    setErr(null)
    setOutcomes(null)
    try {
      setOutcomes(await onImport(parsed.servers))
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const blocked = !parsed || parsed.errors.length > 0 || parsed.servers.length === 0

  return (
    <Modal
      open={open}
      onCancel={close}
      width={720}
      destroyOnHidden
      title={
        <div>
          <div style={{ fontSize: 15, fontWeight: 600 }}>粘贴 JSON 配置</div>
          <div style={{ marginTop: 2, fontSize: 11, color: tokens.textPlaceholder }}>
            可一次导入多个连接器。
          </div>
        </div>
      }
      footer={
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <Button onClick={close}>{outcomes ? '完成' : '取消'}</Button>
          <Button type="primary" disabled={blocked} loading={busy} onClick={() => void run()}>
            导入
          </Button>
        </div>
      }
    >
      <div style={{ fontSize: 11, color: tokens.textSecondary, marginBottom: 4 }}>连接器配置（JSON）</div>
      <Input.TextArea
        value={text}
        onChange={(e) => {
          setText(e.target.value)
          setOutcomes(null)
          setErr(null)
        }}
        rows={10}
        spellCheck={false}
        placeholder={PLACEHOLDER}
        style={{ fontFamily: tokens.fontMono, fontSize: 12.5 }}
      />
      <div style={{ marginTop: 4, fontSize: 10.5, color: tokens.textPlaceholder }}>
        支持命令方式（STDIO）或地址方式（SSE / HTTP）；同名连接器会被跳过。
        命令方式需要填写完整的可执行文件路径（例如 /usr/local/bin/npx）。
      </div>

      {parsed && parsed.errors.length > 0 && (
        <Alert
          type="error"
          showIcon
          style={{ marginTop: 8 }}
          message="配置有问题"
          description={
            <ul style={{ margin: 0, paddingLeft: 16 }}>
              {parsed.errors.map((e, i) => (
                <li key={i}>{e}</li>
              ))}
            </ul>
          }
        />
      )}
      {parsed && parsed.errors.length === 0 && parsed.servers.length > 0 && (
        <Alert
          type="info"
          showIcon
          style={{ marginTop: 8, fontSize: 11.5 }}
          message={`解析到 ${parsed.servers.length} 个连接器：${parsed.servers.map((s) => s.id).join('、')}`}
          description={
            skipPreview.length > 0
              ? `其中 ${skipPreview.join('、')} 已存在，导入时会跳过（不覆盖）。`
              : '密钥值请使用 store: / env: 引用；界面不会保存明文。'
          }
        />
      )}

      <Alert
        type="warning"
        showIcon
        style={{ marginTop: 8, fontSize: 11.5 }}
        message="逐条导入，部分失败不阻断"
        description="每一条的结果会列在下面（成功 / 跳过 / 失败 + 原因），不只看一个 toast。"
      />

      {err && <Alert type="error" showIcon style={{ marginTop: 8 }} message="导入失败" description={err} />}

      {outcomes && (
        <div style={{ marginTop: 10, maxHeight: 220, overflow: 'auto' }}>
          {outcomes.map((o) => (
            <Alert
              key={o.id}
              type={o.status === 'created' ? 'success' : o.status === 'skipped' ? 'info' : 'error'}
              showIcon
              style={{ marginBottom: 6, fontSize: 11.5 }}
              message={`${o.id}：${o.status === 'created' ? '已创建' : o.status === 'skipped' ? '已跳过' : '失败'}`}
              description={o.message}
            />
          ))}
        </div>
      )}
    </Modal>
  )
}
