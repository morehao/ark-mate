import { useCallback, useEffect, useMemo, useState } from 'react'
import { App as AntApp, Button, Input, Popconfirm, Tooltip } from 'antd'
import { SearchOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import type { MarketApp } from '@ark-mate/types'
import { installApp, listMarket, refreshMarket, setAppEnabled, uninstallApp, upgradeApp } from '@ark-mate/api'
import { CapabilityCard, CapabilityEmpty, SectionHeader, StatusTag } from '../capability/shared'
import { appIconNode } from './appIcons'

/**
 * 应用市场：浏览可安装的应用包（来自内置清单/可换源 registry），一键装/卸/启用/更新。
 *
 * 卡片态机（执行计划 §4.4）：
 *   未装             → 「安装」（installApp(dir, false)，装完刷新）
 *   已装未启用        → 「启用」（setAppEnabled(true)）
 *   已装运行          → 「已启用」+「卸载」（uninstallApp）
 *   可更新            → 「更新」（upgradeApp；本期后端可能返"接线中"）
 *   installable=false → 按钮置灰 + tooltip「需远程分发」
 */
export function AppMarketPanel() {
  const { message } = AntApp.useApp()
  const [apps, setApps] = useState<MarketApp[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  // 正在操作的 app id（按钮 loading；同一时刻只允许一个动作）。
  const [busy, setBusy] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setApps(await listMarket())
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  // 刷新：先让后端清缓存（下次 listMarket 重拉远端），再重新拉清单。
  const onRefresh = useCallback(async () => {
    setLoading(true)
    try {
      await refreshMarket()
    } catch {
      // 刷新缓存失败不阻断：下面仍尝试拉一次（走既有缓存/内置兜底）。
    }
    await load()
  }, [load])

  const run = useCallback(
    async (id: string, action: () => Promise<void>, ok: string) => {
      setBusy(id)
      try {
        await action()
        message.success(ok)
        await load()
      } catch (e) {
        message.error(e instanceof Error ? e.message : String(e))
      } finally {
        setBusy(null)
      }
    },
    [load, message],
  )

  const visible = useMemo(() => filterMarket(apps, query), [apps, query])

  return (
    <PageContainer
      title="应用市场"
      description="浏览可安装的应用——把一个业务场景（专家、定时任务、技能、工具）打包成一个整体，一键装进来。"
    >
      <SectionHeader
        title="可安装的应用"
        hint="装进来后可随时启用/停用；停用不会删除应用本身。"
        count={visible.length}
        loading={loading}
        onRefresh={() => void onRefresh()}
        tools={
          <Input
            size="small"
            allowClear
            prefix={<SearchOutlined style={{ color: tokens.textPlaceholder }} />}
            style={{ width: 200 }}
            placeholder="搜索应用、标签"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        }
      />
      <div style={{ maxHeight: 560, overflowY: 'auto' }}>
        {error ? (
          // 拉清单彻底失败（连内置兜底都没回）——给可重试入口，不白屏。
          <CapabilityEmpty text={`加载应用市场失败：${error}`} action={<a onClick={() => void onRefresh()}>重试</a>} />
        ) : visible.length === 0 ? (
          <CapabilityEmpty text={apps.length === 0 ? '市场暂无应用。' : `没有找到匹配「${query}」的应用。`} />
        ) : (
          <div style={gridStyle}>
            {visible.map((a) => (
              <MarketCard
                key={a.id}
                app={a}
                busy={busy === a.id}
                onInstall={() => {
                  const dir = a.source.dir ?? ''
                  if (!dir) {
                    message.error('该应用缺少来源目录，无法安装')
                    return
                  }
                  void run(a.id, () => installApp(dir, false).then(() => undefined), `已安装「${a.name}」`)
                }}
                onEnable={() => void run(a.id, () => setAppEnabled(a.id, true).then(() => undefined), `已启用「${a.name}」`)}
                onUpgrade={() => void run(a.id, () => upgradeApp(a.id).then(() => undefined), `已更新「${a.name}」`)}
                onUninstall={() => void run(a.id, () => uninstallApp(a.id).then(() => undefined), `已卸载「${a.name}」`)}
              />
            ))}
          </div>
        )}
      </div>
    </PageContainer>
  )
}

/** 单张市场卡片：图标·名称·版本·id·摘要·标签 + 底栏状态与动作按钮（态机）。 */
function MarketCard({
  app,
  busy,
  onInstall,
  onEnable,
  onUpgrade,
  onUninstall,
}: {
  app: MarketApp
  busy: boolean
  onInstall: () => void
  onEnable: () => void
  onUpgrade: () => void
  onUninstall: () => void
}) {
  const health = marketHealthView(app)
  const remoteOnly = !app.installable && !app.installed
  return (
    <CapabilityCard
      dim={remoteOnly}
      avatar={appIconNode(app.icon)}
      title={
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 7 }}>
          {app.name}
          <span style={{ fontSize: 10.5, color: tokens.textPlaceholder, fontFamily: tokens.fontMono, fontWeight: 400 }}>
            v{app.version}
          </span>
        </span>
      }
      tags={<StatusTag label={health.label} tone={health.tone} hint={health.hint} />}
      description={app.summary?.zh || '（无摘要）'}
      meta={
        <span style={{ display: 'inline-flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
          <span style={{ fontFamily: tokens.fontMono, fontSize: 10.5 }}>{app.id}</span>
          {app.tags?.map((t) => (
            <span key={t} style={tagChipStyle}>
              {t}
            </span>
          ))}
        </span>
      }
      actions={
        <MarketActions
          app={app}
          busy={busy}
          remoteOnly={remoteOnly}
          onInstall={onInstall}
          onEnable={onEnable}
          onUpgrade={onUpgrade}
          onUninstall={onUninstall}
        />
      }
    />
  )
}

/** 底栏动作组：按态机渲染按钮。 */
function MarketActions({
  app,
  busy,
  remoteOnly,
  onInstall,
  onEnable,
  onUpgrade,
  onUninstall,
}: {
  app: MarketApp
  busy: boolean
  remoteOnly: boolean
  onInstall: () => void
  onEnable: () => void
  onUpgrade: () => void
  onUninstall: () => void
}) {
  // installable=false 且未装：不可装（需远程分发 C8）——按钮置灰 + tooltip。
  if (remoteOnly) {
    return (
      <Tooltip title="该应用需从远端下载（C8 签名链），暂不可装">
        <span>
          <Button size="small" disabled>
            安装
          </Button>
        </span>
      </Tooltip>
    )
  }

  // 未装：安装。
  if (!app.installed) {
    return (
      <Button size="small" type="primary" loading={busy} onClick={onInstall}>
        {busy ? '安装中…' : '安装'}
      </Button>
    )
  }

  // 已装：有更新给「更新」；未启用给「启用」，启用后显示「已启用」；始终给「卸载」。
  return (
    <>
      {app.updatable && (
        <Button size="small" loading={busy} onClick={onUpgrade}>
          更新
        </Button>
      )}
      {!app.enabled ? (
        <Button size="small" loading={busy} onClick={onEnable}>
          {busy ? '启用中…' : '启用'}
        </Button>
      ) : (
        <span style={{ fontSize: 11, color: tokens.success }}>已启用</span>
      )}
      <Popconfirm
        title={`卸载「${app.name}」？`}
        description="卸载会先停运行期、清包目录，再删除应用定义。"
        okText="卸载"
        okButtonProps={{ danger: true }}
        cancelText="取消"
        onConfirm={onUninstall}
        disabled={busy}
      >
        <Button size="small" danger type="text" loading={busy}>
          卸载
        </Button>
      </Popconfirm>
    </>
  )
}

/** 卡片态 → StatusTag 的 label/tone/hint（active→success、pending→warning、failed→error、其它→muted）。 */
function marketHealthView(app: MarketApp): { label: string; tone: 'muted' | 'success' | 'warning' | 'error'; hint?: string } {
  if (!app.installed) {
    return app.installable ? { label: '未安装', tone: 'muted' } : { label: '需远程分发', tone: 'muted' }
  }
  if (!app.enabled) return { label: '已安装 · 未启用', tone: 'muted' }
  switch (app.state) {
    case 'active':
      return { label: '已安装 · 运行中', tone: 'success' }
    case 'pending':
      return { label: '已安装 · 启动中', tone: 'warning' }
    case 'failed':
      return { label: '已安装 · 启动失败', tone: 'error' }
    default:
      return { label: '已安装 · 已启用', tone: 'success' }
  }
}

function filterMarket(apps: MarketApp[], query: string): MarketApp[] {
  const q = query.trim().toLowerCase()
  if (!q) return apps
  return apps.filter((a) =>
    [a.name, a.id, a.summary?.zh ?? '', a.summary?.en ?? '', ...(a.tags ?? [])].join(' ').toLowerCase().includes(q),
  )
}

// 卡片网格（与能力卡片页同列宽；外层 maxHeight 滚动容器负责滚动）。
const gridStyle = {
  display: 'grid',
  gap: 14,
  padding: '18px 16px',
  gridTemplateColumns: 'repeat(auto-fill, minmax(300px, 1fr))',
} as const

const tagChipStyle = {
  fontSize: 10.5,
  color: tokens.textSecondary,
  background: tokens.softFill,
  border: `1px solid ${tokens.border}`,
  borderRadius: 5,
  padding: '1px 7px',
  lineHeight: '16px',
} as const
