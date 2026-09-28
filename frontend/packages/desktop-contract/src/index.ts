/**
 * @ark/desktop-contract —— 桌面载体契约的**唯一真源**（设计文档 §7.3）。
 *
 * 为什么单独一个包：两个入口（浏览器 / 桌面窗口）共用同一份 `apps/web/dist`，所以"桌面多出来的
 * 东西"只能经契约进入。契约分两半——**声明面**走 HTTP（`GET /v1/forge/capabilities`，两端同构），
 * **执行面**走壳注入的唯一命名空间 `__FORGE_DESKTOP__`。本包把两半的形状、常量与判定函数收到一处：
 *
 *   - preload 侧（`apps/desktop/src/preload-app.ts`）与前端侧（`apps/web/src/carrier.ts`）
 *     引用**同一份**类型与**同一个**运行期守卫，字段不许各说各话；
 *   - 本包 **dependencies 为空、零 Electron 引用**——`apps/web` 要能安全消费它（引了 Electron 类型
 *     就会让共享 UI 的类型面依赖 Electron，违反设计 §2.1 的第 1 条依赖边界）。契约只描述**结构化形状**。
 *
 * 类型与常量在下面直接声明，`carrier.ts` / `attach.ts` 反向 `import ... from './index.ts'`
 * 再由本文件 `export *` 出去，构成一个**刻意的模块环**。环的安全条件（实测踩过一次）：
 * 两个子模块只允许在**函数体内**读这些常量——模块顶层求值会在 `index.ts` 的常量初始化之前
 * 执行（ESM 先求值被导入的模块），拿到 TDZ 错误 `Cannot access 'X' before initialization`。
 */
export const DESKTOP_NAMESPACE = '__FORGE_DESKTOP__'
export const FORGE_SCHEME = 'forge-app'
export const APP_ORIGIN = 'forge-app://app'
export const SHELL_ORIGIN = 'forge-app://shell'
export const CAPABILITIES_PATH = '/v1/forge/capabilities'
export const META_PATH = '/v1/forge/meta'

/**
 * CSP 策略（两处下发的**同一串**：核心在它服务的文档上加响应头 = 浏览器入口；壳在
 * `forge-app://app` 的文档响应上加同一个头 = 桌面窗口）。
 *
 * 值本身是契约：Go 侧同值常量与这里**逐字相同**，由 archtest 守卫断言（M1-5）。纪律：
 * **不列任何外域**（无在线字体 / CDN / 遥测），`script-src` 不含 `unsafe-eval`（产物不得依赖 eval），
 * `connect-src` 只放行自身与回环 WS（事件流直连核心，设计 §7.2）。
 */
export const CSP_POLICY =
  "default-src 'self'; connect-src 'self' ws://127.0.0.1:*; img-src 'self' data:; script-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

/** WS 首帧 `attach` 的截止时限：连接建立后超时未 attach（或凭据不符）→ 服务端主动关闭（1008）。 */
export const STREAM_ATTACH_DEADLINE_MS = 5000

/** 屏幕/窗口矩形（`native.openExternal` 的定位锚点、`platform.setBounds` 的目标）。 */
export type Rect = { x: number; y: number; width: number; height: number }

/** 只能在渲染进程内完成、必须回主进程做的事。 */
export type NativeBridge = {
  /** 原生目录选择器；用户取消 → `null`。 */
  pickDirectory(): Promise<string | null>
  /** 拖入 / 粘贴文件的真实路径；**没有真实路径**（粘贴字节）时返回空串。同步：走 webUtils，无 IPC。 */
  pathFor(file: File): string
  /** 用系统默认程序打开路径（文件或目录）。 */
  openPath(path: string): Promise<void>
  /** 用系统浏览器打开核心托管的页面（用量 / 充值），`bounds` 给出锚点。 */
  openExternal(page: 'usage' | 'top-up', bounds: Rect): Promise<void>
}

/** 窗口自身控制（非业务能力）。 */
export type PlatformBridge = {
  setBounds(bounds: Rect): Promise<void>
  close(): Promise<void>
}

/**
 * 注入面形状（`globalThis[DESKTOP_NAMESPACE]`）。
 *
 * **只有"执行面"在这里**——能力**声明**走 HTTP（`CAPABILITIES_PATH`），两端同源同路径、结构同构，
 * 所以这里**没有** `capabilities()`（设计 §7.3 的声明面 / 执行面分工）。
 */
export type ForgeDesktop = {
  boot(): Promise<{ streamBaseUrl: string; streamToken: string }>
  native: NativeBridge
  platform: PlatformBridge
}

/** 声明面里被依赖的能力 id（闭合集合：新增能力必须同时改这里、Go 侧与验收脚本）。 */
export type CapabilityId =
  | 'tray'
  | 'native-dialog'
  | 'open-path'
  | 'diagnostics-export'
  | 'deep-link'
  | 'update'

/**
 * 浏览器入口的等价路径（V3 单向一致性的机器判据）：桌面独有能力必须在浏览器端有可执行的服务端等价物。
 * `none` 只允许出现在**纯增强、不阻塞任何流程**的能力上（当前仅 `tray`）。
 */
export type BrowserFallback =
  | 'none'
  | 'server:directory-browse'
  | 'ui:show-path-copy'
  | 'http:download'
  | 'url:direct'
  | 'deployer:replace-binary'

export type CapabilityEntry = {
  id: CapabilityId
  available: boolean
  browserFallback: BrowserFallback
}

/** `GET /v1/forge/capabilities` 的响应形状（两端同构，只有 `available` 的值不同）。 */
export type Capabilities = {
  entries: { browser: boolean; desktop: boolean }
  capabilities: CapabilityEntry[]
}

export * from './carrier.ts'
export * from './attach.ts'
