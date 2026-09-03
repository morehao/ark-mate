// Settings（M2 占位）：自定义 agent / provider 配置（§4.8 / §5.3）。
import { c, s } from "../ui"

export function SettingsPage() {
  return (
    <div style={{ padding: 20, maxWidth: 960, margin: "0 auto", width: "100%", boxSizing: "border-box" }}>
      <div style={{ fontSize: 16, fontWeight: 700 }}>Settings</div>
      <div style={{ ...s.card, cursor: "default", color: c.muted, marginTop: 8 }}>
        M2 实现：自定义 agent 注册向导（definitions + 校验）、provider 配置注入（env cascade）、
        能力标记裁剪入口。当前 M1 面板不做编辑面。
      </div>
    </div>
  )
}
