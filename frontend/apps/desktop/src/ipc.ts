/**
 * 主进程侧的 IPC 处理（M1-4）——它是 preload 暴露面的**另一端**。
 *
 * 设计约束（设计 §7.3）：
 *   - 这里只实现"渲染进程做不了的事"：原生对话框、真实文件路径、用系统程序打开、窗口控制；
 *   - **不提供 `capabilities()`**：能力**声明**走 HTTP（`GET /v1/mate/capabilities`，两端同源同路径），
 *     注入面只承载执行面；
 *   - handler 只服务**当前窗口**（单窗口形态下即主窗口），不接受渲染进程传来的窗口标识；
 *   - 渲染进程不能指定任意 URL/scheme：`mate:open-path` 只接受"路径"或"页面名"两种意图。
 *
 * M1-5 会给每个 handler 补 origin 校验；本轮先由 preload 侧的暴露前置守卫把关
 * （不满足 origin 就连桥都不建，IPC 无从发起）。
 */
import { BrowserWindow, dialog, ipcMain, shell, type IpcMainInvokeEvent } from 'electron'

import { isDesktopCarrier, type Rect } from '@ark-mate/desktop-contract'
import { CH, type OpenTarget } from './channels'

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
 * 为什么是抛错而不是 `return`：`return ''`/`return null` 在调用方看来与"成功"无异
 * （`shell.openPath` 的约定就是"空串=成功"），静默放行会把一次越权尝试伪装成正常结果。
 * 抛错会经 `ipcRenderer.invoke` 变成页面的 rejection，日志里也留一条。
 */
function denyNonCarrier(event: IpcMainInvokeEvent, channel: string): void {
  console.warn(`[shell] IPC 拒绝：${channel} 的调用方不是桌面载体主框架（${event.senderFrame?.url ?? 'unknown'}）`)
  throw new Error(`IPC 拒绝：${channel} 只对桌面载体主框架开放`)
}

export type IpcDeps = {
  /** 取当前核心句柄（boot 要下发给页面的流基址与 streamToken）。 */
  streamEndpoint: () => { streamBaseUrl: string; streamToken: string } | null
  /** 核心的 HTTP 根（打开"用量/充值"页面用；核心未就绪时返回 null）。 */
  coreBaseUrl: () => string | null
  /** 取当前窗口（对话框父窗口 / 窗口控制目标）。 */
  window: () => BrowserWindow | null
}

/**
 * 安装 IPC handlers。**幂等**：重复调用会覆盖前一份（Electron 的 handle 语义），
 * 因此调用点只需保证在开窗口之前跑一次。
 */
export function installIpcHandlers(deps: IpcDeps): void {
  // boot：页面据此建事件流。返回的 streamToken **只在渲染进程内存中**
  // （不落 localStorage —— M1-5 的验收会搜一遍存储面；前端侧由 M1-8 保证）。
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

  // 打开"一个东西"。两类意图共用一个冻结频道（§0.3 的表里只有 mate:open-path）：
  //   - {kind:'page'} → 核心托管的浏览器入口页面（用量/充值），系统浏览器打开；
  //   - {kind:'path'} → http(s) 交给浏览器，其余交给系统默认程序（shell.openPath 的返回值即错误文案）。
  ipcMain.handle(CH.openPath, async (event, target: unknown) => {
    if (!isCarrierMainFrame(event)) denyNonCarrier(event, CH.openPath)
    const t = target as OpenTarget
    if (t?.kind === 'page') {
      const base = deps.coreBaseUrl()
      if (!base) return '核心尚未就绪'
      await shell.openExternal(`${base}/${t.page}`)
      return ''
    }
    if (t?.kind !== 'path') return '未知的打开目标'
    const s = t.path
    if (/^https?:\/\//i.test(s)) {
      await shell.openExternal(s)
      return ''
    }
    if (s === '') return '路径为空'
    return shell.openPath(s)
  })

  ipcMain.handle(CH.platformBounds, async (event, bounds: Rect) => {
    if (!isCarrierMainFrame(event)) denyNonCarrier(event, CH.platformBounds)
    deps.window()?.setBounds({ x: bounds.x, y: bounds.y, width: bounds.width, height: bounds.height })
  })

  ipcMain.handle(CH.platformClose, async (event) => {
    if (!isCarrierMainFrame(event)) denyNonCarrier(event, CH.platformClose)
    deps.window()?.close()
  })
}
