import { Button } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { tokens } from '@ark/ui'
import { Dot, sessionStatusMeta } from '@ark/ui'
import type { SessionSummary } from '@ark/types'
import { useForge } from '../store'
import { agentNameOf, titleFor } from '../console/util'

interface Props {
  onCreate: () => void
}

/** 会话侧栏（console.html .side）：新建入口 + 会话列表 + 底座 foot。 */
export function SessionRail({ onCreate }: Props) {
  const navigate = useNavigate()
  const sessions = useForge((s) => s.sessions)
  const agents = useForge((s) => s.agents)
  const runtimes = useForge((s) => s.runtimes)
  const views = useForge((s) => s.views)
  const activeId = useForge((s) => s.activeId)
  const drawerOpen = useForge((s) => s.drawerOpen)
  const setDrawer = useForge((s) => s.setDrawer)

  const busy = activeId ? (views[activeId]?.streaming ?? false) : false

  const openSession = (id: string) => {
    setDrawer(false)
    navigate(`/sessions/${id}`)
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
        {sessions.map((sm: SessionSummary) => {
          const view = views[sm.id]
          const meta = sessionStatusMeta(sm.status)
          const running = sm.status === 'in_progress'
          const on = sm.id === activeId
          return (
            <button
              key={sm.id}
              type="button"
              role="listitem"
              className={`fg-sess-item${on ? ' fg-on' : ''}`}
              disabled={busy && !on}
              onClick={() => openSession(sm.id)}
              style={{
                width: '100%',
                display: 'block',
                textAlign: 'left',
                borderRadius: 10,
                padding: '9px 10px 9px 12px',
                marginBottom: 2,
                border: '1px solid transparent',
                color: tokens.text,
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
                <span style={{ marginLeft: 'auto', flex: 'none' }}>{view && view.lastSeq > 0 ? `seq ${view.lastSeq}` : ''}</span>
              </span>
            </button>
          )
        })}
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
