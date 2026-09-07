import { create } from 'zustand'
import {
  cancelSession,
  createSession,
  listAgents,
  listRuntimes,
  listSessions,
  promptSession,
  resolveElicitation,
  resolvePermission,
  toErrorMessage,
  updateSession,
} from '@ark/api'
import type {
  Agent,
  BusEvent,
  ElicitationFormSchema,
  PermissionOption,
  Runtime,
  SessionSummary,
  StreamEntry,
  WsServerFrame,
} from '@ark/types'
import { Ev } from '@ark/types'
import { WsTransport } from './transport/ws'

export type WsState = 'idle' | 'connecting' | 'open' | 'closed'

export interface SessionView {
  entries: StreamEntry[]
  streaming: boolean
  lastSeq: number
}

const emptyView = (): SessionView => ({ entries: [], streaming: false, lastSeq: 0 })

let uid = 0
const nextId = (): string => `e${++uid}`

/**
 * 事件应用（纯函数）：user/assistant/thought 增量合并、tool 卡更新、终态收流。
 * 语义与旧 useReducer 版一致（M1 DoD 验收过），原样迁移到 zustand。
 */
// 终态事件：到达后服务端会话不再 busy，触发一次会话列表刷新，让「服务端忙水位」状态收敛。
const TERMINAL_TYPES = new Set<string>([Ev.TurnEnded, Ev.TurnError, Ev.RunCancelled])

export function applyEvent(v: SessionView, e: BusEvent): SessionView {
  // seq 幂等去重：事件只前进不回退（§4.5「按 seq 去重」）。快照/重放为开区间、服务端不重发，
  // 此守卫兜底重复 attach / 竞态下的重复投递，避免重连把同一段文本再拼一遍。
  if (e.seq <= v.lastSeq) return v
  const entries = v.entries
  const lastSeq = e.seq
  const text = String(e.payload?.text ?? '')
  const msgId = e.payload?.msg_id !== undefined && e.payload.msg_id !== null ? String(e.payload.msg_id) : ''

  const push = (kind: StreamEntry['kind'], text_?: string, extra?: Partial<StreamEntry>): StreamEntry[] =>
    [...entries, { kind, id: nextId(), text: text_, ...extra }]

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
    case Ev.TurnError:
      return {
        ...v,
        entries: settleCards(
          push('system', undefined, {
            tone: 'err',
            text: `失败 [${String(e.payload?.class ?? '')}/${String(e.payload?.code ?? '')}] ${String(
              e.payload?.details ?? '',
            )}`,
          }),
          '已结束',
        ),
        streaming: false,
        lastSeq,
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
      // 用户点选 allow/reject × once/always 后经 POST /sessions/:id/permission 回传。
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
    case Ev.ElicitationRequest: {
      // ACP 结构化提问（elicitation/create）：agent 请求 → 服务端发布 session.elicitation_request
      // → 前端渲染表单/URL 提问卡；用户提交/放弃经 POST /sessions/:id/elicitation 回传。
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
  runtimes: Runtime[]
  sessions: SessionSummary[]
  views: Record<string, SessionView>
  activeId: string | null
  wsState: WsState
  toast: Toast | null
  drawerOpen: boolean
  /** 会话侧栏当前视图：active=未归档列表；archived=已归档列表（REST archived=true）。 */
  railMode: 'active' | 'archived'
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
  refreshRuntimes: () => Promise<void>
  refreshSessions: () => Promise<void>
  activate: (id: string | null, sinceSeq?: number) => void
  createAndRun: (agentId: string, prompt: string) => Promise<string | undefined>
  sendPrompt: (text: string) => Promise<void>
  cancel: () => Promise<void>
  respondPermission: (callId: string, decision: PermissionDecision) => Promise<void>
  expirePermission: (callId: string) => void
  respondElicitation: (elicitationId: string, decision: ElicitationDecision) => Promise<void>
  expireElicitation: (elicitationId: string) => void
  renameSession: (id: string, title: string) => Promise<void>
  togglePin: (id: string) => Promise<void>
  archiveSession: (id: string, archived: boolean) => Promise<void>
  setRailMode: (mode: 'active' | 'archived') => void
  showToast: (msg: string) => void
  hideToast: (id: number) => void
  setDrawer: (open: boolean) => void
  // transport 接线（内部）
  setWsState: (st: WsState) => void
  applyFrame: (f: WsServerFrame) => void
}

export type ForgeStore = ForgeState & ForgeActions

export const initialState: ForgeState = {
  agents: [],
  runtimes: [],
  sessions: [],
  views: {},
  activeId: null,
  wsState: 'idle',
  toast: null,
  drawerOpen: false,
  railMode: 'active',
}

let toastSeq = 0
let transportRef: WsTransport | null = null

// transport 单例：常驻整个会话生命周期（跨路由保活），重连/重订阅由 transport 自理。
function ensureTransport(): WsTransport {
  if (transportRef) return transportRef
  transportRef = new WsTransport(
    (f) => {
      // 水位推进（供重连续传）
      if (f.type === 'event') transportRef?.ackSeq(f.agg_id, f.event.seq)
      if (f.type === 'snapshot') transportRef?.ackSeq(f.agg_id, f.event_seq)
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
              views: { ...s.views, [f.agg_id]: { entries: [], streaming: false, lastSeq: Math.max(f.event_seq, 0) } },
            }
          }
          case 'event': {
            terminal = TERMINAL_TYPES.has(f.event.type)
            const view = applyEvent(s.views[f.agg_id] ?? emptyView(), f.event)
            return { ...s, views: { ...s.views, [f.agg_id]: view } }
          }
          default:
            return s
        }
      })
      // 终态已到：会话列表 status 同步刷新（侧栏/服务端忙水位随之收敛）。
      if (terminal) void get().refreshSessions()
    },

    setDrawer: (open) => set({ drawerOpen: open }),

    refreshAgents: async () => {
      try {
        set({ agents: await listAgents() })
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },
    refreshRuntimes: async () => {
      try {
        set({ runtimes: await listRuntimes() })
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },
    refreshSessions: async () => {
      try {
        // 按侧栏当前视图拉取：active=未归档；archived=已归档。
        set({ sessions: await listSessions(get().railMode === 'archived') })
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    boot: () => {
      // 常驻连接：console 首帧即反映真实 WS 状态（appbar/状态条），attach 免等待
      ensureTransport().connect()
      void get().refreshAgents()
      void get().refreshSessions()
      void get().refreshRuntimes()
    },

    activate: (id, sinceSeq) => {
      const s = get()
      const t = ensureTransport()
      if (s.activeId && s.activeId !== id) t.detach(s.activeId)
      if (id) {
        t.connect()
        if (!t.isAttached(id)) {
          // 首次挂载/切回：视图先清空，attach（since_seq）后按序重放重建（防重复）
          const cur = s.views[id]
          if (cur && cur.entries.length > 0) {
            set((st) => ({ views: { ...st.views, [id]: emptyView() } }))
          }
          t.attach(id, sinceSeq)
        }
        set({ activeId: id, drawerOpen: false })
      } else {
        set({ activeId: null })
      }
    },

    createAndRun: async (agentId, prompt) => {
      try {
        const resp = await createSession({ agent_id: agentId, prompt })
        // 本地先落用户气泡（echo 事件 seq 与返回水位相等，重放 seq>水位 会跳过它）。
        // attach since_seq=event_seq（exclusive）续传首轮后续事件。
        set((s) => ({
          views: {
            ...s.views,
            [resp.id]: {
              entries: [{ kind: 'user', id: nextId(), text: prompt }],
              streaming: true,
              lastSeq: resp.event_seq,
            },
          },
        }))
        get().activate(resp.id, resp.event_seq)
        // 新建后回「会话」视图并刷新列表（避免停在归档视图看不到新会话）。
        if (get().railMode !== 'active') set({ railMode: 'active' })
        void get().refreshSessions()
        return resp.id
      } catch (err) {
        showToast(toErrorMessage(err))
        return undefined
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
        await promptSession(id, { prompt: text })
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
        await cancelSession(id)
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

    expirePermission: (callId) => {
      // 服务端超时自动拒绝（无用户决策）→ 前端把对应卡片就地标记为已收敛，不发 API。
      const id = get().activeId
      if (!id) return
      set((s) => {
        const view = s.views[id]
        if (!view) return {}
        const entries = view.entries.map((en): StreamEntry =>
          en.kind === 'permission' && en.callId === callId && !en.resolved
            ? { ...en, resolved: true, decision: '超时自动拒绝' }
            : en,
        )
        return { views: { ...s.views, [id]: { ...view, entries } } }
      })
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

    expireElicitation: (elicitationId) => {
      // 服务端超时自动取消（无用户决策）→ 前端把对应卡片就地标记为已收敛，不发 API。
      const id = get().activeId
      if (!id) return
      set((s) => {
        const view = s.views[id]
        if (!view) return {}
        const entries = view.entries.map((en): StreamEntry =>
          en.kind === 'elicitation' && en.elicitationId === elicitationId && !en.resolved
            ? { ...en, resolved: true, decision: '超时自动取消' }
            : en,
        )
        return { views: { ...s.views, [id]: { ...view, entries } } }
      })
    },

    renameSession: async (id, title) => {
      try {
        await updateSession(id, { title })
        void get().refreshSessions()
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    togglePin: async (id) => {
      const cur = get().sessions.find((x) => x.id === id)
      if (!cur) return
      try {
        await updateSession(id, { pinned: !cur.pinned })
        void get().refreshSessions()
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    archiveSession: async (id, archived) => {
      // 归档/恢复（软归档：archived 为真即从「会话」移入「归档」视图，数据不删）。
      try {
        await updateSession(id, { archived })
        void get().refreshSessions()
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    setRailMode: (mode) => {
      if (get().railMode === mode) return
      set({ railMode: mode })
      void get().refreshSessions()
    },
  }
})
