import { type ReactNode, useMemo, useState } from 'react'
import { Button, Modal, Tooltip } from 'antd'
import { tokens } from '@ark-mate/ui'
import type { App as AppType, MarketApp } from '@ark-mate/types'
import { DetailGrid, StatusTag } from '../capability/shared'
import { appIconNode } from './appIcons'
import { appTaglineOf, defaultCompose, parseGuide } from './appUsage'

/**
 * 应用「使用说明」弹层（v42）——统一承载「这个应用怎么用自然语言驱动」。
 *
 * 为什么是弹层而不是抽屉：与专家 / MCP 详情同一交互语言（`McpDetailModal` 750/780 居中弹窗），
 * 不新造一种"侧滑"形态。
 *
 * 两个 Tab：
 *   使用说明  = 能做什么（tagline）→ 你可以这样用（examples，点一句填进输入框）→ 需要准备什么（usage_guide 四段）→ 开始用
 *   组成      = 成员 / 工具域 / 页面入口 / 版本 / 来源（工程字段，不挤在卡片上）
 *
 * 两种来源：
 *   - 我的应用（`kind='mine'`）：底栏「卸载 / 关闭 / 在新会话里用这个应用」；示例话术点了会回填「开始用」输入框，
 *     点主按钮 = 带着 `capabilities.apps:[id]` + 这句话开新会话。
 *   - 应用市场（`kind='market'`）：装前预览；底栏「关闭 / 安装或启用」（动作由父级回调决定，本组件只发起）。
 *
 * **用法（usage_guide）是给人看的**，不是模型 system prompt——两者不能混（后者另立字段）。
 */

export type AppUsageTarget =
  | { kind: 'mine'; app: AppType }
  | { kind: 'market'; app: MarketApp }

/** 从卡片派生能力句（re-export，供卡片直接调用）。 */
export { appTaglineOf }

export function AppUsageModal({
  target,
  open,
  onClose,
  onLaunch,
  onUninstall,
  marketAction,
}: {
  target: AppUsageTarget | null
  open: boolean
  onClose: () => void
  /** 我的应用：用「开始用」输入框当前那句话开新会话（带 apps:[id]）。 */
  onLaunch?: (app: AppType, prompt: string) => void
  /** 我的应用：卸载（父级负责确认）。 */
  onUninstall?: (app: AppType) => void
  /** 市场：安装 / 启用（父级负责实际动作与刷新）；返回 label 用于按钮文案。 */
  marketAction?: { label: string; disabled?: boolean; reason?: string; run: (app: MarketApp) => void }
}) {
  const [tab, setTab] = useState<'usage' | 'compose'>('usage')
  const [compose, setCompose] = useState('')

  const name = target ? (target.kind === 'mine' ? target.app.name || target.app.id : target.app.name) : ''

  // 打开时（或切换 target 时）重置 tab 与输入框。
  const key = target ? `${target.kind}:${target.kind === 'mine' ? target.app.id : target.app.id}` : 'none'
  const memoKey = useMemo(() => key, [key])
  const [lastKey, setLastKey] = useState('')
  if (open && memoKey !== lastKey) {
    setLastKey(memoKey)
    setTab('usage')
    if (target) {
      const tagline = target.kind === 'mine' ? target.app.tagline : target.app.tagline
      const examples = target.kind === 'mine' ? target.app.examples : target.app.examples
      setCompose(defaultCompose(name, tagline, examples))
    }
  }

  const app = target?.kind === 'mine' ? target.app : undefined
  const market = target?.kind === 'market' ? target.app : undefined
  const tagline = target ? (target.kind === 'mine' ? appTaglineOf(target.app) : taglineOfMarket(target.app)) : ''
  const examples: string[] = target ? (target.kind === 'mine' ? target.app.examples ?? [] : target.app.examples ?? []) : []
  const guideRaw = target?.kind === 'mine' ? target.app.usage_guide : target?.app.usage_guide
  const guide = parseGuide(guideRaw)

  const launchable = !!app && app.enabled

  return (
    <Modal
      open={open}
      onCancel={onClose}
      footer={null}
      width={760}
      destroyOnClose
      title={null}
      styles={{ body: { padding: 0 } }}
    >
      {target && (
        <>
          {/* 头 */}
          <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start', padding: '18px 22px 14px', borderBottom: `1px solid ${tokens.border}` }}>
            <span style={avaStyle}>{target.kind === 'mine' ? appIconNode(target.app.avatar) : appIconNode(target.app.icon)}</span>
            <div style={{ minWidth: 0, flex: 1 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                <b style={{ fontSize: 17, letterSpacing: '-.01em' }}>{name}</b>
                {target.kind === 'mine' && <AppStateBadge app={target.app} />}
                {market && <StatusTag label={marketBadge(market).label} tone={marketBadge(market).tone} />}
              </div>
              <div style={{ fontFamily: tokens.fontMono, fontSize: 10.5, color: tokens.textPlaceholder, marginTop: 3 }}>
                {target.app.id}
                {market ? ` · v${market.version}` : ''}
              </div>
            </div>
          </div>

          {/* Tabs */}
          <div style={{ display: 'flex', gap: 2, padding: '0 22px', borderBottom: `1px solid ${tokens.border}` }}>
            {([['usage', '使用说明'], ['compose', '组成']] as const).map(([k, label]) => (
              <div key={k} onClick={() => setTab(k)} style={tabStyle(tab === k)}>
                {label}
              </div>
            ))}
          </div>

          {/* Body */}
          <div style={{ padding: '18px 22px', maxHeight: '60vh', overflowY: 'auto' }}>
            {tab === 'usage' ? (
              <UsageTab
                source={target.kind}
                tagline={tagline}
                examples={examples}
                guide={guide}
                compose={compose}
                enabled={launchable || target.kind === 'market'}
                onPickExample={(ex) => setCompose(ex)}
                onComposeChange={setCompose}
              />
            ) : (
              <ComposeTab target={target} />
            )}
          </div>

          {/* Footer */}
          <div style={{ display: 'flex', gap: 8, padding: '14px 22px', borderTop: `1px solid ${tokens.border}`, alignItems: 'center' }}>
            {target.kind === 'mine' ? (
              <>
                <Button type="text" danger onClick={() => onUninstall?.(target.app)}>
                  卸载
                </Button>
                <div style={{ flex: 1 }} />
                <Button onClick={onClose}>关闭</Button>
                <Tooltip title={launchable ? '带上这个应用的能力，用输入框里的那句话开新会话' : '应用未启用：先在列表里启用，再在新会话里用它'}>
                  <Button type="primary" disabled={!launchable} onClick={() => onLaunch?.(target.app, compose)}>
                    在新会话里用这个应用
                  </Button>
                </Tooltip>
              </>
            ) : (
              <>
                <Button type="text" onClick={onClose}>关闭</Button>
                <div style={{ flex: 1 }} />
                {marketAction && (
                  <Tooltip title={marketAction.disabled ? marketAction.reason : ''}>
                    <Button type="primary" disabled={marketAction.disabled} onClick={() => marketAction.run(target.app)}>
                      {marketAction.label}
                    </Button>
                  </Tooltip>
                )}
              </>
            )}
          </div>
        </>
      )}
    </Modal>
  )
}

function UsageTab({
  source,
  tagline,
  examples,
  guide,
  compose,
  enabled,
  onPickExample,
  onComposeChange,
}: {
  source: 'mine' | 'market'
  tagline: string
  examples: string[]
  guide: ReturnType<typeof parseGuide>
  compose: string
  enabled: boolean
  onPickExample: (ex: string) => void
  onComposeChange: (v: string) => void
}) {
  return (
    <>
      <Section title="能做什么" hint="tagline">
        <div style={{ fontSize: 14, lineHeight: 1.65 }}>{tagline}</div>
      </Section>

      <Section title="你可以这样用" hint={source === 'mine' ? 'examples · 点一句填进输入框' : 'examples · 装好后点一句填进输入框'}>
        {examples.length > 0 ? (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 7 }}>
            {examples.map((ex, i) => (
              <div key={i} className="app-example-chip" onClick={() => onPickExample(ex)} style={exampleChipStyle}>
                <span>{ex}</span>
                <span style={arrStyle}>↵ 填进输入框</span>
              </div>
            ))}
          </div>
        ) : (
          <div style={{ fontSize: 12.5, color: tokens.textPlaceholder }}>
            这个应用还没写示例。启用后直接在对话里描述你的需求即可。
          </div>
        )}
      </Section>

      <Section title="需要准备什么" hint="usage_guide">
        {!guide ? (
          <div style={{ fontSize: 12.5, color: tokens.textPlaceholder }}>还没有写使用说明。</div>
        ) : guide.plain ? (
          <div style={guideBoxStyle}>{guide.plain}</div>
        ) : (
          <div style={guideBoxStyle}>
            <GuideRow k="适用场景" v={guide.scene} />
            <GuideRow k="需要提供" v={guide.need} />
            <GuideRow k="产出" v={guide.out} />
            <GuideRow k="边界" v={guide.limit} />
          </div>
        )}
      </Section>

      <Section title="开始用">
        <div style={{ border: `1px solid ${tokens.border}`, borderRadius: 10, padding: 11 }}>
          <div style={{ fontSize: 11.5, color: tokens.textPlaceholder, marginBottom: 6 }}>
            {enabled
              ? '在新会话里带上这个应用的能力；输入框会自动填好这句话，可直接改'
              : '应用未启用——先在列表里启用，就能带着这句话开始对话'}
          </div>
          <textarea
            value={compose}
            onChange={(e) => onComposeChange(e.target.value)}
            rows={2}
            style={{
              width: '100%',
              resize: 'vertical',
              fontSize: 13,
              lineHeight: 1.55,
              color: tokens.text,
              background: tokens.softFill,
              border: 'none',
              borderRadius: 7,
              padding: '8px 10px',
              fontFamily: 'inherit',
              outline: 'none',
            }}
          />
        </div>
      </Section>
    </>
  )
}

function ComposeTab({ target }: { target: AppUsageTarget }) {
  const items: { label: string; value: ReactNode }[] =
    target.kind === 'mine'
      ? [
          { label: '成员', value: membersSummary(target.app.members) },
          { label: '工具域', value: target.app.tool_domains?.length ? target.app.tool_domains.join('、') : '无' },
          { label: '页面入口', value: target.app.has_page ? target.app.page?.route || '有' : '无' },
          { label: '版本', value: target.app.revision != null ? `rev ${target.app.revision}` : '无' },
          { label: '来源', value: sourceLabel(target.app.source) },
        ]
      : [
          { label: '简介', value: target.app.summary?.zh || '无' },
          { label: '版本', value: target.app.version },
          { label: '标签', value: target.app.tags?.length ? target.app.tags.join('、') : '无' },
          { label: '主页', value: target.app.homepage || '无' },
          { label: '来源', value: '应用市场' },
        ]
  return <DetailGrid items={items} />
}

// ——— 小部件 ———

function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <div style={{ marginBottom: 20 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 9 }}>
        <span style={{ fontSize: 12.5, fontWeight: 600, color: tokens.text }}>{title}</span>
        {hint && (
          <span style={{ fontSize: 10.5, color: tokens.accent, background: tokens.accentSoft, border: '1px dashed #c9d2ff', borderRadius: 6, padding: '1px 7px' }}>
            {hint}
          </span>
        )}
      </div>
      {children}
    </div>
  )
}

function GuideRow({ k, v }: { k: string; v?: string }) {
  if (!v) return null
  return (
    <div style={{ display: 'flex', gap: 8 }}>
      <span style={{ color: tokens.textSecondary, width: 66, flex: 'none', fontSize: 12 }}>{k}</span>
      <span style={{ fontSize: 13 }}>{v}</span>
    </div>
  )
}

function AppStateBadge({ app }: { app: AppType }) {
  if (!app.enabled) return <StatusTag label="未启用" tone="muted" />
  switch (app.state) {
    case 'active':
      return <StatusTag label="运行中" tone="success" />
    case 'pending':
      return <StatusTag label="等待中" tone="warning" hint={app.why || '等待依赖服务就绪'} />
    case 'failed':
      return <StatusTag label="启动失败" tone="error" hint={app.why || '运行期启动失败'} />
    default:
      return <StatusTag label="已启用" tone="muted" />
  }
}

function marketBadge(m: MarketApp): { label: string; tone: 'muted' | 'success' | 'warning' | 'error' } {
  if (m.state === 'active') return { label: '运行中', tone: 'success' }
  if (m.state === 'failed') return { label: '启动失败', tone: 'error' }
  if (m.installed && !m.enabled) return { label: '已安装 · 未启用', tone: 'muted' }
  if (m.installed) return { label: '已安装', tone: 'success' }
  if (!m.installable) return { label: '需远程分发', tone: 'muted' }
  return { label: '未安装', tone: 'muted' }
}

function taglineOfMarket(m: MarketApp): string {
  return (m.tagline && m.tagline.trim()) || m.summary?.zh || `用「${m.name}」帮你处理相关任务。`
}

function membersSummary(m?: AppType['members']): string {
  if (!m) return '未引用'
  const parts: string[] = []
  if (m.experts?.length) parts.push(`专家 ${m.experts.length}`)
  if (m.automations?.length) parts.push(`定时 ${m.automations.length}`)
  if (m.skills?.length) parts.push(`技能 ${m.skills.length}`)
  if (m.mcps?.length) parts.push(`连接器 ${m.mcps.length}`)
  return parts.length ? parts.join('、') : '未引用'
}

function sourceLabel(s?: AppType['source']): string {
  if (s === 'market') return '应用市场'
  return '自建'
}

const avaStyle: React.CSSProperties = {
  width: 44,
  height: 44,
  borderRadius: 10,
  background: tokens.accentSoft,
  color: tokens.accent,
  display: 'grid',
  placeItems: 'center',
  flex: 'none',
  fontSize: 22,
}

const exampleChipStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: 8,
  background: tokens.softFill,
  border: '1px solid transparent',
  borderRadius: 7,
  padding: '9px 12px',
  fontSize: 13,
  color: tokens.text,
  cursor: 'pointer',
}

const arrStyle: React.CSSProperties = {
  color: tokens.textPlaceholder,
  marginLeft: 'auto',
  fontFamily: tokens.fontMono,
  fontSize: 11,
}

const guideBoxStyle: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: 8,
  fontSize: 13,
  lineHeight: 1.7,
  background: tokens.codeBg,
  border: `1px solid ${tokens.border}`,
  borderRadius: 10,
  padding: '12px 14px',
}

function tabStyle(on: boolean): React.CSSProperties {
  return {
    padding: '10px 12px',
    fontSize: 13,
    color: on ? tokens.accent : tokens.textSecondary,
    fontWeight: on ? 600 : 400,
    cursor: 'pointer',
    borderBottom: `2px solid ${on ? tokens.accent : 'transparent'}`,
    marginBottom: -1,
  }
}
