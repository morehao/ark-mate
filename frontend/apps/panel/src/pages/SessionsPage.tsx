// Sessions 主视图（§4.8）：会话列表 + 详情（composer → POST prompt → WS 流式渲染）。
// 支持路由 /sessions 与 /sessions/:id（URL 直达自动激活/续看）。
import { useEffect, useRef, useState } from "react"
import { useNavigate, useParams } from "react-router-dom"
import { usePanelCtx } from "../panel-context"
import type { SessionSummary } from "../types"
import { c, EntryView, s, StatusDot } from "../ui"

export function SessionsPage() {
  const p = usePanelCtx()
  const { state } = p
  const params = useParams<{ id: string }>()
  const navigate = useNavigate()
  const paramId = params.id ?? null

  const [agentId, setAgentId] = useState("")
  const [prompt1, setPrompt1] = useState("")
  const [creating, setCreating] = useState(false)
  const [draft, setDraft] = useState("")

  const scrollRef = useRef<HTMLDivElement | null>(null)
  const lastParamRef = useRef<string | null | undefined>(undefined)

  // URL → 激活（刷新/直达 /sessions/:id 续看；参数不变不重复 attach）
  useEffect(() => {
    if (lastParamRef.current === paramId) return
    lastParamRef.current = paramId
    if (paramId) p.activate(paramId)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paramId])

  const activeId = state.activeId
  const view = activeId ? state.views[activeId] : undefined
  const activeSum = activeId ? state.sessions.find((x) => x.id === activeId) : undefined

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
      const id = await p.createAndRun(agentId, prompt1.trim())
      setPrompt1("")
      if (id) navigate(`/sessions/${id}`)
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

  const agentNameOf = (id: string | undefined) => state.agents.find((a) => a.id === id)?.name ?? id ?? "?"

  return (
    <div style={{ ...s.layout, flex: 1 }}>
      {/* 左栏：新建 + 会话列表 */}
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
              onClick={() => navigate(`/sessions/${sm.id}`)}
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
  )
}
