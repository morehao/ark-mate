/**
 * IPC 频道名（`mate:` 前缀，见计划 §0.3 的命名表）。**集合按「有没有调用方」收窄过**（2026-10-11）。
 *
 * **为什么单独一个文件**：主进程与 preload 都要用这些名字，而 `src/ipc.ts` 里 import 了
 * 只在主进程可用的 Electron 模块（`BrowserWindow` / `dialog` / `ipcMain` / `shell`）——
 * preload 若从那里取常量，esbuild 会把主进程模块一起打进 preload 产物，sandbox preload
 * 一 require 就炸。常量与实现分离是这里唯一的目的。
 */
export const CH = {
  boot: 'mate:boot',
  directoryPick: 'mate:directory-pick',
} as const

// 2026-10-11（P3/D5）删掉三个频道：`mate:open-path` / `mate:platform-bounds` / `mate:platform-close`。
// 它们由 preload 与主进程两端完整实现，但唯一的界面（`apps/web/src`，2026-10-11 P5 起为 `packages/console/src`）从来没有调用方——
// 频道名一旦冻结在文件里，看起来就像有人依赖它。少一个频道是零成本的，多一个不是。
// 注意频道名本身**不再是冻结契约**：要加回来时按功能设计（含浏览器侧等价交互）重新过评审。
