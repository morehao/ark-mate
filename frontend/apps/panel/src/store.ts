import { create } from 'zustand'
import {
  cancelSession,
  createSession,
  listAgents,
  listRuntimes,
  listSessions,
  promptSession,
  toErrorMessage,
} from '@ark/api'
import type { Agent, BusEvent, Runtime, SessionSummary, StreamEntry, WsServerFrame } from '@ark/types'
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
export function applyEvent(v: SessionView, e: BusEvent): SessionView {
  const entries = v.entries
  const lastSeq = Math.max(v.lastSeq, e.seq)
  const text = String(e.payload?.text ?? '')

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

  switch (e.type) {
    case Ev.UserMessage:
      return { ...v, entries: push('user', text), lastSeq }
    case Ev.Message:
      return { ...v, entries: appendToTail('assistant', text), lastSeq }
    case Ev.Thought:
      return { ...v, entries: appendToTail('thought', text), lastSeq }
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
        entries: push('system', undefined, {
          tone: 'ok',
          text: `完成 · ${String(e.payload?.stop_reason ?? '')}`.replace(/ · $/, ''),
        }),
        streaming: false,
        lastSeq,
      }
    case Ev.TurnError:
      return {
        ...v,
        entries: push('system', undefined, {
          tone: 'err',
          text: `失败 [${String(e.payload?.class ?? '')}/${String(e.payload?.code ?? '')}] ${String(
            e.payload?.details ?? '',
          )}`,
        }),
        streaming: false,
        lastSeq,
      }
    case Ev.RunCancelled:
      return {
        ...v,
        entries: push('system', undefined, { tone: 'cancel', text: '已取消' }),
        streaming: false,
        lastSeq,
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
            const view = applyEvent(s.views[f.agg_id] ?? emptyView(), f.event)
            return { ...s, views: { ...s.views, [f.agg_id]: view } }
          }
          default:
            return s
        }
      })
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
        set({ sessions: await listSessions() })
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
      try {
        ensureTransport().connect()
        await promptSession(id, { prompt: text })
        set((s) => {
          const view = s.views[id] ?? emptyView()
          return { views: { ...s.views, [id]: { ...view, streaming: true } } }
        })
      } catch (err) {
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
  }
})
