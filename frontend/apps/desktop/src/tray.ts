/**
 * 托盘（M1-6，设计 §7.4 / §8）：显示/隐藏、核心状态、开机自启（默认关）、退出。
 *
 * 三条语义：
 *   1. **关窗默认隐藏**——托盘是"应用还在跑"的可视锚点；第一次隐藏前给一次持久化确认
 *      （T10 的文案在 main.ts 的 `closeToTrayNotice`，只提示一次）；
 *   2. **退出必须显式**——托盘菜单的「退出」与 ⌘Q 都走 main.ts 的同一条退出流程
 *      （有在途轮次 → 二次确认），不在这里直接 `app.quit()`；
 *   3. **核心状态如实**——菜单顶部只读一行：核心地址或"未运行"，不给"正在连接中…"这种模糊说法。
 */
import { Menu, Tray, nativeImage, type BrowserWindow } from 'electron'

/**
 * 16×16 单色托盘图标（内联 PNG，100 字节）。
 *
 * 为什么内联而不是放一个 png 文件：它是**应用图标**（macOS 的 template image 按主题自动反色），
 * 不是设计令牌约束下的 UI 色值——壳静态页的色值才走 `shell-tokens.css`（生成的）。
 * 放成二进制资源会让"这一个 100 字节的圆点"变成仓库里一个无法 review 的文件。
 */
const TRAY_ICON_PNG_BASE64 =
  'iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAK0lEQVR42mNgGKzgPw5MkWaiDPlPJKZIM05DRg2gggEURyNVEhJVkjJ9AQArMneJ/ooQVgAAAABJRU5ErkJggg=='

export type TrayDeps = {
  window: () => BrowserWindow | null
  /** 核心状态文案（如 `127.0.0.1:51234` 或 `未运行`）。 */
  coreStatus: () => string
  /** 在托盘里触发"显示/隐藏"（含窗口不存在时重建）。 */
  toggleWindow: () => void
  /** 走完整退出流程（含在途轮次确认）。 */
  requestQuit: () => void
  openAtLogin: () => boolean
  setOpenAtLogin: (on: boolean) => void
}

export type TrayHandle = {
  tray: Tray
  /** 重建菜单（核心状态变化、开机自启开关变化时调用）。 */
  rebuild: () => void
}

/** 安装托盘并返回句柄（调用方负责在退出时让它随进程一起消失）。 */
export function installTray(deps: TrayDeps): TrayHandle {
  const icon = nativeImage.createFromDataURL(`data:image/png;base64,${TRAY_ICON_PNG_BASE64}`)
  if (process.platform === 'darwin') icon.setTemplateImage(true)

  const tray = new Tray(icon)
  tray.setToolTip('ark-forge')

  const rebuild = (): void => {
    tray.setContextMenu(
      Menu.buildFromTemplate([
        { label: `核心：${deps.coreStatus()}`, enabled: false },
        { type: 'separator' },
        { label: '显示 / 隐藏窗口', click: () => deps.toggleWindow() },
        {
          label: '开机自启',
          type: 'checkbox',
          checked: deps.openAtLogin(),
          click: (item) => deps.setOpenAtLogin(item.checked),
        },
        { type: 'separator' },
        { label: '退出 ark-forge', click: () => deps.requestQuit() },
      ]),
    )
  }

  rebuild()
  // 左键单击 = 切换窗口显示（macOS 上左键默认弹菜单，这里统一成 toggle 更符合直觉）。
  tray.on('click', () => deps.toggleWindow())

  return { tray, rebuild }
}
