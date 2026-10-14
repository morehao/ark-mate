import { type CSSProperties, type ReactNode, type Dispatch, type SetStateAction } from 'react'
import { Alert, Button, Input, Modal, Tag, Tooltip } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { skillRuntimesView } from '../../console/capabilityView'

/** 定义编辑器的状态（hook 与面板共用的类型）。 */
export interface CapabilityEditState { kind: 'skill' | 'mcp'; id: string; text: string; created: boolean }

export const SOURCE_COLOR: Record<string, string> = { builtin: 'blue', mcp: 'green', observed: 'gold' }
export const TRANSPORT_COLOR: Record<string, string> = { stdio: 'default', sse: 'blue', http: 'green', streamable_http: 'purple' }

export const mono: CSSProperties = { fontFamily: tokens.fontMono, fontSize: 12.5, color: tokens.text, overflowWrap: 'anywhere' }

export function SectionHeader({
  title,
  hint,
  count,
  loading,
  onRefresh,
  extra,
}: {
  title: string
  hint: string
  count: number
  loading: boolean
  onRefresh: () => void
  extra?: ReactNode
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
      {/* 左栏常驻后卡片头变窄：说明文字最多两行（全文给 hover 的 title），避免卡头被小字撑高 */}
      <span
        title={hint}
        style={{
          flex: 1,
          minWidth: 0,
          fontSize: 11.5,
          color: tokens.textPlaceholder,
          display: '-webkit-box',
          WebkitLineClamp: 2,
          WebkitBoxOrient: 'vertical',
          overflow: 'hidden',
        }}
      >
        {hint}
      </span>
      {extra}
      <Button size="small" icon={<ReloadOutlined />} loading={loading} onClick={onRefresh}>
        刷新
      </Button>
    </header>
  )
}

export function SectionEmpty({ text }: { text: string }) {
  return <div style={{ padding: '28px 20px', textAlign: 'center', color: tokens.textPlaceholder, fontSize: 12.5 }}>{text}</div>
}

export function Row({ children }: { children: ReactNode }) {
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8, padding: '10px 16px', borderBottom: `1px solid ${tokens.border}` }}>
      {children}
    </div>
  )
}

/** 行内小标签统一样式（新增代码用；既有行内样式保持不动，避免无关联改动）。 */
export const smallTag: CSSProperties = { marginInlineEnd: 0, fontSize: 10.5 }

// v19：这里原来有 `SkillActivationTags`（「已禁用 / 不适用 / 可用」三个并列标签），
// 输入是 agent 视角的 `AgentSkill`。那张视角没了，而这层语义在 `/library` 的行里本来就有
// （`enabled` 出「已禁用」，逐 runtime 的 applicability 出「可用/未实测/未配置/不适用/已禁用」
// 且原因进 tooltip），所以直接删掉而不是换个输入再抄一遍。

/**
 * 投放对象标签（M1：取代旧的 `runtime·format` 标签）。
 *
 * 空 runtimes 显示为「全部 runtime」而不是什么都不显示：后者会被读成"这个技能没有投放对象"，
 * 而真实语义正好相反（空 = 全部，是升级安全默认）。
 */
export function SkillRuntimeTags({ runtimes, scope }: { runtimes?: string[]; scope?: string }) {
  const v = skillRuntimesView(runtimes, scope)
  return (
    <>
      {v.labels.map((label, i) => (
        <Tooltip key={i} title={v.emptyHint}>
          <Tag style={{ fontSize: 10, marginInlineEnd: 0 }} color={runtimes && runtimes.length > 0 ? undefined : 'default'}>
            {label}
          </Tag>
        </Tooltip>
      ))}
      {v.scopeTag && (
        <Tooltip title={v.scopeTag.hint}>
          <Tag color="blue" style={{ fontSize: 10, marginInlineEnd: 0 }}>
            {v.scopeTag.text}
          </Tag>
        </Tooltip>
      )}
    </>
  )
}

export function Card({ children }: { children: ReactNode }) {
  return (
    <section style={{ border: `1px solid ${tokens.border}`, borderRadius: 12, background: tokens.surface, overflow: 'hidden' }}>
      {children}
    </section>
  )
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
              ? `${edit.created ? '新建' : '编辑'} ${edit.kind === 'skill' ? 'skill' : 'MCP server'}${edit.id ? ` · ${edit.id}` : ''}`
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
                请求体就是**定义原文**（JSON 对象，<code>id</code> 必填，只能是小写字母/数字/-/_ 且 ≤64 字符）。
                <br />
              </>
            ) : (
              <>
                **只改这个框里出现过的键**：没写的键原样保留（含你自己加的字段）、写 <code>null</code> 删键、
                写 <code>""</code>/<code>[]</code> 清空。
                <br />
              </>
            )}
            {edit?.kind === 'mcp' && !edit.created && (
              <>
                MCP 的明文 env 与密钥引用**后端从不读回**（界面只知道键名），所以这里不预填——
                不动它们就别写，写了就会覆盖。密钥引用写成 <code>store:路径</code>（值在库的 <code>mcp_secrets</code> 里）或 <code>env:变量名</code>。
                <br />
              </>
            )}
            保存前先过闸门：正文扫描（critical 直接拒）、transport/路径/引用词法校验；
            <b>被拒的定义不会落盘</b>（也不会进注册表）。
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
