import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, Drawer, Input, Popconfirm, Spin, Tag } from 'antd'
import {
  CheckOutlined,
  CloseOutlined,
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  InboxOutlined,
  ReloadOutlined,
} from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { fmtDateTime } from '../console/time'
import { Dot, Pill, sessionStatusMeta } from '@ark-mate/ui'
import type { ArtifactMeta, PermissionMemory, PermissionOptionKind, PermissionResp, SessionDetail } from '@ark-mate/types'
import { modelValues } from '@ark-mate/types'
import {
  getSession,
  importArtifact,
  listMaterialNodes,
  listSessionArtifacts,
  promptSession,
  revokeMemory,
  sessionHistory,
  toErrorMessage,
} from '@ark-mate/api'
import { useMate } from '../store'
import { pinnedCapabilitiesView } from '../console/capabilityView'
import { ModelSelect } from './ModelSelect'
import { shortId } from '../console/util'
import { copyText } from '../console/clipboard'
import { summarizeRun, type FailedRun, type RunSummary } from '../console/runSummary'

function fmtBytes(n: number): string {
  if (!n || n < 0) return '—'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

/** 产物来源的中文（只做展示；后端枚举是唯一事实源）。 */
const ORIGIN_LABEL: Record<string, string> = {
  tool_write: '工具写入',
  tool_edit: '工具编辑',
  upload: '上传',
  inline: '内联',
  manual: '手动登记',
}

function optionId(p: PermissionResp, kind: PermissionOptionKind): string | undefined {
  return p.options.find((o) => o.kind === kind)?.option_id
}

/**
 * 会话详情面板（管理面）：元数据只读一瞥 + 运行统计 + 产物 + 挂起权限（可就地决策）。
 *
 * 产物区是「会话 → 资料库」这一跳的入口：产物默认只登记在 artifacts（全量可查），进资料库是
 * **显式动作**（`POST /materials/import-artifact`）。没有这个入口，用户就只能在会话里看到文件
 * 已生成、却在资料库里永远找不到它。
 */
export function SessionDetailDrawer({
  open,
  sessionId,
  onClose,
}: {
  open: boolean
  sessionId: string
  onClose: () => void
}) {
  const navigate = useNavigate()
  const views = useMate((s) => s.views)
  const respond = useMate((s) => s.respondPermission)
  const renameSession = useMate((s) => s.renameSession)
  const togglePin = useMate((s) => s.togglePin)
  const archiveSession = useMate((s) => s.archiveSession)
  const showToast = useMate((s) => s.showToast)
  const modelCatalog = useMate((s) => s.modelCatalog)
  const loadModelCatalog = useMate((s) => s.loadModelCatalog)
  const updateSessionModel = useMate((s) => s.updateSessionModel)

  const [detail, setDetail] = useState<SessionDetail | null>(null)
  const [run, setRun] = useState<RunSummary | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState('')
  // 产物（v34/v35）：本会话的 artifacts 行 + 哪些已收录进资料库（artifact id → 节点 id）。
  const [artifacts, setArtifacts] = useState<ArtifactMeta[]>([])
  const [artLoading, setArtLoading] = useState(false)
  const [importingId, setImportingId] = useState<string | null>(null)
  const [importedNodes, setImportedNodes] = useState<Record<string, string>>({})

  const load = useCallback(async () => {
    if (!sessionId) return
    setLoading(true)
    setErr(null)
    try {
      setDetail(await getSession(sessionId))
    } catch (e) {
      setErr(toErrorMessage(e))
    }
    // 运行统计/失败轮由历史推导；读不到不阻塞主数据。
    try {
      const page = await sessionHistory(sessionId, { limit: 500 })
      setRun(summarizeRun(page.events))
    } catch {
      setRun(null)
    } finally {
      setLoading(false)
    }
  }, [sessionId])

  // 产物列表独立拉：它是旁路数据，读失败不该把「会话详情」整块拖进错误态。
  //
  // 同时拉一次资料库的产物节点：抽取成功后平台会**自动收录**，界面要能立刻显示「已收录」而不是
  // 让用户再点一次（不读这一份就只能靠本地点击状态，刷新一次又变回「存进资料库」）。
  const loadArtifacts = useCallback(async () => {
    if (!sessionId) return
    setArtLoading(true)
    try {
      const [resp, nodes] = await Promise.all([
        listSessionArtifacts(sessionId),
        listMaterialNodes({ category: 'artifact', limit: 200 }).catch(() => null),
      ])
      setArtifacts(resp.artifacts ?? [])
      if (nodes) {
        const map: Record<string, string> = {}
        for (const n of nodes.nodes) {
          if (n.provenance_id) map[n.provenance_id] = n.id
        }
        // 本地刚点过的结果优先（它一定是这次会话里最新的）。
        setImportedNodes((prev) => ({ ...map, ...prev }))
      }
    } catch {
      setArtifacts([])
    } finally {
      setArtLoading(false)
    }
  }, [sessionId])

  useEffect(() => {
    if (open) {
      setEditingTitle(false)
      void load()
      void loadArtifacts()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, sessionId])

  // 同一工作区路径只保留最新版（资料库那边也按这个口径去重）：更早的产物在列表里标「历史版本」，
  // 不给收录按钮——否则点一下会把资料库节点又翻回旧快照。
  const latestArtifactByPath = useMemo(() => {
    const m = new Map<string, string>()
    for (const a of artifacts) {
      if (a.path && !m.has(a.path)) m.set(a.path, a.id) // artifacts 已按 created_at 降序
    }
    return m
  }, [artifacts])

  // 收录一个产物进资料库（幂等：重复点返回既有节点，不会长第二份）。
  const doImport = (a: ArtifactMeta) => {
    setImportingId(a.id)
    void importArtifact(a.id)
      .then((node) => {
        setImportedNodes((prev) => ({ ...prev, [a.id]: node.id }))
        showToast(`已收录进资料库：${node.title || a.title}`, 'success')
      })
      .catch((e) => showToast(toErrorMessage(e), 'error'))
      .finally(() => setImportingId(null))
  }

  // 会话级模型目录（v19 起这条读面**无参**）。
  //
  // 原来它要带 `?agent=<会话绑定的那个>`：模型候选只能来自该会话那个底座，取错会列出别的底座的
  // 模型（不同底座的目录本就不同），选下去必被运行时拒绝。现在底座构建期固定、读面自己按它取样，
  // 于是"取错"这条路不再存在——但**打开详情才拉**这条时序仍然保留（不打开就不打这次请求）。
  useEffect(() => {
    if (open) void loadModelCatalog()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

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
          : { allowed: false, always: false, optionId: optionId(p, 'reject_once') ?? optionId(p, 'reject_always'), label: '拒绝' }
    void respond(p.call_id, decision).then(() => load())
  }

  // —— 会话级权限记忆（始终允许 / 始终拒绝，对称）：撤销（单条 / 全部）后刷新 ——
  const revokeOne = (item: PermissionMemory) => {
    void revokeMemory(sessionId, { title: item.title, kind: item.kind })
      .then(() => load())
      .catch((e) => setErr(toErrorMessage(e)))
  }
  const revokeAll = () => {
    void revokeMemory(sessionId, {})
      .then(() => load())
      .catch((e) => setErr(toErrorMessage(e)))
  }

  // —— 失败轮一键重试：重发该轮用户输入（事件经 WS 续流），随后刷新统计 ——
  const retryFailedRun = (ft: FailedRun) => {
    if (!ft.text) return
    void promptSession(sessionId, { prompt: ft.text })
      .then(() => {
        showToast('已重发该轮输入，结果将实时流出…', 'success')
        window.setTimeout(() => void load(), 1500)
      })
      .catch((e) => setErr(toErrorMessage(e)))
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
          onClick={() => {
            void load()
            void loadArtifacts()
          }}
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
              {/* 所属工作空间（v14）：与"在工作目录"是两件事——那行是**执行事实**（cwd），
                  这行是**归属事实**（左栏分组）。两者在正常情况下指向同一目录，但登记被删、
                  或目录后来不可用时就会分叉；分开显示让"它归在哪个分组下"有答案。 */}
              {s.workspace_title &&
                row(
                  '所属工作空间',
                  <span style={{ fontSize: 12 }} title={s.workspace_id ?? ''}>
                    {s.workspace_title}
                  </span>,
                )}
              {(() => {
                // 是否可切换由**平台清单 ∩ 运行时广告**说了算（v27；`modelValues` 只回平台声明过的
                // 那些——不再按 runtime 名硬编码，也不再把选不了的广告项摆出来）。没有可选项时
                // 不摆空下拉（那看着像"模型没了"），只如实说明一行。
                const modelOptions = modelValues(modelCatalog ?? undefined)
                if (modelOptions.length === 0) {
                  // 不展示不透明取值串：会话的 `model` 仍是平台行 id，这里没有清掉任何东西。
                  // 措辞只说"当前没有可选项"，**不说**"没配置"——目录为空可能只是运行时此刻
                  // 没广告候选（平台清单里其实有模型），把它讲成"没配置"会把人引向错误的方向。
                  return row('模型', s.model ? '已设置（目录里当前没有可选项）' : '跟随默认（暂无可选模型）')
                }
                // v26：会话里存的是**平台模型行 id**，候选上带着 `model_id` 注解——用它反查当前
                // 选中项。反查不到（模型行被删了 / 换了清单 / 运行时此刻没广告它）就补一个只读项
                // **如实显示不可用**，而不是显示成空（空会让用户以为这条会话没选模型）。
                const values =
                  s.model && !modelOptions.some((v) => v.model_id === s.model)
                    ? [{ value: s.model, name: '当前模型（模型目录里没有它）' }, ...modelOptions]
                    : modelOptions
                return row(
                  '模型',
                  <ModelSelect
                    values={values}
                    value={s.model ?? null}
                    allowClear
                    size="small"
                    defaultLabel="默认"
                    onChange={(v) => {
                      if ((v ?? undefined) === s.model) return
                      void updateSessionModel(s.id, v ?? undefined).then(() => void load())
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
                        showToast(copied ? '已复制会话 ID' : '复制失败（浏览器未授权剪贴板）', copied ? 'success' : 'error'),
                      )
                    }
                  />
                </span>,
              )}
              {row('创建', <span style={{ fontFamily: tokens.fontMono, fontSize: 11.5 }}>{fmtDateTime(s.created_at)}</span>)}
              {row('更新', <span style={{ fontFamily: tokens.fontMono, fontSize: 11.5 }}>{fmtDateTime(s.updated_at)}</span>)}
              {(() => {
                // 钉住的能力选定（M3）：展示的是**创建时钉住的快照**，不是"现在能用什么"——
                // 定义文件改了以后这个会话仍跑在原来那一代上，这个差别正是用户需要看见的。
                const pinned = pinnedCapabilitiesView(s.capabilities)
                if (!pinned) {
                  return row(
                    '能力',
                    <span style={{ color: tokens.textPlaceholder }} title="未单独设置：使用默认能力">跟随默认
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
                    {run.failedRuns.length > 0 ? (
                      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                        {run.failedRuns.map((ft) => (
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
                                <span>运行出错</span>
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
                              onClick={() => retryFailedRun(ft)}
                              style={{ borderRadius: 8, flex: 'none' }}
                            >
                              重试
                            </Button>
                          </div>
                        ))}
                      </div>
                    ) : (
                      <div style={{ fontSize: 12, color: tokens.textPlaceholder, lineHeight: 1.6 }}>
                        无失败轮次。运行出错时，可在此一键重试该轮。
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

            {/* 产物：本会话由工具写出/登记的文件。默认只进 artifacts；「存进资料库」是显式收录。 */}
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
                <span>产物</span>
                <span style={{ fontFamily: tokens.fontMono, fontSize: 11 }}>
                  {artLoading ? '…' : artifacts.length}
                </span>
              </div>
              {artLoading ? (
                <div style={{ display: 'grid', placeItems: 'center', padding: '18px 0' }}>
                  <Spin size="small" />
                </div>
              ) : artifacts.length === 0 ? (
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
                  本会话还没有产物。助手用 write / edit 工具写出工作区文件后会自动登记到这里；
                  要长期留存的，点「存进资料库」收录进资料库的「产物」分栏。
                </div>
              ) : (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                  {artifacts.map((a) => {
                    const nodeId = importedNodes[a.id]
                    const latest = !a.path || latestArtifactByPath.get(a.path) === a.id
                    return (
                      <div
                        key={a.id}
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
                        <InboxOutlined style={{ flex: 'none', color: tokens.textSecondary, fontSize: 13 }} />
                        <div style={{ flex: 1, minWidth: 0 }}>
                          <div
                            title={a.path || a.title}
                            style={{
                              fontSize: 12.5,
                              fontWeight: 600,
                              lineHeight: 1.4,
                              overflow: 'hidden',
                              textOverflow: 'ellipsis',
                              whiteSpace: 'nowrap',
                              color: tokens.text,
                            }}
                          >
                            {a.title}
                          </div>
                          <div
                            style={{
                              marginTop: 2,
                              display: 'flex',
                              alignItems: 'center',
                              gap: 8,
                              flexWrap: 'wrap',
                              fontFamily: tokens.fontMono,
                              fontSize: 10.5,
                              color: tokens.textSecondary,
                            }}
                          >
                            <span>{ORIGIN_LABEL[a.origin] ?? a.origin}</span>
                            <span>{fmtBytes(a.size_bytes)}</span>
                            {a.path && <span title={a.path}>{a.path}</span>}
                            {a.dirty && <Tag color="orange" style={{ marginInlineEnd: 0, fontSize: 10, lineHeight: '16px' }}>已改动</Tag>}
                          </div>
                        </div>
                        {!latest ? (
                          <Tag style={{ flex: 'none', marginInlineEnd: 0, fontSize: 10, lineHeight: '16px' }}>
                            历史版本
                          </Tag>
                        ) : nodeId ? (
                          <Button
                            size="small"
                            type="link"
                            style={{ flex: 'none', fontSize: 12 }}
                            onClick={() => navigate('/materials?category=artifact')}
                            title="在资料库的「产物」分栏中查看"
                          >
                            已收录 · 去查看
                          </Button>
                        ) : (
                          <Button
                            size="small"
                            loading={importingId === a.id}
                            onClick={() => doImport(a)}
                            style={{ borderRadius: 8, flex: 'none', fontSize: 12 }}
                          >
                            存进资料库
                          </Button>
                        )}
                      </div>
                    )
                  })}
                </div>
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
                  无待确认的权限请求。需要确认工具权限时会实时出现在这里与聊天流中。
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
                        <span>截止 {fmtDateTime(p.timeout_at)}</span>
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
                          <span>授予 {fmtDateTime(item.since)}</span>
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
