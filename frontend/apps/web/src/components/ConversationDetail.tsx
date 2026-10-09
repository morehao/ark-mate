import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { Button, Drawer, Input, Popconfirm, Spin, Tag } from 'antd'
import {
  CheckOutlined,
  CloseOutlined,
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  ReloadOutlined,
} from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { Dot, Pill, conversationStatusMeta } from '@ark-mate/ui'
import type { PermissionMemory, PermissionOptionKind, PermissionResp, ConversationDetail } from '@ark-mate/types'
import { modelValues } from '@ark-mate/types'
import { getConversation, promptConversation, revokeMemory, conversationHistory, toErrorMessage } from '@ark-mate/api'
import { useMate } from '../store'
import { pinnedCapabilitiesView } from '../console/capabilityView'
import { ModelSelect } from './ModelSelect'
import { agentNameOf, shortId } from '../console/util'
import { copyText } from '../console/clipboard'
import { summarizeRun, type FailedTurn, type RunSummary } from '../console/runSummary'

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

/** 会话详情面板（管理面）：元数据只读一瞥 + 挂起权限列表（可就地决策）。数据来自 GET /conversations/:id。 */
export function ConversationDetailDrawer({
  open,
  conversationId,
  onClose,
}: {
  open: boolean
  conversationId: string
  onClose: () => void
}) {
  const agents = useMate((s) => s.agents)
  const views = useMate((s) => s.views)
  const respond = useMate((s) => s.respondPermission)
  const renameConversation = useMate((s) => s.renameConversation)
  const togglePin = useMate((s) => s.togglePin)
  const archiveConversation = useMate((s) => s.archiveConversation)
  const showToast = useMate((s) => s.showToast)
  const modelCatalog = useMate((s) => s.modelCatalog)
  const loadModelCatalog = useMate((s) => s.loadModelCatalog)
  const updateConversationModel = useMate((s) => s.updateConversationModel)

  const [detail, setDetail] = useState<ConversationDetail | null>(null)
  const [run, setRun] = useState<RunSummary | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState('')

  const load = useCallback(async () => {
    if (!conversationId) return
    setLoading(true)
    setErr(null)
    try {
      setDetail(await getConversation(conversationId))
    } catch (e) {
      setErr(toErrorMessage(e))
    }
    // 运行统计/失败轮由历史推导；读不到不阻塞主数据。
    try {
      const page = await conversationHistory(conversationId, { limit: 500 })
      setRun(summarizeRun(page.events))
    } catch {
      setRun(null)
    } finally {
      setLoading(false)
    }
  }, [conversationId])

  useEffect(() => {
    if (open) {
      setEditingTitle(false)
      void load()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, conversationId])

  // 会话级模型目录：**按会话绑定的 agent 取样**（不是「当前 Agent」）——会话与 Agent 一对一绑定，
  // 模型候选只能来自该会话那个底座（dsh 会话级可改 / opencode 会话级 set_config_option），
  // 取错 agent 会列出别的底座的模型（实测 dsh 与 opencode 目录完全不同），选下去必被运行时拒绝；
  // 且目录的 agent 必须与会话一致，运行时推送的 session.config_options 才会被 store 认领刷新。
  const detailAgentId = detail?.agent_id
  useEffect(() => {
    if (open && detailAgentId) void loadModelCatalog(detailAgentId)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, detailAgentId])

  const view = views[conversationId]
  const viewTitle = detail?.title
  const smTitle =
    detail?.title ||
    view?.entries.find((e) => e.kind === 'user')?.text ||
    view?.entries.find((e) => e.kind === 'assistant')?.text ||
    conversationId
  const title = viewTitle || smTitle || conversationId

  const saveTitle = () => {
    const t = titleDraft.trim()
    setEditingTitle(false)
    if (t && t !== title) void renameConversation(conversationId, t).then(() => load())
  }

  const act = (p: PermissionResp, kind: 'allow' | 'allow_always' | 'reject') => {
    const decision =
      kind === 'allow'
        ? { allowed: true, always: false, optionId: optionId(p, 'allow_once'), label: '允许本次' }
        : kind === 'allow_always'
          ? { allowed: true, always: true, optionId: optionId(p, 'allow_always'), label: '始终允许' }
          : { allowed: false, always: false, optionId: optionId(p, 'reject_once') ?? optionId(p, 'reject_always'), label: '拒绝' }
    void respond(p.call_id, decision).then(() => load())
  }

  // —— 会话级权限记忆（始终允许 / 始终拒绝，对称）：撤销（单条 / 全部）后刷新 ——
  const revokeOne = (item: PermissionMemory) => {
    void revokeMemory(conversationId, { title: item.title, kind: item.kind })
      .then(() => load())
      .catch((e) => setErr(toErrorMessage(e)))
  }
  const revokeAll = () => {
    void revokeMemory(conversationId, {})
      .then(() => load())
      .catch((e) => setErr(toErrorMessage(e)))
  }

  // —— 失败轮一键重试：重发该轮用户输入（事件经 WS 续流），随后刷新统计 ——
  const retryFailedTurn = (ft: FailedTurn) => {
    if (!ft.text) return
    void promptConversation(conversationId, { prompt: ft.text })
      .then(() => {
        showToast('已重发该轮输入，结果将实时流出…')
        window.setTimeout(() => void load(), 1500)
      })
      .catch((e) => setErr(toErrorMessage(e)))
  }

  const s = detail
  const meta = s ? conversationStatusMeta(s.status) : undefined

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
              <Button size="small" onClick={() => void archiveConversation(s.id, !s.archived).then(() => load())} style={{ borderRadius: 8 }}>
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
              {/* 工作空间（= 会话的 ACP cwd，也是原生权限 workspace-write 的沙箱边界）：
                  创建后不可变，故这里只读展示。「平台分配」标记来自服务端权威标志
                  workspace_temporary，不从路径形状猜。路径过长时中间省略，全文走 title。 */}
              {row(
                '在工作目录',
                s.cwd ? (
                  <span
                    style={{
                      display: 'inline-flex',
                      alignItems: 'center',
                      gap: 6,
                      minWidth: 0,
                      maxWidth: '100%',
                      fontFamily: tokens.fontMono,
                      fontSize: 11.5,
                    }}
                  >
                    <span
                      title={s.cwd}
                      style={{
                        minWidth: 0,
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                        direction: 'rtl', // 尾段（项目名）比前缀更重要
                        textAlign: 'left',
                      }}
                    >
                      {s.cwd}
                    </span>
                    {s.workspace_temporary && (
                      <Tag style={{ marginInlineEnd: 0, fontSize: 10, lineHeight: '16px' }}>平台分配</Tag>
                    )}
                  </span>
                ) : (
                  '未记录（早于工作空间落库的会话）'
                ),
              )}
              {(() => {
                // 是否可切换**由运行时说了算**：目录里有 category=model 候选才显示选择器
                // （不再按 runtime 名硬编码——opencode 同样支持会话级 set_config_option）。
                const modelOptions = modelValues(modelCatalog ?? undefined)
                if (modelOptions.length === 0) {
                  // 运行时没给出目录（未启动/未登录）：只如实说明，不展示不透明取值串。
                  return row('模型', s.model ? '已设置（当前无目录）' : '运行时默认')
                }
                // 会话上存的模型若不在当前目录（换过底座/档案），补一个只读项如实显示，
                // 避免 antd 回落到原始不透明取值串。
                const values =
                  s.model && !modelOptions.some((v) => v.value === s.model)
                    ? [{ value: s.model, name: '当前模型（不在目录中）' }, ...modelOptions]
                    : modelOptions
                return row(
                  '模型',
                  <ModelSelect
                    values={values}
                    value={s.model ?? null}
                    allowClear
                    size="small"
                    defaultLabel="运行时默认"
                    onChange={(v) => {
                      if ((v ?? undefined) === s.model) return
                      void updateConversationModel(s.id, v ?? undefined).then(() => void load())
                    }}
                  />,
                )
              })()}
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
              {row(
                '会话 ID',
                <span
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 4,
                    fontFamily: tokens.fontMono,
                    fontSize: 11.5,
                  }}
                >
                  <span title={s.id}>{shortId(s.id)}</span>
                  <Button
                    type="text"
                    size="small"
                    aria-label="复制会话 ID"
                    title="复制完整会话 ID"
                    icon={<CopyOutlined style={{ fontSize: 11 }} />}
                    onClick={() =>
                      void copyText(s.id).then((copied) =>
                        showToast(copied ? '已复制会话 ID' : '复制失败（浏览器未授权剪贴板）'),
                      )
                    }
                  />
                </span>,
              )}
              {row('创建', <span style={{ fontFamily: tokens.fontMono, fontSize: 11.5 }}>{fmtLocal(s.created_at)}</span>)}
              {row('更新', <span style={{ fontFamily: tokens.fontMono, fontSize: 11.5 }}>{fmtLocal(s.updated_at)}</span>)}
              {(() => {
                // 钉住的能力选定（M3）：展示的是**创建时钉住的快照**，不是"现在能用什么"——
                // 定义文件改了以后这个会话仍跑在原来那一代上，这个差别正是用户需要看见的。
                const pinned = pinnedCapabilitiesView(s.capabilities)
                if (!pinned) {
                  return row(
                    '能力',
                    <span style={{ color: tokens.textPlaceholder }} title="未声明会话级能力选定：按当前 Agent 的声明投放">
                      跟随默认
                    </span>,
                  )
                }
                return row(
                  '能力',
                  <span style={{ fontSize: 11.5 }} title={pinned.hint}>
                    {pinned.text}
                    {pinned.fingerprint && (
                      <span
                        style={{ marginLeft: 6, fontFamily: tokens.fontMono, fontSize: 10.5, color: tokens.textPlaceholder }}
                        title={`代际指纹 ${pinned.fingerprint}`}
                      >
                        #{pinned.fingerprint.slice(0, 8)}
                      </span>
                    )}
                  </span>,
                )
              })()}
              {row(
                '标记',
                <span style={{ display: 'inline-flex', gap: 5 }}>
                  {s.pinned && <Pill>置顶</Pill>}
                  {s.archived && <Pill>已归档</Pill>}
                  {!s.pinned && !s.archived && <span style={{ color: tokens.textPlaceholder }}>—</span>}
                </span>,
              )}
            </div>

            {/* 运行统计 + 失败轮（由历史推导；失败轮可就地一键重试） */}
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
                <span>运行统计</span>
                <span style={{ fontFamily: tokens.fontMono, fontSize: 11 }}>
                  {run ? `轮次 ${run.completed + run.failed + run.cancelled}` : '—'}
                </span>
              </div>
              <div
                style={{
                  border: `1px solid ${tokens.border}`,
                  borderRadius: 12,
                  background: tokens.surface,
                  padding: '12px 14px',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: 8,
                }}
              >
                {run ? (
                  <>
                    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                      {[
                        { label: '完成', n: run.completed, color: tokens.success },
                        { label: '失败', n: run.failed, color: tokens.error },
                        { label: '取消', n: run.cancelled, color: tokens.warning },
                        { label: '工具', n: run.tools, color: tokens.textSecondary },
                      ].map((st) => (
                        <span
                          key={st.label}
                          style={{
                            display: 'inline-flex',
                            alignItems: 'baseline',
                            gap: 4,
                            border: `1px solid ${tokens.border}`,
                            background: tokens.bg,
                            borderRadius: 999,
                            padding: '1px 9px',
                            fontFamily: tokens.fontMono,
                            fontSize: 11,
                            color: st.color,
                          }}
                        >
                          <b style={{ fontWeight: 650 }}>{st.n}</b>
                          {st.label}
                        </span>
                      ))}
                    </div>
                    {run.failedTurns.length > 0 ? (
                      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                        {run.failedTurns.map((ft) => (
                          <div
                            key={ft.seq}
                            style={{
                              border: `1px solid ${tokens.error}33`,
                              background: `${tokens.error}0a`,
                              borderRadius: 10,
                              padding: '8px 10px',
                              display: 'flex',
                              alignItems: 'center',
                              gap: 8,
                            }}
                          >
                            <div style={{ flex: 1, minWidth: 0 }}>
                              <div
                                style={{
                                  fontFamily: tokens.fontMono,
                                  fontSize: 10.5,
                                  color: tokens.error,
                                  display: 'flex',
                                  gap: 8,
                                }}
                              >
                                <span>seq {ft.seq}</span>
                                {ft.code && <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{ft.code}</span>}
                              </div>
                              <div
                                style={{
                                  marginTop: 2,
                                  fontSize: 12,
                                  lineHeight: 1.5,
                                  overflowWrap: 'anywhere',
                                  color: tokens.text,
                                }}
                              >
                                {ft.text ? `「${ft.text}」` : '该轮无输入记录'}
                              </div>
                              {ft.details && (
                                <div
                                  style={{
                                    marginTop: 2,
                                    fontSize: 11,
                                    lineHeight: 1.5,
                                    color: tokens.textSecondary,
                                    overflowWrap: 'anywhere',
                                  }}
                                >
                                  {ft.details}
                                </div>
                              )}
                            </div>
                            <Button
                              size="small"
                              icon={<ReloadOutlined />}
                              disabled={!ft.text}
                              title={ft.text ? '重新发送该轮输入' : '该轮无输入可重试'}
                              onClick={() => retryFailedTurn(ft)}
                              style={{ borderRadius: 8, flex: 'none' }}
                            >
                              重试
                            </Button>
                          </div>
                        ))}
                      </div>
                    ) : (
                      <div style={{ fontSize: 12, color: tokens.textPlaceholder, lineHeight: 1.6 }}>
                        无失败轮次。agent 运行出错时，可在此一键重试该轮。
                      </div>
                    )}
                  </>
                ) : (
                  <div style={{ fontSize: 12, color: tokens.textPlaceholder, lineHeight: 1.6 }}>
                    运行统计不可用（历史读取失败）。
                  </div>
                )}
              </div>
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

            {/* 会话级权限记忆（始终允许 / 始终拒绝，对称语义；可逐条/全部撤销） */}
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
                <span>权限记忆</span>
                {(s.memories ?? []).length > 0 && (
                  <Popconfirm
                    title="撤销全部权限记忆？"
                    description="始终允许 / 始终拒绝都将清除，此后同类工具调用需重新逐次确认。"
                    okText="撤销全部"
                    cancelText="取消"
                    okButtonProps={{ danger: true }}
                    onConfirm={() => revokeAll()}
                  >
                    <Button size="small" type="text" danger icon={<DeleteOutlined />} style={{ fontSize: 12 }}>
                      撤销全部
                    </Button>
                  </Popconfirm>
                )}
              </div>
              {(s.memories ?? []).length === 0 ? (
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
                  无会话级权限记忆。对权限确认卡点「始终允许 / 始终拒绝」后会记录在这里，后续同类请求自动放行 / 自动拒绝。
                </div>
              ) : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                  {(s.memories ?? []).map((item) => (
                    <div
                      key={`${item.kind}|${item.title}|${item.decision}`}
                      style={{
                        border: `1px solid ${tokens.border}`,
                        background: tokens.surface,
                        borderRadius: 10,
                        padding: '8px 10px 8px 12px',
                        display: 'flex',
                        alignItems: 'center',
                        gap: 8,
                      }}
                    >
                      <div style={{ flex: 1, minWidth: 0 }}>
                        <div
                          style={{
                            fontSize: 12.5,
                            fontWeight: 600,
                            lineHeight: 1.4,
                            overflowWrap: 'anywhere',
                            color: tokens.text,
                          }}
                        >
                          {item.title}
                        </div>
                        <div
                          style={{
                            marginTop: 2,
                            display: 'flex',
                            gap: 8,
                            fontFamily: tokens.fontMono,
                            fontSize: 10.5,
                            color: tokens.textSecondary,
                          }}
                        >
                          <span style={{ color: item.decision === 'allow' ? tokens.success : tokens.error }}>
                            {item.decision === 'allow' ? '始终允许' : '始终拒绝'}
                          </span>
                          <span>kind: {item.kind || '—'}</span>
                          <span>授予 {fmtLocal(item.since)}</span>
                        </div>
                      </div>
                      <Popconfirm
                        title={`撤销这条「${item.decision === 'allow' ? '始终允许' : '始终拒绝'}」？`}
                        okText="撤销"
                        cancelText="取消"
                        okButtonProps={{ danger: true }}
                        onConfirm={() => revokeOne(item)}
                      >
                        <Button size="small" type="text" danger icon={<DeleteOutlined />} title="撤销" aria-label="撤销" />
                      </Popconfirm>
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
