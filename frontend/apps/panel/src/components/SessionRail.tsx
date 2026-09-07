import { Button } from 'antd'
import { PlusOutlined, PushpinFilled, PushpinOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { tokens } from '@ark/ui'
import { Dot, sessionStatusMeta } from '@ark/ui'
import type { SessionSummary } from '@ark/types'
import { useForge } from '../store'
import { agentNameOf, titleFor } from '../console/util'

interface Props {
  onCreate: () => void
}

/** 置顶组 + 按创建日分组的未置顶会话（轻量时间线管理面）。 */
function groupSessions(all: SessionSummary[]): Array<{ label: string; items: SessionSummary[]; pinned: boolean }> {
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

/** 会话侧栏（console.html .side）：新建入口 + 会话列表（分组/置顶）+ 底座 foot。 */
export function SessionRail({ onCreate }: Props) {
  const navigate = useNavigate()
  const sessions = useForge((s) => s.sessions)
  const agents = useForge((s) => s.agents)
  const runtimes = useForge((s) => s.runtimes)
  const views = useForge((s) => s.views)
  const activeId = useForge((s) => s.activeId)
  const drawerOpen = useForge((s) => s.drawerOpen)
  const setDrawer = useForge((s) => s.setDrawer)
  const togglePin = useForge((s) => s.togglePin)

  const busy = activeId ? (views[activeId]?.streaming ?? false) : false

  const openSession = (id: string) => {
    setDrawer(false)
    navigate(`/sessions/${id}`)
  }

  const renderItem = (sm: SessionSummary) => {
    const view = views[sm.id]
    const meta = sessionStatusMeta(sm.status)
    const running = sm.status === 'in_progress'
    const on = sm.id === activeId
    return (
      <div
        key={sm.id}
        className={`fg-sess-item${on ? ' fg-on' : ''}`}
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 4,
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
          onClick={() => openSession(sm.id)}
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
              color: tokens.text,
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
            <span style={{ marginLeft: 'auto', flex: 'none' }}>
              {view && view.lastSeq > 0 ? `seq ${view.lastSeq}` : ''}
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
      </div>
    )
  }

  return (
    <aside className={`fg-rail${drawerOpen ? ' fg-open' : ''}`} aria-label="会话列表">
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 10,
          padding: '14px 14px 10px',
        }}
      >
        <span style={{ fontSize: 13, fontWeight: 660, letterSpacing: '-0.01em' }}>会话</span>
        <Button size="small" icon={<PlusOutlined />} onClick={onCreate}>
          新建会话
        </Button>
      </div>

      <div style={{ flex: 1, overflowY: 'auto', padding: '4px 8px 12px' }} role="list">
        {sessions.length === 0 && (
          <div style={{ padding: '18px 12px', color: tokens.textPlaceholder, fontSize: 12.5, lineHeight: 1.7 }}>
            暂无会话。点击右上「新建会话」，选择 Agent 底座并发起首轮问答。
          </div>
        )}
        {groupSessions(sessions).map((g) => (
          <div key={g.label} role="group" aria-label={g.label}>
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
              {g.label}
            </div>
            {g.items.map(renderItem)}
          </div>
        ))}
      </div>

      <div
        style={{
          flex: 'none',
          borderTop: `1px solid ${tokens.border}`,
          padding: '10px 14px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 8,
          fontFamily: tokens.fontMono,
          fontSize: 10.5,
          color: tokens.textSecondary,
        }}
      >
        {runtimes.length === 0 ? (
          <span>底座未知（服务不可达）</span>
        ) : (
          runtimes.map((rt) => (
            <span key={rt.name} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <Dot color={tokens.success} size={6} />
              {rt.name}
            </span>
          ))
        )}
      </div>
    </aside>
  )
}
