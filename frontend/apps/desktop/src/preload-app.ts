/**
 * 桌面窗口的 preload —— **M0-5 占位版**：只做载体判定与自检，**不暴露任何东西**。
 *
 * M1-4 在此暴露唯一命名空间（设计 §7.3 的执行面）：
 *   1. 只在 `isDesktopCarrier(location, process.isMainFrame)` 为真时 `contextBridge.exposeInMainWorld`，
 *      其它 origin / 子框架**一次都不调用**（V18 负例）；
 *   2. 暴露前用 `assertForgeDesktop` 自检形状（形状错了就在 preload 阶段炸，不让页面拿到半个对象）；
 *   3. 暴露面 = `boot()` + `native.*` + `platform.*`，**不含** `capabilities()`（能力声明走
 *      `GET /v1/forge/capabilities`，两端同源同路径）。
 *
 * 输出是 CommonJS（`dist/preload-app.cjs`）——preload 必须 CJS；`@ark/desktop-contract`
 * 由 esbuild 打进同一份产物（它是纯 TS 源、零运行期依赖）。
 */
import { assertForgeDesktop, isDesktopCarrier } from '@ark/desktop-contract'

const carrier = isDesktopCarrier({ protocol: location.protocol, hostname: location.hostname }, process.isMainFrame)

// 占位：M0-5 只断言"判定函数在真实 Electron 环境里可用"，形状守卫留到 M1-4 暴露前调用。
if (carrier && typeof assertForgeDesktop !== 'function') {
  throw new Error('preload: @ark/desktop-contract 未正确打包（assertForgeDesktop 缺失）')
}

console.log(`[preload] carrier=${carrier} isMainFrame=${process.isMainFrame} url=${location.protocol}//${location.hostname}`)
