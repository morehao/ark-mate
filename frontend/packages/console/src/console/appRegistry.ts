// ─────────────────────────────────────────────────────────────────────────────
// appRegistry.ts —— 前端页面入口的**统一注册表**（唯一事实源）
//
// 参照 `components/preview/routes.ts` 的 `PreviewDefinition` / `resolvePreview` 形状：
// 一处「注册 + resolve」，消除「新增一个页面要手改 navView 与 app.tsx 两处（外加图标）」
// 的漂移。左栏 `NAV_ENTRIES` 与 `app.tsx` 的 `<Route>` 都由本表**派生**（同源）。
//
// 两级注册（Lead 裁决，设计 §2.2「封闭静态包」+ §5.6 前端统一注册表）：
//
//   1. **内置静态**：构建期在本文件用 `registerAppPage(...)` 登记内置页。组件由
//      **编译期写死的 `React.lazy(() => import('../pages/...'))` 工厂**加载，import 路径
//      在编译期固化、随产物一起打包，仍符合「一份 UI 产物 / 无运行期装载陌生代码」的红线。
//      ⚠ lazy 不只是省首包：它**打断了静态依赖** `appRegistry → UsagePage/AutomationsPage
//      → store → navView → appRegistry` 的循环 import（WP7 回归：静态 import 时 navView 模块
//      eval 到 appRegistry 尚未初始化，`registry.values()` 抛 undefined，App 整体不挂载）。
//   2. **从 /apps 动态补**：运行期从 `GET /v1/mate/apps` 读到带 `page`
//      （`{nav_label, route, component}`）的 app 后，**只登记页面元数据**
//      （route / nav 文案 / 组件**名**）。渲染时按组件名到**编译期组件映射表**
//      `BUILTIN_PAGE_COMPONENTS` 里查——即：接口只给「用哪个已编译进包里的组件」的名字，
//      **绝不**按接口字符串 `import()` 任意模块。组件本身仍必须在编译期在包里。
//
//   解析不到的组件名 = 该 app 的页面**不可渲染**（fail-closed：只丢这一个页的入口，
//   不牵连其它 app），与「封闭静态包」一致：接口能声明的组件名集合被编译期映射表封死。
//
// 纪律：新增一个**内置**页面 = 在 `BUILTIN_PAGE_COMPONENTS` 加一行 + 在
// `BUILTIN_APP_PAGES` 注册一条；`navView.tsx` 的 `NAV_ENTRIES` 与 `app.tsx` 的 `<Route>`
// 自动跟随，不再手改。
// ─────────────────────────────────────────────────────────────────────────────
import { createElement, lazy, type ComponentType, type ReactNode } from 'react'
import {
  AppstoreOutlined,
  BarChartOutlined,
  ClockCircleOutlined,
  FolderOpenOutlined,
} from '@ant-design/icons'

/**
 * 编译期组件映射表：**组件名 → 已编译进包的组件**。
 *
 * 这是「动态补」唯一允许的组件来源。接口（`page.component`）只能命中这里的键；
 * 命中不了 = 不可渲染。**绝不**在此之外做按字符串的 `import()`——那会把「一份静态
 * 产物」变成「运行期装载陌生代码」，越出 A+Slot 红线（设计 §2.2）。
 *
 * 表用 `Record<string, ComponentType>`（宽类型）：页面组件各自的 props 由壳统一注入
 * （内置页现均为无参组件；动态 app 页组件同样约定为无参、自取数据）。
 *
 * **懒加载**：值均为 `React.lazy(() => import(...))`。工厂的 import 路径**编译期写死**，
 * 接口仍只能命中本表的键，构造不出新组件——这与静态 import 是同一条「封闭静态包」红线；
 * 差别只是模块加载实际发生在**首次渲染该页**时，从而打断 `appRegistry ↔ navView` 的循环 import。
 * 渲染 lazy 组件需要 `<Suspense>` 边界（`app.tsx` 的 `<Route>` 处已包裹）。
 */
export type AppPageComponent = ComponentType<Record<string, never>>

/** 页面入口所属的一级分组（左栏「扩展」等组）。`undefined` = 顶层一级项。 */
export type AppPageGroup = 'extensions'

/**
 * 一条页面入口定义（仿 `PreviewDefinition`）。
 *
 * - `route`：落地路由（`/extensions/experts` 等），`<Route path>` 与左栏 `to` 同源；
 * - `nav`：左栏呈现（文案 / 排序 / 可选图标）。为 `null` 表示**不挂左栏**（纯路由页，
 *   如已并入问答区的 `/sessions`——本表不收它，此处仅类型上留口）；
 * - `component`：渲染组件。内置页为 `React.lazy` 工厂（渲染期才加载，打断 `appRegistry ↔
 *   navView` 初始化环）；动态 app 页复用 `BUILTIN_PAGE_COMPONENTS` 里的同名 lazy 组件。
 *   对外契约不变：仍是可渲染的 `ComponentType`（壳侧用 `<Suspense>` 兜加载态）。
 * - `priority`：同名 route 冲突时的取舍。`builtin` 胜 `app`，与 preview 的
 *   `builtin > extension` 同义（内置页锁死，动态 app 不能抢占内置路由）。
 */
export interface AppPageDefinition {
  /** 唯一标识（内置页用稳定 id；动态 app 页用 `app:<appId>`，与 app id 一一对应）。 */
  readonly id: string
  /** 落地路由；`<Route path>` 与左栏 `to` 同源。 */
  readonly route: string
  /** 左栏呈现；`null` = 不挂左栏（纯路由页）。 */
  readonly nav: AppPageNav | null
  /** 渲染组件（可渲染组件；内置页为 lazy，需外层 Suspense）。 */
  readonly component: AppPageComponent
  /** 冲突取舍优先级。 */
  readonly priority: 'builtin' | 'app'
}

/** 左栏呈现配置。 */
export interface AppPageNav {
  /** 左栏文案。 */
  readonly label: string
  /** 排序（升序；同级内）。 */
  readonly order: number
  /** 一级分组：给了就落进该组的二级项，否则是顶层一级项。 */
  readonly group?: AppPageGroup
  /** 顶层一级项的图标（分组内的二级项不挂图标，对齐 navView 的层级纪律）。 */
  readonly icon?: ReactNode
}

// ── 编译期组件映射表（唯一允许的动态组件来源）──────────────────────────────────
//
// 内置页组件在此**懒加载**（`React.lazy` + 编译期写死的 `import()` 路径）。动态 app 页要
// 渲染，其 `page.component` 必须是下表的键之一——接口只选「已编译的组件名」，构造不出新组件
// （懒加载工厂的路径也是编译期常量，不是按接口字符串动态拼的，符合「封闭静态包」安全边界）。
//
// 为什么用 `React.lazy` 而非静态 import：页组件（如 UsagePage/AutomationsPage）会 import
// `store.ts`，而 `store.ts` import 了 `navView.tsx`；若此处静态 import 页组件，会形成
// `navView → appRegistry → UsagePage → store → navView` 的**模块初始化环**——navView 顶层
// `NAV_ENTRIES = buildNavEntries()` 执行时 appRegistry 尚未初始化完，`registry` 还是
// undefined，`registry.values()` 崩溃、整棵 App 挂不上。懒加载把「页组件」推迟到渲染期才
// import，模块初始化期 appRegistry 不再静态拉入页组件，环断开。渲染侧由 app.tsx 的
// `<Suspense>` 兜底（参照 PreviewProvider 的 lazy 口径）。
const lazyPage = (loader: () => Promise<{ default: ComponentType<any> }>) =>
  lazy(loader) as unknown as AppPageComponent

export const BUILTIN_PAGE_COMPONENTS: Readonly<Record<string, AppPageComponent>> = {
  ExpertsPage: lazyPage(() => import('../pages/extensions/ExpertsPage').then((m) => ({ default: m.ExpertsPage }))),
  SkillsPage: lazyPage(() => import('../pages/extensions/SkillsPage').then((m) => ({ default: m.SkillsPage }))),
  ConnectorsPage: lazyPage(() => import('../pages/extensions/ConnectorsPage').then((m) => ({ default: m.ConnectorsPage }))),
  AppMarketPage: lazyPage(() => import('../pages/extensions/AppMarketPage').then((m) => ({ default: m.AppMarketPage }))),
  MyAppsPage: lazyPage(() => import('../pages/extensions/MyAppsPage').then((m) => ({ default: m.MyAppsPage }))),
  UsagePage: lazyPage(() => import('../pages/UsagePage').then((m) => ({ default: m.UsagePage }))),
  AutomationsPage: lazyPage(() => import('../pages/AutomationsPage').then((m) => ({ default: m.AutomationsPage }))),
  MaterialPage: lazyPage(() => import('../pages/MaterialPage').then((m) => ({ default: m.MaterialPage }))),
}

// ── 内置页注册（构建期静态；顺序即 `NAV_ENTRIES` 内同级排序的兜底）────────────────
//
// 这些是当前左栏的全部内置入口。图标沿用 navView 原值。
const BUILTIN_APP_PAGES: readonly AppPageDefinition[] = [
  {
    id: 'page:experts',
    route: '/extensions/experts',
    nav: { label: '专家', order: 21, group: 'extensions' },
    component: BUILTIN_PAGE_COMPONENTS.ExpertsPage,
    priority: 'builtin',
  },
  {
    id: 'page:skills',
    route: '/extensions/skills',
    nav: { label: '技能', order: 22, group: 'extensions' },
    component: BUILTIN_PAGE_COMPONENTS.SkillsPage,
    priority: 'builtin',
  },
  {
    id: 'page:connectors',
    route: '/extensions/connectors',
    nav: { label: '连接器', order: 23, group: 'extensions' },
    component: BUILTIN_PAGE_COMPONENTS.ConnectorsPage,
    priority: 'builtin',
  },
  {
    id: 'page:market',
    route: '/extensions/market',
    nav: { label: '应用市场', order: 24, group: 'extensions' },
    component: BUILTIN_PAGE_COMPONENTS.AppMarketPage,
    priority: 'builtin',
  },
  {
    id: 'page:my-apps',
    route: '/extensions/my-apps',
    nav: { label: '我的应用', order: 25, group: 'extensions' },
    component: BUILTIN_PAGE_COMPONENTS.MyAppsPage,
    priority: 'builtin',
  },
  {
    id: 'page:materials',
    route: '/materials',
    nav: { label: '资料库', order: 30, icon: createElement(FolderOpenOutlined) },
    component: BUILTIN_PAGE_COMPONENTS.MaterialPage,
    priority: 'builtin',
  },
  {
    id: 'page:automations',
    route: '/automations',
    nav: { label: '定时任务', order: 40, icon: createElement(ClockCircleOutlined) },
    component: BUILTIN_PAGE_COMPONENTS.AutomationsPage,
    priority: 'builtin',
  },
  {
    id: 'page:usage',
    route: '/usage',
    nav: { label: '用量', order: 50, icon: createElement(BarChartOutlined) },
    component: BUILTIN_PAGE_COMPONENTS.UsagePage,
    priority: 'builtin',
  },
]

// 「扩展」组的组长元数据（组本身没有落地路由，只承载图标/文案/排序）。
// 组不是一个页面入口——故不放进 `AppPageDefinition`，单独持有；`buildNavEntries` 需要它
// 才能把扩展组下的二级项拼回一个 NavGroup。
export const EXTENSIONS_GROUP = {
  id: 'extensions',
  label: '扩展',
  order: 20,
  icon: createElement(AppstoreOutlined),
} as const

// ── 注册表本体 ────────────────────────────────────────────────────────────────
//
// 进程级单例。内置页在模块加载时一次性注册；动态 app 页由 `applyAppPagesFromApps`
// 在拿到 /apps 列表后补登（幂等：同 id 覆盖）。`Map` 保留插入序，resolve 时再按 order 排序。
const registry = new Map<string, AppPageDefinition>()

/**
 * 登记一条页面入口（幂等：同 `id` 覆盖，不会重复注册）。
 *
 * 这是**唯一**的写入口。内置页在下方 `seedBuiltins()` 里调用；动态 app 页由
 * `applyAppPagesFromApps` 调用。返回入参便于链式/测试。
 */
export function registerAppPage(def: AppPageDefinition): AppPageDefinition {
  registry.set(def.id, def)
  return def
}

/** 内置页一次性登记（模块加载时执行；id 幂等，热重载重复加载也安全）。 */
function seedBuiltins(): void {
  for (const def of BUILTIN_APP_PAGES) registerAppPage(def)
}
seedBuiltins()

/**
 * 解析当前**全部**页面入口（内置 + 已补登的 app 页）。
 *
 * - 按 `priority`（builtin > app）+ `nav.order`（无 nav 的排最后）+ `id` 稳定排序；
 * - 同 `route` 冲突时：builtin 胜 app（内置路由锁死，动态 app 抢不走）。
 *
 * 返回只读快照（调用方不得改注册表内部）。
 */
export function resolveAppPages(): readonly AppPageDefinition[] {
  // 同 route 去重：builtin 优先，保留先出现的 app（其它被抢走的 app 页丢弃其入口）。
  const byRoute = new Map<string, AppPageDefinition>()
  for (const def of registry.values()) {
    const prev = byRoute.get(def.route)
    if (!prev) {
      byRoute.set(def.route, def)
      continue
    }
    const prevRank = prev.priority === 'builtin' ? 0 : 1
    const curRank = def.priority === 'builtin' ? 0 : 1
    if (curRank < prevRank) byRoute.set(def.route, def)
  }
  return [...byRoute.values()].sort((a, b) => {
    const pa = a.priority === 'builtin' ? 0 : 1
    const pb = b.priority === 'builtin' ? 0 : 1
    if (pa !== pb) return pa - pb
    const oa = a.nav?.order ?? Number.MAX_SAFE_INTEGER
    const ob = b.nav?.order ?? Number.MAX_SAFE_INTEGER
    if (oa !== ob) return oa - ob
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0
  })
}

/**
 * 从 `GET /v1/mate/apps` 读到的 app 列表，把带 `page` 的 app **页面元数据**补登进注册表。
 *
 * 安全边界（设计 §2.2「封闭静态包」）：
 *   - `page.component` 必须是 `BUILTIN_PAGE_COMPONENTS` 的键；**不在表里 → 跳过该 app**，
 *     不做任何按字符串的动态 `import`。只丢这一个 app 的页面入口，不牵连其它。
 *   - `page.route` 必须是站内绝对路径（以 `/` 开头），否则跳过（防注入怪异路由）。
 *
 * 幂等：多次调用（如重新拉列表）以 `app:<id>` 为键覆盖，不累积幽灵页。
 *
 * @param apps    `/apps` 列表里每条至少含 `id / name / enabled / page`
 * @returns       本次**实际补登**的页面数（便于调用方/测试断言）
 */
export function applyAppPagesFromApps(
  apps: ReadonlyArray<{
    id: string
    name: string
    enabled: boolean
    page?: { nav_label: string; route: string; component: string } | null
  }>,
): number {
  let applied = 0
  for (const app of apps) {
    // 停用的 app 不挂页面入口（与「关即回退默认」一致：停用 → 页面不出现）。
    if (!app.enabled) continue
    const page = app.page
    if (!page || !page.route || !page.component) continue
    if (!page.route.startsWith('/')) continue
    const component = BUILTIN_PAGE_COMPONENTS[page.component]
    // 组件名不在编译期映射表 → fail-closed 跳过（封闭静态包：接口选不出包外组件）。
    if (!component) continue
    registerAppPage({
      id: `app:${app.id}`,
      route: page.route,
      nav: { label: page.nav_label || app.name, order: 60, group: 'extensions' },
      component,
      priority: 'app',
    })
    applied += 1
  }
  return applied
}

/** 清空动态 app 页（**保留**内置页），供「重新拉列表前先撤旧动态页」用。测试也用它复位。 */
export function clearAppPages(): void {
  for (const [id, def] of registry) {
    if (def.priority === 'app') registry.delete(id)
  }
}
