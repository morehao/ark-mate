import { create } from 'zustand'
import {
  HttpError,
  cancelConversation,
  conversationHistory,
  createConversation,
  listAgents,
  listConversations,
  listModels,
  promptConversation,
  resolveElicitation,
  resolvePermission,
  setToken as persistToken,
  getToken,
  toErrorMessage,
  updateConversation,
} from '@ark/api'
import type {
  Agent,
  BusEvent,
  ElicitationFormSchema,
  PermissionOption,
  ConfigOption,
  ConversationSummary,
  ModelCatalogResp,
  ModelRef,
  StreamEntry,
  WsServerFrame,
} from '@ark/types'
import { BizCode, Ev } from '@ark/types'
import { WsTransport } from './transport/ws'
import { olderCursor, prependOlder } from './console/transcriptView'

export type WsState = 'idle' | 'connecting' | 'open' | 'closed'

export interface ConversationView {
  entries: StreamEntry[]
  streaming: boolean
  lastSeq: number
  /** 冷挂载 hydration：activate 全量 attach（snapshot 到达）前为 true（P3 加载四态）。 */
  hydrating: boolean
}

const emptyView = (): ConversationView => ({ entries: [], streaming: false, lastSeq: 0, hydrating: false })

/** 权限终局事件载荷 → 卡片决策摘要（reason/allowed/rejected/always 组合出可读文案）。 */
function permissionResolvedLabel(p: Record<string, unknown> | undefined): string {
  const reason = String(p?.reason ?? '')
  const always = p?.always === true
  if (reason === 'timeout') return '超时自动拒绝'
  if (reason === 'cancel') return '已取消'
  if (p?.allowed === true) return always ? '允许 · 始终' : '允许 · 本次'
  if (p?.rejected === true) return always ? '拒绝 · 始终' : '拒绝 · 本次'
  return '已结束'
}

/** 提问终局事件载荷 → 卡片决策摘要。 */
function elicitationResolvedLabel(p: Record<string, unknown> | undefined): string {
  const reason = String(p?.reason ?? '')
  const action = String(p?.action ?? '')
  // agent 告知 URL 模式提问已在 agent 侧完成（elicitation/complete）→ 不是「用户提交了内容」。
  if (reason === 'complete') return '链接流程已完成'
  if (action === 'accept') return '已提交'
  if (action === 'decline') return '已拒绝'
  if (reason === 'timeout') return '超时自动取消'
  return '已取消'
}

let uid = 0
const nextId = (): string => `e${++uid}`

/**
 * 事件应用（纯函数）：user/assistant/thought 增量合并、tool 卡更新、终态收流。
 * 语义与旧 useReducer 版一致（M1 DoD 验收过），原样迁移到 zustand。
 */
// 终态事件：到达后服务端会话不再 busy，触发一次会话列表刷新，让「服务端忙水位」状态收敛。
const TERMINAL_TYPES = new Set<string>([Ev.TurnEnded, Ev.TurnError, Ev.RunCancelled])

export function applyEvent(v: ConversationView, e: BusEvent): ConversationView {
  // seq 幂等去重：事件只前进不回退（§4.5「按 seq 去重」）。快照/重放为开区间、服务端不重发，
  // 此守卫兜底重复 attach / 竞态下的重复投递，避免重连把同一段文本再拼一遍。
  if (e.seq <= v.lastSeq) return v
  const entries = v.entries
  const lastSeq = e.seq
  const text = String(e.payload?.text ?? '')
  const msgId = e.payload?.msg_id !== undefined && e.payload.msg_id !== null ? String(e.payload.msg_id) : ''

  const push = (kind: StreamEntry['kind'], text_?: string, extra?: Partial<StreamEntry>): StreamEntry[] =>
    [...entries, { kind, id: nextId(), text: text_, seq: e.seq, ...extra }]

  const appendToTail = (kind: StreamEntry['kind'], text_: string): StreamEntry[] => {
    const tail = entries[entries.length - 1]
    if (tail && tail.kind === kind && typeof tail.text === 'string') {
      const next = [...entries]
      next[next.length - 1] = { ...tail, text: tail.text + text_ }
      return next
    }
    return push(kind, text_)
  }

  // 终态/取消收敛：把未决的交互卡（权限/结构化提问）标记为已结束（不再显示待决按钮）。
  // 覆盖「会话被取消/结束但仍挂着卡」的边缘（如超时后端已收敛、agent 继续产生末尾终态）。
  const settleCards = (list: StreamEntry[], label: string): StreamEntry[] =>
    list.map((en): StreamEntry => {
      if (en.resolved) return en
      if (en.kind === 'permission' || en.kind === 'elicitation') return { ...en, resolved: true, decision: label }
      return en
    })

  // 文本段归并：带 msg_id（ACP unstable messageId）时并入同 kind、同 msgId 的既有段
  // （允许跨工具穿插，决策 v5①）；无 msg_id 回退「同 kind 尾拼接」（老行为）。
  const mergeText = (kind: StreamEntry['kind'], text_: string): StreamEntry[] => {
    if (msgId) {
      for (let i = entries.length - 1; i >= 0; i--) {
        const en = entries[i]
        if (en.kind === kind && en.msgId === msgId && typeof en.text === 'string') {
          const next = [...entries]
          next[i] = { ...en, text: en.text + text_ }
          return next
        }
      }
      return push(kind, text_, { msgId })
    }
    return appendToTail(kind, text_)
  }

  switch (e.type) {
    case Ev.UserMessage:
      return { ...v, entries: mergeText('user', text), lastSeq }
    case Ev.Message:
      return { ...v, entries: mergeText('assistant', text), lastSeq }
    case Ev.Thought:
      return { ...v, entries: mergeText('thought', text), lastSeq }
    case Ev.ToolCall: {
      const id = String(e.payload?.tool_call_id ?? '')
      const title = String(e.payload?.title ?? id)
      const status = String(e.payload?.status ?? '')
      return { ...v, entries: push('tool', undefined, { toolId: id, title, status }), lastSeq }
    }
    case Ev.ToolUpdate: {
      const id = String(e.payload?.tool_call_id ?? '')
      return {
        ...v,
        entries: entries.map((en) =>
          en.kind === 'tool' && en.toolId === id
            ? {
                ...en,
                title: String(e.payload?.title ?? en.title ?? id),
                status: String(e.payload?.status ?? en.status ?? ''),
              }
            : en,
        ),
        lastSeq,
      }
    }
    case Ev.SessionResumed: {
      // P3 会话可移植：进程重启/跨 runtime 后会话按需恢复（reason=restart 原生会话续接 /
      // handoff 历史摘要重灌）。以系统提示入流，避免用户误以为会话被「重新开始」。
      const reason = String(e.payload?.reason ?? '')
      const runtime = String(e.payload?.runtime ?? '')
      const label =
        reason === 'handoff' ? '会话已恢复（历史摘要重灌）' : '会话已恢复（原生会话续接）'
      return {
        ...v,
        entries: push('system', undefined, {
          tone: 'info',
          text: runtime ? `${label} · ${runtime}` : label,
        }),
        lastSeq,
      }
    }
    case Ev.TurnEnded:
      return {
        ...v,
        entries: settleCards(
          push('system', undefined, {
            tone: 'ok',
            text: `完成 · ${String(e.payload?.stop_reason ?? '')}`.replace(/ · $/, ''),
          }),
          '已结束',
        ),
        streaming: false,
        lastSeq,
      }
    case Ev.TurnError: {
      // A3：失败事件可能携带 runtime stderr 诊断尾巴（数组；非数组/空则忽略）。
      const rawTail = e.payload?.stderr_tail
      const stderrTail = Array.isArray(rawTail) ? rawTail.map((l) => String(l)) : undefined
      return {
        ...v,
        entries: settleCards(
          push('system', undefined, {
            tone: 'err',
            text: `失败 [${String(e.payload?.class ?? '')}/${String(e.payload?.code ?? '')}] ${String(
              e.payload?.details ?? '',
            )}`,
            stderrTail: stderrTail && stderrTail.length > 0 ? stderrTail : undefined,
          }),
          '已结束',
        ),
        streaming: false,
        lastSeq,
      }
    }
    case Ev.RunCancelled:
      return {
        ...v,
        entries: settleCards(push('system', undefined, { tone: 'cancel', text: '已取消' }), '已取消'),
        streaming: false,
        lastSeq,
      }
    case Ev.PermissionRequest: {
      // ACP 权限确认：agent 发起请求 → 服务端发布 session.permission_request → 前端渲染卡片，
      // 用户点选 allow/reject × once/always 后经 POST /conversations/:id/permission 回传。
      const callId = String(e.payload?.call_id ?? '')
      const title = String(e.payload?.title ?? '权限请求')
      const toolKind = String(e.payload?.kind ?? '')
      const toolCallId = String(e.payload?.tool_call_id ?? '')
      const timeoutAt = e.payload?.timeout_at != null ? String(e.payload.timeout_at) : undefined
      const optionKinds = Array.isArray(e.payload?.options)
        ? (e.payload.options as PermissionOption[])
        : []
      return {
        ...v,
        entries: push('permission', undefined, { callId, title, optionKinds, toolKind, toolCallId, resolved: false, timeoutAt }),
        lastSeq,
      }
    }
    case Ev.PermissionResolved: {
      // 挂起权限终局（用户决策/超时自动拒绝/会话取消）——服务端事件是收敛唯一事实，前端不再自设时钟。
      const callId = String(e.payload?.call_id ?? '')
      return {
        ...v,
        entries: entries.map((en): StreamEntry =>
          en.kind === 'permission' && en.callId === callId && !en.resolved
            ? { ...en, resolved: true, decision: permissionResolvedLabel(e.payload) }
            : en,
        ),
        lastSeq,
      }
    }
    case Ev.ElicitationResolved: {
      // 挂起提问终局（accept/decline/cancel/超时自动取消）——同上，收敛以服务端事件为准。
      const elicitationId = String(e.payload?.elicitation_id ?? '')
      return {
        ...v,
        entries: entries.map((en): StreamEntry =>
          en.kind === 'elicitation' && en.elicitationId === elicitationId && !en.resolved
            ? { ...en, resolved: true, decision: elicitationResolvedLabel(e.payload) }
            : en,
        ),
        lastSeq,
      }
    }
    case Ev.ElicitationRequest: {
      // ACP 结构化提问（elicitation/create）：agent 请求 → 服务端发布 session.elicitation_request
      // → 前端渲染表单/URL 提问卡；用户提交/放弃经 POST /conversations/:id/elicitation 回传。
      const elicitationId = String(e.payload?.elicitation_id ?? '')
      const mode = String(e.payload?.mode ?? 'form') === 'url' ? 'url' : 'form'
      const message = String(e.payload?.message ?? '')
      const timeoutAt = e.payload?.timeout_at != null ? String(e.payload.timeout_at) : undefined
      const schema = (e.payload?.schema as ElicitationFormSchema | undefined) ?? undefined
      const url = e.payload?.url != null ? String(e.payload.url) : undefined
      return {
        ...v,
        entries: push('elicitation', undefined, {
          elicitationId,
          mode,
          message,
          schema,
          url,
          resolved: false,
          timeoutAt,
        }),
        lastSeq,
      }
    }
    case Ev.Usage:
      return { ...v, lastSeq }
    default:
      return { ...v, lastSeq }
  }
}

interface Toast {
  id: number
  msg: string
}

export interface ForgeState {
  agents: Agent[]
  conversations: ConversationSummary[]
  views: Record<string, ConversationView>
  activeId: string | null
  wsState: WsState
  toast: Toast | null
  drawerOpen: boolean
  /** 会话侧栏当前视图：active=未归档列表；archived=已归档列表（REST archived=true）。 */
  railMode: 'active' | 'archived'
  /** 搜索命中跳转锚点：目标会话的 seq（ChatPage 消费后清空；null = 无跳转请求）。 */
  anchorSeq: number | null
  /** A6 单操作员令牌是否已配置（true = 后端启用鉴权且本机已填令牌）。 */
  token: string
  /** 令牌设置弹窗开关（顶栏锁图标 / 收到 401 时自动打开）。 */
  tokenModalOpen: boolean
  /** C2：该会话是否已翻到最早（false/缺省 = 可能还有更早，ChatPage 显示「加载更早」）。 */
  historyStart: Record<string, boolean>
  /** 模型目录（当前 agent 的运行时会话配置项投影；null = 未加载）。 */
  modelCatalog: ModelCatalogResp | null
}

export interface PermissionDecision {
  allowed: boolean
  optionId?: string
  always: boolean
  label: string
}

export interface ElicitationDecision {
  action: 'accept' | 'decline' | 'cancel'
  content?: Record<string, unknown>
  label: string
}

export interface ForgeActions {
  boot: () => void
  refreshAgents: () => Promise<void>
  refreshConversations: () => Promise<void>
  activate: (id: string | null, sinceSeq?: number) => void
  /** 跳转到某会话的锚点消息（搜索命中点击）；activate 后由 ChatPage 滚动定位并清空。 */
  jumpToAnchor: (id: string, seq: number) => void
  /** 清除跳转锚点（ChatPage 定位完成后调用）。 */
  clearAnchor: () => void
  /**
   * 新建会话并起首轮。
   * `workspace` = 会话工作空间绝对路径（服务端规范化后的原文）；null/省略 = 由平台代建临时空间。
   * 工作空间创建时定稿、之后不可变（它是底座 ACP session 的 cwd 与 workspace-write 的边界）。
   */
  createAndRun: (prompt: string, model?: ModelRef, workspace?: string | null) => Promise<string | undefined>
  /** 拉取模型目录（`?agent=<id>`；来源 session|resume|cache|none，失败 → 空目录 + 提示）。 */
  loadModelCatalog: (agentId?: string) => Promise<void>
  /** 会话级模型变更（经运行时 set_config_option 立即生效；运行时拒绝 → 报错且不改元数据）。 */
  updateConversationModel: (id: string, model?: string) => Promise<void>
  sendPrompt: (text: string) => Promise<void>
  cancel: () => Promise<void>
  respondPermission: (callId: string, decision: PermissionDecision) => Promise<void>
  respondElicitation: (elicitationId: string, decision: ElicitationDecision) => Promise<void>
  renameConversation: (id: string, title: string) => Promise<void>
  togglePin: (id: string) => Promise<void>
  archiveConversation: (id: string, archived: boolean) => Promise<void>
  setRailMode: (mode: 'active' | 'archived') => void
  showToast: (msg: string) => void
  hideToast: (id: number) => void
  setDrawer: (open: boolean) => void
  /** A6：设置/清除单操作员令牌并重连 WS + 重新拉取数据。 */
  saveToken: (token: string) => void
  setTokenModal: (open: boolean) => void
  /** C2：向服务端翻页加载更早的事件（before 游标），返回本页是否还有更早。 */
  loadOlder: (id: string) => Promise<boolean>
  /** P3 加载四态：hydration 超时兜底（仍无 snapshot/事件 → 退出加载态显示空态）。 */
  expireHydration: (id: string) => void
  // transport 接线（内部）
  setWsState: (st: WsState) => void
  applyFrame: (f: WsServerFrame) => void
}

export type ForgeStore = ForgeState & ForgeActions

export const initialState: ForgeState = {
  agents: [],
  conversations: [],
  views: {},
  activeId: null,
  anchorSeq: null,
  wsState: 'idle',
  toast: null,
  drawerOpen: false,
  railMode: 'active',
  token: getToken(),
  tokenModalOpen: false,
  historyStart: {},
  modelCatalog: null,
}

/** C2：服务端「加载更早」每页条数。 */
const HISTORY_PAGE = 50

let toastSeq = 0
let transportRef: WsTransport | null = null

// reportApiError：401（A6 令牌缺失/无效）时自动打开令牌弹窗，其余走 toast。
function reportApiError(err: unknown): void {
  if (err instanceof HttpError && err.code === BizCode.Unauthorized) {
    useForge.setState({ tokenModalOpen: true })
    useForge.getState().showToast('需要访问令牌：请在顶栏「令牌」中填写 FORGE_TOKEN')
    return
  }
  useForge.getState().showToast(toErrorMessage(err))
}

// transport 单例：常驻整个会话生命周期（跨路由保活），重连/重订阅由 transport 自理。
function ensureTransport(): WsTransport {
  if (transportRef) return transportRef
  transportRef = new WsTransport(
    (f) => {
      // 水位推进（供重连续传）
      if (f.type === 'event') transportRef?.ackSeq(f.agg_id, f.event.seq)
      if (f.type === 'snapshot') transportRef?.ackSeq(f.agg_id, f.event_seq)
      if (f.type === 'gap') {
        // 慢订阅丢帧（服务端缓冲满）：按当前已应用水位立即重挂该聚合——
        // 服务端 EventsAfter 为开区间（seq > since），重放不会重复已应用事件；若起点已被淘汰则回 snapshot。
        transportRef?.attach(f.agg_id)
        return
      }
      useForge.getState().applyFrame(f)
    },
    (st) => {
      useForge.getState().setWsState(st === 'open' ? 'open' : st === 'connecting' ? 'connecting' : 'closed')
    },
  )
  return transportRef
}

export const useForge = create<ForgeStore>((set, get) => {
  const showToast = (msg: string): void => {
    const id = ++toastSeq
    set({ toast: { id, msg } })
    window.setTimeout(() => get().hideToast(id), 4200)
  }

  return {
    ...initialState,

    hideToast: (id) => {
      const cur = get().toast
      if (cur && cur.id === id) set({ toast: null })
    },
    showToast,

    // C2：向服务端翻页加载更早的事件（before 游标）。
    // 取当前视图最小事件 seq 作为游标（新会话/空视图 → before=0 = 取末尾一页），
    // 折叠到空视图后整体前插（事件 seq 全小于游标 → 与现有条目天然不重叠），lastSeq 不回退。
    loadOlder: async (id) => {
      const cur = get().views[id] ?? emptyView()
      const before = olderCursor(cur.entries)
      try {
        const page = await conversationHistory(id, { before, limit: HISTORY_PAGE })
        let older = emptyView()
        for (const e of page.events) older = applyEvent(older, e)
        set((s) => {
          const view = s.views[id] ?? emptyView()
          const merged = prependOlder(older.entries, view.entries, before)
          return {
            ...s,
            views: { ...s.views, [id]: { ...view, entries: merged } },
            historyStart: { ...s.historyStart, [id]: !page.has_more },
          }
        })
        if (page.events.length === 0) showToast('已到会话最早一条')
        return page.has_more
      } catch (err) {
        reportApiError(err)
        return true
      }
    },

    setWsState: (st) => {
      if (get().wsState !== st) set({ wsState: st })
    },

    applyFrame: (f) => {
      let terminal = false
      set((s) => {
        switch (f.type) {
          case 'snapshot': {
            // snapshot = 服务端整包终态视图（冷挂载或「淘汰即快照回退」）：
            // M0/M1 state 为空对象，客户端重置本会话视图并以 event_seq 初始化游标。
            return {
              ...s,
              views: {
                ...s.views,
                [f.agg_id]: { entries: [], streaming: false, lastSeq: Math.max(f.event_seq, 0), hydrating: false },
              },
            }
          }
          case 'event': {
            terminal = TERMINAL_TYPES.has(f.event.type)
            const view = applyEvent(s.views[f.agg_id] ?? emptyView(), f.event)
            // 首个事件即证明已就绪（配合 snapshot 与 hydration 超时兜底）
            if (view.hydrating) view.hydrating = false
            // 运行时推送的配置项更新（全量语义）→ 就地刷新模型目录（同一 agent 才生效）：
            // 模型下拉与「当前模型」无需轮询即可跟随运行时的真实状态。
            let catalog = s.modelCatalog
            if (f.event.type === Ev.ConfigOptions && catalog) {
              const payload = f.event.payload ?? {}
              const options = payload.options as ConfigOption[] | undefined
              if (options && String(payload.agent_id ?? '') === catalog.agent) {
                catalog = { ...catalog, source: 'session', options }
              }
            }
            return { ...s, modelCatalog: catalog, views: { ...s.views, [f.agg_id]: view } }
          }
          default:
            return s
        }
      })
      // 终态已到：会话列表 status 同步刷新（侧栏/服务端忙水位随之收敛）。
      if (terminal) void get().refreshConversations()
    },

    setDrawer: (open) => set({ drawerOpen: open }),

    refreshAgents: async () => {
      try {
        set({ agents: await listAgents() })
      } catch (err) {
        reportApiError(err)
      }
    },
    refreshConversations: async () => {
      try {
        // 按侧栏当前视图拉取：active=未归档；archived=已归档。
        set({ conversations: await listConversations(get().railMode === 'archived') })
      } catch (err) {
        reportApiError(err)
      }
    },

    saveToken: (token) => {
      const v = token.trim()
      persistToken(v)
      set({ token: v, tokenModalOpen: false })
      // 令牌变化必须重连 WS（握手时经 ?token= 传递）并重新拉取数据。
      transportRef?.close()
      ensureTransport().connect()
      void get().refreshAgents()
      void get().refreshConversations()
      get().showToast(v ? '已保存访问令牌，正在重连…' : '已清除访问令牌')
    },
    setTokenModal: (open) => set({ tokenModalOpen: open }),

    boot: () => {
      // 常驻连接：console 首帧即反映真实 WS 状态（appbar/状态条），attach 免等待
      ensureTransport().connect()
      void get().refreshAgents()
      void get().refreshConversations()
    },

    activate: (id, sinceSeq) => {
      const s = get()
      const t = ensureTransport()
      if (s.activeId && s.activeId !== id) t.detach(s.activeId)
      if (id) {
        t.connect()
        if (!t.isAttached(id)) {
          const cur = s.views[id]
          // 冷挂载/切回（无 since_seq）：视图重置为 hydrating（P3 四态），等服务端
          // snapshot（或首个事件）置回就绪；带 since_seq 的增量续传（createAndRun
          // 本地气泡续流）不视为冷挂载。
          const cold = sinceSeq === undefined
          if (cold && (!cur || cur.entries.length === 0)) {
            set((st) => ({
              views: { ...st.views, [id]: { ...emptyView(), hydrating: true } },
            }))
          } else if (cold) {
            set((st) => ({ views: { ...st.views, [id]: emptyView() } }))
          }
          t.attach(id, sinceSeq)
        }
        set({ activeId: id, drawerOpen: false, anchorSeq: null })
      } else {
        set({ activeId: null })
      }
    },

    jumpToAnchor: (id, seq) => {
      // 复用 activate 的挂载/续传语义，再把锚点交给 ChatPage（先清空再设置，保证跨会话重复跳转也触发）。
      set({ anchorSeq: null })
      get().activate(id)
      set({ anchorSeq: seq })
    },

    clearAnchor: () => set({ anchorSeq: null }),

    createAndRun: async (prompt, model, workspace) => {
      try {
        const resp = await createConversation({
          prompt,
          model: model?.modelID,
          // 不外发空串：cwd 缺省才是「平台代建临时空间」语义（后端把空/缺省当作未指定）。
          cwd: workspace ?? undefined,
        })
        // 本地先落用户气泡（echo 事件 seq 与返回水位相等，重放 seq>水位 会跳过它）。
        // attach since_seq=event_seq（exclusive）续传首轮后续事件。
        set((s) => ({
          views: {
            ...s.views,
            [resp.id]: {
              entries: [{ kind: 'user', id: nextId(), text: prompt }],
              streaming: true,
              lastSeq: resp.event_seq,
              hydrating: false,
            },
          },
        }))
        get().activate(resp.id, resp.event_seq)
        // 新建后回「会话」视图并刷新列表（避免停在归档视图看不到新会话）。
        if (get().railMode !== 'active') set({ railMode: 'active' })
        void get().refreshConversations()
        return resp.id
      } catch (err) {
        showToast(toErrorMessage(err))
        return undefined
      }
    },

    loadModelCatalog: async (agentId) => {
      try {
        const resp = await listModels(agentId || undefined)
        set({ modelCatalog: resp })
      } catch (err) {
        // 取不到目录 = 运行时没有可用来源（未启动/未登录）：保持空目录（面板显示空态 + 提示），
        // 不阻塞建会话（不指定模型即跟随运行时默认）。
        set({ modelCatalog: null })
        showToast(toErrorMessage(err))
      }
    },

    updateConversationModel: async (id, model) => {
      try {
        await updateConversation(id, { model })
        await get().refreshConversations()
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    sendPrompt: async (text) => {
      const id = get().activeId
      if (!id) return
      if (get().views[id]?.streaming) return // 已有一轮在跑，防重入（连点/并发）
      try {
        // 乐观置忙：在 POST 返回前就把 Composer 置为禁用，消除「请求在途仍可再发」的竞态窗口，
        // 避免第二次 send 打到后端 turn_in_progress（决策 v5 前修复）。
        set((s) => {
          const view = s.views[id] ?? emptyView()
          return { views: { ...s.views, [id]: { ...view, streaming: true } } }
        })
        ensureTransport().connect()
        await promptConversation(id, { prompt: text })
      } catch (err) {
        // 失败恢复忙态（若无终态事件，回退为非忙以便重试）。
        set((s) => {
          const view = s.views[id]
          if (!view) return {}
          return { views: { ...s.views, [id]: { ...view, streaming: false } } }
        })
        showToast(toErrorMessage(err))
      }
    },

    cancel: async () => {
      const id = get().activeId
      if (!id) return
      try {
        await cancelConversation(id)
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    respondPermission: async (callId, decision) => {
      const id = get().activeId
      if (!id) return
      try {
        await resolvePermission(id, {
          call_id: callId,
          option_id: decision.optionId,
          always: decision.always,
          allowed: decision.allowed,
        })
        // 本地就地标记该权限卡已决策（按钮禁用 + 展示决策），agent 随后经 WS 续流。
        set((s) => {
          const view = s.views[id]
          if (!view) return {}
          const entries = view.entries.map((en): StreamEntry =>
            en.kind === 'permission' && en.callId === callId
              ? { ...en, resolved: true, decision: decision.label }
              : en,
          )
          return { views: { ...s.views, [id]: { ...view, entries } } }
        })
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    respondElicitation: async (elicitationId, decision) => {
      const id = get().activeId
      if (!id) return
      try {
        await resolveElicitation(id, {
          elicitation_id: elicitationId,
          action: decision.action,
          content: decision.content,
        })
        // 本地就地标记该提问卡已决策（按钮禁用 + 展示结果），agent 随后经 WS 续流。
        set((s) => {
          const view = s.views[id]
          if (!view) return {}
          const entries = view.entries.map((en): StreamEntry =>
            en.kind === 'elicitation' && en.elicitationId === elicitationId
              ? { ...en, resolved: true, decision: decision.label, content: decision.content }
              : en,
          )
          return { views: { ...s.views, [id]: { ...view, entries } } }
        })
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    renameConversation: async (id, title) => {
      try {
        await updateConversation(id, { title })
        void get().refreshConversations()
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    togglePin: async (id) => {
      const cur = get().conversations.find((x) => x.id === id)
      if (!cur) return
      try {
        await updateConversation(id, { pinned: !cur.pinned })
        void get().refreshConversations()
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    archiveConversation: async (id, archived) => {
      // 归档/恢复（软归档：archived 为真即从「会话」移入「归档」视图，数据不删）。
      try {
        await updateConversation(id, { archived })
        void get().refreshConversations()
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    expireHydration: (id) => {
      set((s) => {
        const view = s.views[id]
        if (!view || !view.hydrating) return {}
        return { views: { ...s.views, [id]: { ...view, hydrating: false } } }
      })
    },

    setRailMode: (mode) => {
      if (get().railMode === mode) return
      set({ railMode: mode })
      void get().refreshConversations()
    },
  }
})
