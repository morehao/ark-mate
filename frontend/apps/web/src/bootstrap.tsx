/**
 * 应用启动（M2-4）：**先过版本闸门，再挂载界面**。
 *
 * 顺序是有意的：闸门不过时不该把应用树拉起来（拉起来就会去拉数据、连事件流，
 * 然后在"版本不对"的根因上刷一堆衍生错误）。闸门过了才 createRoot。
 */
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { AppShell } from '@ark-mate/ui'
import { App } from './app'
import { loadDirectoryPickerCapability } from './directoryPicker'
import { renderVersionMismatch, runVersionGate } from './versionGate'

export async function bootstrap(): Promise<void> {
  const root = document.getElementById('root')
  if (!root) throw new Error('缺少 #root 挂载点')

  const gate = await runVersionGate()
  if (gate.unverifiable) {
    // 无法判定：放行 + 留痕（核心里没有 build-id.txt 是合法状态，不该把界面锁死）。
    console.warn(`[version-gate] 无法校验前端与核心版本是否一致：${gate.unverifiable}`)
  }
  if (!gate.ok && gate.mismatch) {
    // 混搭：整屏状态页（设计 §6.7），不是白屏、不是零散接口报错。
    console.error(`[version-gate] 版本不一致：前端 ${gate.mismatch.web} / 核心 ${gate.mismatch.core}`)
    renderVersionMismatch(root, gate.mismatch.web, gate.mismatch.core)
    return
  }

  // 目录选择器的形态是**服务端声明**的（宿主能不能抬系统面板），所以在挂载界面前读一次：
  // 挂载后再读会让"第一次点选目录"落在错的那条路上（本机浏览器本该用系统面板却开了应用内对话框）。
  // 失败不阻断：拉不到就按 browse（应用内浏览那半永远可用）。
  await loadDirectoryPickerCapability()

  createRoot(root).render(
    <StrictMode>
      <AppShell>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </AppShell>
    </StrictMode>,
  )
}
