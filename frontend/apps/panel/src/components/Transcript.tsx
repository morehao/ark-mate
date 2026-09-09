import { useState, type CSSProperties, type ReactNode } from 'react'
import { Button, Checkbox, Input, InputNumber, Popover, Radio } from 'antd'
import { CheckOutlined, CloseOutlined, CopyOutlined, ReloadOutlined } from '@ant-design/icons'
import { tokens } from '@ark/ui'
import type { ElicitationFieldSchema, PermissionOption, StreamEntry } from '@ark/types'
import { useForge, type PermissionDecision } from '../store'
import { copyText } from '../console/clipboard'
import { parseMsg } from '../console/message'
import { runStatRows, type RunUsageInfo } from '../console/usageView'
import { Md } from './Md'

const roleLabel: CSSProperties = {
  fontFamily: tokens.fontMono,
  fontSize: 10,
  letterSpacing: '0.07em',
  textTransform: 'uppercase',
  color: tokens.textPlaceholder,
  display: 'flex',
  alignItems: 'center',
  gap: 8,
}

function Role({ children }: { children: ReactNode }) {
  return <div style={roleLabel}>{children}</div>
}

function toolStatusMeta(status: string): { label: string; color: string; pulse?: boolean } {
  const st = status.toLowerCase()
  if (['completed', 'ok', 'success', 'done'].includes(st)) return { label: 'ok', color: tokens.success }
  if (['running', 'started', 'pending', 'queued'].includes(st)) return { label: status || 'running', color: tokens.primary, pulse: true }
  if (['failed', 'error', 'err'].includes(st)) return { label: 'failed', color: tokens.error }
  return { label: status || '…', color: tokens.textSecondary }
}

const toneColor: Record<NonNullable<StreamEntry['tone']>, string> = {
  ok: tokens.success,
  err: tokens.error,
  cancel: tokens.warning,
  info: tokens.textSecondary,
}

const systemIcon = {
  ok: <CheckOutlined style={{ fontSize: 11, color: tokens.success }} />,
  err: <CloseOutlined style={{ fontSize: 11, color: tokens.error }} />,
  cancel: <CloseOutlined style={{ fontSize: 11, color: tokens.warning }} />,
  info: <CheckOutlined style={{ fontSize: 11, color: tokens.textSecondary }} />,
}

/** 由选项 kind 推导决策（allow/reject × once/always 映射为 ACP 选项语义）。 */
function decisionFor(opt: PermissionOption): PermissionDecision {
  const allowed = opt.kind.startsWith('allow')
  const always = opt.kind.includes('_always')
  const label = `${allowed ? '允许' : '拒绝'} · ${always ? '始终' : '本次'}`
  return { allowed, optionId: opt.option_id, always, label }
}

/** 权限确认卡（ACP session/request_permission → 用户点选经 REST 回传）。 */
function PermissionCard({ e }: { e: StreamEntry }) {
  const respond = useForge((s) => s.respondPermission)
  const options = e.optionKinds ?? []
  const resolved = e.resolved ?? false
  // 兜底「拒绝」在 agent 提供 deny 选项时自动带选首个（拒绝与取消/超时保持可区分）。
  const denyOption = options.find((o) => o.kind.startsWith('reject'))?.option_id

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 5, minWidth: 0 }}>
      <Role>assistant · permission</Role>
      <div
        style={{
          border: `1px solid ${tokens.warning}`,
          borderRadius: 12,
          background: tokens.surface,
          padding: '11px 13px',
          display: 'flex',
          flexDirection: 'column',
          gap: 10,
          maxWidth: 720,
          minWidth: 0,
        }}
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
          <span style={{ fontSize: 13.5, fontWeight: 620, color: tokens.text }}>
            {e.title || '工具权限请求'}
          </span>
          <span
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 6,
              fontFamily: tokens.fontMono,
              fontSize: 10.5,
              color: tokens.textSecondary,
            }}
          >
            {e.toolKind && <span>{e.toolKind}</span>}
            {e.toolKind && e.toolCallId && <span style={{ color: tokens.textPlaceholder }}>·</span>}
            {e.toolCallId && <span>{e.toolCallId}</span>}
          </span>
        </div>
        {resolved ? (
          <span
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 7,
              fontFamily: tokens.fontMono,
              fontSize: 11,
              color: tokens.textSecondary,
            }}
          >
            <CheckOutlined style={{ fontSize: 11, color: tokens.success }} />
            已确认：{e.decision ?? '已响应'}
          </span>
        ) : options.length > 0 ? (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
            {options.map((opt) => (
              <Button
                key={opt.option_id}
                size="small"
                onClick={() => void respond(e.callId ?? '', decisionFor(opt))}
                style={{ borderRadius: 8, margin: 0 }}
              >
                {opt.name || decisionFor(opt).label}
              </Button>
            ))}
            <Button
              size="small"
              danger
              onClick={() =>
                void respond(e.callId ?? '', { allowed: false, always: false, optionId: denyOption, label: '拒绝 · 本次' })
              }
              style={{ borderRadius: 8, margin: 0 }}
            >
              拒绝
            </Button>
          </div>
        ) : (
          <div style={{ display: 'flex', gap: 8 }}>
            <Button
              size="small"
              type="primary"
              onClick={() => void respond(e.callId ?? '', { allowed: true, always: false, label: '允许 · 本次' })}
              style={{ borderRadius: 8, margin: 0 }}
            >
              允许 · 本次
            </Button>
            <Button
              size="small"
              onClick={() => void respond(e.callId ?? '', { allowed: true, always: true, label: '允许 · 始终' })}
              style={{ borderRadius: 8, margin: 0 }}
            >
              允许 · 始终
            </Button>
            <Button
              size="small"
              danger
              onClick={() => void respond(e.callId ?? '', { allowed: false, always: false, label: '拒绝' })}
              style={{ borderRadius: 8, margin: 0 }}
            >
              拒绝
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}

/** 单选项可读名（oneOf 标题优先；enum/多选项用裸值）。 */
function labelOf(value: unknown, titles?: { const: string; title: string }[]): string {
  if (titles) {
    for (const o of titles) if (o.const === value) return o.title
  }
  return String(value ?? '')
}

/** 提交内容 → 可读摘要（键稳定序，单选/多选回显标题，供已决卡片展示）。 */
function summarizeContent(schema: StreamEntry['schema'], content?: Record<string, unknown>): string {
  if (!content || Object.keys(content).length === 0) return '（未提供内容）'
  const props = schema?.properties ?? {}
  const entries = Object.keys(content)
    .sort()
    .map((k) => {
      const f = props[k]
      const v = content[k]
      if (f?.type === 'array' && Array.isArray(v)) {
        const titles = f.items?.oneOf
        return `${k} = [${(v as unknown[]).map((x) => labelOf(x, titles)).join(', ')}]`
      }
      if (f?.type === 'boolean') return `${k} = ${v ? '是' : '否'}`
      return `${k} = ${labelOf(v, f?.oneOf)}`
    })
  return entries.join('；')
}

/** 结构化提问卡（ACP elicitation/create → schema 渲染表单 / 引导链接 → 用户提交经 REST 回传）。 */
function ElicitationCard({ e }: { e: StreamEntry }) {
  const respond = useForge((s) => s.respondElicitation)
  const resolved = e.resolved ?? false
  const busy = useForge((s) => {
    const v = s.activeId ? s.views[s.activeId] : undefined
    return v?.streaming ?? false
  })

  const form = e.mode === 'url'
    ? null
    : { properties: e.schema?.properties ?? {}, required: new Set(e.schema?.required ?? []) }

  const [answers, setAnswers] = useState<Record<string, unknown>>(() => {
    const init: Record<string, unknown> = {}
    if (form) {
      for (const [k, f] of Object.entries(form.properties)) {
        if (f.default !== undefined) init[k] = f.default
        if (f.type === 'boolean' && init[k] === undefined) init[k] = false
      }
    }
    return init
  })
  const [error, setError] = useState<string | null>(null)

  const validate = (): Record<string, unknown> | null => {
    if (!form) return null
    const out: Record<string, unknown> = {}
    for (const [k, f] of Object.entries(form.properties)) {
      const v = answers[k]
      const required = form.required.has(k)
      if (f.type === 'boolean') {
        out[k] = v === true || v === false ? v : false
        continue
      }
      if (v === undefined || v === null || v === '') {
        if (required) {
          setError(`「${f.title ?? k}」为必填项`)
          return null
        }
        continue
      }
      if (f.type === 'array') {
        const list = Array.isArray(v) ? v : []
        if (required && list.length === 0) {
          setError(`「${f.title ?? k}」至少选择 1 项`)
          return null
        }
        if (f.minItems != null && list.length < f.minItems) {
          setError(`「${f.title ?? k}」至少选择 ${f.minItems} 项`)
          return null
        }
        if (f.maxItems != null && list.length > f.maxItems) {
          setError(`「${f.title ?? k}」最多选择 ${f.maxItems} 项`)
          return null
        }
        out[k] = list
        continue
      }
      if (f.type === 'integer' || f.type === 'number') {
        const n = typeof v === 'number' ? v : Number(v)
        if (!Number.isFinite(n)) {
          setError(`「${f.title ?? k}」需为数字`)
          return null
        }
        if (f.type === 'integer' && !Number.isInteger(n)) {
          setError(`「${f.title ?? k}」需为整数`)
          return null
        }
        if (f.minimum != null && n < f.minimum) {
          setError(`「${f.title ?? k}」不能小于 ${f.minimum}`)
          return null
        }
        if (f.maximum != null && n > f.maximum) {
          setError(`「${f.title ?? k}」不能大于 ${f.maximum}`)
          return null
        }
        out[k] = n
        continue
      }
      const str = String(v)
      if (f.minLength != null && str.length < f.minLength) {
        setError(`「${f.title ?? k}」至少 ${f.minLength} 个字符`)
        return null
      }
      if (f.maxLength != null && str.length > f.maxLength) {
        setError(`「${f.title ?? k}」至多 ${f.maxLength} 个字符`)
        return null
      }
      out[k] = str
    }
    return out
  }

  const submit = () => {
    const content = validate()
    if (!e.elicitationId) return
    if (form && content === null) return // 校验失败，错误已置位
    setError(null)
    void respond(e.elicitationId, { action: 'accept', content: content ?? {}, label: '已提交' })
  }
  const decline = () => {
    if (!e.elicitationId) return
    setError(null)
    void respond(e.elicitationId, { action: 'decline', label: '已拒绝' })
  }
  const cancel = () => {
    if (!e.elicitationId) return
    setError(null)
    void respond(e.elicitationId, { action: 'cancel', label: '已取消' })
  }

  const fieldTitle = (k: string, f: NonNullable<typeof form>['properties'][string]): string =>
    `${f?.title ?? k}${form?.required.has(k) ? ' *' : ''}`

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 5, minWidth: 0 }}>
      <Role>assistant · question</Role>
      <div
        style={{
          border: `1px solid ${tokens.primary}`,
          borderRadius: 12,
          background: tokens.surface,
          padding: '11px 13px',
          display: 'flex',
          flexDirection: 'column',
          gap: 10,
          maxWidth: 720,
          minWidth: 0,
        }}
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
          <span style={{ fontSize: 13.5, fontWeight: 620, color: tokens.text }}>{e.message || '需要你确认一些信息'}</span>
          <span style={{ fontFamily: tokens.fontMono, fontSize: 10.5, color: tokens.textSecondary }}>
            elicitation · {form ? 'form' : 'url'}
            {e.elicitationId ? ` · ${e.elicitationId}` : ''}
          </span>
        </div>

        {resolved ? (
          <span
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 7,
              fontFamily: tokens.fontMono,
              fontSize: 11,
              color: tokens.textSecondary,
              overflowWrap: 'anywhere',
            }}
          >
            <CheckOutlined style={{ fontSize: 11, color: tokens.success }} />
            {e.decision ?? '已响应'}
            {e.content && Object.keys(e.content).length > 0 && `：${summarizeContent(e.schema, e.content)}`}
          </span>
        ) : form ? (
          <>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 9 }}>
              {Object.entries(form.properties).map(([k, f]) => (
                <div key={k} style={{ display: 'flex', flexDirection: 'column', gap: 3, minWidth: 0 }}>
                  <span style={{ fontSize: 12, fontWeight: 600, color: tokens.textSecondary }}>{fieldTitle(k, f)}</span>
                  {f?.description && (
                    <span style={{ fontSize: 11, color: tokens.textPlaceholder }}>{f.description}</span>
                  )}
                  <ElicitationField k={k} f={f} value={answers[k]} onChange={(v) => setAnswers((a) => ({ ...a, [k]: v }))} />
                </div>
              ))}
            </div>
            {error && (
              <span style={{ fontSize: 11.5, color: tokens.error }}>{error}</span>
            )}
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
              <Button size="small" type="primary" disabled={busy} onClick={submit} style={{ borderRadius: 8, margin: 0 }}>
                提交
              </Button>
              <Button size="small" danger disabled={busy} onClick={decline} style={{ borderRadius: 8, margin: 0 }}>
                拒绝提供
              </Button>
              <Button size="small" disabled={busy} onClick={cancel} style={{ borderRadius: 8, margin: 0 }}>
                取消
              </Button>
            </div>
          </>
        ) : (
          <>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
              {e.url && (
                <a href={e.url} target="_blank" rel="noreferrer" style={{ color: tokens.primary, fontSize: 12.5, overflowWrap: 'anywhere' }}>
                  {e.url}
                </a>
              )}
              <span style={{ fontSize: 11.5, color: tokens.textSecondary }}>完成外部操作后回到这里告知 agent（URL 模式演示）。</span>
            </div>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
              <Button size="small" type="primary" disabled={busy} onClick={submit} style={{ borderRadius: 8, margin: 0 }}>
                我已完成
              </Button>
              <Button size="small" danger disabled={busy} onClick={decline} style={{ borderRadius: 8, margin: 0 }}>
                拒绝
              </Button>
              <Button size="small" disabled={busy} onClick={cancel} style={{ borderRadius: 8, margin: 0 }}>
                取消
              </Button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

/** 单个表单字段控件：单选（enum/oneOf）→ radio；多选（array）→ checkbox；布尔 → 是/否；其余按类型输入。 */
function ElicitationField({
  k,
  f,
  value,
  onChange,
}: {
  k: string
  f: ElicitationFieldSchema
  value: unknown
  onChange: (v: unknown) => void
}) {
  if (f.type === 'array') {
    const items = f.items
    const opts = items?.oneOf ?? (items?.enum ?? []).map((v) => ({ const: v, title: v }))
    const chosen = Array.isArray(value) ? (value as string[]) : []
    return (
      <Checkbox.Group
        options={opts.map((o) => ({ label: o.title, value: o.const }))}
        value={chosen}
        onChange={(vals) => onChange(vals as string[])}
        style={{ display: 'flex', flexDirection: 'column', gap: 4 }}
      />
    )
  }
  if (f.oneOf || (f.type === 'string' && f.enum)) {
    const opts = f.oneOf ?? (f.enum ?? []).map((v) => ({ const: v, title: v }))
    return (
      <Radio.Group value={value as string} onChange={(ev) => onChange(ev.target.value)}>
        {opts.map((o) => (
          <Radio key={o.const} value={o.const}>
            {o.title}
          </Radio>
        ))}
      </Radio.Group>
    )
  }
  if (f.type === 'boolean') {
    return (
      <Radio.Group
        value={value === true || value === false ? value : undefined}
        onChange={(ev) => onChange(ev.target.value === true)}
      >
        <Radio value={false as unknown}>否</Radio>
        <Radio value={true as unknown}>是</Radio>
      </Radio.Group>
    )
  }
  if (f.type === 'integer' || f.type === 'number') {
    return (
      <InputNumber
        size="small"
        style={{ width: 160 }}
        value={typeof value === 'number' ? value : undefined}
        onChange={(v) => onChange(v)}
      />
    )
  }
  return (
    <Input
      size="small"
      value={typeof value === 'string' ? value : ''}
      onChange={(ev) => onChange(ev.target.value)}
      placeholder={f?.description || `${k}…`}
      style={{ maxWidth: 420 }}
    />
  )
}

/** 消息头右侧的「复制整条消息」小按钮（Clipboard API + execCommand 兜底）。 */
function MsgCopy({ text }: { text: string }) {
  const toast = useForge((s) => s.showToast)
  return (
    <Button
      type="text"
      size="small"
      icon={<CopyOutlined />}
      title="复制消息全文"
      aria-label="复制消息全文"
      disabled={!text}
      onClick={async () => {
        const ok = await copyText(text)
        toast(ok ? '已复制消息' : '复制失败，请手动选择文本')
      }}
      style={{ flex: 'none', color: tokens.textPlaceholder, height: 20, minWidth: 20, padding: 0 }}
    />
  )
}

/** 围栏代码块：mono 卡片 + 语言/行数 + 一键复制 + 长块折叠。 */
function CodeBlock({ lang, text }: { lang?: string; text: string }) {
  const toast = useForge((s) => s.showToast)
  const code = text
  const lines = code.split('\n').length
  const long = code.length > 700 || lines > 22
  const [folded, setFolded] = useState(long)
  return (
    <div
      style={{
        margin: '8px 0',
        border: `1px solid ${tokens.border}`,
        borderRadius: 10,
        overflow: 'hidden',
        background: tokens.codeBg,
        fontFamily: tokens.fontMono,
        maxWidth: '100%',
        minWidth: 0,
      }}
    >
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          padding: '4px 6px 4px 11px',
          background: tokens.surface,
          borderBottom: `1px solid ${tokens.border}`,
        }}
      >
        <span style={{ fontSize: 11, fontWeight: 600, color: tokens.textSecondary, textTransform: 'lowercase' }}>
          {lang || 'code'}
        </span>
        <span style={{ fontSize: 10, color: tokens.textPlaceholder }}>{lines} 行</span>
        <span style={{ marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 2 }}>
          {long && (
            <Button type="text" size="small" onClick={() => setFolded((v) => !v)} style={{ fontSize: 11, color: tokens.textSecondary }}>
              {folded ? '展开' : '收起'}
            </Button>
          )}
          <Button
            type="text"
            size="small"
            icon={<CopyOutlined />}
            title="复制代码"
            onClick={async () => {
              const ok = await copyText(code)
              toast(ok ? '已复制代码' : '复制失败，请手动选择')
            }}
            style={{ fontSize: 11, color: tokens.textSecondary }}
          >
            复制
          </Button>
        </span>
      </div>
      <pre
        style={{
          margin: 0,
          padding: '10px 12px',
          fontSize: 12,
          lineHeight: 1.6,
          whiteSpace: 'pre',
          overflow: 'auto',
          maxHeight: folded ? 264 : undefined,
          color: tokens.text,
        }}
      >
        {code}
      </pre>
    </div>
  )
}

/** 消息富文本：普通文本按 pre-wrap 原样呈现，``` 围栏段渲染为可复制/折叠的代码块。 */
function RichText({ text }: { text: string }) {
  const parts = parseMsg(text)
  return (
    <>
      {parts.map((p, i) =>
        p.kind === 'code' ? (
          <CodeBlock key={i} lang={p.lang} text={p.text} />
        ) : (
          <Md key={i} text={p.text} />
        ),
      )}
    </>
  )
}

/** 一条流式会话条目（console.html .msg 家族：气泡 / 思考 / 工具卡 / meta 行）。 */
export function EntryView({
  e,
  onRetry,
  usageText,
  runInfo,
}: {
  e: StreamEntry
  onRetry?: () => void
  /** 该轮用量展示行（如 `上下文 30.0k / 128.0k · Δ +3.2k`；仅终态轮的 turn-end 条目有值）。 */
  usageText?: string
  /** 该轮用量的结构化数据（含首令牌/总耗时/成本），供 goose 风格 popover；无则仅显示 usageText。 */
  runInfo?: RunUsageInfo
}) {
  switch (e.kind) {
    case 'permission':
      return <PermissionCard e={e} />
    case 'elicitation':
      return <ElicitationCard e={e} />
    case 'user':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 5, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 4 }}>
            <MsgCopy text={e.text ?? ''} />
            <Role>user</Role>
          </div>
          <div
            style={{
              alignSelf: 'flex-end',
              maxWidth: 'min(78%, 560px)',
              background: tokens.softFill,
              border: `1px solid ${tokens.border}`,
              borderRadius: '14px 14px 4px 14px',
              padding: '9px 13px',
              fontSize: 15,
              lineHeight: 1.65,
              whiteSpace: 'pre-wrap',
              overflowWrap: 'anywhere',
              color: tokens.text,
              minWidth: 0,
            }}
          >
            <RichText text={e.text ?? ''} />
          </div>
        </div>
      )

    case 'assistant':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 5, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 4 }}>
            <Role>assistant</Role>
            <MsgCopy text={e.text ?? ''} />
          </div>
          <div
            style={{
              color: tokens.text,
              fontSize: 15,
              lineHeight: 1.72,
              whiteSpace: 'pre-wrap',
              overflowWrap: 'anywhere',
              padding: '0 2px',
            }}
          >
            <RichText text={e.text ?? ''} />
          </div>
        </div>
      )

    case 'thought':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 5, minWidth: 0 }}>
          <Role>assistant · thinking</Role>
          <div
            style={{
              display: 'flex',
              gap: 10,
              borderLeft: `2px solid ${tokens.border}`,
              padding: '2px 0 2px 12px',
              color: tokens.textSecondary,
              fontSize: 13.5,
              lineHeight: 1.7,
              minWidth: 0,
            }}
          >
            <span
              style={{
                flex: 'none',
                fontFamily: tokens.fontMono,
                fontSize: 10,
                letterSpacing: '0.08em',
                textTransform: 'uppercase',
                color: tokens.textPlaceholder,
                paddingTop: 3,
              }}
            >
              思考
            </span>
            <span style={{ minWidth: 0, overflowWrap: 'anywhere', whiteSpace: 'pre-wrap' }}>{e.text}</span>
          </div>
        </div>
      )

    case 'tool': {
      const meta = toolStatusMeta(e.status ?? '')
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 5, minWidth: 0 }}>
          <Role>assistant · tool</Role>
          <div
            style={{
              border: `1px solid ${tokens.border}`,
              borderRadius: 12,
              background: tokens.surface,
              padding: '10px 13px',
              display: 'flex',
              flexDirection: 'column',
              gap: 8,
              maxWidth: 720,
              minWidth: 0,
            }}
          >
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 7,
                fontFamily: tokens.fontMono,
                fontSize: 11.5,
                minWidth: 0,
              }}
            >
              <span style={{ color: tokens.text, fontWeight: 600 }}>tool_call</span>
              <span style={{ color: tokens.textPlaceholder }}>·</span>
              <span
                style={{
                  color: tokens.textSecondary,
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                  minWidth: 0,
                }}
              >
                {e.title}
              </span>
            </div>
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 6,
                fontFamily: tokens.fontMono,
                fontSize: 10.5,
                color: tokens.textSecondary,
              }}
            >
              {meta.pulse ? (
                <span
                  className="fg-anim-pulse"
                  style={{ flex: 'none', width: 7, height: 7, borderRadius: '50%', background: meta.color, display: 'inline-block' }}
                />
              ) : meta.label === 'ok' ? (
                <CheckOutlined style={{ fontSize: 11, color: meta.color }} />
              ) : (
                <span style={{ flex: 'none', width: 7, height: 7, borderRadius: '50%', background: meta.color, display: 'inline-block' }} />
              )}
              <span style={{ color: tokens.text, fontWeight: 500 }}>{meta.label}</span>
              <span style={{ marginLeft: 'auto' }}>tool_result · {e.status || 'running'}</span>
            </div>
          </div>
        </div>
      )
    }

    case 'system': {
      const tone = (e.tone ?? 'info') as NonNullable<StreamEntry['tone']>
      const retry = tone === 'err' && !!onRetry
      return (
        <div
          style={{
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            gap: 6,
            margin: '4px 0',
          }}
        >
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              gap: 7,
              fontFamily: tokens.fontMono,
              fontSize: 10.5,
              color: toneColor[tone],
              textAlign: 'center',
              overflowWrap: 'anywhere',
            }}
          >
            {systemIcon[tone]}
            <span>{e.text}</span>
            {usageText && (
              <Popover
                title="本轮用量"
                trigger="hover"
                placement="bottom"
                content={
                  runInfo ? (
                    <div style={{ minWidth: 230 }}>
                      {runStatRows(runInfo).map((r) => (
                        <div
                          key={r.label}
                          style={{ display: 'flex', justifyContent: 'space-between', gap: 24, padding: '2px 0' }}
                        >
                          <span style={{ color: tokens.textSecondary }}>{r.label}</span>
                          <span style={{ fontFamily: tokens.fontMono }}>{r.value}</span>
                        </div>
                      ))}
                      <div
                        style={{
                          marginTop: 6,
                          paddingTop: 6,
                          borderTop: `1px solid ${tokens.border}`,
                          color: tokens.textPlaceholder,
                          fontSize: 11,
                          lineHeight: 1.5,
                        }}
                      >
                        上下文为窗口快照（非增量）；input/output 明细未由底座上报。
                      </div>
                    </div>
                  ) : null
                }
              >
                <span
                  style={{
                    color: tokens.textSecondary,
                    display: 'inline-flex',
                    gap: 7,
                    alignItems: 'center',
                    cursor: runInfo ? 'help' : 'default',
                  }}
                >
                  <span style={{ color: tokens.border }}>·</span>
                  {usageText}
                </span>
              </Popover>
            )}
          </div>
          {retry && (
            <Button
              size="small"
              icon={<ReloadOutlined />}
              onClick={onRetry}
              title="重新发送该轮输入"
              style={{ borderRadius: 8, fontSize: 12 }}
            >
              重试该轮
            </Button>
          )}
          {e.stderrTail && e.stderrTail.length > 0 && (
            // A3：runtime stderr 诊断尾巴（默认折叠，避免长输出挤压对话区）。
            <details
              style={{
                width: '100%',
                maxWidth: 720,
                border: `1px solid ${tokens.border}`,
                borderRadius: 10,
                background: tokens.softFill,
                padding: '6px 10px',
                fontSize: 11.5,
                color: tokens.textSecondary,
              }}
            >
              <summary style={{ cursor: 'pointer', fontFamily: tokens.fontMono, fontSize: 11 }}>
                runtime stderr 诊断尾巴（{e.stderrTail.length} 行）
              </summary>
              <pre
                style={{
                  margin: '6px 0 0',
                  maxHeight: 220,
                  overflow: 'auto',
                  fontFamily: tokens.fontMono,
                  fontSize: 11,
                  lineHeight: 1.5,
                  whiteSpace: 'pre-wrap',
                  overflowWrap: 'anywhere',
                  color: tokens.text,
                }}
              >
                {e.stderrTail.join('\n')}
              </pre>
            </details>
          )}
        </div>
      )
    }
  }
}
