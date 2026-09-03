import { useCallback, useEffect, useReducer, useRef, useState } from "react"
import {
  asApiError,
  cancelSession,
  createSession,
  listAgents,
  listSessions,
  promptSession,
} from "./api"
import { WsTransport } from "./transport/ws"
import type {
  Agent,
  BusEvent,
  EntryKind,
  SessionSummary,
  StreamEntry,
  WsServerFrame,
} from "./types"
import { Ev } from "./types"

export type WsState = "idle" | "connecting" | "open" | "closed"

export interface SessionView {
  entries: StreamEntry[]
  streaming: boolean
  lastSeq: number
}

export interface PanelState {
  agents: Agent[]
  sessions: SessionSummary[]
  views: Record<string, SessionView>
  activeId: string | null
  wsState: WsState
  toast: { code: number; msg: string } | null
}

const emptyView: SessionView = { entries: [], streaming: false, lastSeq: 0 }

let uidCounter = 0
const nextId = (): string => `e${++uidCounter}`

export const initialState: PanelState = {
  agents: [],
  sessions: [],
  views: {},
  activeId: null,
  wsState: "idle",
  toast: null,
}

/** 事件应用（纯函数）：user/assistant/thought 增量合并、tool 卡更新、终态收流。 */
export function applyEvent(v: SessionView, e: BusEvent): SessionView {
  const entries = v.entries
  const lastSeq = Math.max(v.lastSeq, e.seq)
  const text = String(e.payload?.text ?? "")

  const push = (kind: EntryKind, text_?: string, extra?: Partial<StreamEntry>): StreamEntry[] =>
    [...entries, { kind, id: nextId(), text: text_, ...extra }]

  const appendToTail = (kind: EntryKind, text_: string): StreamEntry[] => {
    const tail = entries[entries.length - 1]
    if (tail && tail.kind === kind && typeof tail.text === "string") {
      const next = [...entries]
      next[next.length - 1] = { ...tail, text: tail.text + text_ }
      return next
    }
    return push(kind, text_)
  }

  switch (e.type) {
    case Ev.UserMessage:
      return { ...v, entries: push("user", text), lastSeq }
    case Ev.Message:
      return { ...v, entries: appendToTail("assistant", text), lastSeq }
    case Ev.Thought:
      return { ...v, entries: appendToTail("thought", text), lastSeq }
    case Ev.ToolCall: {
      const id = String(e.payload?.tool_call_id ?? "")
      const title = String(e.payload?.title ?? id)
      const status = String(e.payload?.status ?? "")
      return { ...v, entries: push("tool", undefined, { toolId: id, title, status }), lastSeq }
    }
    case Ev.ToolUpdate: {
      const id = String(e.payload?.tool_call_id ?? "")
      return {
        ...v,
        entries: entries.map((en) =>
          en.kind === "tool" && en.toolId === id
            ? {
                ...en,
                title: String(e.payload?.title ?? en.title ?? id),
                status: String(e.payload?.status ?? en.status ?? ""),
              }
            : en,
        ),
        lastSeq,
      }
    }
    case Ev.TurnEnded:
      return {
        ...v,
        entries: push("system", undefined, {
          tone: "ok",
          text: `完成 · ${String(e.payload?.stop_reason ?? "")}`,
        }),
        streaming: false,
        lastSeq,
      }
    case Ev.TurnError:
      return {
        ...v,
        entries: push("system", undefined, {
          tone: "err",
          text: `失败 [${String(e.payload?.class ?? "")}/${String(e.payload?.code ?? "")}] ${String(
            e.payload?.details ?? "",
          )}`,
        }),
        streaming: false,
        lastSeq,
      }
    case Ev.RunCancelled:
      return {
        ...v,
        entries: push("system", undefined, { tone: "cancel", text: "已取消" }),
        streaming: false,
        lastSeq,
      }
    case Ev.Usage:
      return { ...v, lastSeq }
    default:
      return { ...v, lastSeq }
  }
}

export type PanelAction =
  | { type: "frame"; f: WsServerFrame }
  | { type: "agents"; agents: Agent[] }
  | { type: "sessions"; sessions: SessionSummary[] }
  | { type: "activate"; id: string | null }
  | { type: "resetView"; id: string }
  | { type: "createLocal"; id: string; text: string }
  | { type: "markStreaming"; id: string }
  | { type: "toast"; toast: { code: number; msg: string } | null }

export function reducer(s: PanelState, a: PanelAction): PanelState {
  switch (a.type) {
    case "agents":
      return { ...s, agents: a.agents }
    case "sessions":
      return { ...s, sessions: a.sessions }
    case "activate":
      return { ...s, activeId: a.id }
    case "resetView": {
      const cur = s.views[a.id]
      if (cur && cur.entries.length === 0) return s
      return { ...s, views: { ...s.views, [a.id]: { entries: [], streaming: false, lastSeq: 0 } } }
    }
    case "createLocal": {
      const view = s.views[a.id] ?? emptyView
      const entries = [...view.entries, { kind: "user" as const, id: nextId(), text: a.text }]
      return { ...s, views: { ...s.views, [a.id]: { ...view, entries } } }
    }
    case "markStreaming": {
      const view = s.views[a.id] ?? emptyView
      return { ...s, views: { ...s.views, [a.id]: { ...view, streaming: true } } }
    }
    case "toast":
      return { ...s, toast: a.toast }
    case "frame": {
      const f = a.f
      switch (f.type) {
        case "snapshot": {
          const view = s.views[f.agg_id] ?? emptyView
          return {
            ...s,
            views: { ...s.views, [f.agg_id]: { ...view, lastSeq: Math.max(view.lastSeq, f.event_seq) } },
          }
        }
        case "event": {
          const view = applyEvent(s.views[f.agg_id] ?? emptyView, f.event)
          return { ...s, views: { ...s.views, [f.agg_id]: view } }
        }
        case "detached":
          return s
        case "pong":
          return s
        default:
          return s
      }
    }
    default:
      return s
  }
}

/** 面板控制器：REST 动作 + WS 订阅接线（transport 常驻，重连/重订阅自处理）。 */
export function usePanel() {
  const [state, dispatch] = useReducer(reducer, initialState)
  const transportRef = useRef<WsTransport | null>(null)
  const [wsState, setWsState] = useState<WsState>("idle")

  useEffect(() => {
    if (transportRef.current) return
    const t = new WsTransport(
      (f) => {
        // 水位推进（供重连续传）
        if (f.type === "event") t.ackSeq(f.agg_id, f.event.seq)
        if (f.type === "snapshot") t.ackSeq(f.agg_id, f.event_seq)
        dispatch({ type: "frame", f })
      },
      (st) => {
        setWsState(st === "open" ? "open" : st === "connecting" ? "connecting" : "closed")
      },
    )
    transportRef.current = t
    return () => {
      t.close()
      transportRef.current = null
    }
  }, [])

  const showToast = useCallback((msg: string, code = -1) => {
    dispatch({ type: "toast", toast: { code, msg } })
    setTimeout(() => dispatch({ type: "toast", toast: null }), 4000)
  }, [])

  const refreshAgents = useCallback(async () => {
    try {
      dispatch({ type: "agents", agents: await listAgents() })
    } catch (err) {
      showToast(asApiError(err).msg)
    }
  }, [showToast])

  const refreshSessions = useCallback(async () => {
    try {
      dispatch({ type: "sessions", sessions: await listSessions() })
    } catch (err) {
      showToast(asApiError(err).msg)
    }
  }, [showToast])

  const boot = useCallback(() => {
    void refreshAgents()
    void refreshSessions()
  }, [refreshAgents, refreshSessions])

  const activate = useCallback(
    (id: string | null, sinceSeq?: number) => {
      const t = transportRef.current
      const prev = state.activeId
      if (prev && prev !== id) t?.detach(prev)
      if (id) {
        t?.connect()
        if (t && !t.isAttached(id)) {
          // 首次挂载/切回：视图按重放重建（防重复）
          dispatch({ type: "resetView", id })
          t.attach(id, sinceSeq)
        }
        dispatch({ type: "activate", id })
      } else {
        dispatch({ type: "activate", id: null })
      }
    },
    [state.activeId],
  )

  /** 新建会话：POST /sessions（首轮即后台跑）→ attach（since=event_seq 续传首轮）→ 高亮。
   *  本地先落一条用户气泡（echo 事件 seq 与水位相等，重放会跳过它）。 */
  const createAndRun = useCallback(
    async (agentId: string, prompt: string) => {
      try {
        const resp = await createSession(agentId, prompt)
        dispatch({ type: "resetView", id: resp.id })
        dispatch({ type: "createLocal", id: resp.id, text: prompt })
        dispatch({ type: "markStreaming", id: resp.id })
        activate(resp.id, resp.event_seq)
        void refreshSessions()
      } catch (err) {
        showToast(asApiError(err).msg)
      }
    },
    [activate, refreshSessions, showToast],
  )

  /** 追加 prompt：REST 触发 → 置 streaming（用户回显经 WS live/重放到达；若 socket 断开，
   *  connect 重连后 transport 会按水位自动重订阅补齐缺口）。 */
  const sendPrompt = useCallback(
    async (text: string) => {
      const id = state.activeId
      if (!id) return
      try {
        transportRef.current?.connect()
        await promptSession(id, text)
        dispatch({ type: "markStreaming", id })
      } catch (err) {
        showToast(asApiError(err).msg)
      }
    },
    [state.activeId, showToast],
  )

  const cancel = useCallback(async () => {
    const id = state.activeId
    if (!id) return
    try {
      await cancelSession(id)
    } catch (err) {
      showToast(asApiError(err).msg)
    }
  }, [state.activeId, showToast])

  return {
    state: { ...state, wsState },
    boot,
    refreshAgents,
    refreshSessions,
    activate,
    createAndRun,
    sendPrompt,
    cancel,
    showToast,
  }
}
