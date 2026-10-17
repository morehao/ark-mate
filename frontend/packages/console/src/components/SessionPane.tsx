import { useEffect, useState } from 'react'
import { Dropdown, Input, Modal } from 'antd'
import {
  DownloadOutlined,
  EditOutlined,
  InboxOutlined,
  LoadingOutlined,
  MoreOutlined,
  PushpinFilled,
  PushpinOutlined,
  UndoOutlined,
} from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { tokens } from '@ark-mate/ui'
import type { MenuProps } from 'antd'
import type { SessionSearchHit, SessionSummary, MessageSearchHit, MessageSearchResp } from '@ark-mate/types'
import { sessionSearch, exportSessions, messageSearch, toErrorMessage } from '@ark-mate/api'
import { dedupeHits, exportDownloadName, roleLabel, sourceLabel } from '../console/searchView'
import { useMate } from '../store'
import { RAIL_PAGE, pageSessions, relTime } from '../console/railView'
import { archivedFilterToRailMode, railModeToArchivedFilter } from '../console/workspaceView'
import { RailSectionHeader } from './RailSectionHeader'
import { WorkspaceGroupPane } from './WorkspaceGroupPane'
import { runtimeLabelOf, titleFor } from '../console/util'

interface Props {
  onCreate: () => void
}

/** 分组模式的前端本地持久键（视图偏好，不进 db）。 */
const LIST_MODE_KEY = 'mate.web.listMode'

/** 左栏「＋」把要预选的工作空间交给新建面板的唯一入口。 */
function setPreferredWorkspace(id: string): void {
  useMate.getState().setPreferredWorkspace(id)
}

/** 置顶组 + 按创建日分组的未置顶会话（轻量时间线管理面）。 */
function groupSessions(
  all: SessionSummary[],
): Array<{ label: string; items: SessionSummary[]; pinned: boolean }> {
  const groups: Array<{ label: string; items: SessionSummary[]; pinned: boolean }> = []
  const pinned = all.filter((s) => s.pinned)
  if (pinned.length > 0) groups.push({ label: '置顶', items: pinned, pinned: true })
  const rest = all.filter((s) => !s.pinned)
  const byDay = new Map<string, SessionSummary[]>()
  for (const s of rest) {
    const label = dayLabel(s.created_at)
    const arr = byDay.get(label)
    if (arr) arr.push(s)
    else byDay.set(label, [s])
  }
  for (const [label, items] of byDay) groups.push({ label, items, pinned: false })
  return groups
}

function dayLabel(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '更早'
  const now = new Date()
  const sd = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const diff = Math.round((sd(now) - sd(d)) / 86_400_000)
  if (diff <= 0) return '今天'
  if (diff === 1) return '昨天'
  return `${d.getMonth() + 1}月${d.getDate()}日`
}

/** 会话列表区（原会话侧栏 .side）：区头（视图选项 + 折叠搜索 + 添加工作区）+ 会话列表
 * （分组/置顶/行内更多操作）。
 *
 * 外壳（宽度 / 抽屉位移 / `.fg-rail` 类）已上移给 NavRail——本组件只负责"内容体"，
 * 由 NavRail 提供左栏容器与窄屏抽屉态。 */
export function SessionPane({ onCreate }: Props) {
  const navigate = useNavigate()
  const sessions = useMate((s) => s.sessions)
  const runtime = useMate((s) => s.runtime)
  const views = useMate((s) => s.views)
  const activeId = useMate((s) => s.activeId)
  const railMode = useMate((s) => s.railMode)
  const setDrawer = useMate((s) => s.setDrawer)
  const togglePin = useMate((s) => s.togglePin)
  const archiveSession = useMate((s) => s.archiveSession)
  const renameSession = useMate((s) => s.renameSession)
  const setRailMode = useMate((s) => s.setRailMode)
  const jumpToAnchor = useMate((s) => s.jumpToAnchor)
  const showToast = useMate((s) => s.showToast)

  // 行内「重命名」弹窗
  const [renaming, setRenaming] = useState<{ id: string; init: string } | null>(null)
  const [draft, setDraft] = useState('')

  // 会话搜索：后端扫 transcript（标题/底座/用户消息正文；含归档），250ms 防抖。
  // 搜索框在区头里是**折叠**的（v14.2，对齐 dsh）：没有它这条列表上方就少一行常驻控件。
  const [searchQ, setSearchQ] = useState('')
  const [searchOpen, setSearchOpen] = useState(false)
  const [hits, setHits] = useState<SessionSearchHit[] | null>(null) // null = 未搜索
  const [msgHits, setMsgHits] = useState<MessageSearchResp | null>(null) // M3 ②：消息级命中（带锚点）
  const [searchBusy, setSearchBusy] = useState(false)
  const [exportBusy, setExportBusy] = useState(false)

  useEffect(() => {
    const q = searchQ.trim()
    if (!q) {
      setHits(null)
      setMsgHits(null)
      setSearchBusy(false)
      return
    }
    let live = true
    const t = setTimeout(() => {
      setSearchBusy(true)
      // 会话命中（标题/正文聚合）与消息命中（带 seq 锚点）并发；任一失败都退化为空结果。
      Promise.allSettled([sessionSearch(q), messageSearch(q)])
        .then(([conv, msgs]) => {
          if (!live) return
          setHits(conv.status === 'fulfilled' ? conv.value : [])
          setMsgHits(
            msgs.status === 'fulfilled'
              ? { ...msgs.value, hits: dedupeHits(msgs.value.hits) }
              : { query: q, source: 'sqlite', hits: [], total: 0, has_more: false },
          )
        })
        .finally(() => live && setSearchBusy(false))
    }, 250)
    return () => {
      live = false
      clearTimeout(t)
    }
  }, [searchQ])

  const busy = activeId ? (views[activeId]?.streaming ?? false) : false
  const archivedView = railMode === 'archived'

  // 列表分页（P3 余留）：只在**时间线**视图生效。分组视图的条数由"每组 5 条空闲会话 +
  // 展开其余"约束（`railView.quotaSessions`，在 WorkspaceGroupPane 里）。两套一起上会在同一个
  // 列表里同时出现「展开其余 N 个会话」和「显示更早的 N 条」——两个语义不同的"还有更多"，
  // 用户没法判断哪个才是真的还有。切视图时复位。
  const [shown, setShown] = useState(RAIL_PAGE)
  useEffect(() => setShown(RAIL_PAGE), [railMode])
  const railPage = pageSessions(sessions, shown, activeId)

  // 分组模式（v14）：`workspace` = 按工作空间分组（默认）；`timeline` = 按创建日的旧视图。
  //
  // 为什么保留旧视图：分组视图回答"我在哪个项目里有哪些会话"，而按时间回答"我最近干了什么"。
  // 两者都常用，且**登记表不可用**（memory 持久层 / 老服务端）时必须能退回时间线——
  // 那时没有分组可挂，硬渲染分组会让会话列表整个消失。
  const workspaces = useMate((s) => s.workspaces)
  const grouped = (workspaces?.items.length ?? 0) > 0
  const [listMode, setListMode] = useState<'workspace' | 'timeline'>(() =>
    localStorage.getItem(LIST_MODE_KEY) === 'timeline' ? 'timeline' : 'workspace',
  )
  // 登记表拿不到（null / 空）时**强制**走时间线：这不是用户选择，而是"没有分组可挂"的事实。
  const effectiveMode: 'workspace' | 'timeline' = grouped ? listMode : 'timeline'
  const setMode = (m: 'workspace' | 'timeline') => {
    setListMode(m)
    try {
      localStorage.setItem(LIST_MODE_KEY, m)
    } catch {
      // 记不住偏好不影响功能，不打扰用户。
    }
  }

  // 点分组行的「＋」＝在此工作空间新建会话：回到新建面板并把该工作空间预选上。
  // 这是"左栏浏览面"与"新建选择面"之间唯一的桥（设计 §5.1）。
  const createInWorkspace = (workspaceId: string) => {
    setPreferredWorkspace(workspaceId)
    onCreate()
  }

  // 分组视图里没被任何登记覆盖的会话（登记被删/归属指向已不存在的行）：兜一个「其他」组，
  // 而不是让它们从列表里凭空消失。分组视图不分页（见上面的说明），所以这里看的是**全量**。
  const ungrouped = grouped
    ? sessions.filter((c) => {
        const id = c.workspace_id ?? ''
        return !(workspaces?.items ?? []).some((w) => w.id === id)
      })
    : []

  const openSession = (id: string) => {
    setDrawer(false)
    navigate(`/sessions/${id}`)
  }

  const submitRename = () => {
    if (!renaming) return
    const t = draft.trim()
    const target = renaming
    setRenaming(null)
    if (t && t !== target.init) void renameSession(target.id, t)
  }

  const openHit = (h: SessionSearchHit) => {
    setSearchQ('')
    // 命中即收起搜索：区头回到"标题 + 动作"，浏览模式随之恢复（对齐 dsh：选择结果会清空并收起搜索）。
    setSearchOpen(false)
    setHits(null)
    setMsgHits(null)
    openSession(h.id)
  }

  // 消息命中点击：跳转到该会话并带上 seq 锚点（ChatPage 负责滚动定位与高亮）。
  const openMessageHit = (h: MessageSearchHit) => {
    setSearchQ('')
    setSearchOpen(false)
    setHits(null)
    setMsgHits(null)
    setDrawer(false)
    jumpToAnchor(h.session_id, h.seq)
    navigate(`/sessions/${h.session_id}`)
  }

  // 单会话导出（行内菜单）：markdown 单文件下载。
  const exportOne = async (id: string, title: string) => {
    setExportBusy(true)
    try {
      const resp = await exportSessions([id], 'markdown')
      const f = resp.files[0]
      if (!f) {
        showToast('该会话暂无可导出内容')
        return
      }
      const blob = new Blob([f.content], { type: 'text/markdown;charset=utf-8' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = exportDownloadName(f.filename, id, f.format)
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
      showToast(`已导出「${title || id}」（${f.bytes} 字节）`)
    } catch (e) {
      showToast(`导出失败：${toErrorMessage(e)}`)
    } finally {
      setExportBusy(false)
    }
  }

  /** 消息命中行：角色徽标 + 片段 + 所属会话（点击跳转并定位到该条消息）。 */
  const renderMessageHit = (h: MessageSearchHit) => (
    <button
      key={`${h.session_id}#${h.seq}`}
      type="button"
      onClick={() => openMessageHit(h)}
      title={`${h.title || h.session_id} · seq ${h.seq}`}
      style={{
        display: 'block',
        width: '100%',
        textAlign: 'left',
        background: 'transparent',
        border: 0,
        padding: '7px 12px',
        borderRadius: 10,
        cursor: 'pointer',
        color: tokens.text,
      }}
    >
      <span style={{ display: 'flex', gap: 6, alignItems: 'center', marginBottom: 3 }}>
        <span
          style={{
            flex: 'none',
            fontFamily: tokens.fontMono,
            fontSize: 9.5,
            padding: '0 5px',
            borderRadius: 5,
            border: `1px solid ${tokens.border}`,
            color: h.role === 'user' ? tokens.textSecondary : tokens.textPlaceholder,
          }}
        >
          {roleLabel(h.role)}
        </span>
        <span
          style={{
            minWidth: 0,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
            fontSize: 11.5,
            color: tokens.textPlaceholder,
          }}
        >
          {h.title || h.session_id}
        </span>
      </span>
      <span
        style={{
          display: '-webkit-box',
          WebkitLineClamp: 2,
          WebkitBoxOrient: 'vertical',
          overflow: 'hidden',
          fontSize: 12.5,
          lineHeight: 1.5,
          color: tokens.textSecondary,
        }}
      >
        {h.snippet}
      </span>
    </button>
  )

  /** 搜索结果行：标题 + 命中片段 + 底座/归档/命中数。 */
  const renderHit = (h: SessionSearchHit) => (
    <button
      key={h.id}
      type="button"
      onClick={() => openHit(h)}
      style={{
        display: 'block',
        width: '100%',
        textAlign: 'left',
        background: 'transparent',
        border: 0,
        padding: '7px 12px',
        borderRadius: 10,
        cursor: 'pointer',
        color: tokens.text,
      }}
    >
      <span
        style={{
          display: '-webkit-box',
          WebkitLineClamp: 2,
          WebkitBoxOrient: 'vertical',
          overflow: 'hidden',
          fontSize: 13.5,
          fontWeight: 600,
          letterSpacing: '-0.005em',
          lineHeight: 1.4,
          color: h.archived ? tokens.textSecondary : tokens.text,
        }}
      >
        {h.title || runtimeLabelOf(runtime, h.runtime_id)}
      </span>
      {h.snippet ? (
        <span
          style={{
            display: '-webkit-box',
            WebkitLineClamp: 2,
            WebkitBoxOrient: 'vertical',
            overflow: 'hidden',
            marginTop: 3,
            fontSize: 12.5,
            lineHeight: 1.5,
            color: tokens.textSecondary,
          }}
        >
          {h.snippet}
        </span>
      ) : (
        <span style={{ marginTop: 3, display: 'block', fontSize: 11.5, color: tokens.textPlaceholder }}>（元数据命中）</span>
      )}
      <span
        style={{
          marginTop: 7,
          display: 'flex',
          alignItems: 'center',
          gap: 6,
          minWidth: 0,
          fontFamily: tokens.fontMono,
          fontSize: 10.5,
          color: tokens.textSecondary,
        }}
      >
        <span
          style={{
            display: 'inline-flex',
            alignItems: 'center',
            gap: 5,
            border: `1px solid ${tokens.border}`,
            borderRadius: 999,
            padding: '0 7px',
            whiteSpace: 'nowrap',
            color: tokens.text,
          }}
        >
          {runtimeLabelOf(runtime, h.runtime_id)}
        </span>
        {h.archived && (
          <span
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              border: `1px solid ${tokens.warningBorder}`,
              background: tokens.warningBg,
              color: tokens.warning,
              borderRadius: 999,
              padding: '0 7px',
              whiteSpace: 'nowrap',
            }}
          >
            归档
          </span>
        )}
        {h.matches > 1 && <span>命中 {h.matches}</span>}
      </span>
    </button>
  )

  const renderItem = (sm: SessionSummary) => {
    const view = views[sm.id]
    const on = sm.id === activeId

    // 行内「更多」：重命名 / 置顶·取消置顶 / 归档（或恢复）
    const menuItems: MenuProps['items'] = [
      {
        key: 'rename',
        icon: <EditOutlined />,
        label: '重命名',
        onClick: () => {
          setDraft(sm.title || titleFor(sm, view, runtime?.name))
          setRenaming({ id: sm.id, init: sm.title || titleFor(sm, view, runtime?.name) })
        },
      },
      { type: 'divider' },
      sm.pinned
        ? { key: 'unpin', icon: <PushpinFilled />, label: '取消置顶' }
        : { key: 'pin', icon: <PushpinOutlined />, label: '置顶' },
      sm.archived
        ? { key: 'restore', icon: <UndoOutlined />, label: '从归档恢复' }
        : { key: 'archive', icon: <InboxOutlined />, label: '归档' },
      { type: 'divider' },
      // 单会话导出（批量导出已于 2026-10-10 按反馈撤掉：列表工具栏不再放导出）
      { key: 'export', icon: exportBusy ? <LoadingOutlined /> : <DownloadOutlined />, label: '导出 Markdown', disabled: exportBusy },
    ]
    const onMenu: MenuProps['onClick'] = ({ key }) => {
      if (key === 'pin' || key === 'unpin') void togglePin(sm.id)
      else if (key === 'archive') void archiveSession(sm.id, true)
      else if (key === 'restore') void archiveSession(sm.id, false)
      else if (key === 'export') void exportOne(sm.id, sm.title || titleFor(sm, view, runtime?.name))
    }

    return (
      <div
        key={sm.id}
        className={`fg-sess-item${on ? ' fg-on' : ''}`}
        style={{
          position: 'relative',
          display: 'flex',
          flexDirection: 'column',
          gap: 2,
          width: '100%',
          // 百分比宽度是**内容盒**宽度：不换成 border-box 时，内边距与边框会把行顶出
          // 滚动盒 10px，横向滚动条又回来了（需求 5 的实测根因之一）。
          boxSizing: 'border-box',
          borderRadius: 10,
          padding: '4px 4px 4px 12px',
          marginBottom: 2,
          border: '1px solid transparent',
          color: tokens.text,
          opacity: sm.archived ? 0.68 : 1,
        }}
      >
        <button
          type="button"
          role="listitem"
          disabled={busy && !on}
          onClick={() => openSession(sm.id)}
          style={{
            flex: 1,
            minWidth: 0,
            textAlign: 'left',
            background: 'transparent',
            border: 0,
            padding: '2px 0',
            color: 'inherit',
            cursor: busy && !on ? 'not-allowed' : 'pointer',
          }}
        >
          {/* 会话行**只有一行**（需求 9 / 10）：标题在左、相对时间贴行右端，同一基线。
              行内不再有状态点 / 阶段文案 / 来源标记——「这一轮在干什么」在会话页头与主区
              确认区里说，左栏只回答「这条叫什么、什么时候」。 */}
          <span
            data-sess-line="title"
            style={{ display: 'flex', alignItems: 'center', gap: 5, minWidth: 0, paddingRight: 0 }}
          >
            <span
              style={{
                flex: 1,
                minWidth: 0,
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
                fontSize: 13.5,
                fontWeight: 560,
                letterSpacing: '-0.005em',
                color: sm.archived ? tokens.textSecondary : tokens.text,
              }}
              title={titleFor(sm, view, runtime?.name)}
            >
              {titleFor(sm, view, runtime?.name)}
            </span>
            {sm.pinned && <PushpinFilled style={{ fontSize: 11, color: tokens.primary, flex: 'none' }} />}
            <span className="fg-sess-time" title={sm.updated_at}>
              {relTime(sm.updated_at)}
            </span>
          </span>
        </button>
        {/* 行尾「更多」：绝对定位在行右端（原型 `.sess .ops`）。
            放在按钮**外面**是为了不出现嵌套 button；它与时间抢同一块空间，所以 hover / 当前行
            让时间淡出（D11），而不是把时间挤到左边。 */}
        <Dropdown menu={{ items: menuItems, onClick: onMenu }} trigger={['click']} placement="bottomRight">
          <button
            type="button"
            className={`fg-sess-more${on ? ' fg-keep' : ''}`}
            title="更多操作"
            aria-label="更多操作"
            onClick={(ev) => ev.stopPropagation()}
            style={{
              position: 'absolute',
              top: 3,
              right: 3,
              display: 'inline-grid',
              placeItems: 'center',
              width: 22,
              height: 22,
              border: 0,
              borderRadius: 7,
              background: 'transparent',
              color: tokens.textSecondary,
              cursor: 'pointer',
            }}
          >
            <MoreOutlined style={{ fontSize: 14 }} />
          </button>
        </Dropdown>
      </div>
    )
  }

  return (
    <div className="fg-rail-pane" aria-label="会话列表">
      {/* 区头：`会话 / 归档` 与 `工作空间 / 时间线` 两组分段切换、以及那条常驻搜索框，
          一起收进这里（v14.2，对齐 dsh 的工作区段头）。 */}
      <RailSectionHeader
        query={searchQ}
        onQueryChange={setSearchQ}
        searching={searchOpen}
        onSearchingChange={setSearchOpen}
        groupMode={effectiveMode}
        onGroupModeChange={setMode}
        canGroup={grouped}
        archivedFilter={railModeToArchivedFilter(railMode)}
        onArchivedFilterChange={(f) => setRailMode(archivedFilterToRailMode(f))}
        writable={workspaces?.writable ?? false}
      />

      {/* 左栏永远不横滚（需求 5）：只写 overflowY 时浏览器把另一轴算成 auto，
          任何长子节点都会带回横向滚动条。这里显式裁掉横轴。 */}
      <div style={{ flex: 1, overflowY: 'auto', overflowX: 'hidden', padding: '4px 8px 12px' }} role="list">
        {searchBusy ? (
          <div style={{ padding: '14px 12px', color: tokens.textPlaceholder, fontSize: 12.5 }}>搜索中…</div>
        ) : hits !== null ? (
          hits.length === 0 ? (
            <div style={{ padding: '18px 12px', color: tokens.textPlaceholder, fontSize: 12.5, lineHeight: 1.7 }}>
              无匹配「{searchQ.trim()}」的会话。可换关键词，或直接发起新会话。
            </div>
          ) : (
            <>
              <div
                style={{
                  padding: '10px 12px 5px',
                  fontFamily: tokens.fontMono,
                  fontSize: 10,
                  letterSpacing: '0.06em',
                  textTransform: 'uppercase',
                  color: tokens.textPlaceholder,
                }}
              >
                {hits.length} 个匹配{hits.some((x) => x.archived) ? ' · 含归档' : ''}
              </div>
              {hits.map(renderHit)}
              {(msgHits?.hits.length ?? 0) > 0 && (
                <div style={{ marginTop: 6, borderTop: `1px solid ${tokens.border}` }}>
                  <div
                    style={{
                      padding: '10px 12px 5px',
                      fontFamily: tokens.fontMono,
                      fontSize: 10,
                      letterSpacing: '0.06em',
                      textTransform: 'uppercase',
                      color: tokens.textPlaceholder,
                    }}
                  >
                    消息命中 {msgHits!.hits.length} 条 · {sourceLabel(msgHits!.source)}
                    {msgHits!.has_more ? ' · 仅前若干条' : ''}
                  </div>
                  {msgHits!.hits.map(renderMessageHit)}
                </div>
              )}
            </>
          )
        ) : sessions.length === 0 ? (
          <div style={{ padding: '18px 12px', color: tokens.textPlaceholder, fontSize: 12.5, lineHeight: 1.7 }}>
            {archivedView
              ? '暂无归档会话。点会话行的 ⋯ →「归档」即可把会话移到这里（数据保留，可恢复）。'
              : '暂无会话。点左栏顶部「新建会话」，选好工作空间即可发起首轮问答。'}
          </div>
        ) : effectiveMode === 'workspace' && grouped ? (
          <>
            <WorkspaceGroupPane
              archived={archivedView}
              onCreate={createInWorkspace}
              renderBody={(sessions) => <>{sessions.map(renderItem)}</>}
            />
            {ungrouped.length > 0 && (
              <div role="group" aria-label="其他">
                {/* 「其他」= 归属指向已不存在的登记（或读到旧数据）。不隐藏它们：
                    会话凭空消失是用户无法自行解释的状态。 */}
                <div
                  style={{
                    padding: '10px 12px 5px',
                    fontFamily: tokens.fontMono,
                    fontSize: 10,
                    letterSpacing: '0.06em',
                    textTransform: 'uppercase',
                    color: tokens.textPlaceholder,
                  }}
                >
                  其他
                </div>
                {ungrouped.map(renderItem)}
              </div>
            )}
          </>
        ) : (
          <>
            {groupSessions(railPage.visible).map((g) => (
            <div key={g.label} role="group" aria-label={g.label}>
              <div
                style={{
                  padding: '10px 12px 5px',
                  fontFamily: tokens.fontMono,
                  fontSize: 10,
                  letterSpacing: '0.06em',
                  textTransform: 'uppercase',
                  color: tokens.textPlaceholder,
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                }}
              >
                <span>{g.label}</span>
                {archivedView && <span>{g.items.length}</span>}
              </div>
              {g.items.map(renderItem)}
            </div>
          ))}
            {railPage.remaining > 0 && (
              <button
                type="button"
                onClick={() => setShown((n) => n + RAIL_PAGE)}
                style={{
                  display: 'block',
                  width: 'calc(100% - 16px)',
                  margin: '8px 8px 4px',
                  padding: '7px 10px',
                  background: 'transparent',
                  border: `1px dashed ${tokens.border}`,
                  borderRadius: 10,
                  color: tokens.textSecondary,
                  fontSize: 12,
                  cursor: 'pointer',
                }}
              >
                显示更早的 {Math.min(RAIL_PAGE, railPage.remaining)} 条（还有 {railPage.remaining} 条）
              </button>
            )}
          </>
        )}
      </div>

      <Modal
        open={renaming !== null}
        title="重命名会话"
        okText="保存"
        cancelText="取消"
        width={380}
        onOk={submitRename}
        onCancel={() => setRenaming(null)}
        destroyOnHidden
        style={{ top: 120 }}
      >
        <Input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onPressEnter={submitRename}
          placeholder="会话标题"
          autoFocus
          maxLength={80}
        />
      </Modal>
    </div>
  )
}
