import { useState, type CSSProperties, type ReactNode, type Dispatch, type SetStateAction } from 'react'
import { Alert, Button, Input, Modal, Tag, Tooltip } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { transportLabel } from '../../console/capabilityView'
import { CardToolbar } from '../CardToolbar'

/** 定义编辑器的状态（hook 与面板共用的类型）。 */
export interface CapabilityEditState { kind: 'skill' | 'mcp'; id: string; text: string; created: boolean }

export const SOURCE_COLOR: Record<string, string> = { builtin: 'blue', mcp: 'green', observed: 'gold' }
export const TRANSPORT_COLOR: Record<string, string> = { stdio: 'default', sse: 'blue', http: 'green', streamable_http: 'purple' }

export const mono: CSSProperties = { fontFamily: tokens.fontMono, fontSize: 12.5, color: tokens.text, overflowWrap: 'anywhere' }

/**
 * 内容区工具行（标题组 + 右端工具）。
 *
 * 左：`标题 + 计数 chip + 说明`（一个语义组）；右：`tools`（搜索/新建等页面工具）+ 刷新。
 * 中间用 flex:1 的留白隔开——**模块名与工具分层**，不再把「标题 + 刷新」挤成一条。
 * `tools` 可选：扩展三页（专家 / 技能 / 连接器）都把「搜索 + 新建」放这里。
 * 结构与 `CardToolbar` 同源，保证与用量 / 定时任务 / 资料库的工具行完全一致。
 */
export function SectionHeader({
  title,
  hint,
  count,
  loading,
  onRefresh,
  tools,
}: {
  title: string
  hint: string
  count: number
  loading: boolean
  onRefresh: () => void
  tools?: ReactNode
}) {
  return (
    <CardToolbar
      left={
        <>
          <b style={{ fontSize: 13.5, fontWeight: 600, flex: 'none' }}>{title}</b>
          <Tag
            style={{
              marginInlineEnd: 0,
              fontSize: 11,
              fontVariantNumeric: 'tabular-nums',
              background: tokens.softFill,
              border: `1px solid ${tokens.border}`,
              color: tokens.textSecondary,
            }}
          >
            {count}
          </Tag>
          {/* 说明文字最多一行（全文给 hover 的 title），把空间让给右侧工具 */}
          <span
            title={hint}
            style={{
              flex: 1,
              minWidth: 0,
              fontSize: 11.5,
              color: tokens.textPlaceholder,
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
            }}
          >
            {hint}
          </span>
        </>
      }
      tools={
        <>
          {tools}
          <Button size="small" style={{ flex: 'none' }} icon={<ReloadOutlined />} loading={loading} onClick={onRefresh}>
            刷新
          </Button>
        </>
      }
    />
  )
}

/** 行内小标签统一样式（新增代码用；既有行内样式保持不动，避免无关联改动）。 */
export const smallTag: CSSProperties = { marginInlineEnd: 0, fontSize: 10.5 }

// v19：这里原来有 `SkillActivationTags`（「已禁用 / 不适用 / 可用」三个并列标签），
// 输入是 agent 视角的 `AgentSkill`。那张视角没了，而这层语义在 `/library` 的行里本来就有
// （`enabled` 出「已禁用」，逐 runtime 的 applicability 出「可用/未实测/未配置/不适用/已禁用」
// 且原因进 tooltip），所以直接删掉而不是换个输入再抄一遍。

/**
 * 两行式能力行（名称/描述/元信息 + 右侧动作）。
 *
 * 技能页与连接器页共用；总览页删掉后这里只剩两行形态（原单行 `Row` 与 `Card` 一并退役）。
 */
export function CapabilityRow({
  title,
  description,
  meta,
  actions,
}: {
  title: ReactNode
  description: ReactNode
  meta: ReactNode
  actions: ReactNode
}) {
  const [hover, setHover] = useState(false)
  return (
    <article
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      style={{
        display: 'flex',
        alignItems: 'flex-start',
        gap: 10,
        padding: '11px 16px',
        borderBottom: `1px solid ${tokens.border}`,
        background: hover ? tokens.bg : undefined,
        transition: 'background .12s ease',
      }}
    >
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>{title}</div>
        <p
          title={typeof description === 'string' ? description : undefined}
          style={{
            margin: '2px 0 0',
            fontSize: 11.5,
            lineHeight: 1.55,
            color: tokens.textSecondary,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {description}
        </p>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 5,
            minWidth: 0,
            marginTop: 5,
            overflow: 'hidden',
            color: tokens.textPlaceholder,
            fontSize: 10.5,
            whiteSpace: 'nowrap',
          }}
        >
          {meta}
        </div>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 4, flex: 'none', paddingTop: 1 }}>{actions}</div>
    </article>
  )
}

/** 行内状态（点 + 文案）。`tone` 只决定那个点，不把整行染色。 */
export function StatusTag({
  label,
  tone = 'muted',
  hint,
}: {
  label: string
  tone?: 'muted' | 'success' | 'warning' | 'error'
  hint?: ReactNode
}) {
  const color =
    tone === 'success' ? tokens.success : tone === 'warning' ? tokens.warning : tone === 'error' ? tokens.error : tokens.textPlaceholder
  const node = (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 10.5, color: tokens.textSecondary }}>
      <span style={{ width: 5, height: 5, borderRadius: '50%', background: color, flex: 'none' }} />
      {label}
    </span>
  )
  return hint ? <Tooltip title={hint}>{node}</Tooltip> : node
}

/** 传输 tag：界面只有 STDIO / SSE / HTTP 三值（旧 http 显示为 HTTP）。 */
export function TransportTag({ transport }: { transport: string }) {
  return (
    <Tag style={{ ...smallTag, fontFamily: tokens.fontMono, color: tokens.textSecondary }}>{transportLabel(transport)}</Tag>
  )
}

/** 空态：一句说明 + 下一步动作（不画插画；空态的价值是「接下来做什么」）。 */
export function CapabilityEmpty({ text, action }: { text: ReactNode; action?: ReactNode }) {
  return (
    <div style={{ padding: '28px 20px', textAlign: 'center', color: tokens.textPlaceholder, fontSize: 12.5 }}>
      <div style={{ marginBottom: action ? 10 : 0 }}>{text}</div>
      {action}
    </div>
  )
}

/** 详情弹窗里的键值网格（两列，hairline 分隔）。 */
export function DetailGrid({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <div
      style={{
        display: 'grid',
        gridTemplateColumns: '1fr 1fr',
        border: `1px solid ${tokens.border}`,
        borderRadius: tokens.radiusSm,
        overflow: 'hidden',
      }}
    >
      {items.map((it, i) => (
        <div
          key={`${it.label}-${i}`}
          style={{
            padding: '8px 10px',
            borderTop: i >= 2 ? `1px solid ${tokens.border}` : undefined,
            borderLeft: i % 2 === 1 ? `1px solid ${tokens.border}` : undefined,
            minWidth: 0,
          }}
        >
          <div style={{ fontSize: 10.5, color: tokens.textPlaceholder, marginBottom: 2 }}>{it.label}</div>
          <div style={{ fontSize: 12, color: tokens.text, overflowWrap: 'anywhere' }}>{it.value}</div>
        </div>
      ))}
    </div>
  )
}

/** 弹窗小节标题（详情/编辑共用，避免每个弹窗各写一套字号）。 */
export function SectionLabel({ children }: { children: ReactNode }) {
  return <div style={{ fontSize: 10.5, color: tokens.textPlaceholder, margin: '12px 0 5px' }}>{children}</div>
}

// promptText 把 prompts/get 的 content（MCP 联合类型）取成可读文本：
// 文本块取 text，其余类型原样 JSON 展示（不猜语义）。
export function promptText(content: unknown): string {
  if (content && typeof content === 'object') {
    const blk = content as { type?: string; text?: string }
    if (blk.type === 'text' && typeof blk.text === 'string') return blk.text
  }
  try {
    return JSON.stringify(content)
  } catch {
    return String(content)
  }
}

// schemaSkeleton 由工具 inputSchema 生成参数骨架（properties 键 + 类型占位；无 schema → "{}"）。
// 仅作输入便利：不做校验（服务端才是权威），也不猜默认值。
export function schemaSkeleton(schema: unknown): string {
  if (!schema || typeof schema !== 'object') return '{}'
  const props = (schema as { properties?: Record<string, { type?: string; default?: unknown }> }).properties
  if (!props || typeof props !== 'object') return '{}'
  const out: Record<string, unknown> = {}
  for (const [key, def] of Object.entries(props)) {
    if (def && typeof def === 'object' && 'default' in def) {
      out[key] = def.default
      continue
    }
    const type = def && typeof def === 'object' ? def.type : undefined
    out[key] = type === 'number' || type === 'integer' ? 0 : type === 'boolean' ? false : type === 'array' ? [] : type === 'object' ? {} : ''
  }
  return JSON.stringify(out, null, 2)
}

/**
 * 定义编辑器（skill / MCP 共用；收原文 JSON 对象）——技能页与连接器页各挂一份。
 */
export function CapabilityEditorModal({
  edit,
  setEdit,
  submitEdit,
  editBusy,
  editErr,
}: {
  edit: CapabilityEditState | null
  setEdit: Dispatch<SetStateAction<CapabilityEditState | null>>
  submitEdit: () => Promise<void>
  editBusy: boolean
  editErr: string | null
}) {
  return (
        <Modal
          title={
            edit
              ? `${edit.created ? '新建' : '编辑'} ${edit.kind === 'skill' ? '技能' : '连接器'}${edit.id ? ` · ${edit.id}` : ''}`
              : ''
          }
          open={edit !== null}
          onCancel={() => setEdit(null)}
          onOk={() => void submitEdit()}
          okText={edit?.created ? '新建' : '保存'}
          confirmLoading={editBusy}
          cancelText="取消"
          width={720}
          destroyOnClose
        >
          <div style={{ fontSize: 12, color: tokens.textSecondary, lineHeight: 1.8, marginBottom: 8 }}>
            {edit?.created ? (
              <>
                填写配置内容（JSON 对象）。
                <br />
              </>
            ) : (
              <>
                只修改这里写到的字段；没写的字段保持不变。
                <br />
              </>
            )}
            {edit?.kind === 'mcp' && !edit.created && (
              <>
                连接器的明文密钥**不会回显**（界面只知道名称），所以这里不预填——
                不改它们就别写，写了就会覆盖。密钥引用写成 <code>store:路径</code> 或 <code>env:变量名</code>。
                <br />
              </>
            )}
            保存前会检查填写内容；<b>不通过时不会保存</b>。
          </div>
          <Input.TextArea
            value={edit?.text ?? ''}
            onChange={(e) => setEdit((cur) => (cur ? { ...cur, text: e.target.value } : cur))}
            autoSize={{ minRows: 12, maxRows: 22 }}
            spellCheck={false}
            style={{ fontFamily: tokens.fontMono, fontSize: 12.5 }}
          />
          {editErr && <Alert type="error" showIcon style={{ marginTop: 8 }} message="保存失败" description={editErr} />}
        </Modal>
  )
}
