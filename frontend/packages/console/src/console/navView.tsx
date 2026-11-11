/**
 * 左栏导航的事实源（唯一）：一级/二级条目、图标、选中判定、展开态。
 *
 * 纪律（设计 §模块划分与职责）：组件里不得再出现 `to` / `label` / 图标字面量——
 * 新增一个入口只改本文件。
 * 命名口径对齐 workdsh：MCP → 「连接器」、skill → 「技能」，只在 UI 文案层生效，
 * REST / 事件 / 目录名仍是 skills / mcp。
 *
 * 例外：「设置」不是路由入口（它是左栏 footer 的菜单 + 居中弹窗），定义在 `console/settingsView.tsx`。
 */
import type { ReactNode } from 'react'
import { APPS_GROUP, EXTENSIONS_GROUP, resolveAppPages, type AppPageGroup } from './appRegistry'
import { getRuntimeAppPages } from './runtimeAppPages'

export interface NavLeaf {
  readonly id: string
  readonly label: string
  /** pending 项为空串：只渲染、不注册路由、不给占位页（对齐 workdsh 的 pending 规则）。 */
  readonly to: string
  readonly order: number
  readonly pending?: boolean
  /** hover 说明：pending 项必须给，避免"点了没反应"被当成坏入口。 */
  readonly hint?: string
  /**
   * 一级项的图标。**二级项不给图标**：缩进 26px 的子项再挂图标会把层级读平
   * （workdsh 的二级项也是纯文字），图标只用来区分一级入口。
   */
  readonly icon?: ReactNode
}

export interface NavGroup {
  readonly id: string
  readonly label: string
  readonly order: number
  readonly icon: ReactNode
  readonly children: readonly NavLeaf[]
}

export type NavEntry = NavLeaf | NavGroup

/**
 * 从统一注册表（`console/appRegistry.ts`）**派生**左栏条目。
 *
 * 本文件不再手写 `to` / `label` / 图标的字面量——新增页面只在注册表登记一条，
 * `NAV_ENTRIES` 与 `app.tsx` 的 `<Route>` 自动跟随（同源）。派生的规则：
 *   - `nav` 为空的页（纯路由页）不进左栏；
 *   - `nav.group` 命中 `GROUP_META` 的二级项聚进对应一级组（组元数据取自表内 GROUP）；
 *   - 其余 `nav` 为顶层一级项，图标取 `nav.icon`；
 *   - 同级按 `nav.order` 升序（`resolveAppPages` 已排好，这里只做分桶）。
 */

/**
 * 一级组的元数据表：`group id → 组长元数据`。
 *
 * `buildNavEntries` 按本表**遍历**建组（不再 hardcode 单个扩展组的特判）。新增一个一级组 =
 * 在 appRegistry 加一条 `XXX_GROUP` 常量 + 在此表登记一行，建组逻辑无需再改。
 * 组的顺序仍由各 `GROUP.order` 决定。
 */
const GROUP_META: Readonly<Record<AppPageGroup, { id: string; label: string; order: number; icon: ReactNode }>> = {
  extensions: EXTENSIONS_GROUP,
  apps: APPS_GROUP,
}

function buildNavEntries(): readonly NavEntry[] {
  const leaves: NavLeaf[] = []
  const groupChildren = new Map<string, NavLeaf[]>()

  for (const def of resolveAppPages()) {
    if (!def.nav) continue
    const leaf: NavLeaf = {
      id: def.id,
      label: def.nav.label,
      to: def.route,
      order: def.nav.order,
      icon: def.nav.group ? undefined : def.nav.icon,
    }
    if (def.nav.group) {
      const bucket = groupChildren.get(def.nav.group) ?? []
      bucket.push(leaf)
      groupChildren.set(def.nav.group, bucket)
    } else {
      leaves.push(leaf)
    }
  }

  const entries: NavEntry[] = [...leaves]
  // 按 GROUP_META 表**遍历**建组：每个组仅当有二级项时出现（空组没有意义）。
  // 二期 C（WP-C7）：运行期沙箱应用页作为「应用」组的二级项并入（与内置页并列），
  // 它们**不**来自 A+Slot 的编译期 `resolveAppPages()`，而是独立运行期注册表
  // （`runtimeAppPages`），渲染走沙箱 iframe（见 app.tsx / SandboxAppFrame）。
  for (const group of Object.values(GROUP_META)) {
    const children: NavLeaf[] = [...(groupChildren.get(group.id) ?? [])]
    if (group.id === 'apps') {
      for (const rp of getRuntimeAppPages()) {
        children.push({ id: `runtime:${rp.appId}`, label: rp.navLabel, to: rp.route, order: rp.order })
      }
    }
    if (children.length > 0) {
      entries.push({
        id: group.id,
        label: group.label,
        order: group.order,
        icon: group.icon,
        children: [...children].sort((a, b) => a.order - b.order),
      })
    }
  }

  return entries.sort((a, b) => a.order - b.order)
}

/**
 * 左栏导航条目（唯一事实源，**由注册表派生**）。模块加载时算一次；
 * 动态 app 页补登后如需刷新左栏，由消费方重新 import 本模块取最新值（当前 app 页
 * 在 bootstrap 期一次性补登，先于挂载，故此处取值即为最终态）。
 */
export const NAV_ENTRIES: readonly NavEntry[] = buildNavEntries()
export function isGroup(entry: NavEntry): entry is NavGroup {
  return 'children' in entry
}

/**
 * 命中判定：按**路径段前缀**——`/sessions/abc` 命中 `/sessions`，
 * `/settings/model` 命中 `/settings`（设置区内分节，左栏入口仍要高亮）。
 * 用段边界而不是裸 `startsWith`：`/settings-x` 不该命中 `/settings`。
 *
 * 组**没有自己的落地路由**（点组行只切换展开）：组的命中只看它有没有二级项被命中。
 */
export function isEntryActive(pathname: string, entry: NavEntry): boolean {
  if (isGroup(entry)) {
    return entry.children.some((c) => c.to !== '' && isPathActive(pathname, c.to))
  }
  return isPathActive(pathname, entry.to)
}

export function isChildActive(pathname: string, child: NavLeaf): boolean {
  return child.to !== '' && isPathActive(pathname, child.to)
}

/** 当前命中的二级项（父组用于决定展开与弱选中态）。 */
export function activeChildOf(pathname: string, group: NavGroup): NavLeaf | undefined {
  return group.children.find((c) => isChildActive(pathname, c))
}

function isPathActive(pathname: string, to: string): boolean {
  return pathname === to || pathname.startsWith(`${to}/`)
}

/**
 * 左栏/页面上下文：**只**用于状态条上那句底部事实提示（"会话跑在哪个底座上…"只在问答页有意义）。
 * 会话区不由此判定——它常驻左栏（切页不跳版），不随路由出现或消失。
 */
export function railContextOf(pathname: string): 'chat' | 'extensions' | 'manage' {
  if (pathname.startsWith('/sessions')) return 'chat'
  if (pathname.startsWith('/extensions')) return 'extensions'
  return 'manage'
}

/**
 * 展开态的唯一变更入口（纯函数，便于回归）。
 *
 * 收**目标值**而不是"取反"：组的有效展开态可能是"没被切过 + 当前路由命中"（见 `groupExpanded`），
 * 只按 state 取反会让自动展开的组第一次点击变成空操作。
 */
export function setExpanded(
  state: Readonly<Record<string, boolean>>,
  groupId: string,
  expanded: boolean,
): Record<string, boolean> {
  return { ...state, [groupId]: expanded }
}

/**
 * 组的展开态：显式切换过就听用户的（`false` 也算显式），否则**只在当前路由命中该组时默认展开**。
 *
 * 为什么不默认全展开：左栏把导航与会话列表合在一栏里，「扩展」的 3 个二级项常驻要吃掉
 * 90px；1440×800 下会话列表可视高度只剩 332px（≈5.5 条），而 T5 的阈值是 ≥7 条。
 * 会话页根本用不到二级项——按需展开两边都不吃亏。
 */
export function groupExpanded(
  state: Readonly<Record<string, boolean>>,
  pathname: string,
  group: NavGroup,
): boolean {
  return state[group.id] ?? isEntryActive(pathname, group)
}
