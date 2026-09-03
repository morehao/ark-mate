// 共享视觉常量与小组件（panel 各页复用；M1 内联样式，无样式框架）。
import type { StreamEntry } from "./types"

export const c = {
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

export const s = {
  page: {
    background: c.bg,
    color: c.text,
    minHeight: "100vh",
    margin: 0,
    font: "14px/1.6 -apple-system, Segoe UI, Roboto, 'PingFang SC', 'Microsoft YaHei', sans-serif",
    display: "flex",
    flexDirection: "column",
  },
  header: {
    display: "flex",
    alignItems: "center",
    gap: 12,
    padding: "12px 20px",
    borderBottom: `1px solid ${c.border}`,
    background: c.panel,
    flex: "none",
  } as const,
  title: { fontSize: 16, fontWeight: 700 },
  sub: { color: c.muted, fontSize: 12 },
  dot: { width: 8, height: 8, borderRadius: "50%", display: "inline-block" },
  layout: { display: "flex", alignItems: "stretch", flex: 1, minHeight: 0 },
  rail: {
    width: 300,
    borderRight: `1px solid ${c.border}`,
    padding: 12,
    background: c.panel,
    overflowY: "auto",
    flex: "none",
    boxSizing: "border-box",
  },
  main: { flex: 1, display: "flex", flexDirection: "column", minWidth: 0, minHeight: 0 },
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
  banner: {
    display: "flex",
    alignItems: "center",
    gap: 8,
    padding: "6px 20px",
    fontSize: 12,
    background: "#2a2416",
    color: c.busy,
    borderBottom: `1px solid ${c.border}`,
  } as const,
} as const

// 会话状态/agent 状态 → 颜色（两份词典合并查询）。
const toneColor: Record<string, string> = {
  in_progress: c.busy,
  completed: c.ok,
  cancelled: c.cancel,
  failed: c.err,
  running: c.ok,
  stopped: c.muted,
}

export function StatusDot({ st }: { st: string }) {
  return <span style={{ ...s.dot, background: toneColor[st] ?? c.muted }} title={st} />
}

export function EntryView({ e }: { e: StreamEntry }) {
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
      const color = e.tone === "err" ? c.err : e.tone === "ok" ? c.ok : e.tone === "cancel" ? c.cancel : c.muted
      return <div style={{ ...s.rowSystem, color }}>{e.text}</div>
    }
  }
}
