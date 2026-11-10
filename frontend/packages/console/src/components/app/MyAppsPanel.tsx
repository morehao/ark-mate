import { useCallback, useEffect, useMemo, useState } from 'react'
import { App as AntApp, Button, Input, Modal, Popconfirm, Switch, Tooltip } from 'antd'
import { SearchOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import type { App as AppType } from '@ark-mate/types'
import { listApps, setAppEnabled, uninstallApp } from '@ark-mate/api'
import { CapabilityEmpty, DetailGrid, SectionHeader, StatusTag } from '../capability/shared'
import { appIconNode } from './appIcons'

/**
 * 我的应用：列出**全部** app 定义（含通过对话创建、无独立页面的应用），可启停 / 看详情 / 卸载。
 *
 * 这补的是 §0.1 的缺口——前端过去只把「带 page 的 app」补登进导航，无 page 的 app 在界面上
 * 哪儿都不显示；本页消费 GET /apps（含 state/why/enabled/has_page），让所有 app 定义可见可控。
 */
export function MyAppsPanel() {
  const { message } = AntApp.useApp()
  const [apps, setApps] = useState<AppType[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  // 正在启停/卸载的 app id（按钮/Switch loading）。
  const [busy, setBusy] = useState<string | null>(null)
  // 当前展开详情的 app（null=关闭）。
  const [detail, setDetail] = useState<AppType | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setApps(await listApps())
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const toggle = useCallback(
    async (app: AppType, enabled: boolean) => {
      setBusy(app.id)
      try {
        await setAppEnabled(app.id, enabled)
        message.success(enabled ? `已启用「${app.name}」` : `已停用「${app.name}」`)
        await load()
      } catch (e) {
        message.error(e instanceof Error ? e.message : String(e))
      } finally {
        setBusy(null)
      }
    },
    [load, message],
  )

  const remove = useCallback(
    async (app: AppType) => {
      setBusy(app.id)
      try {
        await uninstallApp(app.id)
        message.success(`已卸载「${app.name}」`)
        await load()
      } catch (e) {
        message.error(e instanceof Error ? e.message : String(e))
      } finally {
        setBusy(null)
      }
    },
    [load, message],
  )

  const visible = useMemo(() => filterApps(apps, query), [apps, query])

  return (
    <PageContainer
      title="我的应用"
      description="所有应用定义，包括通过对话创建、没有独立页面的应用。可启停、查看详情、卸载。"
    >
      <SectionHeader
        title="全部应用"
        hint="启用后助手在合适的对话里使用该应用的能力；停用只暂不使用，定义保留。"
        count={visible.length}
        loading={loading}
        onRefresh={() => void load()}
        tools={
          <Input
            size="small"
            allowClear
            prefix={<SearchOutlined style={{ color: tokens.textPlaceholder }} />}
            style={{ width: 200 }}
            placeholder="搜索应用"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        }
      />
      <div style={{ maxHeight: 560, overflowY: 'auto' }}>
        {error ? (
          <CapabilityEmpty text={`加载应用列表失败：${error}`} action={<a onClick={() => void load()}>重试</a>} />
        ) : visible.length === 0 ? (
          <CapabilityEmpty text={apps.length === 0 ? '还没有应用。可以在对话里创建，或到应用市场安装。' : `没有找到匹配「${query}」的应用。`} />
        ) : (
          visible.map((a) => (
            <AppRow
              key={a.id}
              app={a}
              busy={busy === a.id}
              onToggle={(next) => void toggle(a, next)}
              onDetail={() => setDetail(a)}
              onUninstall={() => void remove(a)}
            />
          ))
        )}
      </div>

      <AppDetailModal app={detail} onClose={() => setDetail(null)} onUninstall={(a) => void remove(a)} />
    </PageContainer>
  )
}

/** 一行应用：图标 · 名称+id · 描述 → state 徽标 · Switch · 详情 · 卸载。 */
function AppRow({
  app,
  busy,
  onToggle,
  onDetail,
  onUninstall,
}: {
  app: AppType
  busy: boolean
  onToggle: (next: boolean) => void
  onDetail: () => void
  onUninstall: () => void
}) {
  const badge = appStateView(app)
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 14,
        padding: '12px 16px',
        borderBottom: `1px solid ${tokens.border}`,
      }}
    >
      <span style={iconBoxStyle}>{appIconNode(app.avatar)}</span>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontSize: 13.5, fontWeight: 600, display: 'flex', alignItems: 'center', gap: 8 }}>
          {app.name || app.id}
          <span style={{ fontSize: 11, color: tokens.textPlaceholder, fontFamily: tokens.fontMono, fontWeight: 400 }}>
            {app.id}
          </span>
        </div>
        <div
          title={app.description}
          style={{
            fontSize: 12,
            color: tokens.textSecondary,
            marginTop: 2,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {app.description || (app.has_page ? '（无描述）' : '（无描述 · 无独立页面）')}
        </div>
      </div>
      <div style={{ flex: 'none', width: 130 }}>
        <StatusTag label={badge.label} tone={badge.tone} hint={badge.hint} />
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flex: 'none' }}>
        <Tooltip title={app.enabled ? '停用：助手暂不使用，可随时恢复' : '启用：助手会在合适的对话里使用'}>
          <Switch size="small" checked={app.enabled} loading={busy} onChange={onToggle} />
        </Tooltip>
        <Button size="small" type="text" onClick={onDetail}>
          详情
        </Button>
        <Popconfirm
          title={`卸载「${app.name || app.id}」？`}
          description="卸载会先停运行期、清包目录，再删除应用定义，不可恢复。"
          okText="卸载"
          okButtonProps={{ danger: true }}
          cancelText="取消"
          onConfirm={onUninstall}
          disabled={busy}
        >
          <Button size="small" type="text" danger loading={busy}>
            卸载
          </Button>
        </Popconfirm>
      </div>
    </div>
  )
}

/** 详情弹窗：复用 DetailGrid（描述/头像/标签/页面入口/工具域/成员/状态）。 */
function AppDetailModal({
  app,
  onClose,
  onUninstall,
}: {
  app: AppType | null
  onClose: () => void
  onUninstall: (app: AppType) => void
}) {
  const badge = app ? appStateView(app) : null
  const items = app
    ? [
        { label: '描述', value: app.description || '（无）' },
        { label: '头像', value: app.avatar || '（默认）' },
        { label: '标签', value: app.tags?.length ? app.tags.join('、') : '（无）' },
        { label: '页面入口', value: app.has_page ? app.page?.route || '有' : '无' },
        { label: '工具域', value: app.tool_domains?.length ? app.tool_domains.join('、') : '（无）' },
        { label: '成员', value: membersSummary(app) },
        { label: '状态', value: <StatusTag label={badge!.label} tone={badge!.tone} hint={badge!.hint} /> },
        { label: '版本', value: app.revision != null ? `rev ${app.revision}` : '（无）' },
      ]
    : []

  return (
    <Modal
      open={app !== null}
      title={app ? `${app.name || app.id} · ${app.id}` : ''}
      onCancel={onClose}
      destroyOnClose
      width={560}
      footer={
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <Button onClick={onClose}>关闭</Button>
          <Button
            danger
            onClick={() => {
              if (app) onUninstall(app)
              onClose()
            }}
          >
            卸载
          </Button>
        </div>
      }
    >
      <DetailGrid items={items} />
    </Modal>
  )
}

/** state → StatusTag（active→success / pending→warning / failed→error / off·其它→muted）。 */
function appStateView(app: AppType): { label: string; tone: 'muted' | 'success' | 'warning' | 'error'; hint?: string } {
  // 未启用：无论运行期 state 都视为「未启用」（与后端「停用即 off」一致）。
  if (!app.enabled) return { label: '未启用', tone: 'muted' }
  switch (app.state) {
    case 'active':
      return { label: '运行中', tone: 'success' }
    case 'pending':
      return { label: '等待中', tone: 'warning', hint: app.why || '等待依赖服务就绪' }
    case 'failed':
      return { label: '启动失败', tone: 'error', hint: app.why || '运行期启动失败' }
    case 'disposed':
      return { label: '已停用', tone: 'muted', hint: app.why }
    case 'off':
      return { label: '未启用', tone: 'muted' }
    default:
      return { label: app.enabled ? '已启用' : '未启用', tone: 'muted' }
  }
}

function membersSummary(app: AppType): string {
  const m = app.members
  if (!m) return '未引用'
  const parts: string[] = []
  if (m.experts?.length) parts.push(`专家 ${m.experts.length}`)
  if (m.automations?.length) parts.push(`定时 ${m.automations.length}`)
  if (m.skills?.length) parts.push(`技能 ${m.skills.length}`)
  if (m.mcps?.length) parts.push(`连接器 ${m.mcps.length}`)
  return parts.length ? parts.join('、') : '未引用'
}

function filterApps(apps: AppType[], query: string): AppType[] {
  const q = query.trim().toLowerCase()
  if (!q) return apps
  return apps.filter((a) => [a.name, a.id, a.description ?? '', ...(a.tags ?? [])].join(' ').toLowerCase().includes(q))
}

const iconBoxStyle = {
  width: 34,
  height: 34,
  borderRadius: 8,
  flex: 'none',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  fontSize: 16,
  background: tokens.accentSoft,
  color: tokens.accent,
  border: '1px solid #e2e7ff',
} as const
