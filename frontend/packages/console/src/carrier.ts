/**
 * 载体接入（M1-8，设计 §3 / §7.1）：**一份产物、两个入口**，差别只在启动时拿到什么。
 *
 * 三条规矩：
 *   1. **存在性探测，不嗅探**：桌面态的判据是 `globalThis[DESKTOP_NAMESPACE]` 在不在
 *      （preload 先于页面脚本执行，所以**不需要 boot 门**——这一点刻意不照搬参照实现的
 *      注入表/门：我们的产物是封闭静态包，没有运行期插件装载）；禁止 UA 嗅探、禁止读
 *      `window.electron`、禁止编译期分支（设计 §10.1）。
 *   2. **global 名字与帧形状只在契约包**：本文件从 `@ark-mate/desktop-contract` 取 `DESKTOP_NAMESPACE`
 *      与 `assertMateDesktop`，业务代码里不得再出现字符串字面量 `'__MATE_DESKTOP__'`。
 *   3. **桌面态要问壳要基址**：窗口的 origin 是 `mate-app://app`，事件流却必须直连回环
 *      （`protocol.handle` 不承载 WebSocket upgrade，设计 §7.2），所以基址只能由 `boot()` 给。
 *
 * 浏览器态则一切同源：REST 与事件流都打 `location`。
 */
import { DESKTOP_NAMESPACE, assertMateDesktop, type MateDesktop } from '@ark-mate/desktop-contract'

/** 事件流连接配置：基址。 */
export type StreamConfig = {
  baseUrl: string
}

/**
 * 同步探测桌面载体。形状不对**当场抛错**，而不是让页面拿着半个对象跑（preload 已经自检过一次，
 * 这里是第二道，覆盖"契约升级了但壳没跟上"的混搭场景）。
 */
function desktopCarrier(): MateDesktop | null {
  const raw = (globalThis as Record<string, unknown>)[DESKTOP_NAMESPACE]
  if (raw === undefined) return null
  return assertMateDesktop(raw)
}

/** 是否运行在桌面窗口里（供 UI 决定要不要显示"在浏览器中打开"之类的入口）。 */
export function isDesktopCarrier(): boolean {
  return desktopCarrier() !== null
}

/**
 * 解析事件流配置。
 *
 * - 桌面态：`boot()` 下发 `streamBaseUrl`（`ws://127.0.0.1:<port>`）；
 * - 浏览器态：同源（dev 走 vite 的 ws 代理、生产直连核心）。
 */
export async function resolveStreamConfig(): Promise<StreamConfig> {
  const carrier = desktopCarrier()
  if (!carrier) {
    return { baseUrl: sameOriginWsBase() }
  }
  const { streamBaseUrl } = await carrier.boot()
  if (!streamBaseUrl) {
    throw new Error('桌面载体没有给出事件流基址（boot() 返回空）')
  }
  return { baseUrl: streamBaseUrl }
}

/** 同源 WS 基址（浏览器入口）。 */
function sameOriginWsBase(): string {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  return `${proto}://${location.host}`
}
