import { useEffect, useRef, useState } from "react"
import { usePanel } from "./store"
import type { SessionSummary, StreamEntry } from "./types"

const c = {
  bg: "#0f1117",
  panel: "#171a23",
  border: "#262b3a",
  text: "#dbe1ee",
  muted: "#8b93a7",
  accent: "#5b8cff",
  userBg: "#1d3a5f",
  err: "#ff6b6b",
  ok: "#4cc38a",
  cancel: "#d9a13b",
  busy: "#e0a84c",
}

const s = {
  page: {
    background: c.bg,
    color: c.text,
    minHeight: "100vh",
    margin: 0,
    font: "14px/1.6 -apple-system, Segoe UI, Roboto, 'PingFang SC', 'Microsoft YaHei', sans-serif",
  },
  header: {
    display: "flex",
    alignItems: "center",
    gap: 12,
    padding: "12px 20px",
    borderBottom: `1px solid ${c.border}`,
    background: c.panel,
  },
  title: { fontSize: 16, fontWeight: 700 },
  sub: { color: c.muted, fontSize: 12 },
  dot: { width: 8, height: 8, borderRadius: "50%", display: "inline-block" },
  layout: { display: "flex", alignItems: "stretch", minHeight: "calc(100vh - 49px)" },
  rail: {
    width: 300,
    borderRight: `1px solid ${c.border}`,
    padding: 12,
    background: c.panel,
    overflowY: "auto",
    flex: "none",
    boxSizing: "border-box",
  },
  main: { flex: 1, display: "flex", flexDirection: "column", minWidth: 0, height: "calc(100vh - 49px)" },
  card: {
    background: c.bg,
    border: `1px solid ${c.border}`,
    borderRadius: 8,
    padding: "10px 12px",
    marginBottom: 8,
    cursor: "pointer",
    fontSize: 13,
  },
  cardActive: { border: `1px solid ${c.accent}` },
  chip: {
    display: "inline-block",
    padding: "0 6px",
    borderRadius: 999,
    fontSize: 11,
    marginLeft: 6,
    border: `1px solid ${c.border}`,
    color: c.muted,
    whiteSpace: "nowrap",
  },
  label: { color: c.muted, fontSize: 12, margin: "14px 0 6px", fontWeight: 600 },
  input: {
    width: "100%",
    boxSizing: "border-box",
    background: c.bg,
    color: c.text,
    border: `1px solid ${c.border}`,
    borderRadius: 6,
    padding: "8px 10px",
    font: "inherit",
    outline: "none",
  },
  btn: {
    background: c.accent,
    color: "#fff",
    border: 0,
    borderRadius: 6,
    padding: "8px 14px",
    cursor: "pointer",
    fontWeight: 600,
  },
  btnGhost: {
    background: "transparent",
    color: c.muted,
    border: `1px solid ${c.border}`,
    borderRadius: 6,
    padding: "6px 12px",
    cursor: "pointer",
  },
  transcript: { flex: 1, overflowY: "auto", padding: "16px 20px" },
  rowUser: {
    background: c.userBg,
    margin: "6px 0 6px auto",
    maxWidth: "78%",
    padding: "8px 12px",
    borderRadius: 10,
    whiteSpace: "pre-wrap",
    wordBreak: "break-word",
  },
  rowAssistant: {
    margin: "6px auto 6px 0",
    maxWidth: "85%",
    padding: "8px 12px",
    whiteSpace: "pre-wrap",
    wordBreak: "break-word",
  },
  rowThought: {
    color: c.muted,
    fontStyle: "italic",
    fontSize: 13,
    borderLeft: `3px solid ${c.border}`,
    paddingLeft: 10,
    margin: "4px 0",
    whiteSpace: "pre-wrap",
  },
  rowSystem: { textAlign: "center", color: c.muted, fontSize: 12, margin: "8px 0" },
  tool: {
    display: "inline-block",
    background: c.panel,
    border: `1px solid ${c.border}`,
    borderRadius: 6,
    padding: "3px 8px",
    margin: "2px 6px 2px 0",
    fontSize: 12,
  },
  composer: {
    borderTop: `1px solid ${c.border}`,
    padding: 12,
    display: "flex",
    gap: 8,
    alignItems: "flex-end",
  },
  caret: {
    display: "inline-block",
    width: 2,
    height: 14,
    background: c.accent,
    marginLeft: 2,
    animation: "blink 1s infinite",
  },
  toast: {
    position: "fixed",
    right: 16,
    top: 16,
    background: "#2a1d1d",
    border: `1px solid ${c.err}`,
    color: c.err,
    borderRadius: 8,
    padding: "10px 14px",
    maxWidth: 420,
    zIndex: 9,
    boxShadow: "0 4px 16px rgba(0,0,0,.5)",
  },
} as const

const statusColor: Record<string, string> = {
  in_progress: c.busy,
  completed: c.ok,
  cancelled: c.cancel,
  failed: c.err,
}

function StatusDot({ st }: { st: string }) {
  return <span style={{ ...s.dot, background: statusColor[st] ?? c.muted }} title={st} />
}

function EntryView({ e }: { e: StreamEntry }) {
  switch (e.kind) {
    case "user":
      return <div style={s.rowUser}>{e.text}</div>
    case "assistant":
      return <div style={s.rowAssistant}>{e.text}</div>
    case "thought":
      return <div style={s.rowThought}>💭 {e.text}</div>
    case "tool":
      return (
        <div style={{ margin: "2px 0" }}>
          <span style={s.tool}>
            🛠 {e.title} <span style={{ color: c.muted }}>[{e.status}]</span>
          </span>
        </div>
      )
    case "system": {
      const color =
        e.tone === "err" ? c.err : e.tone === "ok" ? c.ok : e.tone === "cancel" ? c.cancel : c.muted
      return <div style={{ ...s.rowSystem, color }}>{e.text}</div>
    }
  }
}

export function App() {
  const p = usePanel()
  const { state } = p
  const scrollRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    p.boot()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const activeId = state.activeId
  const view = activeId ? state.views[activeId] : undefined
  const activeSum = activeId ? state.sessions.find((x) => x.id === activeId) : undefined

  const [agentId, setAgentId] = useState("")
  const [prompt1, setPrompt1] = useState("")
  const [creating, setCreating] = useState(false)
  const [draft, setDraft] = useState("")

  // 新事件/流式变化时滚到底部
  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [view?.entries.length, view?.streaming])

  const canSend = !!view && !view.streaming && draft.trim().length > 0
  const canCreate = agentId !== "" && prompt1.trim().length > 0 && !creating

  const submitNew = async () => {
    if (!canCreate) return
    setCreating(true)
    try {
      await p.createAndRun(agentId, prompt1.trim())
      setPrompt1("")
    } finally {
      setCreating(false)
    }
  }

  const submitDraft = async () => {
    if (!canSend || !activeId) return
    const text = draft.trim()
    setDraft("")
    await p.sendPrompt(text)
  }

  const agentNameOf = (id: string | undefined) =>
    state.agents.find((a) => a.id === id)?.name ?? id ?? "?"

  const wsColor =
    state.wsState === "open" ? c.ok : state.wsState === "connecting" ? c.busy : c.err
  const wsLabel =
    state.wsState === "open" ? "WS 已连接" : state.wsState === "connecting" ? "连接中…" : "WS 断开"

  return (
    <div style={s.page}>
      <style>{`@keyframes blink { 0%,100%{opacity:1} 50%{opacity:0} } body{margin:0}`}</style>
      {state.toast && <div style={s.toast}>⚠ {state.toast.msg}</div>}

      <header style={s.header}>
        <span style={s.title}>ark-forge 面板 · M1 前哨</span>
        <span style={s.sub}>REST 触发 · WS 流式 · 多轮</span>
        <span style={{ flex: 1 }} />
        <span style={{ display: "inline-flex", gap: 6, alignItems: "center", fontSize: 12, color: c.muted }}>
          <span style={{ ...s.dot, background: wsColor }} />
          {wsLabel}
        </span>
        <button style={s.btnGhost} onClick={() => void p.refreshSessions()}>
          刷新会话
        </button>
      </header>

      <div style={s.layout}>
        {/* 左侧：新建 + 列表 */}
        <aside style={s.rail}>
          <div style={s.label}>新建会话</div>
          <select style={s.input} value={agentId} onChange={(e) => setAgentId(e.target.value)}>
            <option value="">选择 agent…</option>
            {state.agents.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}（{a.runtime} · {a.state}）
              </option>
            ))}
          </select>
          <textarea
            style={{ ...s.input, marginTop: 6, minHeight: 64, resize: "vertical" }}
            placeholder="首轮 prompt…"
            value={prompt1}
            onChange={(e) => setPrompt1(e.target.value)}
          />
          <button
            style={{ ...s.btn, marginTop: 6, width: "100%", opacity: canCreate ? 1 : 0.5 }}
            disabled={!canCreate}
            onClick={() => void submitNew()}
          >
            {creating ? "创建中…" : "创建并发送"}
          </button>

          <div style={s.label}>会话（{state.sessions.length}）</div>
          {state.sessions.length === 0 && <div style={{ color: c.muted, fontSize: 12 }}>暂无会话</div>}
          {state.sessions.map((sm: SessionSummary) => {
            const active = sm.id === activeId
            const v = state.views[sm.id]
            return (
              <div
                key={sm.id}
                style={active ? { ...s.card, ...s.cardActive } : s.card}
                onClick={() => p.activate(sm.id)}
                title={sm.id}
              >
                <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
                  <StatusDot st={sm.status} />
                  <span
                    style={{
                      color: c.muted,
                      fontSize: 11,
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                  >
                    {sm.id.length > 18 ? `${sm.id.slice(0, 18)}…` : sm.id}
                  </span>
                  <span style={s.chip}>{sm.runtime}</span>
                  {v?.streaming && <span style={{ color: c.busy, fontSize: 11 }}>…</span>}
                </div>
              </div>
            )
          })}
        </aside>

        {/* 主区：会话视图 + 输入 */}
        <main style={s.main}>
          {!activeId || !view ? (
            <div style={{ ...s.transcript, color: c.muted, textAlign: "center", paddingTop: 80 }}>
              从左侧选择一个会话，或新建会话开始对话
            </div>
          ) : (
            <>
              <div
                style={{
                  padding: "8px 20px",
                  borderBottom: `1px solid ${c.border}`,
                  background: c.panel,
                  fontSize: 12,
                  color: c.muted,
                  display: "flex",
                  gap: 8,
                  alignItems: "center",
                  flex: "none",
                }}
              >
                <StatusDot st={activeSum?.status ?? "in_progress"} />
                <span style={{ fontFamily: "monospace" }}>{activeId}</span>
                <span style={s.chip}>{activeSum?.runtime ?? "?"}</span>
                <span style={s.chip}>agent: {agentNameOf(activeSum?.agent_id)}</span>
                <span style={{ flex: 1 }} />
                {view.streaming && (
                  <button style={s.btnGhost} onClick={() => void p.cancel()}>
                    ⏹ 取消
                  </button>
                )}
              </div>

              <div style={s.transcript} ref={scrollRef}>
                {view.entries.length === 0 && <div style={{ color: c.muted }}>（尚无事件）</div>}
                {view.entries.map((e) => (
                  <EntryView key={e.id} e={e} />
                ))}
                {view.streaming && (
                  <div style={{ padding: "4px 0" }}>
                    <span style={s.caret} />
                  </div>
                )}
              </div>

              <div style={s.composer}>
                <textarea
                  style={{ ...s.input, flex: 1, minHeight: 44, resize: "vertical" }}
                  placeholder={view.streaming ? "agent 正在回复…" : "输入下一条 prompt（Enter 发送 / Shift+Enter 换行）"}
                  value={draft}
                  disabled={view.streaming}
                  onChange={(e) => setDraft(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !e.shiftKey) {
                      e.preventDefault()
                      void submitDraft()
                    }
                  }}
                />
                <button style={s.btn} disabled={!canSend} onClick={() => void submitDraft()}>
                  发送
                </button>
              </div>
            </>
          )}
        </main>
      </div>
    </div>
  )
}
