// 面板全局 store Provider：usePanel 只挂载一次（transport 常驻），
// 路由切页不重建 WS/会话视图（跨页保活，§4.8「连接即订阅」语义）。
import { createContext, useContext, useEffect, type ReactNode } from "react"
import { usePanel } from "./store"

export type PanelCtx = ReturnType<typeof usePanel>

const Ctx = createContext<PanelCtx | null>(null)

export function PanelProvider({ children }: { children: ReactNode }) {
  const p = usePanel()
  // 挂载即拉取 agents/sessions/runtimes（跨页共享一份状态）
  useEffect(() => {
    p.boot()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  return <Ctx.Provider value={p}>{children}</Ctx.Provider>
}

export function usePanelCtx(): PanelCtx {
  const p = useContext(Ctx)
  if (!p) throw new Error("usePanelCtx 必须在 <PanelProvider> 内使用")
  return p
}
