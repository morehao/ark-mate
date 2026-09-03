// Agents 只读列表页（M1 #2；M2 增 CRUD/注册向导，§4.8）。
import { useEffect } from "react"
import { usePanelCtx } from "../panel-context"
import { c, s, StatusDot } from "../ui"

export function AgentsPage() {
  const p = usePanelCtx()
  const { agents } = p.state
  useEffect(() => {
    void p.refreshAgents()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div style={{ padding: 20, maxWidth: 960, margin: "0 auto", width: "100%", boxSizing: "border-box" }}>
      <div style={{ fontSize: 16, fontWeight: 700 }}>Agents</div>
      <div style={s.sub}>每 runtime 一个默认播种 Agent；CRUD/自定义注册为 M2（§4.4/§5.3）</div>
      {agents.length === 0 && <div style={{ color: c.muted, marginTop: 12 }}>（空）</div>}
      {agents.map((a) => (
        <div key={a.id} style={{ ...s.card, cursor: "default", display: "flex", alignItems: "center", gap: 10 }}>
          <StatusDot st={a.state} />
          <b>{a.name}</b>
          <span style={s.chip}>{a.runtime}</span>
          <span style={s.chip}>{a.state}</span>
          <span style={{ color: c.muted, fontSize: 12, fontFamily: "monospace", marginLeft: "auto" }}>{a.id}</span>
        </div>
      ))}
      <button style={{ ...s.btnGhost, marginTop: 8 }} onClick={() => void p.refreshAgents()}>
        刷新
      </button>
    </div>
  )
}
