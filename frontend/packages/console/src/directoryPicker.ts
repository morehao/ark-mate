/**
 * 目录选择器的**两态编排**（v14.5，对齐 dsh 的 `directory-picker-auto`）。
 *
 * 参照实现把"用宿主原生选目录器还是用应用内浏览"判在**宿主侧**：`dsh web`（浏览器入口）只要绑回环、
 * 不是 SSH 启动、有可服务的显示会话，选目录照样走**系统面板**——因为面板是**宿主进程**抬起来的
 * （macOS `osascript`、Linux zenity/kdialog），与客户端是浏览器还是 Electron 窗口无关。远程 / SSH /
 * 无显示会话才退化成应用内浏览。
 *
 * 本仓的对应物：
 *   - 判定与命令配方在核心（`backend/internal/manager/workspace_picker.go`，纯函数可测）；
 *   - 判定结果经 `GET /capabilities` 的 `native-dialog` 声明下发；前端**只读声明**，不自己猜；
 *   - 原生那半是 `POST /workspaces/pick-directory`（核心进程 spawn 系统面板），
 *     浏览器入口与桌面窗口**打的是同一个端点**。
 *
 * 两条纪律：
 *   1. **不嗅探客户端**：判据只有服务端声明（禁 UA / `window.electron` / "有没有 Electron 桥"）。
 *      早期版本拿 `window.__MATE_DESKTOP__` 在不在当判据，那是错的——它把"本机浏览器"误判成
 *      "没有系统面板"，而那正是 `dsh web` 能用上面板的场景。
 *   2. **拿不准就当 browse**：声明没到 / 请求失败 → 应用内浏览（那半永远可用）。
 */
import { pickDirectoryHost } from '@ark-mate/api'
import { declaredFacts } from './capabilityFacts'

/** 本次交互走哪一套选目录实现（与 dsh 的 `DirectoryPickerBackendKind` 同义）。 */
export type DirectoryPickerKind = 'native' | 'browse'

/** 一次选定：路径 / 用户取消。取消**不是**错误，调用方据此静默收手。 */
export type DirectoryPickOutcome = { path: string } | { cancelled: true }

/** 解析判定的输入事实（只有一项：服务端声明的"宿主能不能抬系统面板"）。 */
export interface DirectoryPickerFacts {
  /** 宿主原生选择器是否可用（`/capabilities` 的 `native-dialog.available`）。 */
  nativeDialog: boolean
}

/**
 * 纯函数：由宿主事实解析出本次交互的实现（可断言，不读全局）。
 * @param facts - 采样到的宿主事实。
 * @returns 要使用的实现。
 */
export function resolveDirectoryPicker(facts: DirectoryPickerFacts): DirectoryPickerKind {
  return facts.nativeDialog ? 'native' : 'browse'
}

/** 当前该用哪一套（读事实；`capabilityFacts.loadCapabilityFacts()` 之前恒为 browse）。
 *
 * **读取点不在这里**：声明只由 `capabilityFacts.ts` 读一次（同一份声明还被设置面用来判
 * "入口是不是桌面壳"，两处各读一次就会有两个真相）。本模块只做**策略**：拿事实解析两态。 */
export function directoryPickerKind(): DirectoryPickerKind {
  return resolveDirectoryPicker({ nativeDialog: declaredFacts().nativeDialog })
}

/**
 * 抬一次**宿主原生**目录选择器（系统面板，由核心进程 spawn）。
 *
 * 与 dsh 的 `NativeDirectoryFlow` 同语义：一次调用恰好一个结果，取消回 `{cancelled:true}`；
 * 只有解析为 `native` 时才该调用它。
 *
 * 失败（含服务端回 `40501 native_pick_unsupported`——声明与实际不一致的兜底）按**取消**处理：
 * 那是"这条设备上没有面板"，不是用户做错了什么，静默收手比弹一个用户无法处置的报错好。
 * @returns 选中的路径，或取消。
 */
export async function pickDirectoryNative(): Promise<DirectoryPickOutcome> {
  try {
    const resp = await pickDirectoryHost()
    if (resp?.cancelled || !resp?.path) return { cancelled: true }
    return { path: resp.path }
  } catch {
    return { cancelled: true }
  }
}
