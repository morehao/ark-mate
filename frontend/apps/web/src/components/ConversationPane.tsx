import { useEffect, useState, type ReactNode } from 'react'
import { Button, Dropdown, Input, Modal } from 'antd'
import {
  DownloadOutlined,
  EditOutlined,
  InboxOutlined,
  MoreOutlined,
  PlusOutlined,
  PushpinFilled,
  PushpinOutlined,
  SearchOutlined,
  UndoOutlined,
} from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { tokens } from '@ark-mate/ui'
import { Dot, conversationStatusMeta } from '@ark-mate/ui'
import { automationIDFromOrigin, isAutomationOrigin } from '../console/automationView'
import type { MenuProps } from 'antd'
import type { ConversationSearchHit, ConversationSummary, MessageSearchHit, MessageSearchResp } from '@ark-mate/types'
import { conversationSearch, exportConversations, messageSearch, toErrorMessage } from '@ark-mate/api'
import { dedupeHits, exportDownloadName, exportSummary, roleLabel, sourceLabel } from '../console/searchView'
import { useMate } from '../store'
import { RAIL_PAGE, pageConversations, relTime } from '../console/railView'
import { agentNameOf, titleFor } from '../console/util'

interface Props {
  onCreate: () => void
}

/** 置顶组 + 按创建日分组的未置顶会话（轻量时间线管理面）。 */
function groupConversations(
  all: ConversationSummary[],
): Array<{ label: string; items: ConversationSummary[]; pinned: boolean }> {
  const groups: Array<{ label: string; items: ConversationSummary[]; pinned: boolean }> = []
  const pinned = all.filter((s) => s.pinned)
  if (pinned.length > 0) groups.push({ label: '置顶', items: pinned, pinned: true })
  const rest = all.filter((s) => !s.pinned)
  const byDay = new Map<string, ConversationSummary[]>()
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

/** 会话侧栏「会话 / 归档」视图切换小标签。 */
function ViewTab({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      style={{
        flex: 'none',
        border: 0,
        borderRadius: 8,
        padding: '3px 11px',
        fontSize: 12.5,
        fontWeight: 620,
        letterSpacing: '-0.005em',
        background: active ? tokens.selectedBg : 'transparent',
        color: active ? tokens.text : tokens.textSecondary,
        cursor: 'pointer',
      }}
    >
      {children}
    </button>
  )
}

/** 会话列表区（原会话侧栏 .side）：视图切换 + 搜索 + 会话列表（分组/置顶/行内更多操作）。
 *
 * 外壳（宽度 / 抽屉位移 / `.fg-rail` 类）已上移给 NavRail——本组件只负责"内容体"，
 * 由 NavRail 提供左栏容器与窄屏抽屉态。 */
export function ConversationPane({ onCreate }: Props) {
  const navigate = useNavigate()
  const conversations = useMate((s) => s.conversations)
  const agents = useMate((s) => s.agents)
  const views = useMate((s) => s.views)
  const activeId = useMate((s) => s.activeId)
  const railMode = useMate((s) => s.railMode)
  const setDrawer = useMate((s) => s.setDrawer)
  const togglePin = useMate((s) => s.togglePin)
  const archiveConversation = useMate((s) => s.archiveConversation)
  const renameConversation = useMate((s) => s.renameConversation)
  const setRailMode = useMate((s) => s.setRailMode)
  const jumpToAnchor = useMate((s) => s.jumpToAnchor)
  const showToast = useMate((s) => s.showToast)

  // 行内「重命名」弹窗
  const [renaming, setRenaming] = useState<{ id: string; init: string } | null>(null)
  const [draft, setDraft] = useState('')

  // 会话搜索：后端扫 transcript（标题/Agent/用户消息正文；含归档），250ms 防抖。
  const [searchQ, setSearchQ] = useState('')
  const [hits, setHits] = useState<ConversationSearchHit[] | null>(null) // null = 未搜索
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
      Promise.allSettled([conversationSearch(q), messageSearch(q)])
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

  // 列表分页（P3 余留）：默认渲染一页，超出的以「显示更早」增量展开；切视图/搜索时复位。
  const [shown, setShown] = useState(RAIL_PAGE)
  useEffect(() => setShown(RAIL_PAGE), [railMode])
  const railPage = pageConversations(conversations, shown, activeId)

  const openConversation = (id: string) => {
    setDrawer(false)
    navigate(`/conversations/${id}`)
  }

  const submitRename = () => {
    if (!renaming) return
    const t = draft.trim()
    const target = renaming
    setRenaming(null)
    if (t && t !== target.init) void renameConversation(target.id, t)
  }

  const openHit = (h: ConversationSearchHit) => {
    setSearchQ('')
    setHits(null)
    setMsgHits(null)
    openConversation(h.id)
  }

  // 消息命中点击：跳转到该会话并带上 seq 锚点（ChatPage 负责滚动定位与高亮）。
  const openMessageHit = (h: MessageSearchHit) => {
    setSearchQ('')
    setHits(null)
    setMsgHits(null)
    setDrawer(false)
    jumpToAnchor(h.conversation_id, h.seq)
    navigate(`/conversations/${h.conversation_id}`)
  }

  // 导出当前视图全部会话（markdown）：多文件逐个下载（浏览器无目录写权限，逐个触发最稳）。
  const exportVisible = async () => {
    const ids = (railPage.visible ?? []).map((c) => c.id)
    if (ids.length === 0) return
    setExportBusy(true)
    try {
      const resp = await exportConversations(ids, 'markdown')
      for (const f of resp.files) {
        const blob = new Blob([f.content], { type: 'text/markdown;charset=utf-8' })
        const url = URL.createObjectURL(blob)
        const a = document.createElement('a')
        a.href = url
        a.download = exportDownloadName(f.filename, f.conversation_id, f.format)
        document.body.appendChild(a)
        a.click()
        a.remove()
        URL.revokeObjectURL(url)
      }
      showToast(
        exportSummary(resp.exported, resp.total_bytes, resp.skipped ?? [], resp.files.filter((f) => f.truncated).length),
      )
    } catch (e) {
      showToast(`导出失败：${toErrorMessage(e)}`)
    } finally {
      setExportBusy(false)
    }
  }

  // 单会话导出（行内菜单）：markdown 单文件下载。
  const exportOne = async (id: string, title: string) => {
    setExportBusy(true)
    try {
      const resp = await exportConversations([id], 'markdown')
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
      key={`${h.conversation_id}#${h.seq}`}
      type="button"
      onClick={() => openMessageHit(h)}
      title={`${h.title || h.conversation_id} · seq ${h.seq}`}
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
          {h.title || h.conversation_id}
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

  /** 搜索结果行：标题 + 命中片段 + Agent/归档/命中数。 */
  const renderHit = (h: ConversationSearchHit) => (
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
        {h.title || h.agent_id}
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
          {agentNameOf(agents, h.agent_id)}
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

  const renderItem = (sm: ConversationSummary) => {
    const view = views[sm.id]
    const meta = conversationStatusMeta(sm.status)
    const running = sm.status === 'in_progress'
    const on = sm.id === activeId

    // 行内「更多」：重命名 / 置顶·取消置顶 / 归档（或恢复）
    const menuItems: MenuProps['items'] = [
      {
        key: 'rename',
        icon: <EditOutlined />,
        label: '重命名',
        onClick: () => {
          setDraft(sm.title || titleFor(sm, view, agents))
          setRenaming({ id: sm.id, init: sm.title || titleFor(sm, view, agents) })
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
      { key: 'export', icon: <DownloadOutlined />, label: '导出 Markdown' },
    ]
    const onMenu: MenuProps['onClick'] = ({ key }) => {
      if (key === 'pin' || key === 'unpin') void togglePin(sm.id)
      else if (key === 'archive') void archiveConversation(sm.id, true)
      else if (key === 'restore') void archiveConversation(sm.id, false)
      else if (key === 'export') void exportOne(sm.id, sm.title || titleFor(sm, view, agents))
    }

    return (
      <div
        key={sm.id}
        className={`fg-sess-item${on ? ' fg-on' : ''}`}
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 2,
          width: '100%',
          borderRadius: 10,
          padding: '4px 4px 4px 12px',
          marginBottom: 2,
          border: '1px solid transparent',
          color: tokens.text,
        }}
      >
        <button
          type="button"
          role="listitem"
          disabled={busy && !on}
          onClick={() => openConversation(sm.id)}
          style={{
            flex: 1,
            minWidth: 0,
            textAlign: 'left',
            background: 'transparent',
            border: 0,
            padding: '5px 0',
            color: 'inherit',
            cursor: busy && !on ? 'not-allowed' : 'pointer',
          }}
        >
          <span
            style={{
              display: '-webkit-box',
              WebkitLineClamp: 2,
              WebkitBoxOrient: 'vertical',
              overflow: 'hidden',
              fontSize: 13.5,
              fontWeight: 560,
              letterSpacing: '-0.005em',
              lineHeight: 1.4,
              color: sm.archived ? tokens.textSecondary : tokens.text,
            }}
          >
            {titleFor(sm, view, agents)}
          </span>
          <span
            style={{
              marginTop: 7,
              display: 'flex',
              alignItems: 'center',
              gap: 7,
              minWidth: 0,
              fontFamily: tokens.fontMono,
              fontSize: 10.5,
              color: tokens.textSecondary,
            }}
          >
            <span
              title={sm.agent_id}
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: 5,
                border: `1px solid ${tokens.border}`,
                borderRadius: 999,
                padding: '1px 7px',
                color: tokens.text,
                fontWeight: 500,
                whiteSpace: 'nowrap',
                maxWidth: 150,
                overflow: 'hidden',
                textOverflow: 'ellipsis',
              }}
            >
              {agentNameOf(agents, sm.agent_id)}
            </span>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5, whiteSpace: 'nowrap', minWidth: 0 }}>
              <Dot color={meta.color} pulse={running} size={6} />
              <span style={{ color: running ? tokens.text : tokens.textSecondary }}>{meta.label}</span>
            </span>
            {/* 来源标记：由定时任务触发的会话（`origin=automation:<id>`）——人工与会话流混在一起时，
                "这条是谁开的"必须一眼可见，而不是靠时间去猜。 */}
            {isAutomationOrigin(sm.origin) && (
              <span
                title={`由定时任务 ${automationIDFromOrigin(sm.origin)} 触发`}
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 4,
                  border: `1px solid ${tokens.border}`,
                  borderRadius: 999,
                  padding: '1px 7px',
                  color: tokens.primary,
                  whiteSpace: 'nowrap',
                  flex: 'none',
                }}
              >
                ⏱ 定时
              </span>
            )}
            <span
              style={{ marginLeft: 'auto', flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 6 }}
              title={sm.updated_at}
            >
              <span>{relTime(sm.updated_at)}</span>
              {view && view.lastSeq > 0 && <span style={{ color: tokens.textPlaceholder }}>seq {view.lastSeq}</span>}
            </span>
          </span>
        </button>
        <button
          type="button"
          title={sm.pinned ? '取消置顶' : '置顶'}
          aria-label={sm.pinned ? '取消置顶' : '置顶'}
          onClick={() => void togglePin(sm.id)}
          style={{
            flex: 'none',
            display: 'inline-grid',
            placeItems: 'center',
            width: 26,
            height: 26,
            border: 0,
            borderRadius: 8,
            background: 'transparent',
            color: sm.pinned ? tokens.primary : tokens.textPlaceholder,
            cursor: 'pointer',
          }}
        >
          {sm.pinned ? <PushpinFilled style={{ fontSize: 13 }} /> : <PushpinOutlined style={{ fontSize: 13 }} />}
        </button>
        <Dropdown menu={{ items: menuItems, onClick: onMenu }} trigger={['click']} placement="bottomRight">
          <button
            type="button"
            className={`fg-sess-more${on ? ' fg-keep' : ''}`}
            title="更多操作"
            aria-label="更多操作"
            onClick={(ev) => ev.stopPropagation()}
            style={{
              flex: 'none',
              display: 'inline-grid',
              placeItems: 'center',
              width: 26,
              height: 26,
              border: 0,
              borderRadius: 8,
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
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 8,
          padding: '12px 12px 8px',
        }}
      >
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 2, padding: 2, borderRadius: 10 }}>
          <ViewTab active={!archivedView} onClick={() => setRailMode('active')}>
            会话
          </ViewTab>
          <ViewTab active={archivedView} onClick={() => setRailMode('archived')}>
            归档
          </ViewTab>
        </span>
        <Button
          size="small"
          icon={<DownloadOutlined />}
          loading={exportBusy}
          disabled={railPage.visible.length === 0}
          onClick={() => void exportVisible()}
          title="导出当前列表全部会话（Markdown，逐个下载）"
          style={{ borderRadius: 8 }}
        >
          导出
        </Button>
        {!archivedView && (
          <Button size="small" icon={<PlusOutlined />} onClick={onCreate} style={{ borderRadius: 8 }}>
            新建会话
          </Button>
        )}
      </div>

      <div style={{ flex: 'none', padding: '0 10px 4px' }}>
        <Input
          allowClear
          value={searchQ}
          onChange={(e) => setSearchQ(e.target.value)}
          prefix={<SearchOutlined style={{ fontSize: 12, color: tokens.textPlaceholder }} />}
          placeholder="搜索会话与消息（点消息命中可跳转定位）"
          aria-label="搜索会话"
          style={{
            borderRadius: 8,
            fontSize: 12.5,
            background: tokens.softFill,
            border: '1px solid transparent',
            height: 30,
          }}
          variant="filled"
        />
      </div>

      <div style={{ flex: 1, overflowY: 'auto', padding: '4px 8px 12px' }} role="list">
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
        ) : conversations.length === 0 ? (
          <div style={{ padding: '18px 12px', color: tokens.textPlaceholder, fontSize: 12.5, lineHeight: 1.7 }}>
            {archivedView
              ? '暂无归档会话。在「会话」里点会话行的 ⋯ →「归档」，即可把会话移到这里（数据保留，可恢复）。'
              : '暂无会话。点击右上「新建会话」，选择 Agent 并发起首轮问答。'}
          </div>
        ) : (
          <>
            {groupConversations(railPage.visible).map((g) => (
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
