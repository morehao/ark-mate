/**
 * @ark-mate/desktop-contract —— 桌面载体契约的**唯一真源**（设计文档 §7.3）。
 *
 * 为什么单独一个包：两个入口（浏览器 / 桌面窗口）共用同一份 `apps/web/dist`，所以"桌面多出来的
 * 东西"只能经契约进入。契约分两半——**声明面**走 HTTP（`GET /v1/mate/capabilities`，两端同构），
 * **执行面**走壳注入的唯一命名空间 `__MATE_DESKTOP__`。本包把两半的形状、常量与判定函数收到一处：
 *
 *   - preload 侧（`apps/desktop/src/preload-app.ts`）与前端侧（`packages/console/src/carrier.ts`，2026-10-11 P5 前在 `apps/web/src/carrier.ts`）
 *     引用**同一份**类型与**同一个**运行期守卫，字段不许各说各话；
 *   - 本包 **dependencies 为空、零 Electron 引用**——`apps/web` 要能安全消费它（引了 Electron 类型
 *     就会让共享 UI 的类型面依赖 Electron，违反设计 §2.1 的第 1 条依赖边界）。契约只描述**结构化形状**。
 *
 * 类型与常量在下面直接声明，`carrier.ts` / `attach.ts` 反向 `import ... from './index.ts'`
 * 再由本文件 `export *` 出去，构成一个**刻意的模块环**。环的安全条件（实测踩过一次）：
 * 两个子模块只允许在**函数体内**读这些常量——模块顶层求值会在 `index.ts` 的常量初始化之前
 * 执行（ESM 先求值被导入的模块），拿到 TDZ 错误 `Cannot access 'X' before initialization`。
 */
export const DESKTOP_NAMESPACE = '__MATE_DESKTOP__'
export const MATE_SCHEME = 'mate-app'
export const APP_ORIGIN = 'mate-app://app'
export const SHELL_ORIGIN = 'mate-app://shell'
export const CAPABILITIES_PATH = '/v1/mate/capabilities'
export const META_PATH = '/v1/mate/meta'

/**
 * CSP 策略（两处下发的**同一串**：核心在它服务的文档上加响应头 = 浏览器入口；壳在
 * `mate-app://app` 的文档响应上加同一个头 = 桌面窗口）。
 *
 * 值本身是契约：Go 侧同值常量与这里**逐字相同**，由 archtest 守卫断言（M1-5）。纪律：
 * **不列任何外域**（无在线字体 / CDN / 遥测），`script-src` 不含 `unsafe-eval`（产物不得依赖 eval），
 * `connect-src` 只放行自身与回环 WS（事件流直连核心，设计 §7.2）。
 */
export const CSP_POLICY =
  "default-src 'self'; connect-src 'self' ws://127.0.0.1:*; img-src 'self' data:; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

/** WS 首帧 `attach` 的截止时限：连接建立后超时未 attach（或凭据不符）→ 服务端主动关闭（1008）。 */
export const STREAM_ATTACH_DEADLINE_MS = 5000

/** 只能在渲染进程内完成、必须回主进程做的事。 */
export type NativeBridge = {
  /** 原生目录选择器；用户取消 → `null`。 */
  pickDirectory(): Promise<string | null>
}

/**
 * 注入面形状（`globalThis[DESKTOP_NAMESPACE]`）。
 *
 * **只有"执行面"在这里**——能力**声明**走 HTTP（`CAPABILITIES_PATH`），两端同源同路径、结构同构，
 * 所以这里**没有** `capabilities()`（设计 §7.3 的声明面 / 执行面分工）。
 *
 * **2026-10-11 收窄（P3/D5）**：`platform.{setBounds,close}` 整个删除，`native.{pathFor,openPath,
 * openExternal}` 也删除——执行面里**只留真的有人在调用的那一个**（`pickDirectory`）。
 * 被删的五个方法原本相当完整（契约 + preload + 主进程 IPC + 越权拒绝），但界面（2026-10-11 P5 起在 `packages/console/src`）
 * 一次都没调用过：一份"能跑但没人用"的执行面比少一个方法更糟——它让 IPC 面看起来有人用，
 * 越权校验、载体判定这些代码都在为不存在的行为服务。将来要做"打开文件/文件夹"这类功能，
 * 按功能单独立项（连同浏览器侧的等价交互一起设计），不是从这份清单里往回捡。
 */
export type MateDesktop = {
  boot(): Promise<{ streamBaseUrl: string; streamToken: string }>
  native: NativeBridge
}

/**
 * 声明面里被依赖的能力 id（闭合集合：新增能力必须同时改这里、Go 侧与验收脚本）。
 *
 * **收窄到两条**（2026-10-11，P3/D5）：`open-path`/`diagnostics-export`/`deep-link`/`update`
 * 在两个入口都没有消费者，已从 Go 的 `capabilityDecls` 一起删除；这条联合类型与它**一一对应**。
 * `tray` 是唯一"声明了但界面不读"的登记项（壳真的装了托盘，见 `apps/desktop/src/tray.ts`）——
 * `internal/archtest` 的守卫要求其余每条都在界面源码（2026-10-11 P5 起为 `packages/console/src`，见 archtest 的 `uiSrcRoots()`）里被真实引用，不满足即红。
 */
export type CapabilityId = 'tray' | 'native-dialog'

/**
 * 浏览器入口的等价路径（V3 单向一致性的机器判据）：桌面独有能力必须在浏览器端有可执行的服务端等价物。
 * `none` 只允许出现在**纯增强、不阻塞任何流程**的能力上（当前仅 `tray`）。
 */
export type BrowserFallback = 'none' | 'server:directory-browse'

export type CapabilityEntry = {
  id: CapabilityId
  available: boolean
  browserFallback: BrowserFallback
}

/** `GET /v1/mate/capabilities` 的响应形状（两端同构，只有 `available` 的值不同）。 */
export type Capabilities = {
  entries: { browser: boolean; desktop: boolean }
  capabilities: CapabilityEntry[]
}

/**
 * 壳服务静态资产时的 Content-Type 映射（M2-5 的唯一真源）。
 *
 * 为什么要有这张表而不是"把文件交给 Chromium 的 MIME 嗅探"：Chromium 认不出 `.webmanifest`
 * （退回 `text/plain`），而核心侧用 Go 的 mime 表 + 显式覆盖给出 `application/manifest+json`——
 * 同一个文件从两条服务路径拿到的头不一样，asset-parity 会红（实测抓到过）。
 *
 * 核心侧有一份**等价**的 Go 映射（`internal/router` 的 `mimeOverrides`），由
 * `TestAssetContentTypesMatchContract` 守卫逐字比对——两边各写一份、靠守卫锁住，
 * 是本仓"两处下发同一策略"的既有做法（CSP 就是这么管的）。
 */
export const ASSET_CONTENT_TYPES: Record<string, string> = {
  '.webmanifest': 'application/manifest+json',
  '.svg': 'image/svg+xml',
  '.js': 'text/javascript',
  '.mjs': 'text/javascript',
  '.css': 'text/css',
  '.html': 'text/html; charset=utf-8',
  '.json': 'application/json',
  '.map': 'application/json',
}

/** 按扩展名取 Content-Type（命中映射即用，否则交给宿主嗅探/兜底）。 */
export function assetContentType(pathname: string): string | null {
  const dot = pathname.lastIndexOf('.')
  if (dot < 0) return null
  return ASSET_CONTENT_TYPES[pathname.slice(dot).toLowerCase()] ?? null
}

export * from './carrier.ts'
export * from './attach.ts'
export * from './gate.ts'
export * from './nodeEnv.ts'
