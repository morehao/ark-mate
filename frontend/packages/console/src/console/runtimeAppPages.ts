// ─────────────────────────────────────────────────────────────────────────────
// runtimeAppPages.ts —— 二期 C「运行期应用页」（沙箱 iframe）的纯逻辑层
//
// 与一期 A+Slot 的统一注册表 `console/appRegistry.ts` **并列、互不影响**：
//
//   - A+Slot（appRegistry）：页面组件**编译期在包里**，`resolveAppPages()` 出来的页由主窗
//     直接渲染（`<Route>`）。它的红线是「封闭静态包」——绝不允许运行期装载陌生代码。
//   - 二期 C（本文件）：页面代码来自「运行期应用包」，**不编进主窗产物**，必须由沙箱 iframe
//     （`sandbox="allow-scripts"` + `connect-src 'none'`）加载，页面只经 postMessage 桥与主窗
//     通讯。这是设计 §17.2「C 需要运行期注入沙箱页」的落点，**不触碰** A+Slot 的编译期映射语义。
//
// 本文件只放**纯函数**（无 React、无 DOM、无 npm 运行时），便于 `pnpm test:console`（node --test）
// 直接校验：① 从 `/apps` 列表派生运行期页（含与内置路由的**冲突检测 / fail-closed 让位**）；
// ② postMessage 桥的**白名单/来源/限流**判定。React 容器与 iframe 在 `SandboxAppFrame.tsx`。
// ─────────────────────────────────────────────────────────────────────────────

/** 一条运行期应用页（沙箱 iframe 渲染）。 */
export interface RuntimeAppPage {
  /** 应用 id（= 包 id；与 A+Slot 的 App 定义同一 id 词法）。 */
  readonly appId: string
  /** 左栏文案（来自清单 page.nav_label，缺省回退 app name）。 */
  readonly navLabel: string
  /** 站内落地路由（来自清单 page.route；iframe 页面挂在它下面的 `/app-frame/<appId>/`）。 */
  readonly route: string
  /** 页面访问 token（后端按包 spec 签发；前端只透传给 iframe 的 `/_app/<token>/`）。 */
  readonly frameToken: string
  /** 左栏排序（运行期页统一排在内置页之后）。 */
  readonly order: number
  /** 该 app 清单声明的 permissions（决定桥放行哪些读；缺省 = 无任何读放行）。 */
  readonly permissions: readonly string[]
}

/** 运行期页在左栏的排序基值：内置页 order 最大到 50，运行期页从 60 起排在其后。 */
const RUNTIME_PAGE_ORDER_BASE = 60

/** 后端 `/apps` 列表项的最小形状（仅本模块关心的字段；其余字段忽略）。 */
export interface AppsListItem {
  id: string
  name: string
  enabled: boolean
  page?: { nav_label?: string; route?: string; component?: string } | null
  /**
   * 页面访问 token（二期 C 由后端签发；本 WP 后端接口尚未回填，属 **C6 接线点**）。
   * 缺失时该 app 的沙箱页**不登记**（fail-closed：无 token 无法安全加载页面）。
   */
  frame_token?: string | null
  /** 清单声明的 permissions（可选；C6 接线后回填。缺省 = 桥不放行任何读）。 */
  permissions?: readonly string[] | null
}

/** 一条因冲突/缺字段被丢弃的运行期页（带原因，供告警/测试断言）。 */
export interface DroppedRuntimePage {
  readonly appId: string
  readonly route: string
  readonly reason: string
}

export interface DeriveRuntimePagesResult {
  readonly pages: readonly RuntimeAppPage[]
  readonly dropped: readonly DroppedRuntimePage[]
}

/**
 * 从 `/apps` 列表派生**运行期沙箱页**，并与内置路由做**冲突检测**（fail-closed 让位）。
 *
 * 规则（对齐设计 §17.2 与计划 §10「冲突检测」）：
 *   1. 只收 `enabled && page.route && frame_token` 都齐备的 app（缺 token → 不登记，记 dropped）；
 *   2. `page.route` 必须是站内绝对路径（以 `/` 开头）；
 *   3. **冲突检测**：`page.route`（及其 `/app-frame/<appId>` 实际挂载路径）若与内置路由
 *      （`builtinRoutes`，来自 A+Slot 的编译期页 route 集）**相同或互为前缀**，则该运行期页
 *      **让位丢弃**（fail-closed：绝不覆盖/遮蔽内置页），记 dropped 原因；
 *   4. 运行期页彼此 route 冲突：保留先出现者，后者丢弃（稳定、可预期）；
 *   5. 排序：按 appId 稳定升序（左栏 order 从 RUNTIME_PAGE_ORDER_BASE 起顺序分配）。
 *
 * 返回 pages + dropped（dropped 供调用方 console.warn 告警——「让位 + 告警」是计划明确要求）。
 */
export function deriveRuntimeAppPages(
  apps: ReadonlyArray<AppsListItem>,
  builtinRoutes: ReadonlyArray<string>,
): DeriveRuntimePagesResult {
  const pages: RuntimeAppPage[] = []
  const dropped: DroppedRuntimePage[] = []
  const takenRoutes = new Set<string>()

  // 稳定顺序：按 appId 升序，保证「先到先得」可预期。
  const sorted = [...apps].sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))

  for (const app of sorted) {
    if (!app.enabled) continue
    const page = app.page
    const route = (page?.route ?? '').trim()
    if (!route) continue // 没有页面声明 → 不是运行期页（合法的"无页 app"）
    if (!route.startsWith('/')) {
      dropped.push({ appId: app.id, route, reason: 'page.route 不是站内绝对路径（须以 / 开头）' })
      continue
    }
    const frameToken = (app.frame_token ?? '').trim()
    if (!frameToken) {
      // 有页面声明但后端未发 token（C6 接线前）→ fail-closed 不登记。
      dropped.push({ appId: app.id, route, reason: '缺少 frame_token（后端未签发页面 token，待 C6 接线）' })
      continue
    }
    // —— 冲突检测：与内置路由冲突 → fail-closed 让位 ——
    const conflict = firstConflict(route, builtinRoutes, takenRoutes)
    if (conflict) {
      dropped.push({ appId: app.id, route, reason: `路由与内置页/已有运行期页冲突（${conflict}）→ 让位` })
      continue
    }
    takenRoutes.add(route)
    pages.push({
      appId: app.id,
      navLabel: (page?.nav_label || app.name).trim() || app.id,
      route,
      frameToken,
      order: RUNTIME_PAGE_ORDER_BASE + pages.length,
      permissions: Array.isArray(app.permissions) ? app.permissions : [],
    })
  }
  return { pages, dropped }
}

/**
 * 判定 `route` 是否与给定的路由集冲突。冲突定义：
 *   - 完全相同；或
 *   - 互为**路径段前缀**（`/foo` 与 `/foo/bar` 冲突——否则一个会遮蔽另一个的深链）。
 *
 * 用段边界判定（`/foo` vs `/foobar` **不**冲突），与 navView 的 `isPathActive` 同口径。
 */
function firstConflict(
  route: string,
  builtinRoutes: ReadonlyArray<string>,
  takenRoutes: ReadonlySet<string>,
): string | null {
  const norm = normalizeRoute(route)
  for (const b of builtinRoutes) {
    if (routesCollide(norm, normalizeRoute(b))) return `内置 ${b}`
  }
  for (const t of takenRoutes) {
    if (routesCollide(norm, normalizeRoute(t))) return `运行期 ${t}`
  }
  return null
}

/** 归一化路由：去掉尾部 `/`（保留根 `/`）。 */
export function normalizeRoute(route: string): string {
  if (route === '/') return '/'
  return route.replace(/\/+$/u, '')
}

/** 两条路由是否相同或互为路径段前缀（段边界）。 */
export function routesCollide(a: string, b: string): boolean {
  if (a === b) return true
  const aHas = b.startsWith(a.endsWith('/') ? a : `${a}/`)
  const bHas = a.startsWith(b.endsWith('/') ? b : `${b}/`)
  return aHas || bHas
}

// ── 运行期页注册表单例（进程级；bootstrap 期一次性写入，先于 App 挂载）──────────────
//
// 与 appRegistry 的 `registry` 同款：一个模块级数组快照。`NAV_ENTRIES`（navView）与
// `app.tsx` 的 `<Route>` 在 App 模块加载时读取它，故必须在 `import('./app')` 之前写入。
let runtimePages: readonly RuntimeAppPage[] = []

/** 写入运行期页（覆盖式；bootstrap 先 derive 再写）。 */
export function setRuntimeAppPages(pages: readonly RuntimeAppPage[]): void {
  runtimePages = pages
}

/** 读当前运行期页（只读快照）。 */
export function getRuntimeAppPages(): readonly RuntimeAppPage[] {
  return runtimePages
}

// ── 受限 postMessage 桥（主窗侧逻辑）─────────────────────────────────────────────
//
// 安全基线（v37 / magpie `noFramedAPI`）：页面在 `connect-src 'none'` 的沙箱里自身**发不出**
// 任何网络请求；所有「读」只能 postMessage 给主窗，由主窗**按清单 permissions 白名单**代发，
// 再把结果 postMessage 回去。桥的判据全部集中在此（纯函数，单测直接覆盖）：
//   ① 只认**自己那个 iframe** 发来的消息（`ev.source === frameWindow`，由调用方传入比对结果）；
//   ② 只响应 `type → 读种类` 且在**清单 permissions 声明过**的白名单内的请求，其余一律拒绝；
//   ③ 限流（默认 ≤1 次/秒，对齐 magpie「一次 ≤1/s」），超频直接丢弃不回复。

/** 读种类枚举（页面可请求的读）。 */
export type BridgeReadKind = 'usage:summary' | 'providers:list'

/**
 * 权限 → 允许的读种类。清单 `permissions`（`apppkg.Permission`）里声明了某权限，才放行对应读。
 *
 * 只映射「只读聚合」两类；`net:domain`/`file:domain` 在本 WP **不**开放主窗代发（那是 C7+ 之后
 * 的文件/网络能力面，需要更多授权粒度），保持 fail-closed：未映射的读一律拒绝。
 */
export const PERMISSION_READS: Readonly<Record<string, readonly BridgeReadKind[]>> = {
  'read:usage': ['usage:summary'],
  'read:providers': ['providers:list'],
}

/** 由清单 permissions 计算**允许的读种类集合**。 */
export function allowedReads(permissions: ReadonlyArray<string>): Set<BridgeReadKind> {
  const out = new Set<BridgeReadKind>()
  for (const perm of permissions) {
    const reads = PERMISSION_READS[perm]
    if (reads) for (const r of reads) out.add(r)
  }
  return out
}

/** 桥收到的消息形状（页面 → 主窗）。 */
export interface BridgeRequest {
  type: 'read'
  kind: string
  /** 可选请求 id，主窗原样回带（便于页面多路复用）。 */
  id?: string
}

/** 桥的回复形状（主窗 → 页面）。 */
export interface BridgeReply {
  type: 'read:result' | 'read:error'
  kind: string
  id?: string
  data?: unknown
  error?: string
}

export interface BridgeOptions {
  /** 清单声明过的 permissions。 */
  permissions: ReadonlyArray<string>
  /** 判定该消息**是否来自本 iframe 自身**（调用方实现 `ev.source === iframe.contentWindow`）。 */
  sourceMatches: boolean
  /** 实际执行读的实现（主窗侧；返回数据或抛错）。 */
  performRead: (kind: BridgeReadKind) => unknown | Promise<unknown>
  /** 限流窗口（毫秒；默认 1000ms = ≤1/s）。 */
  rateLimitMs?: number
  /** 时间源（测试注入；默认 Date.now）。 */
  now?: () => number
}

export interface Bridge {
  /**
   * 处理一条入站消息。返回回复对象（调用方负责 postMessage 回 iframe）；被拒绝时返回 `null`
   * （不回复——静默丢弃，避免给恶意页面任何反馈信号）。
   */
  handleMessage: (data: unknown) => Promise<BridgeReply | null>
}

/**
 * 构造主窗侧的受限桥。所有判据（来源/白名单/限流）在此收口，消息处理是**纯异步函数**。
 *
 * 拒绝路径**不回复**（返回 null）：沙箱页面拿不到「为什么被拒」的信号，减少被探测面。
 */
export function createAppFrameBridge(opts: BridgeOptions): Bridge {
  const allowed = allowedReads(opts.permissions)
  const rateLimitMs = opts.rateLimitMs ?? 1000
  const now = opts.now ?? Date.now
  let lastAt = Number.NEGATIVE_INFINITY

  return {
    async handleMessage(data: unknown): Promise<BridgeReply | null> {
      // ① 来源必须是自己那个 iframe。
      if (!opts.sourceMatches) return null
      // 结构校验。
      const req = parseRequest(data)
      if (!req) return null
      // ② 白名单：kind 必须由清单 permissions 放行。
      if (!allowed.has(req.kind as BridgeReadKind)) return null
      // ③ 限流：窗口内只放行一次。
      const t = now()
      if (t - lastAt < rateLimitMs) return null
      lastAt = t
      try {
        const result = await opts.performRead(req.kind as BridgeReadKind)
        return { type: 'read:result', kind: req.kind, id: req.id, data: result }
      } catch (err) {
        return {
          type: 'read:error',
          kind: req.kind,
          id: req.id,
          error: err instanceof Error ? err.message : String(err),
        }
      }
    },
  }
}

/** 解析并校验入站请求形状；不合法返回 null。 */
function parseRequest(data: unknown): BridgeRequest | null {
  if (!data || typeof data !== 'object') return null
  const obj = data as Record<string, unknown>
  if (obj.type !== 'read') return null
  if (typeof obj.kind !== 'string' || obj.kind === '') return null
  const req: BridgeRequest = { type: 'read', kind: obj.kind }
  if (typeof obj.id === 'string') req.id = obj.id
  return req
}
