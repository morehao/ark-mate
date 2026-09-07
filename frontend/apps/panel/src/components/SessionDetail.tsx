import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { Button, Drawer, Input, Spin } from 'antd'
import { CheckOutlined, CloseOutlined, EditOutlined, ReloadOutlined } from '@ant-design/icons'
import { tokens } from '@ark/ui'
import { Dot, Pill, sessionStatusMeta } from '@ark/ui'
import type { PermissionOptionKind, PermissionResp, SessionDetail } from '@ark/types'
import { getSession, toErrorMessage } from '@ark/api'
import { useForge } from '../store'
import { agentNameOf } from '../console/util'

function fmtLocal(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function optionId(p: PermissionResp, kind: PermissionOptionKind): string | undefined {
  return p.options.find((o) => o.kind === kind)?.option_id
}

/** 会话详情面板（管理面）：元数据只读一瞥 + 挂起权限列表（可就地决策）。数据来自 GET /sessions/:id。 */
export function SessionDetailDrawer({
  open,
  sessionId,
  onClose,
}: {
  open: boolean
  sessionId: string
  onClose: () => void
}) {
  const agents = useForge((s) => s.agents)
  const views = useForge((s) => s.views)
  const respond = useForge((s) => s.respondPermission)
  const renameSession = useForge((s) => s.renameSession)
  const togglePin = useForge((s) => s.togglePin)
  const archiveSession = useForge((s) => s.archiveSession)

  const [detail, setDetail] = useState<SessionDetail | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState('')

  const load = useCallback(async () => {
    if (!sessionId) return
    setLoading(true)
    setErr(null)
    try {
      setDetail(await getSession(sessionId))
    } catch (e) {
      setErr(toErrorMessage(e))
    } finally {
      setLoading(false)
    }
  }, [sessionId])

  useEffect(() => {
    if (open) {
      setEditingTitle(false)
      void load()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, sessionId])

  const view = views[sessionId]
  const viewTitle = detail?.title
  const smTitle =
    detail?.title ||
    view?.entries.find((e) => e.kind === 'user')?.text ||
    view?.entries.find((e) => e.kind === 'assistant')?.text ||
    sessionId
  const title = viewTitle || smTitle || sessionId

  const saveTitle = () => {
    const t = titleDraft.trim()
    setEditingTitle(false)
    if (t && t !== title) void renameSession(sessionId, t).then(() => load())
  }

  const act = (p: PermissionResp, kind: 'allow' | 'allow_always' | 'reject') => {
    const decision =
      kind === 'allow'
        ? { allowed: true, always: false, optionId: optionId(p, 'allow_once'), label: '允许本次' }
        : kind === 'allow_always'
          ? { allowed: true, always: true, optionId: optionId(p, 'allow_always'), label: '始终允许' }
          : { allowed: false, always: false, optionId: optionId(p, 'reject_once'), label: '拒绝' }
    void respond(p.call_id, decision).then(() => load())
  }

  const s = detail
  const meta = s ? sessionStatusMeta(s.status) : undefined

  const row = (label: string, value: ReactNode) => (
    <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, lineHeight: 1.9 }}>
      <span style={{ flex: 'none', width: 64, fontSize: 11.5, color: tokens.textPlaceholder }}>{label}</span>
      <span style={{ flex: 1, minWidth: 0, overflowWrap: 'anywhere', fontSize: 12.5, color: tokens.text }}>{value}</span>
    </div>
  )

  const actBtn = (label: string, kind: 'allow' | 'allow_always' | 'reject', p: PermissionResp) => (
    <Button
      key={label}
      size="small"
      type={kind === 'reject' ? 'default' : kind === 'allow_always' ? 'text' : 'primary'}
      icon={kind === 'reject' ? <CloseOutlined /> : <CheckOutlined />}
      onClick={() => act(p, kind)}
      style={{ borderRadius: 7, fontSize: 12 }}
    >
      {label}
    </Button>
  )

  return (
    <Drawer
      title="会话详情"
      width={360}
      open={open}
      onClose={onClose}
      styles={{ body: { padding: 0, background: tokens.bg } }}
      extra={
        <Button
          size="small"
          type="text"
          icon={<ReloadOutlined />}
          onClick={() => void load()}
          aria-label="刷新"
          title="刷新"
        />
      }
    >
      <div style={{ padding: '16px 20px 24px', display: 'flex', flexDirection: 'column', gap: 16 }}>
        {err && <div style={{ fontSize: 12.5, color: tokens.error }}>{err}</div>}
        {!s && loading && (
          <div style={{ display: 'grid', placeItems: 'center', padding: '32px 0' }}>
            <Spin size="small" />
          </div>
        )}
        {!s && !loading && !err && (
          <div style={{ fontSize: 12.5, color: tokens.textPlaceholder }}>暂无会话数据。</div>
        )}

        {s && (
          <>
            {/* 标题（行内改名） */}
            <div>
              {editingTitle ? (
                <div style={{ display: 'flex', gap: 6 }}>
                  <Input
                    value={titleDraft}
                    onChange={(e) => setTitleDraft(e.target.value)}
                    onPressEnter={saveTitle}
                    onBlur={saveTitle}
                    autoFocus
                    maxLength={80}
                    style={{ fontSize: 13 }}
                  />
                  <Button size="small" onClick={saveTitle}>
                    保存
                  </Button>
                </div>
              ) : (
                <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8 }}>
                  <div
                    style={{
                      flex: 1,
                      minWidth: 0,
                      fontSize: 14,
                      fontWeight: 650,
                      lineHeight: 1.4,
                      letterSpacing: '-0.01em',
                      overflowWrap: 'anywhere',
                    }}
                  >
                    {title}
                  </div>
                  <Button
                    size="small"
                    type="text"
                    icon={<EditOutlined />}
                    title="重命名"
                    aria-label="重命名"
                    onClick={() => {
                      setTitleDraft(title)
                      setEditingTitle(true)
                    }}
                  />
                </div>
              )}
            </div>

            {/* 操作行：置顶 / 归档（归档态为恢复） */}
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <Button size="small" onClick={() => void togglePin(s.id).then(() => load())} style={{ borderRadius: 8 }}>
                {s.pinned ? '取消置顶' : '置顶'}
              </Button>
              <Button size="small" onClick={() => void archiveSession(s.id, !s.archived).then(() => load())} style={{ borderRadius: 8 }}>
                {s.archived ? '从归档恢复' : '归档'}
              </Button>
            </div>

            {/* 元数据只读一瞥 */}
            <div
              style={{
                border: `1px solid ${tokens.border}`,
                borderRadius: 12,
                background: tokens.surface,
                padding: '12px 14px',
                display: 'flex',
                flexDirection: 'column',
                gap: 2,
              }}
            >
              {row('Agent', agentNameOf(agents, s.agent_id))}
              {row('Runtime', s.runtime)}
              {row(
                '状态',
                meta ? (
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                    <Dot color={meta.color} pulse={s.status === 'in_progress'} size={7} />
                    {meta.label}
                  </span>
                ) : (
                  s.status
                ),
              )}
              {row('会话 ID', <span style={{ fontFamily: tokens.fontMono, fontSize: 11.5 }}>{s.id}</span>)}
              {row('创建', <span style={{ fontFamily: tokens.fontMono, fontSize: 11.5 }}>{fmtLocal(s.created_at)}</span>)}
              {row('更新', <span style={{ fontFamily: tokens.fontMono, fontSize: 11.5 }}>{fmtLocal(s.updated_at)}</span>)}
              {row(
                '标记',
                <span style={{ display: 'inline-flex', gap: 5 }}>
                  {s.pinned && <Pill>置顶</Pill>}
                  {s.archived && <Pill>已归档</Pill>}
                  {!s.pinned && !s.archived && <span style={{ color: tokens.textPlaceholder }}>—</span>}
                </span>,
              )}
            </div>

            {/* 挂起权限（ACP 确认通道待决项，可就地决策） */}
            <div>
              <div
                style={{
                  marginBottom: 8,
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  fontSize: 12,
                  fontWeight: 620,
                  color: tokens.textSecondary,
                }}
              >
                <span>挂起权限</span>
                <span style={{ fontFamily: tokens.fontMono, fontSize: 11 }}>{(s.pending ?? []).length}</span>
              </div>
              {(s.pending ?? []).length === 0 ? (
                <div
                  style={{
                    border: `1px dashed ${tokens.border}`,
                    borderRadius: 10,
                    padding: '12px 14px',
                    fontSize: 12.5,
                    color: tokens.textPlaceholder,
                    lineHeight: 1.6,
                  }}
                >
                  无待确认的权限请求。agent 发起工具权限确认时会实时出现在这里与聊天流中。
                </div>
              ) : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                  {(s.pending ?? []).map((p) => (
                    <div
                      key={p.call_id}
                      style={{
                        border: `1px solid ${tokens.warning}44`,
                        background: `${tokens.warning}0d`,
                        borderRadius: 12,
                        padding: '10px 12px',
                      }}
                    >
                      <div style={{ fontSize: 12.5, fontWeight: 600, lineHeight: 1.4, overflowWrap: 'anywhere' }}>
                        {p.title || '权限请求'}
                      </div>
                      <div
                        style={{
                          marginTop: 3,
                          display: 'flex',
                          alignItems: 'center',
                          justifyContent: 'space-between',
                          gap: 8,
                          fontFamily: tokens.fontMono,
                          fontSize: 10.5,
                          color: tokens.textSecondary,
                        }}
                      >
                        <span>kind: {p.kind || '—'}</span>
                        <span>截止 {fmtLocal(p.timeout_at)}</span>
                      </div>
                      <div style={{ marginTop: 8, display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                        {actBtn('允许本次', 'allow', p)}
                        {actBtn('始终允许', 'allow_always', p)}
                        {actBtn('拒绝', 'reject', p)}
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </>
        )}
      </div>
    </Drawer>
  )
}
