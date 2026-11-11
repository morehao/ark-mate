import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { App as AntApp, Input, Switch, Tooltip } from 'antd'
import { SearchOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import type { App as AppType } from '@ark-mate/types'
import { listApps, setAppEnabled, uninstallApp } from '@ark-mate/api'
import { useMate } from '../../store'
import { CapabilityEmpty, SectionHeader, StatusTag } from '../capability/shared'
import { AppCard } from './AppCard'
import { appIconNode } from './appIcons'
import { AppUsageModal, appTaglineOf, type AppUsageTarget } from './AppUsageModal'

/**
 * 我的应用：列出**全部** app 定义（含通过对话创建、无独立页面的应用），可启停 / 看详情 / 卸载。
 *
 * 这补的是 §0.1 的缺口——前端过去只把「带 page 的 app」补登进导航，无 page 的 app 在界面上
 * 哪儿都不显示；本页消费 GET /apps（含 state/why/enabled/has_page），让所有 app 定义可见可控。
 * 2026-11-11 由列表行改为卡片网格（与市场页同用 `AppCard`）。
 */
export function MyAppsPanel() {
  const { message } = AntApp.useApp()
  const navigate = useNavigate()
  const createAndRun = useMate((s) => s.createAndRun)
  const [apps, setApps] = useState<AppType[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  // 正在启停/卸载的 app id（按钮/Switch loading）。
  const [busy, setBusy] = useState<string | null>(null)
  // 当前打开「使用说明」弹层的 app（null=关闭）。
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

  // 「在新会话里用这个应用」（P2）：开一条带 `capabilities.apps:[id]` 的新会话并跳过去。
  //
  // v1 边界（设计 §6.2）：只带 apps（注入该应用的工具域），**不**把成员专家自动塞进会话
  // （成员专家是另一条正交通道，硬塞会让「带的是什么」说不清）。`POST /sessions` 的 prompt 必填
  // （它即首轮），所以这里给一句简短引导语让会话有起点，而不是造一个 API 不支持的"纯空会话"。
  const launch = useCallback(
    async (app: AppType, prompt?: string) => {
      const label = app.name || app.id
      const text = (prompt && prompt.trim()) || `使用应用「${label}」`
      const id = await createAndRun(text, undefined, undefined, { apps: [app.id] })
      if (id) navigate(`/sessions/${id}`)
    },
    [createAndRun, navigate],
  )

  return (
    <PageContainer
      title="我的应用"
      description="所有应用，包括通过对话创建、没有独立页面的。点「查看用法」看它能做什么、怎么开口，可直接点示例话术开始。"
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
          <div className="fg-cap-grid">
            {visible.map((a) => (
              <MyAppCard
                key={a.id}
                app={a}
                busy={busy === a.id}
                onToggle={(next) => void toggle(a, next)}
                onDetail={() => setDetail(a)}
              />
            ))}
          </div>
        )}
      </div>

      <AppUsageModal
        open={detail !== null}
        target={detail ? ({ kind: 'mine', app: detail } satisfies AppUsageTarget) : null}
        onClose={() => setDetail(null)}
        onUninstall={(a) => {
          void remove(a)
          setDetail(null)
        }}
        onLaunch={(a, prompt) => {
          void launch(a, prompt)
          setDetail(null)
        }}
      />
    </PageContainer>
  )
}

/**
 * 一张「我的应用」卡片（复用 `AppCard`）：图标 · 名称+id · 描述 → 状态徽标，
 * 底栏动作 = 「查看用法 →」（打开 AppUsageModal）+ 右端启停开关；卸载收在弹层里。
 * 点卡片主体同样打开详情。
 */
function MyAppCard({
  app,
  busy,
  onToggle,
  onDetail,
}: {
  app: AppType
  busy: boolean
  onToggle: (next: boolean) => void
  onDetail: () => void
}) {
  const badge = appStateView(app)
  return (
    <AppCard
      icon={appIconNode(app.avatar)}
      name={app.name || app.id}
      statusTag={<StatusTag label={badge.label} tone={badge.tone} hint={badge.hint} />}
      description={appTaglineOf(app)}
      meta={
        <button type="button" className="app-usage-link" style={usageLinkStyle} onClick={onDetail}>
          查看用法 <span aria-hidden>→</span>
        </button>
      }
      onOpenDetail={onDetail}
      actions={
        <Tooltip title={app.enabled ? '停用：助手暂不使用，可随时恢复' : '启用：助手会在合适的对话里使用'}>
          <Switch size="small" checked={app.enabled} loading={busy} onChange={onToggle} />
        </Tooltip>
      }
    />
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

function filterApps(apps: AppType[], query: string): AppType[] {
  const q = query.trim().toLowerCase()
  if (!q) return apps
  return apps.filter((a) => [a.name, a.id, a.description ?? '', ...(a.tags ?? [])].join(' ').toLowerCase().includes(q))
}

/** 卡片左下角「查看用法 →」链接样式（克制：accent 字，无底色）。 */
const usageLinkStyle = {
  border: 'none',
  background: 'transparent',
  padding: 0,
  cursor: 'pointer',
  color: tokens.accent,
  fontSize: 12,
  fontFamily: 'inherit',
} as const
