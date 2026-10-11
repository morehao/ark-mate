/**
 * 桌面壳主进程（M1-6：启动链 + 托盘 + 退出语义）。
 *
 * 启动链（设计 §7.1）：
 *   单实例锁 claim → 注册并锁定 `mate-app://` scheme → spawn 核心（`:0` + 双凭据经 stdin
 *   + `--ready-stdout` + `--exit-with-parent`）→ 读就绪行拿地址 → health 复核
 *   → 开窗口 `loadURL('mate-app://app/')` → 壳服务 index/assets、反代 `/v1/*`（注入 restToken）。
 *
 * 退出语义（设计 §7.4，M1-6）：
 *   关窗 → 隐藏到托盘（首次给一次持久化确认）→ 真正退出时：**先问核心有没有在途轮次**
 *   （2s 上限，超时按"有任务"保守处理）→ 有则二次确认 → `SIGTERM` → 宽限 3s → 对**进程组** `SIGKILL`。
 *   壳被 `kill -9` 时这条链断在中途，由核心自己的 `--exit-with-parent` 兜底。
 *
 * 令牌分层（设计 §7.2）：`restToken` 只在主进程出现（本文件 + scheme 反代）；
 * `streamToken` 经 `boot()` 下发给页面。两者都不进 argv/env/文件/URL。
 */
import { app, BrowserWindow, dialog, shell } from 'electron'
import { join } from 'node:path'

import { APP_ORIGIN, SHELL_ORIGIN } from '@ark-mate/desktop-contract'
import { probeRunningTurns, startCore, stopCoreGracefully, type CoreHandle } from './core'
import { installIpcHandlers } from './ipc'
import { coreExecutable, rendererDir, webDir } from './paths'
import { readPrefs, writePrefs } from './prefs'
import { installSchemeHandler, registerSchemePrivileges, shellPageUrl } from './scheme'
import { installTray, type TrayHandle } from './tray'

/** 壳日志前缀：与核心/子进程的 stdout 混在一起时好区分。 */
const LOG_PREFIX = '[shell]'

const log = (...parts: unknown[]): void => console.log(LOG_PREFIX, ...parts)

// scheme 特权必须在 app.ready 之前注册（Electron 硬要求），因此放在模块顶层。
registerSchemePrivileges()

let core: CoreHandle | null = null
let win: BrowserWindow | null = null
let tray: TrayHandle | null = null
/** 核心起不来时的原因（窗口改显示恢复页；null = 正常）。 */
let bootError: string | null = null
/** 退出流程已批准（避免 before-quit 二次进入）。 */
let quitting = false

// ——————————————————————————— 窗口 ———————————————————————————

function openMainWindow(): BrowserWindow {
  const url = bootError ? shellPageUrl('boot-failed.html', SHELL_ORIGIN) : `${APP_ORIGIN}/`
  const w = createWindow(url)
  win = w
  return w
}

function toggleWindow(): void {
  const w = win
  if (!w) {
    openMainWindow()
    return
  }
  if (w.isVisible() && !w.isMinimized()) {
    w.hide()
    return
  }
  if (w.isMinimized()) w.restore()
  w.show()
  w.focus()
}

function createWindow(url: string): BrowserWindow {
  const w = new BrowserWindow({
    width: 1280,
    height: 840,
    show: false,
    // = tokens.bg（壳页与共享 UI 的同一底色；真源是 renderer/shell-tokens.css 的 --fg-bg）。
    backgroundColor: '#f7f8fa',
    webPreferences: {
      // 加固基线（设计 §9.2）六项：这四项在这里；CSP 在 scheme.ts（两处下发同一策略）、
      // 导航与权限在 hardenWindow()、IPC origin 校验在 ipc.ts。
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      webviewTag: false,
      preload: join(app.getAppPath(), 'dist', 'preload-app.cjs'),
    },
  })

  // 关窗 = 隐藏到托盘（首次给一次持久化确认，T10）。
  w.on('close', (e) => {
    if (quitting) return
    e.preventDefault()
    w.hide()
    void closeToTrayNotice(w)
  })

  void w.loadURL(url)
  hardenWindow(w)
  w.once('ready-to-show', () => {
    w.show()
    log(`window ready-to-show url=${url} visible=${w.isVisible()}`)
  })
  w.webContents.on('did-fail-load', (_e, code, desc, failedUrl) =>
    log(`did-fail-load code=${code} desc=${desc} url=${failedUrl}`),
  )
  w.webContents.on('render-process-gone', (_e, details) =>
    log(`render-process-gone reason=${details.reason} exitCode=${details.exitCode}`),
  )
  w.on('closed', () => {
    log('window closed')
    if (win === w) win = null
  })
  return w
}

/**
 * 首次"关窗只是隐藏"的持久化确认（T10 的文案定在这里）。
 *
 * 存在的理由：用户点红点后进程还在跑，是最容易让人以为"已经退出"的体验坑；
 * 而托盘图标在 macOS 菜单栏里很容易被忽略。提示一次、记住选择，之后不再打扰。
 */
async function closeToTrayNotice(w: BrowserWindow): Promise<void> {
  if (readPrefs().trayNoticeShown) return
  const r = await dialog.showMessageBox(w, {
    type: 'info',
    message: 'ark-mate 仍在后台运行',
    detail:
      '关窗只是把窗口收进托盘：会话与定时任务都不受影响。\n' +
      '想彻底退出，请用菜单栏托盘的「退出 ark-mate」，或按 ⌘Q。',
    buttons: ['明白了', '现在就退出'],
    defaultId: 0,
    cancelId: 0,
    checkboxLabel: '不再提示',
    checkboxChecked: true,
  })
  if (r.checkboxChecked) writePrefs({ trayNoticeShown: true })
  if (r.response === 1) void requestQuit()
}

/**
 * 渲染进程加固的"窗口侧"三项（M1-5，设计 §9.2）：
 *
 *   1. **导航**：只放行自己的 app origin；`http(s)` 交给系统浏览器；其余（`file:`、`javascript:`、
 *      `mate://`、任何第三方 origin）一律 `preventDefault` 拦下——桌面窗口不该变成浏览器；
 *   2. **新窗口**：`setWindowOpenHandler` 一律 `deny`；要开外部链接就走系统浏览器。
 *      将来的多窗口是**我们自己的入口**（按工作区开窗），不是页面 `window.open` 出来的；
 *   3. **权限**：默认**全拒**（当前不需要任何权限）。request 与 check 两个 handler 都装——
 *      只装 request 会让同步检查那条路径漏过去。
 */
function hardenWindow(w: BrowserWindow): void {
  const appPrefix = `${APP_ORIGIN}/`

  w.webContents.on('will-navigate', (e, url) => {
    if (url.startsWith(appPrefix)) return
    e.preventDefault()
    if (/^https?:\/\//i.test(url)) {
      log(`will-navigate 外部链接交给系统浏览器：${url}`)
      void shell.openExternal(url)
      return
    }
    log(`will-navigate 已拦截：${url}`)
  })

  w.webContents.setWindowOpenHandler(({ url }) => {
    if (/^https?:\/\//i.test(url)) {
      log(`window.open 外部链接交给系统浏览器：${url}`)
      void shell.openExternal(url)
    } else {
      log(`window.open 已拦截：${url}`)
    }
    return { action: 'deny' }
  })

  const ses = w.webContents.session
  ses.setPermissionRequestHandler((_wc, permission, callback) => {
    log(`权限请求被拒：${permission}`)
    callback(false)
  })
  ses.setPermissionCheckHandler((_wc, permission) => {
    log(`权限同步检查被拒：${permission}`)
    return false
  })
}

// ——————————————————————————— 深链接 ———————————————————————————

/**
 * `mate://…` 深链接（设计 §7.1：单实例转发）。
 *
 * 本轮只做**最小可用**：聚焦窗口，若 URL 带路径就转到同源的 app 路由
 * （`mate://sessions/abc` → `mate-app://app/sessions/abc`）。
 * 设计文档没有规定深链接的语义映射表（哪条路径开哪个视图），因此不在这里自造规则；
 * 定了之后（与 M5-1 一起）只改这一处。
 */
function handleDeepLink(url: string): void {
  log(`深链接：${url}`)
  const w = win ?? openMainWindow()
  if (w.isMinimized()) w.restore()
  w.show()
  w.focus()
  try {
    const u = new URL(url)
    const path = `${u.hostname}${u.pathname}`.replace(/^\/+|\/+$/g, '')
    if (path) void w.loadURL(`${APP_ORIGIN}/${path}`)
  } catch {
    log(`深链接不是合法 URL，仅聚焦窗口：${url}`)
  }
}

/** 从 argv 里挑出 `mate://…`（第二次启动 / Windows·Linux 的深链接入口）。 */
function deepLinkFromArgv(argv: string[]): string | null {
  return argv.find((a) => a.startsWith('mate://')) ?? null
}

// ——————————————————————————— 退出 ———————————————————————————

/**
 * 退出流程（设计 §7.4）：问在途轮次 → 必要时二次确认 → SIGTERM → 宽限 3s → 进程组 SIGKILL。
 *
 * `probeRunningTurns` 超时/失败时按"有任务"保守处理：宁可多问一句，
 * 也不要静默打断一轮正在跑的对话（会话本身可续聊，但用户会莫名其妙）。
 */
async function requestQuit(): Promise<void> {
  if (quitting) return
  const { known, running } = await probeRunningTurns(core)
  const hasTasks = !known || running > 0
  if (hasTasks) {
    const detail = known
      ? `当前有 ${running} 个会话正在运行。退出会中断它们——会话记录保留，重开后可续聊重发。`
      : '2 秒内没能确认是否有会话在运行（按有任务保守处理）。退出可能中断在途轮次——会话记录保留，可续聊重发。'
    const r = await dialog.showMessageBox({
      type: 'warning',
      message: '仍有会话在运行，确定退出？',
      detail,
      buttons: ['取消', '仍然退出'],
      defaultId: 0,
      cancelId: 0,
    })
    if (r.response === 0) {
      log('用户在二次确认里取消了退出')
      return
    }
  }
  quitting = true
  await stopCoreGracefully(core, { log })
  core = null
  app.quit()
}

// ——————————————————————————— 开机自启 ———————————————————————————

/**
 * 应用开机自启偏好。**默认绝不写登录项**（设计 §8：只在用户显式开启时写）。
 * 这里只在偏好为 true 时调用——即"用户过去显式开过"。
 */
function applyLoginItemPreference(): void {
  if (!readPrefs().openAtLogin) return
  // 不用 openAsHidden：Electron 44 的 Settings 里已没有这个字段（macOS 13+ 的登录项语义也变了），
  // 而"开机自启但窗口先藏着"对桌面形态没有实际收益——真需要时由壳在启动参数里判断。
  app.setLoginItemSettings({ openAtLogin: true })
  log('已按用户偏好写入登录项（开机自启）')
}

function setOpenAtLogin(on: boolean): void {
  writePrefs({ openAtLogin: on })
  app.setLoginItemSettings({ openAtLogin: on })
  log(`开机自启已${on ? '开启' : '关闭'}`)
  tray?.rebuild()
}

// ——————————————————————————— 装配 ———————————————————————————

// 单实例：第二次启动 → 聚焦已有窗口 + 转发深链接（不再开第二个核心）。
if (!app.requestSingleInstanceLock()) {
  log('已有实例在运行，退出本次启动')
  app.quit()
} else {
  app.on('second-instance', (_e, argv) => {
    const link = deepLinkFromArgv(argv)
    log(`second-instance：${link ? `转发深链接 ${link}` : '聚焦已有窗口'}`)
    if (link) handleDeepLink(link)
    else toggleWindow()
  })

  // macOS 的深链接走 open-url（不是 argv）。
  app.on('open-url', (e, url) => {
    e.preventDefault()
    handleDeepLink(url)
  })

  // 深链接 scheme（`mate://…`）。未打包态按 Electron 文档显式给 execPath + 入口，
  // 否则注册到的是 electron 的默认二进制而不是本应用。
  if (!app.isPackaged) {
    app.setAsDefaultProtocolClient('mate', process.execPath, [app.getAppPath()])
  } else {
    app.setAsDefaultProtocolClient('mate')
  }

  app.whenReady().then(async () => {
    log(`app ready, electron=${process.versions.electron} node=${process.versions.node} abi=${process.versions.modules}`)
    applyLoginItemPreference()

    // scheme 处理器先装：核心即使起不来，恢复页也得有人服务。
    installSchemeHandler({ webDir: webDir(), rendererDir: rendererDir(), core: () => core })

    // IPC handlers：preload 暴露面的另一端（M1-4）。必须在开窗口之前装好——
    // 窗口一加载就可能调 boot()。
    installIpcHandlers({
      streamEndpoint: () =>
        core ? { streamBaseUrl: `ws://${core.addr}`, streamToken: core.streamToken } : null,
      window: () => win,
    })

    try {
      core = await startCore({ exe: coreExecutable(), onLog: (l) => console.log(l) })
      log(`core ready addr=${core.addr}`)
      // 核心意外退出要留痕并刷新托盘状态（正常退出走 requestQuit，那时已经在收尾）。
      core.child.once('exit', (code, signal) => {
        log(`核心退出：code=${code} signal=${signal}`)
        core = null
        tray?.rebuild()
      })
    } catch (e) {
      bootError = String(e)
      log(`核心启动失败：${bootError}`)
    }

    win = openMainWindow()

    // 托盘（M1-6）：关窗后应用的可视锚点 + 唯一的状态入口。
    tray = installTray({
      window: () => win,
      coreStatus: () => (core ? core.addr : '未运行'),
      toggleWindow,
      requestQuit: () => void requestQuit(),
      openAtLogin: () => readPrefs().openAtLogin === true,
      setOpenAtLogin,
    })

    app.on('activate', () => {
      if (BrowserWindow.getAllWindows().length === 0) win = openMainWindow()
      else toggleWindow()
    })
  })

  app.on('window-all-closed', () => {
    // 关窗已被 closeToTrayNotice 接管成"隐藏"，走到这里通常是窗口真被销毁；
    // macOS 习惯是留在托盘，其余平台在托盘不可用时才退。
    if (process.platform !== 'darwin' && !tray) app.quit()
  })

  app.on('before-quit', (e) => {
    if (quitting) return
    e.preventDefault()
    void requestQuit()
  })
}

// 兜底：未捕获异常也要留痕（壳的日志是桌面态排障的唯一入口，M4 的 mate doctor 会读它）。
process.on('uncaughtException', (err) => {
  log(`uncaughtException: ${err?.stack ?? String(err)}`)
  dialog.showErrorBox('ark-mate 桌面壳异常', String(err?.stack ?? err))
})
