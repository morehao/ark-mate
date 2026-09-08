import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Button, Dropdown } from 'antd'
import { InfoCircleOutlined, MoreOutlined, PlusOutlined } from '@ant-design/icons'
import { tokens } from '@ark/ui'
import { Pill } from '@ark/ui'
import { Ev } from '@ark/types'
import { conversationHistory } from '@ark/api'
import { useForge } from '../store'
import { ConversationRail } from '../components/ConversationRail'
import { ConversationDetailDrawer } from '../components/ConversationDetail'
import { Composer } from '../components/Composer'
import { StatusBar } from '../components/StatusBar'
import { EntryView } from '../components/Transcript'
import { agentOf, agentNameOf, titleFor } from '../console/util'

/** 问答控制台主视图：会话侧栏 + 聊天（head / transcript / composer）+ 状态条。 */
export function ChatPage() {
  const navigate = useNavigate()
  const params = useParams<{ id: string }>()
  const paramId = params.id ?? null

  const agents = useForge((s) => s.agents)
  const conversations = useForge((s) => s.conversations)
  const views = useForge((s) => s.views)
  const activeId = useForge((s) => s.activeId)
  const drawerOpen = useForge((s) => s.drawerOpen)
  const activate = useForge((s) => s.activate)
  const expireHydration = useForge((s) => s.expireHydration)
  const sendPrompt = useForge((s) => s.sendPrompt)
  const cancel = useForge((s) => s.cancel)
  const archiveConversation = useForge((s) => s.archiveConversation)
  const showToast = useForge((s) => s.showToast)
  const setDrawer = useForge((s) => s.setDrawer)

  const scrollRef = useRef<HTMLDivElement | null>(null)
  const lastParamRef = useRef<string | null | undefined>(undefined)
  const [detailOpen, setDetailOpen] = useState(false)
  // err 轮重试文本回退（会话 id → 历史最后 user_message；undefined=未取）
  const [errFallback, setErrFallback] = useState<Record<string, string | undefined>>({})

  // URL → 激活（刷新/直达 /conversations/:id 续看；参数不变不重复 attach）
  useEffect(() => {
    if (lastParamRef.current === paramId) return
    lastParamRef.current = paramId
    if (paramId) activate(paramId)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paramId])

  const view = activeId ? views[activeId] : undefined
  const activeSum = activeId ? conversations.find((x) => x.id === activeId) : undefined
  const streaming = view?.streaming ?? false
  const hydrating = view?.hydrating ?? false
  // 挂起交互请求条数（权限确认 + 结构化提问的待决项；聊天头角标）
  const pendingPerms =
    activeId && view ? view.entries.filter((en) => (en.kind === 'permission' || en.kind === 'elicitation') && !en.resolved).length : 0
  // 服务端忙水位：本地无流（streaming=false）但会话在服务端仍 in_progress 且本地视图没有终态条目
  // （如刷新/重启后该 turn 仍在跑或已卡住）→ 视为 busy：展示「停止」以自愈（后端 cancel 幂等解 busy）。
  // 一旦本地已收到终态条目（tone ok/err/cancel 的 system 尾条），即使列表 status 还没刷新也不当 busy。
  const lastSettledTone = (() => {
    if (!view) return undefined
    for (let i = view.entries.length - 1; i >= 0; i--) {
      const en = view.entries[i]
      if (en.kind === 'system') return en.tone
      if (en.kind !== 'tool' && en.kind !== 'thought') break // 越过尾部流式/工具残留即停
    }
    return undefined
  })()
  const serverBusy =
    activeSum?.status === 'in_progress' && !streaming && lastSettledTone !== 'ok' && lastSettledTone !== 'err' && lastSettledTone !== 'cancel'
  const busy = streaming || serverBusy
  const agent = agentOf(agents, activeSum?.agent_id)
  const title = activeId ? titleFor(activeSum, view, agents) : ''
  // 失败 turn 一键重试：仅当最新一条为 turn.error（无流/非 busy）时，取其所属轮的用户输入重发。
  // 视图可能是服务端快照的部分重放（该轮 user 气泡不在本地条目里），此时回退到历史事件取输入。
  const entries = view?.entries ?? []
  const lastEntry = entries[entries.length - 1]
  const hasErrTail = !!(!busy && lastEntry && lastEntry.kind === 'system' && lastEntry.tone === 'err')
  const retryLocalText = (() => {
    if (!hasErrTail) return null
    for (let i = entries.length - 1; i >= 0; i--) {
      const en = entries[i]
      if (en === lastEntry) continue
      if (en.kind === 'system') return null
      if (en.kind === 'user' && typeof en.text === 'string' && en.text.trim()) return en.text
    }
    return null
  })()
  const errFallbackText = activeId ? errFallback[activeId] : undefined
  const retryText = hasErrTail ? retryLocalText ?? (errFallbackText || undefined) : undefined
  const retryLast = retryText ? () => void sendPrompt(retryText) : undefined

  // 本地无该轮输入（部分重放/冷挂载后直接失败）时，从历史取最后一条 user_message 作为重试文本。
  useEffect(() => {
    const id = activeId
    if (!id || !hasErrTail || retryLocalText !== null || errFallback[id] !== undefined) return
    let dead = false
    void (async () => {
      let txt = ''
      try {
        const page = await conversationHistory(id, { limit: 30 })
        const last = [...page.events].reverse().find((ev) => ev.type === Ev.UserMessage)
        if (last && typeof last.payload?.text === 'string') txt = last.payload.text
      } catch {
        txt = ''
      }
      if (!dead) setErrFallback((p) => (p[id] !== undefined ? p : { ...p, [id]: txt }))
    })()
    return () => {
      dead = true
    }
  }, [activeId, hasErrTail, retryLocalText, errFallback])
  // P3 加载四态：hydration 超时兜底——attach 后长时间无 snapshot/事件（连接被熔断重连中、
  // 服务端无响应）→ 退出加载态回空态（stalled 保险丝随后会触发重连恢复）。
  useEffect(() => {
    const id = activeId
    if (!id || !hydrating) return
    const t = window.setTimeout(() => expireHydration(id), 12000)
    return () => window.clearTimeout(t)
  }, [activeId, hydrating, expireHydration])

  // 事件增长时若已近底部则跟随滚动（避免打扰上翻阅读）
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80
    if (nearBottom) el.scrollTop = el.scrollHeight
  }, [view?.entries, view?.streaming, view?.lastSeq])

  const onSend = (text: string) => {
    if (!activeId) return
    void sendPrompt(text)
  }

  // 软清空：归档当前会话（数据保留，可在「归档」视图恢复）→ 跳 /new 并预选同 Agent 开新会话。
  const softClear = async () => {
    if (!activeId || !activeSum?.agent_id) return
    const agentId = activeSum.agent_id
    try {
      await archiveConversation(activeId, true)
      showToast('当前会话已归档，可随时从「归档」恢复')
      navigate(`/new?agent=${encodeURIComponent(agentId)}`)
    } catch {
      /* archiveConversation 内部已 toast 错误 */
    }
  }

  const headMenu = {
    items: [{ key: 'soft-clear', label: '归档并新建会话（同 Agent）', title: '保留数据到归档，另开同 Agent 新会话' }],
    onClick: (info: { key: string }) => {
      if (info.key === 'soft-clear') void softClear()
    },
  }

  const renderChatHead = () => {
    if (!activeId) return null
    const seq = view?.lastSeq ?? 0
    const metaText = streaming
      ? '进行中'
      : `${activeSum ? statusLabel(activeSum.status) : '未知状态'}${seq > 0 ? ` · seq ${seq}` : ''}`
    return (
      <div
        style={{
          flex: 'none',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 12,
          flexWrap: 'wrap',
          padding: '10px 20px',
          borderBottom: `1px solid ${tokens.border}`,
          background: tokens.surface,
        }}
      >
        <div style={{ minWidth: 0 }}>
          <div style={{ fontSize: 14.5, fontWeight: 650, letterSpacing: '-0.01em', lineHeight: 1.35, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={title}>
            {title}
          </div>
          <div
            style={{
              marginTop: 4,
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              flexWrap: 'wrap',
              minWidth: 0,
              fontFamily: tokens.fontMono,
              fontSize: 10.5,
              color: tokens.textSecondary,
            }}
          >
            <Pill maxWidth={180}>{agent ? agent.name : agentNameOf(agents, activeSum?.agent_id)}</Pill>
            <span style={{ maxWidth: 220, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={activeId}>
              {activeId}
            </span>
          </div>
        </div>
        <div style={{ display: 'inline-flex', alignItems: 'center', gap: 10, flex: 'none' }}>
          <Dropdown menu={headMenu} trigger={['click']} placement="bottomRight">
            <Button
              size="small"
              type="text"
              icon={<MoreOutlined />}
              title="会话操作"
              aria-label="会话操作"
              style={{ borderRadius: 8, color: tokens.textSecondary }}
            />
          </Dropdown>
          <Button
            size="small"
            type="text"
            icon={<InfoCircleOutlined />}
            title="会话详情"
            aria-label="会话详情"
            onClick={() => setDetailOpen(true)}
            style={{ borderRadius: 8, color: tokens.textSecondary }}
          />
          {pendingPerms > 0 && (
            <span
              title={`${pendingPerms} 条待确认请求（权限/提问）`}
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: 5,
                border: `1px solid ${tokens.warning}`,
                background: `${tokens.warning}14`,
                color: tokens.warning,
                borderRadius: 999,
                padding: '2px 9px',
                fontFamily: tokens.fontMono,
                fontSize: 11,
                fontWeight: 600,
              }}
            >
              <svg viewBox="0 0 24 24" width="11" height="11" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
                <path d="M12 9v4M12 17h.01M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" />
              </svg>
              待确认 {pendingPerms}
            </span>
          )}
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
            {streaming && (
              <span
                className="fg-anim-pulse"
                style={{ width: 7, height: 7, borderRadius: '50%', background: tokens.primary, display: 'inline-block' }}
              />
            )}
            <span style={{ color: streaming ? tokens.text : tokens.textSecondary, fontWeight: streaming ? 600 : 400 }}>{metaText}</span>
          </span>
          {busy && (
            <Button size="small" onClick={() => void cancel()} style={{ borderRadius: 8 }}>
              停止
            </Button>
          )}
        </div>
      </div>
    )
  }

  const renderEmptyPick = () => (
    <div
      style={{
        flex: 1,
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        textAlign: 'center',
        gap: 12,
        padding: 24,
        minHeight: 0,
        background: tokens.bg,
      }}
    >
      <div
        style={{
          width: 48,
          height: 48,
          borderRadius: 14,
          border: `1px solid ${tokens.border}`,
          background: tokens.surface,
          display: 'grid',
          placeItems: 'center',
          color: tokens.text,
        }}
      >
        <svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true">
          <path d="M21 15a2 2 0 0 1-2 2H8l-5 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v10z" />
        </svg>
      </div>
      <div style={{ fontSize: 19, fontWeight: 650, letterSpacing: '-0.01em' }}>开始问答</div>
      <p style={{ margin: 0, maxWidth: '46ch', color: tokens.textSecondary, fontSize: 13.5, lineHeight: 1.7 }}>
        会话绑定唯一的 Agent。从左侧选择一个会话续看，或新建一个会话。
      </p>
      <Button type="primary" icon={<PlusOutlined />} onClick={() => navigate('/new')} style={{ marginTop: 4 }}>
        新建会话
      </Button>
    </div>
  )

  const renderEmptyTranscript = () => {
    const a = agent
    const agentDesc = a ? `${a.name}` : activeId ?? '该会话'
    return (
      <div
        style={{
          padding: '12vh 24px 0',
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          textAlign: 'center',
          gap: 10,
        }}
      >
        <div
          style={{
            width: 44,
            height: 44,
            borderRadius: 12,
            border: `1px solid ${tokens.border}`,
            background: tokens.surface,
            display: 'grid',
            placeItems: 'center',
            color: tokens.text,
          }}
        >
          <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true">
            <path d="M21 15a2 2 0 0 1-2 2H8l-5 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v10z" />
          </svg>
        </div>
        <h1 style={{ margin: '4px 0 0', fontSize: 19, fontWeight: 650, letterSpacing: '-0.01em' }}>问点什么？</h1>
        <p style={{ margin: 0, maxWidth: '46ch', color: tokens.textSecondary, fontSize: 13.5, lineHeight: 1.7 }}>
          当前会话绑定{' '}
          <b style={{ fontFamily: tokens.fontMono, fontWeight: 600, color: tokens.text }}>{agentDesc}</b>
          。在下方输入问题，回复将以流式事件渲染：思考、工具调用与正文实时出现，可随时停止。
        </p>
      </div>
    )
  }

  return (
    <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', background: tokens.bg }}>
      <div style={{ flex: 1, minHeight: 0, display: 'flex', position: 'relative' }}>
        <ConversationRail onCreate={() => navigate('/new')} />
        {drawerOpen && (
          <div
            className="fg-scrim-mobile"
            style={{ position: 'fixed', inset: 0, background: 'rgba(15, 23, 42, 0.34)', zIndex: 35 }}
            onClick={() => setDrawer(false)}
          />
        )}

        <section style={{ flex: 1, display: 'flex', flexDirection: 'column', minWidth: 0, minHeight: 0, background: tokens.bg }}>
          {!activeId ? (
            renderEmptyPick()
          ) : (
            <>
              {renderChatHead()}
              <div ref={scrollRef} style={{ flex: 1, overflowY: 'auto', minHeight: 0, scrollBehavior: 'smooth' }}>
                <div
                  style={{
                    maxWidth: 840,
                    margin: '0 auto',
                    padding: '28px 24px 12px',
                    display: 'flex',
                    flexDirection: 'column',
                    gap: 20,
                    boxSizing: 'border-box',
                  }}
                >
                  {(view?.entries.length ?? 0) === 0 ? (
                    hydrating ? (
                      <div style={{ paddingTop: 56, textAlign: 'center', color: tokens.textSecondary, fontSize: 13 }}>
                        连接会话…
                        <span className="fg-anim-blink" style={{ display: 'inline-block', marginLeft: 6, width: 2, height: '1em', background: tokens.primary, verticalAlign: '-0.12em' }} />
                      </div>
                    ) : (
                      renderEmptyTranscript()
                    )
                  ) : (
                    <>
                      {view?.entries.map((e) => (
                        <EntryView key={e.id} e={e} onRetry={e === lastEntry ? retryLast : undefined} />
                      ))}
                      {streaming && (
                        <div style={{ display: 'flex', alignItems: 'center', gap: 2, color: tokens.text }}>
                          <span
                            className="fg-anim-blink"
                            style={{
                              display: 'inline-block',
                              width: 2,
                              height: '1em',
                              background: tokens.primary,
                              verticalAlign: '-0.12em',
                              marginLeft: 1,
                            }}
                          />
                        </div>
                      )}
                    </>
                  )}
                </div>
              </div>
              {activeId && (
                <Composer
                  busy={busy}
                  busyNote={
                    streaming
                      ? `turn 进行中 · ${agent ? agent.name : ''}`
                      : serverBusy
                        ? '会话在服务端仍处运行中 — 可点「停止」解除'
                        : undefined
                  }
                  placeholder={
                    busy ? 'agent 正在回复…' : `向 ${agent ? agent.name : '当前 Agent'} 提问…（Enter 发送）`
                  }
                  onSend={onSend}
                />
              )}
            </>
          )}
        </section>
      </div>
      <StatusBar />
      {activeId && (
        <ConversationDetailDrawer
          open={detailOpen}
          conversationId={activeId}
          onClose={() => setDetailOpen(false)}
        />
      )}
    </div>
  )
}

function statusLabel(status: string): string {
  const map: Record<string, string> = {
    in_progress: '进行中',
    completed: '已完成',
    cancelled: '已取消',
    failed: '失败',
  }
  return map[status] ?? status
}
