/**
 * 桌面壳主进程（M1-3：自定义 scheme + 反代 + 核心 spawn）。
 *
 * 启动链（设计 §7.1）：
 *   单实例锁 claim → 注册并锁定 `forge-app://` scheme → spawn 核心（`:0` + 双凭据经 stdin
 *   + `--ready-stdout`）→ 读就绪行拿地址 → health 复核 → 开窗口 `loadURL('forge-app://app/')`
 *   → 壳服务 index/assets、反代 `/v1/*`（注入 restToken）、页面自连回环收事件流。
 *
 * 令牌分层（设计 §7.2）：`restToken` 只在**主进程**（本文件 + scheme 反代）出现；
 * `streamToken` 经 M1-8 的 `boot()` 下发给页面。两者都不进 argv/env/文件/URL。
 *
 * 后续里程碑在此之上长：M1-4/M1-5 preload 暴露与渲染进程加固（CSP/导航/权限）、
 * M1-6 托盘与退出语义、M2-6 把 `--web-dir` 指给核心、M3 随包载荷与私有 node。
 */
import { app, BrowserWindow, dialog, shell } from 'electron'
import { join } from 'node:path'

import { APP_ORIGIN, SHELL_ORIGIN } from '@ark/desktop-contract'
import { startCore, stopCore, type CoreHandle } from './core'
import { installIpcHandlers } from './ipc'
import { coreExecutable, rendererDir, webDir } from './paths'
import { installSchemeHandler, registerSchemePrivileges, shellPageUrl } from './scheme'

/** 壳日志前缀：与核心/子进程的 stdout 混在一起时好区分。 */
const LOG_PREFIX = '[shell]'

const log = (...parts: unknown[]): void => console.log(LOG_PREFIX, ...parts)

// scheme 特权必须在 app.ready 之前注册（Electron 硬要求），因此放在模块顶层。
registerSchemePrivileges()

let core: CoreHandle | null = null
let win: BrowserWindow | null = null

function createWindow(url: string): BrowserWindow {
  const w = new BrowserWindow({
    width: 1280,
    height: 840,
    show: false,
    backgroundColor: '#f7f8fa',
    webPreferences: {
      // 加固基线（设计 §9.2）六项：这四项在这里；CSP 在 scheme.ts（两处下发同一策略）、
      // 导航与权限在 hardenWindow()、IPC origin 校验在 ipc.ts。少任何一项都有
      // archtest 的 TestRendererHardeningBaseline 与 CDP 验收盯着。
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      webviewTag: false,
      preload: join(app.getAppPath(), 'dist', 'preload-app.cjs'),
    },
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
 * 渲染进程加固的"窗口侧"三项（M1-5，设计 §9.2）：
 *
 *   1. **导航**：只放行自己的 app origin；`http(s)` 交给系统浏览器；其余（`file:`、`javascript:`、
 *      `forge://`、任何第三方 origin）一律 `preventDefault` 拦下——桌面窗口不该变成浏览器；
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

// 单实例：第二次启动直接退出（M1-6 会改为聚焦已有窗口 + 转发深链接参数）。
if (!app.requestSingleInstanceLock()) {
  log('已有实例在运行，退出本次启动')
  app.quit()
} else {
  app.on('second-instance', () => log('second-instance（M1-6 处理聚焦与深链接）'))

  // 深链接 scheme（`forge://…`）。未打包态按 Electron 文档显式给 execPath + 入口，
  // 否则注册到的是 electron 的默认二进制而不是本应用。消费在 M1-6（单实例转发）。
  if (!app.isPackaged) {
    app.setAsDefaultProtocolClient('forge', process.execPath, [app.getAppPath()])
  } else {
    app.setAsDefaultProtocolClient('forge')
  }

  app.whenReady().then(async () => {
    log(`app ready, electron=${process.versions.electron} node=${process.versions.node} abi=${process.versions.modules}`)

    // scheme 处理器先装：核心即使起不来，恢复页也得有人服务。
    installSchemeHandler({ webDir: webDir(), rendererDir: rendererDir(), core: () => core })

    // IPC handlers：preload 暴露面的另一端（M1-4）。必须在开窗口之前装好——
    // 窗口一加载就可能调 boot()。
    installIpcHandlers({
      streamEndpoint: () =>
        core ? { streamBaseUrl: `ws://${core.addr}`, streamToken: core.streamToken } : null,
      coreBaseUrl: () => (core ? `http://${core.addr}` : null),
      window: () => win,
    })

    let bootError: string | null = null
    try {
      core = await startCore({ exe: coreExecutable(), onLog: (l) => console.log(l) })
      log(`core ready addr=${core.addr}`)
    } catch (e) {
      bootError = String(e)
      log(`核心启动失败：${bootError}`)
    }

    if (bootError) {
      // 恢复页：明确的状态页，而不是白窗口（设计 §7.1 的"再超时 → 恢复页"）。
      win = createWindow(shellPageUrl('boot-failed.html', SHELL_ORIGIN))
    } else {
      win = createWindow(`${APP_ORIGIN}/`)
    }

    app.on('activate', () => {
      if (BrowserWindow.getAllWindows().length === 0) {
        win = createWindow(bootError ? shellPageUrl('boot-failed.html', SHELL_ORIGIN) : `${APP_ORIGIN}/`)
      }
    })
  })

  app.on('window-all-closed', () => {
    // macOS 习惯是关窗不退进程；M1-6 会改成"默认隐藏到托盘 + 首次持久化确认"。
    if (process.platform !== 'darwin') app.quit()
  })

  app.on('before-quit', () => {
    // M1-6 会补"等核心退出（宽限 3s）→ 对进程组 SIGKILL"；本轮先发 SIGTERM。
    stopCore(core)
    core = null
  })
}

// 兜底：未捕获异常也要留痕（壳的日志是桌面态排障的唯一入口，M4 的 forge doctor 会读它）。
process.on('uncaughtException', (err) => {
  log(`uncaughtException: ${err?.stack ?? String(err)}`)
  dialog.showErrorBox('ark-forge 桌面壳异常', String(err?.stack ?? err))
})
