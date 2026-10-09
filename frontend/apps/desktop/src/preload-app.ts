/**
 * 桌面窗口的 preload（M1-4）：**只在桌面载体主框架上**暴露唯一命名空间 `__MATE_DESKTOP__`。
 *
 * 三条硬约束（设计 §7.3 / §9.2，V18 的两层覆盖）：
 *   1. **origin 守卫**：`isDesktopCarrier(location, process.isMainFrame)` 为假时**一次都不调用**
 *      `exposeInMainWorld`——`mate-app://shell/*`、`mate-app://other/*`、http(s) 页面、
 *      以及任意子框架拿到的都是 `undefined`；
 *   2. **形状自检**：暴露前用契约的 `assertMateDesktop` 过一遍，形状错了就在 preload 阶段炸，
 *      而不是让页面拿到半个对象、在某个业务路径上以"某方法是 undefined"晚炸；
 *   3. **只暴露执行面**：`boot()` + `native.*` + `platform.*`；**没有** `capabilities()`
 *      （能力声明走 `GET /v1/mate/capabilities`，两端同源同路径）。
 *
 * 输出是 CommonJS（`dist/preload-app.cjs`）——preload 必须 CJS；`@ark/desktop-contract`
 * 由 esbuild 打进同一份产物（纯 TS 源、零运行期依赖）。
 */
import { contextBridge, ipcRenderer, webUtils } from 'electron'

import { DESKTOP_NAMESPACE, assertMateDesktop, isDesktopCarrier, type MateDesktop, type Rect } from '@ark/desktop-contract'
import { CH } from './channels'

const carrier = isDesktopCarrier({ protocol: location.protocol, hostname: location.hostname }, process.isMainFrame)

if (carrier) {
  const api: MateDesktop = {
    /**
     * 事件流的基址与通道级凭据。REST 不走这里——页面同源反代已由壳注入 restToken，
     * 因此渲染进程从头到尾拿不到 restToken。
     */
    boot: () => ipcRenderer.invoke(CH.boot),

    native: {
      pickDirectory: () => ipcRenderer.invoke(CH.directoryPick),
      /**
       * 拖入 / 粘贴文件的真实路径。**同步**走 webUtils（无 IPC）；
       * 没有真实路径（粘贴字节）时返回空串——契约如此，调用方据此降级。
       */
      pathFor: (file: File): string => {
        try {
          return webUtils.getPathForFile(file) ?? ''
        } catch {
          return ''
        }
      },
      openPath: (path: string) => ipcRenderer.invoke(CH.openPath, { kind: 'path', path }),
      /**
       * 用系统浏览器打开核心托管的页面。`bounds` 是定位锚点（系统浏览器用不上，留给
       * 将来的应用内窗口）；**只传页面名**，URL 由主进程用核心地址拼——渲染进程不指定任意 URL。
       */
      openExternal: (page: 'usage' | 'top-up', _bounds: Rect) =>
        ipcRenderer.invoke(CH.openPath, { kind: 'page', page }),
    },

    platform: {
      setBounds: (b: Rect) => ipcRenderer.invoke(CH.platformBounds, b),
      close: () => ipcRenderer.invoke(CH.platformClose),
    },
  }

  assertMateDesktop(api)
  contextBridge.exposeInMainWorld(DESKTOP_NAMESPACE, api)
}

console.log(
  `[preload] carrier=${carrier} isMainFrame=${process.isMainFrame} url=${location.protocol}//${location.hostname}`,
)
