import { Fragment, useEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { Button, Checkbox, Input, InputNumber, Popover, Radio } from 'antd'
import {
  CheckOutlined,
  CloseOutlined,
  CodeOutlined,
  CopyOutlined,
  DeleteOutlined,
  DownOutlined,
  EditOutlined,
  EyeOutlined,
  FolderOpenOutlined,
  GlobalOutlined,
  ReloadOutlined,
  SearchOutlined,
  ToolOutlined,
} from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { ElicitationFieldSchema, PermissionOption, StreamEntry } from '@ark-mate/types'
import { useMate, type PermissionDecision } from '../store'
import { copyText } from '../console/clipboard'
import { AttachmentChip } from './AttachmentChip'
import { parseMsg } from '../console/message'
import { runStatRows, type RunUsageInfo } from '../console/usageView'
import {
  isThoughtFold,
  toolDetail,
  toolView,
  type ProcessBlock,
  type ToolDetail,
  type ToolIconName,
  type ThoughtFold,
  type ToolView,
  type TurnModel,
} from '../console/transcriptView'
import { Md } from './Md'
import { ThoughtFoldView } from './ThoughtFoldView'

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
export function PermissionCard({ e }: { e: StreamEntry }) {
  const respond = useMate((s) => s.respondPermission)
  const options = e.optionKinds ?? []
  const resolved = e.resolved ?? false
  // 兜底「拒绝」在底座提供 deny 选项时自动带选首个（拒绝与取消/超时保持可区分）。
  const denyOption = options.find((o) => o.kind.startsWith('reject'))?.option_id

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 5, minWidth: 0 }}>
      <Role>assistant · permission</Role>
      <div
        style={{
          // 权限卡视觉（原型 §面 C / `.perm`）：黄底 + 警示描边，与"需要你做决定"的语义一致。
          border: `1px solid ${tokens.warningBorder}`,
          borderRadius: 12,
          background: tokens.warningBg,
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
        {/* 超时转 pending（ADR-11）：**卡片留着**，只多一句"仍可回答"。这一档的全部意义就是
            用户去倒杯水回来还能答——把它画成"已收尾"等于把那一档删掉。 */}
        {e.pendingLate && !resolved && (
          <span style={{ fontSize: 11.5, color: tokens.textSecondary }}>
            这一轮已经不等了，但问题仍然可以回答；回答会作为新一轮发出去。
          </span>
        )}

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

/**
 * 工具失败原因的**用户可读文案**（S4 / 原型场景 E）。
 *
 * 两种原因的处置**相反**，文案也必须分开：
 *
 *	`sandbox_denied`        被沙箱边界拒了 → 可以引导换一个允许写的位置/模式（再试**有用**）
 *	`sandbox_unavailable`   宿主没有可用的沙箱后端 → 再试一次也一样失败（引导升权是**错的**）
 *
 * 未知值按"出了点什么"如实说，不猜成其中任何一种——猜错的那一半会让用户照着一条错的引导去修。
 */
function toolFailReasonLabel(reason: string): string {
  switch (reason) {
    case 'sandbox_denied':
      return '这一步被工作空间的写入边界挡下了：换一个允许写入的位置，或改用允许修改的模式再试。'
    case 'sandbox_unavailable':
      return '这台机器上没有可用的沙箱后端，所以这一步没能执行——重试不会改变结果，需要先装好沙箱依赖。'
    default:
      return '这一步没有成功（原因见上方输出）。'
  }
}

/** 结构化提问卡（ACP elicitation/create → schema 渲染表单 / 引导链接 → 用户提交经 REST 回传）。 */
export function ElicitationCard({ e }: { e: StreamEntry }) {
  const respond = useMate((s) => s.respondElicitation)
  const resolved = e.resolved ?? false
  const busy = useMate((s) => {
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
              <span style={{ fontSize: 11.5, color: tokens.textSecondary }}>完成外部操作后回到这里告知助手（URL 模式演示）。</span>
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
  const toast = useMate((s) => s.showToast)
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
  const toast = useMate((s) => s.showToast)
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

/** 品牌标记：ark·mate favicon 的渐变 A（与左栏品牌位同源，不复刻参照产品头像）。 */
function AgentAvatar() {
  return (
    <span className="fg-agent-avatar" aria-hidden="true">
      <svg viewBox="0 0 64 64">
        <path d="M20 46 32 18l12 28h-7.2l-4.8-12-4.8 12z" />
      </svg>
    </span>
  )
}

function UserMessage({ e, index }: { e: StreamEntry; index?: number }) {
  const text = e.text ?? ''
  const atts = e.attachments ?? []
  if (!text.trim() && atts.length === 0) return null
  return (
    <div className="fg-turn-user" data-fg-idx={index} data-fg-entry={e.id}>
      <div className="fg-user-bubble">
        {/* 附件 chips（v32）：气泡里按元数据画 chip（图片缩略 / 文档文件名 + 预览）。 */}
        {atts.length > 0 && (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: text.trim() ? 6 : 0 }}>
            {atts.map((a) => (
              <AttachmentChip key={a.id} attachment={a} />
            ))}
          </div>
        )}
        {text.trim() && <RichText text={text} />}
      </div>
      <div className="fg-user-actions">
        <MsgCopy text={text} />
      </div>
    </div>
  )
}

function AnswerText({ text, index, entryId }: { text: string; index?: number; entryId?: string }) {
  if (!text.trim()) return null
  return (
    <div className="fg-answer" data-fg-idx={index} data-fg-entry={entryId}>
      <RichText text={text} />
    </div>
  )
}

function ToolGlyph({ icon }: { icon: ToolIconName }) {
  switch (icon) {
    case 'read':
      return <EyeOutlined />
    case 'edit':
      return <EditOutlined />
    case 'command':
      return <CodeOutlined />
    case 'search':
      return <SearchOutlined />
    case 'folder':
      return <FolderOpenOutlined />
    case 'research':
      return <GlobalOutlined />
    case 'delete':
      return <DeleteOutlined />
    default:
      return <ToolOutlined />
  }
}

function detailText(value: unknown): string | null {
  if (value === undefined || value === null) return null
  let text: string
  if (typeof value === 'string') {
    text = value
  } else {
    try {
      text = JSON.stringify(value, null, 2)
    } catch {
      text = String(value)
    }
  }
  if (!text.trim()) return null
  return text.length > 20000 ? `${text.slice(0, 20000)}\n…` : text
}

/**
 * 工具详情的**分类渲染器**：read/list → 路径 + 代码预览；edit/write → diff 行；
 * execute → 命令行 + stdout/stderr + 退出码；search/web → 查询 + 结果列表。
 *
 * 每一类只在事件载荷**确实给了**对应数据时才画那一块；缺的部分由 `notes` 如实说明
 * （「结果不可用（底座未上报）」），不拿标题/状态补一个看起来合理的值。
 */
function ToolDetailBody({ detail }: { detail: ToolDetail }) {
  const notes = detail.notes.map((note) => (
    <div className="fg-tool-note" key={note}>
      {note}
    </div>
  ))

  if (detail.kind === 'read') {
    const lines = detail.lines ?? []
    return (
      <>
        <div className="fg-tool-detail-kicker">文件预览</div>
        {detail.path && (
          <div className="fg-tool-detail-path" title={detail.path}>
            {detail.path}
          </div>
        )}
        {lines.length > 0 && (
          <div className="fg-tool-code" role="region" aria-label="文件内容预览">
            {lines.map((line, index) => (
              <div className="fg-tool-code-line" key={index}>
                {line.no != null && <span className="fg-tool-line-no">{line.no}</span>}
                <span className="fg-tool-code-text">{line.text || ' '}</span>
              </div>
            ))}
          </div>
        )}
        {detail.truncated && <div className="fg-tool-note">仅显示前 {lines.length} 行（其余未展开）。</div>}
        {notes}
      </>
    )
  }

  if (detail.kind === 'diff') {
    return (
      <>
        <div className="fg-tool-detail-kicker">改动</div>
        <div className="fg-tool-diff">
          {(detail.diff ?? []).map((line, index) => (
            <div className={`fg-diff-line fg-diff-${line.kind}`} key={index}>
              <span className="fg-diff-sign">
                {line.kind === 'add' ? '+' : line.kind === 'del' ? '-' : ' '}
              </span>
              <span className="fg-diff-text">{line.text || ' '}</span>
            </div>
          ))}
        </div>
        {notes}
      </>
    )
  }

  if (detail.kind === 'command') {
    return (
      <>
        <div className="fg-tool-detail-kicker">命令</div>
        <div className="fg-tool-command">
          <span className="fg-tool-command-sign">$</span>
          <span className="fg-tool-command-text">{detail.command}</span>
        </div>
        {detail.stdout !== undefined && (
          <>
            <div className="fg-tool-detail-kicker">stdout</div>
            <pre className="fg-tool-output">{detail.stdout}</pre>
          </>
        )}
        {detail.stderr !== undefined && (
          <>
            <div className="fg-tool-detail-kicker">stderr</div>
            <pre className="fg-tool-output fg-tool-output-err">{detail.stderr}</pre>
          </>
        )}
        {detail.output !== undefined && (
          <>
            <div className="fg-tool-detail-kicker">输出</div>
            <pre className="fg-tool-output">{detail.output}</pre>
          </>
        )}
        {detail.exitCode !== undefined && (
          <div className={`fg-tool-exit${detail.exitCode !== 0 ? ' fg-tool-exit-bad' : ''}`}>
            退出码 {detail.exitCode}
          </div>
        )}
        {notes}
      </>
    )
  }

  if (detail.kind === 'search') {
    const results = detail.results ?? []
    return (
      <>
        {detail.query && (
          <>
            <div className="fg-tool-detail-kicker">查询</div>
            <div className="fg-tool-query">{detail.query}</div>
          </>
        )}
        {detail.url && (
          <div className="fg-tool-query" title={detail.url}>
            {detail.url}
          </div>
        )}
        {results.length > 0 && (
          <>
            <div className="fg-tool-detail-kicker">结果 · {results.length}</div>
            <ol className="fg-tool-results">
              {results.map((line, index) => (
                <li key={index}>{line}</li>
              ))}
            </ol>
          </>
        )}
        {notes}
      </>
    )
  }

  return (
    <>
      <div className="fg-tool-detail-kicker">结果</div>
      {detail.summary ? <pre className="fg-tool-output">{detail.summary}</pre> : null}
      {notes}
    </>
  )
}

/** 一条工具过程：默认只是一行，详情（分类预览 / 入参 / 结果 / 失败归类）按需展开。 */
function ToolRow({
  entry,
  view,
  onSettled,
}: {
  entry: StreamEntry
  view?: ToolView
  onSettled?: () => void
}) {
  const tool = view ?? toolView(entry)
  const [open, setOpen] = useState(false)
  const [mounted, setMounted] = useState(false)
  const settleRef = useRef<number | null>(null)
  useEffect(
    () => () => {
      if (settleRef.current) window.clearTimeout(settleRef.current)
    },
    [],
  )
  const detail = toolDetail(entry, tool)
  const input = detailText(tool.input)
  const output = detailText(tool.output)
  const canExpand = detail !== null || input !== null || output !== null || !!tool.failReason || !!tool.bizCode
  return (
    <div className={`fg-tool${open ? ' fg-open' : ''}`} data-fg-entry={entry.id}>
      <button
        type="button"
        className={`fg-tool-head${canExpand ? ' fg-tool-expandable' : ''}`}
        disabled={!canExpand}
        aria-expanded={canExpand ? open : undefined}
        onClick={() => {
          if (!canExpand) return
          if (!open) setMounted(true)
          setOpen((value) => !value)
          if (onSettled) {
            if (settleRef.current) window.clearTimeout(settleRef.current)
            settleRef.current = window.setTimeout(onSettled, 300)
          }
        }}
      >
        <span className="fg-tool-icon">
          <ToolGlyph icon={tool.icon} />
        </span>
        {tool.statusLabel && <span className="fg-tool-status">{tool.statusLabel}</span>}
        {tool.primary && (
          <span
            className={`fg-tool-primary${tool.link ? ' fg-tool-link' : ''}`}
            title={tool.title}
          >
            {tool.primary}
          </span>
        )}
        {tool.secondary && <span className="fg-tool-secondary">{tool.secondary}</span>}
        {/* 弹性占位：把展开箭头推到行尾，也保证长 primary 被挤窄收缩而不是把行撑宽。 */}
        <span className="fg-tool-spacer" aria-hidden="true" />
        {canExpand && <DownOutlined className="fg-tool-arrow" />}
      </button>
      {canExpand && mounted && (
        <div className="fg-tool-body">
          <div className="fg-tool-body-inner">
            <div className="fg-tool-detail">
              {tool.failReason && (
                <div className="fg-tool-detail-error">{toolFailReasonLabel(tool.failReason)}</div>
              )}
              {tool.bizCode && <div className="fg-tool-detail-meta">{tool.bizCode}</div>}
              {detail ? (
                <ToolDetailBody detail={detail} />
              ) : (
                <>
                  {input && (
                    <>
                      <div className="fg-tool-detail-kicker">参数</div>
                      <pre>{input}</pre>
                    </>
                  )}
                  {output && (
                    <>
                      <div className="fg-tool-detail-kicker">结果</div>
                      <pre>{output}</pre>
                    </>
                  )}
                </>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

interface FoldProps {
  title: string
  variant: 'completed' | 'process' | 'summary'
  lead?: ReactNode
  defaultOpen?: boolean
  /** 搜索命中了这块里的条目：这一次渲染就展开（否则命中的条目不在 DOM 里，锚点只能落到轮首）。 */
  autoOpen?: boolean
  onSettled?: () => void
  children: ReactNode
}

/** 过程流里的折叠原语：completed / 过程消息 / 语义摘要共用同一套 0fr→1fr 动画。 */
function Fold({ title, variant, lead, defaultOpen = false, autoOpen = false, onSettled, children }: FoldProps) {
  const [open, setOpen] = useState(defaultOpen)
  const [mounted, setMounted] = useState(defaultOpen)
  const settleRef = useRef<number | null>(null)
  useEffect(() => () => {
    if (settleRef.current) window.clearTimeout(settleRef.current)
  }, [])
  // 自动展开过就保持展开：搜索闪一下之后又把用户正在看的内容收起来是反直觉的。
  useEffect(() => {
    if (!autoOpen) return
    setMounted(true)
    setOpen(true)
  }, [autoOpen])
  const shown = open || autoOpen
  return (
    <div className={`fg-fold fg-fold-${variant}${shown ? ' fg-open' : ''}`}>
      <button
        type="button"
        className="fg-fold-head"
        aria-expanded={shown}
        onClick={() => {
          if (!shown) setMounted(true)
          setOpen(!shown)
          if (onSettled) {
            if (settleRef.current) window.clearTimeout(settleRef.current)
            settleRef.current = window.setTimeout(onSettled, 300)
          }
        }}
      >
        {lead && <span className="fg-fold-lead">{lead}</span>}
        <span className="fg-fold-title">{title}</span>
        <DownOutlined className="fg-fold-chevron" />
      </button>
      <div className="fg-fold-body" aria-hidden={!shown}>
        <div className="fg-fold-inner">{mounted || autoOpen ? children : null}</div>
      </div>
    </div>
  )
}

interface ProcessRenderProps {
  onThoughtToggle?: (id: string, open: boolean) => void
  onSettled?: () => void
  /** 搜索命中的条目 id：含它的折叠块本次渲染自动展开，锚点才能落到轮内较深的条目。 */
  flashId?: string | null
  /** 条目 id → 原始条目下标（搜索锚点用；窗口逻辑不变）。 */
  indexOf?: (id: string) => number
}

/** 已结算的确认卡 = 过程流里的历史节点（未结算的由输入区上方的确认区独占）。 */
function NoticeView({ entry }: { entry: StreamEntry }) {
  return entry.kind === 'permission' ? <PermissionCard e={entry} /> : <ElicitationCard e={entry} />
}

function blockId(block: ProcessBlock): string {
  if (block.kind === 'tool-group') return block.id
  if (block.kind === 'thought') return block.fold.id
  return block.entry.id
}

function blocksContain(blocks: ProcessBlock[], id: string | null | undefined): boolean {
  if (!id) return false
  return blocks.some((block) => {
    if (block.kind === 'tool-group') return block.id === id || block.items.some((item) => item.id === id)
    return blockId(block) === id
  })
}

function ProcessItems({
  items,
  onThoughtToggle,
  onSettled,
  indexOf,
}: { items: Array<StreamEntry | ThoughtFold> } & Omit<ProcessRenderProps, 'flashId'>) {
  return (
    <>
      {items.map((item) => {
        const index = indexOf?.(item.id)
        if (isThoughtFold(item)) {
          return (
            <div key={item.id} className="fg-process-item" data-fg-idx={index} data-fg-entry={item.id}>
              <ThoughtFoldView
                fold={item}
                onUserToggle={onThoughtToggle}
                onSettled={onSettled}
              />
            </div>
          )
        }
        if (item.kind === 'tool') {
          return (
            <div key={item.id} className="fg-process-item" data-fg-idx={index} data-fg-entry={item.id}>
              <ToolRow entry={item} onSettled={onSettled} />
            </div>
          )
        }
        if (item.kind === 'assistant') {
          return <AnswerText key={item.id} text={item.text ?? ''} index={index} entryId={item.id} />
        }
        if (item.kind === 'permission' || item.kind === 'elicitation') {
          if (!item.resolved) return null
          return (
            <div key={item.id} className="fg-process-item" data-fg-idx={index} data-fg-entry={item.id}>
              <NoticeView entry={item} />
            </div>
          )
        }
        return null
      })}
    </>
  )
}

function ProcessBlockView({
  block,
  onThoughtToggle,
  onSettled,
  flashId,
  indexOf,
}: { block: ProcessBlock } & ProcessRenderProps) {
  if (block.kind === 'thought') {
    const index = indexOf?.(block.fold.id)
    return (
      <div className="fg-process-item" data-fg-idx={index} data-fg-entry={block.fold.id}>
        <ThoughtFoldView
          fold={block.fold}
          onUserToggle={onThoughtToggle}
          onSettled={onSettled}
        />
      </div>
    )
  }
  if (block.kind === 'tool') {
    return (
      <div className="fg-process-item" data-fg-idx={indexOf?.(block.entry.id)} data-fg-entry={block.entry.id}>
        <ToolRow entry={block.entry} view={block.tool} onSettled={onSettled} />
      </div>
    )
  }
  if (block.kind === 'notice') {
    return (
      <div className="fg-process-item" data-fg-idx={indexOf?.(block.entry.id)} data-fg-entry={block.entry.id}>
        <NoticeView entry={block.entry} />
      </div>
    )
  }
  if (block.kind === 'tool-group') {
    return (
      <Fold
        title={block.summary.text}
        variant="summary"
        lead={<ToolGlyph icon={block.summary.icon} />}
        autoOpen={blocksContain([block], flashId)}
        onSettled={onSettled}
      >
        <ProcessItems
          items={block.items}
          onThoughtToggle={onThoughtToggle}
          onSettled={onSettled}
          indexOf={indexOf}
        />
      </Fold>
    )
  }
  return <AnswerText text={block.entry.text ?? ''} index={indexOf?.(block.entry.id)} entryId={block.entry.id} />
}

function ProcessBlocks({
  blocks,
  onThoughtToggle,
  onSettled,
  flashId,
  indexOf,
}: { blocks: ProcessBlock[] } & ProcessRenderProps) {
  return (
    <div className="fg-process-list">
      {blocks.map((block) => (
        <ProcessBlockView
          key={blockId(block)}
          block={block}
          onThoughtToggle={onThoughtToggle}
          onSettled={onSettled}
          flashId={flashId}
          indexOf={indexOf}
        />
      ))}
    </div>
  )
}

function collectTurnText(turn: TurnModel): string {
  const parts: string[] = []
  const pushItem = (item: StreamEntry | ThoughtFold) => {
    if (!isThoughtFold(item) && item.kind === 'assistant') {
      const text = item.text?.trim()
      if (text) parts.push(text)
    }
  }
  const pushBlocks = (blocks: ProcessBlock[]) => {
    for (const block of blocks) {
      if (block.kind === 'text') {
        const text = block.entry.text?.trim()
        if (text) parts.push(text)
      } else if (block.kind === 'tool-group') {
        block.items.forEach(pushItem)
      }
    }
  }
  for (const anchor of turn.anchors) {
    const text = anchor.text?.trim()
    if (text) parts.push(text)
  }
  if (parts.length === 0) {
    pushBlocks(turn.blocks)
    if (turn.completed) pushBlocks(turn.completed.before)
  }
  return parts.join('\n\n')
}

function TurnCopy({ text }: { text: string }) {
  const toast = useMate((s) => s.showToast)
  if (!text.trim()) return null
  return (
    <Button
      type="text"
      size="small"
      icon={<CopyOutlined />}
      title="复制本轮回复"
      aria-label="复制本轮回复"
      className="fg-turn-copy"
      onClick={async () => {
        const ok = await copyText(text)
        toast(ok ? '已复制本轮回复' : '复制失败，请手动选择文本')
      }}
    />
  )
}

function TurnUsage({
  usageText,
  info,
  stopReason,
}: {
  usageText?: string
  info?: RunUsageInfo
  stopReason?: string
}) {
  if (!usageText) return null
  const content = info ? (
    <div style={{ minWidth: 230 }}>
      {runStatRows(info).map((row) => (
        <div key={row.label} style={{ display: 'flex', justifyContent: 'space-between', gap: 24, padding: '2px 0' }}>
          <span style={{ color: tokens.textSecondary }}>{row.label}</span>
          <span style={{ fontFamily: tokens.fontMono }}>{row.value}</span>
        </div>
      ))}
      {stopReason && (
        <div style={{ display: 'flex', justifyContent: 'space-between', gap: 24, padding: '2px 0' }}>
          <span style={{ color: tokens.textSecondary }}>结束原因</span>
          <span style={{ fontFamily: tokens.fontMono }}>{stopReason}</span>
        </div>
      )}
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
        上下文为窗口快照（非增量）；用量明细未上报。
      </div>
    </div>
  ) : null
  return (
    <Popover title="本轮用量" trigger="hover" placement="bottom" content={content}>
      <span className="fg-turn-usage">
        <span className="fg-turn-usage-sep">·</span>
        {usageText}
      </span>
    </Popover>
  )
}

/** 一轮对话：用户气泡 + Agent 身份头 + 过程流 / 整轮折叠 + 流式尾部。 */
export function TurnView({
  turn,
  elapsed,
  stage,
  showCaret,
  onThoughtToggle,
  onSettled,
  usageText,
  runInfo,
  stopReason,
  flashId,
  indexOf,
}: {
  turn: TurnModel
  elapsed?: string
  stage?: string
  showCaret?: boolean
  onThoughtToggle?: (id: string, open: boolean) => void
  onSettled?: () => void
  usageText?: string
  runInfo?: RunUsageInfo
  stopReason?: string
  flashId?: string | null
  indexOf?: (id: string) => number
}) {
  const hasAgent =
    turn.live ||
    turn.blocks.length > 0 ||
    !!turn.completed ||
    !!turn.completion ||
    turn.anchors.length > 0
  const copyTextValue = collectTurnText(turn)
  return (
    <section
      className={`fg-turn${turn.live ? ' fg-turn-live' : ''}`}
      data-fg-idx={turn.index}
      data-fg-turn={turn.id}
    >
      {turn.user && <UserMessage e={turn.user} index={indexOf?.(turn.user.id)} />}
      {hasAgent && (
        <div className="fg-agent-head">
          <AgentAvatar />
          <span className="fg-agent-name" title="ark-mate · 个人 Agent 助手">
            ark<span className="fg-agent-dot">·</span>mate
          </span>
          <span className="fg-agent-ai">AI</span>
          <TurnCopy text={copyTextValue} />
        </div>
      )}
      {turn.live && elapsed && <div className="fg-agent-status">已处理 {elapsed}</div>}

      {turn.completed ? (
        <>
          {turn.completed.before.length > 0 ? (
            <Fold
              title={turn.completed.title}
              variant="completed"
              autoOpen={blocksContain(turn.completed.before, flashId)}
              onSettled={onSettled}
            >
              <ProcessBlocks
                blocks={turn.completed.before}
                onThoughtToggle={onThoughtToggle}
                onSettled={onSettled}
                flashId={flashId}
                indexOf={indexOf}
              />
            </Fold>
          ) : (
            <div className="fg-agent-completion">{turn.completed.title}</div>
          )}
          {turn.anchors.map((anchor, index) => {
            const gap = turn.completed?.between[index]
            return (
              <Fragment key={anchor.id}>
                <AnswerText
                  text={anchor.text ?? ''}
                  index={indexOf?.(anchor.id)}
                  entryId={anchor.id}
                />
                {gap && gap.blocks.length > 0 && (
                  <Fold
                    title="过程消息"
                    variant="process"
                    autoOpen={blocksContain(gap.blocks, flashId)}
                    onSettled={onSettled}
                  >
                    <ProcessBlocks
                      blocks={gap.blocks}
                      onThoughtToggle={onThoughtToggle}
                      onSettled={onSettled}
                      flashId={flashId}
                      indexOf={indexOf}
                    />
                  </Fold>
                )}
              </Fragment>
            )
          })}
          {turn.completed.after.length > 0 && (
            <Fold
              title="过程消息"
              variant="process"
              autoOpen={blocksContain(turn.completed.after, flashId)}
              onSettled={onSettled}
            >
              <ProcessBlocks
                blocks={turn.completed.after}
                onThoughtToggle={onThoughtToggle}
                onSettled={onSettled}
                flashId={flashId}
                indexOf={indexOf}
              />
            </Fold>
          )}
        </>
      ) : (
        <>
          {!turn.live && turn.completion && (
            <div className="fg-agent-completion">{turn.completion}</div>
          )}
          {!turn.live && !turn.failed && turn.anchors.length > 0 && turn.anchors.map((anchor) => (
            <AnswerText
              key={anchor.id}
              text={anchor.text ?? ''}
              index={indexOf?.(anchor.id)}
              entryId={anchor.id}
            />
          ))}
          {turn.blocks.length > 0 && (
            <div className="fg-agent-body">
              <ProcessBlocks
                blocks={turn.blocks}
                onThoughtToggle={onThoughtToggle}
                onSettled={onSettled}
                flashId={flashId}
                indexOf={indexOf}
              />
            </div>
          )}
        </>
      )}

      {turn.live && stage && (
        <div className="fg-turn-footer">
          <span className="fg-shimmer">{stage}</span>
          {showCaret && <span className="fg-turn-footer-caret" aria-hidden="true" />}
        </div>
      )}
      {usageText && (
        <TurnUsage usageText={usageText} info={runInfo} stopReason={stopReason} />
      )}
    </section>
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
  /** 该轮用量展示行（如 `上下文 30.0k / 128.0k · Δ +3.2k`；仅终态轮的 run-end 条目有值）。 */
  usageText?: string
  /** 该轮用量的结构化数据（含首令牌/总耗时/成本），供 goose 风格 popover；无则仅显示 usageText。 */
  runInfo?: RunUsageInfo
}) {
  switch (e.kind) {
    // 未结算的确认类卡片**不**渲染在条目流里（S9 / Q2 选①）：它们搬到输入区上方的**确认区**
    // （`ConfirmArea.tsx`）。理由不是审美——条目流是**历史**，用户往上翻的时候被一张浮在中段的
    // 卡片反复拦路；而"现在需要我做什么"是一个**当下的问题**，它该在离输入框最近的地方。
    //
    // 已结算的那些仍然留在流里（那是历史事实：当时被允许/拒绝了什么）。
    case 'permission':
      return e.resolved ? <PermissionCard e={e} /> : null
    case 'elicitation':
      return e.resolved ? <ElicitationCard e={e} /> : null
    case 'user':
      return <UserMessage e={e} />

    case 'assistant':
      return <AnswerText text={e.text ?? ''} />

    case 'thought':
      // 单条思考**不在这里直接渲染**：相邻的 thought 会被 `foldThoughts` 收成一折，
      // 由 `ThoughtFoldView` 统一画（ChatPage 分组）。留一个 null 是为了让"漏分组"不至于
      // 把原始思考又摊回条目流里——它应该是一个可见的缺口，而不是两套标签并存。
      return null

    case 'tool':
      return <ToolRow entry={e} />

    case 'system': {
      const tone = (e.tone ?? 'info') as NonNullable<StreamEntry['tone']>
      const retry = tone === 'err' && !!onRetry
      // 轮末（tone='ok'）只在这里补时长：事件到达时还没有本轮用量，`runInfo` 是稍后到达的
      // 事实；拿不到就只显示「已完成」，不编一个 0.0s。
      const endSuffix =
        tone === 'ok' && runInfo && runInfo.durationMS > 0 ? ` · ${(runInfo.durationMS / 1000).toFixed(1)}s` : ''
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
            <span>
              {e.text}
              {endSuffix}
            </span>
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
                      {e.stopReason && (
                        <div style={{ display: 'flex', justifyContent: 'space-between', gap: 24, padding: '2px 0' }}>
                          <span style={{ color: tokens.textSecondary }}>结束原因</span>
                          <span style={{ fontFamily: tokens.fontMono }}>{e.stopReason}</span>
                        </div>
                      )}
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
                        上下文为窗口快照（非增量）；用量明细未上报。
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
