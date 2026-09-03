// Runtimes 只读列表页（M1 #2；能力基线/版本展示 M2 随 definitions 目录，§4.4/§5.3）。
import { useEffect } from "react"
import { usePanelCtx } from "../panel-context"
import { c, s } from "../ui"

export function RuntimesPage() {
  const p = usePanelCtx()
  const { runtimes } = p.state
  useEffect(() => {
    void p.refreshRuntimes()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div style={{ padding: 20, maxWidth: 960, margin: "0 auto", width: "100%", boxSizing: "border-box" }}>
      <div style={{ fontSize: 16, fontWeight: 700 }}>Runtimes</div>
      <div style={s.sub}>可用 ACP runtime（opencode / deepseek-harness）；启动规格见 README 前置表</div>
      {runtimes.length === 0 && <div style={{ color: c.muted, marginTop: 12 }}>（空）</div>}
      {runtimes.map((rt) => (
        <div key={rt.name} style={{ ...s.card, cursor: "default", display: "flex", alignItems: "center", gap: 10 }}>
          <span style={{ fontSize: 16 }}>🧩</span>
          <b>{rt.name}</b>
          <span style={{ color: c.muted, fontSize: 12 }}>{rt.version}</span>
        </div>
      ))}
      <button style={{ ...s.btnGhost, marginTop: 8 }} onClick={() => void p.refreshRuntimes()}>
        刷新
      </button>
    </div>
  )
}
