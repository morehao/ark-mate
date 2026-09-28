/**
 * 桌面壳主进程 —— **M0-5 最小版**：单实例锁 + 开一个窗口，窗口内容就是 `renderer/welcome.html`。
 *
 * 后面各里程碑在它之上长，但形态已定：
 *   - M1-3：`registerSchemesAsPrivileged` + `protocol.handle` 三路分发（app 静态 / 反代核心 / shell 页），
 *     窗口改 `loadURL('forge-app://app/')`；
 *   - M1-4/M1-5：preload 暴露 `__FORGE_DESKTOP__`（origin 守卫）+ 渲染进程加固基线（CSP/导航/权限）；
 *   - M1-6：托盘、第二次启动聚焦、退出语义（SIGTERM → 宽限 → 进程组 SIGKILL）；
 *   - M2-6/M3：spawn 随包的 `forge-core`（`--web-dir` 指向包内 dist、双凭据经 stdin、`:0` + ready 回写）。
 *
 * 单实例锁锁的是**核心**（M1-6 起转发深链接），不是窗口数——窗口数将来可以按工作区开多个。
 */
import { app, BrowserWindow } from 'electron'
import { join } from 'node:path'

/** 壳自己的日志前缀：与 core 的 stdout 混在一起时（M1-2 的 `FORGE_READY` 扫描）好区分。 */
const LOG_PREFIX = '[shell]'

function createWindow(): BrowserWindow {
  const win = new BrowserWindow({
    width: 1280,
    height: 840,
    show: false,
    backgroundColor: '#f7f8fa',
    webPreferences: {
      // 加固基线（设计 §9.2）从第一版就位，M1-5 再补 CSP / 导航 / 权限三处。
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      webviewTag: false,
      preload: join(app.getAppPath(), 'dist', 'preload-app.cjs'),
    },
  })

  // M0-5 还没有自定义 scheme：直接加载壳静态页，验证 renderer/ 与生成出来的 tokens css 接线正确。
  void win.loadFile(join(app.getAppPath(), 'renderer', 'welcome.html'))
  win.once('ready-to-show', () => {
    win.show()
    console.log(`${LOG_PREFIX} window ready-to-show (M0-5 骨架) visible=${win.isVisible()}`)
  })
  // 这两条诊断从第一版就留：M1 起窗口加载的是 forge-app://app/（核心托管），
  // 静态资源 404 / 渲染进程被杀都会先在这里留下可读的一行，不然只剩"白屏"。
  win.webContents.on('did-fail-load', (_e, code, desc, url) =>
    console.log(`${LOG_PREFIX} did-fail-load code=${code} desc=${desc} url=${url}`),
  )
  win.webContents.on('render-process-gone', (_e, details) =>
    console.log(`${LOG_PREFIX} render-process-gone reason=${details.reason} exitCode=${details.exitCode}`),
  )
  win.on('closed', () => console.log(`${LOG_PREFIX} window closed`))
  return win
}

// 单实例：第二次启动直接退出（M1-6 会改为聚焦已有窗口 + 转发深链接参数）。
if (!app.requestSingleInstanceLock()) {
  console.log(`${LOG_PREFIX} 已有实例在运行，退出本次启动`)
  app.quit()
} else {
  app.on('second-instance', () => console.log(`${LOG_PREFIX} second-instance（M1-6 处理聚焦与深链接）`))
  app.whenReady().then(() => {
    console.log(`${LOG_PREFIX} app ready, electron=${process.versions.electron} node=${process.versions.node} abi=${process.versions.modules}`)
    createWindow()
    app.on('activate', () => {
      if (BrowserWindow.getAllWindows().length === 0) createWindow()
    })
  })
  app.on('window-all-closed', () => {
    // macOS 习惯是关窗不退进程；M1-6 会改成"默认隐藏到托盘 + 首次持久化确认"。
    if (process.platform !== 'darwin') app.quit()
  })
}
