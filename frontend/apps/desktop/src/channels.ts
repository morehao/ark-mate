/**
 * IPC 频道名（冻结：`forge:` 前缀，见计划 §0.3 的命名表）。
 *
 * **为什么单独一个文件**：主进程与 preload 都要用这些名字，而 `src/ipc.ts` 里 import 了
 * 只在主进程可用的 Electron 模块（`BrowserWindow` / `dialog` / `ipcMain` / `shell`）——
 * preload 若从那里取常量，esbuild 会把主进程模块一起打进 preload 产物，sandbox preload
 * 一 require 就炸。常量与实现分离是这里唯一的目的。
 */
export const CH = {
  boot: 'forge:boot',
  directoryPick: 'forge:directory-pick',
  openPath: 'forge:open-path',
  platformBounds: 'forge:platform-bounds',
  platformClose: 'forge:platform-close',
} as const

/** `forge:open-path` 的入参：要打开的东西是本地路径还是核心托管的页面。 */
export type OpenTarget =
  | { kind: 'path'; path: string }
  | { kind: 'page'; page: 'usage' | 'top-up' }
