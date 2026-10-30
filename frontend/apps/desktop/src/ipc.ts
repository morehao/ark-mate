/**
 * 主进程侧的 IPC 处理（M1-4）——它是 preload 暴露面的**另一端**。
 *
 * 设计约束（设计 §7.3）：
 *   - 这里只实现"渲染进程做不了的事"：现在只剩**宿主原生目录对话框**一条
 *     （`mate:directory-pick`）——真实文件路径 / 用系统程序打开 / 窗口控制那三类已随
 *     执行面收窄删除（2026-10-11，P3/D5：两端实现完整但界面（`apps/web/src`，现 `packages/console/src`）零调用）；
 *   - **不提供 `capabilities()`**：能力**声明**走 HTTP（`GET /v1/mate/capabilities`，两端同源同路径），
 *     注入面只承载执行面；
 *   - handler 只服务**当前窗口**（单窗口形态下即主窗口），不接受渲染进程传来的窗口标识；
 *   - 渲染进程不指定任何 URL/scheme：剩下这条 handler 不接路径参数，`dialog` 由用户选。
 *
 * M1-5 会给每个 handler 补 origin 校验；本轮先由 preload 侧的暴露前置守卫把关
 * （不满足 origin 就连桥都不建，IPC 无从发起）。
 */
import { BrowserWindow, dialog, ipcMain, type IpcMainInvokeEvent } from 'electron'

import { isDesktopCarrier } from '@ark-mate/desktop-contract'
import { CH } from './channels'

/**
 * IPC 调用方必须是**桌面载体的主框架**（M1-5，设计 §9.2 的"IPC 与 origin 校验"。
 *
 * 契约里的判定函数吃的是 `{protocol, hostname}`（location 形态），而 Electron 给的是
 * `WebFrameMain`（只有 `url`），因此这里做一次**适配**——判定规则本身仍只有契约那一份。
 * `frame.parent === null` 即主框架；`senderFrame` 可能为 null（帧已销毁）→ 判否。
 */
function isCarrierMainFrame(event: IpcMainInvokeEvent): boolean {
  const frame = event.senderFrame
  if (!frame) return false
  try {
    const u = new URL(frame.url)
    return isDesktopCarrier({ protocol: u.protocol, hostname: u.hostname }, frame.parent === null)
  } catch {
    return false
  }
}

/**
 * 每个 handler 的第一道闸：不是载体主框架就**拒绝**。
 *
 * 为什么是抛错而不是 `return`：`return null` 在调用方看来与"用户取消了"完全一样
 * （目录选择器本来就以 `null` 表示取消），静默放行会把一次越权尝试伪装成一次正常取消。
 * 抛错会经 `ipcRenderer.invoke` 变成页面的 rejection，日志里也留一条。
 */
function denyNonCarrier(event: IpcMainInvokeEvent, channel: string): void {
  console.warn(`[shell] IPC 拒绝：${channel} 的调用方不是桌面载体主框架（${event.senderFrame?.url ?? 'unknown'}）`)
  throw new Error(`IPC 拒绝：${channel} 只对桌面载体主框架开放`)
}

export type IpcDeps = {
  /** 取当前核心句柄（boot 要下发给页面的流基址）。 */
  streamEndpoint: () => { streamBaseUrl: string } | null
  /** 取当前窗口（原生对话框的父窗口）。 */
  window: () => BrowserWindow | null
}

/**
 * 安装 IPC handlers。**幂等**：重复调用会覆盖前一份（Electron 的 handle 语义），
 * 因此调用点只需保证在开窗口之前跑一次。
 */
export function installIpcHandlers(deps: IpcDeps): void {
  // boot：页面据此建事件流。
  ipcMain.handle(CH.boot, (event) => {
    if (!isCarrierMainFrame(event)) denyNonCarrier(event, CH.boot)
    const ep = deps.streamEndpoint()
    if (!ep) throw new Error('核心尚未就绪，无法下发事件流基址')
    return ep
  })

  // 原生目录选择器（浏览器入口的等价物是服务端目录浏览 API，见设计 §10.1）。
  ipcMain.handle(CH.directoryPick, async (event) => {
    if (!isCarrierMainFrame(event)) denyNonCarrier(event, CH.directoryPick)
    const win = deps.window()
    const opts = { properties: ['openDirectory' as const], title: '选择工作目录' }
    const r = win ? await dialog.showOpenDialog(win, opts) : await dialog.showOpenDialog(opts)
    if (r.canceled || r.filePaths.length === 0) return null
    return r.filePaths[0]
  })
}
