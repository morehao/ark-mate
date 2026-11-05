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
import {
  AppstoreOutlined,
  BarChartOutlined,
  ClockCircleOutlined,
  FolderOpenOutlined,
} from '@ant-design/icons'

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
  readonly to: string
  readonly order: number
  readonly icon: ReactNode
  readonly children: readonly NavLeaf[]
}

export type NavEntry = NavLeaf | NavGroup

export const NAV_ENTRIES: readonly NavEntry[] = [
  {
    id: 'extensions',
    label: '扩展',
    to: '/extensions',
    order: 20,
    icon: <AppstoreOutlined />,
    children: [
      { id: 'experts', label: '专家', to: '/extensions/experts', order: 21 },
      { id: 'skills', label: '技能', to: '/extensions/skills', order: 22 },
      { id: 'connectors', label: '连接器', to: '/extensions/connectors', order: 23 },
    ],
  },
  { id: 'materials', label: '资料库', to: '/materials', order: 30, icon: <FolderOpenOutlined /> },
  { id: 'automations', label: '定时任务', to: '/automations', order: 40, icon: <ClockCircleOutlined /> },
  { id: 'usage', label: '用量', to: '/usage', order: 50, icon: <BarChartOutlined /> },
]
export function isGroup(entry: NavEntry): entry is NavGroup {
  return 'children' in entry
}

/**
 * 命中判定：按**路径段前缀**——`/sessions/abc` 命中 `/sessions`，
 * `/settings/model` 命中 `/settings`（设置区内分节，左栏入口仍要高亮）。
 * 用段边界而不是裸 `startsWith`：`/settings-x` 不该命中 `/settings`。
 */
export function isEntryActive(pathname: string, entry: NavEntry): boolean {
  if (isGroup(entry)) {
    if (isPathActive(pathname, entry.to)) return true
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
 * 为什么不默认全展开：左栏把导航与会话列表合在一栏里，「扩展」的 2 个二级项 + 占位项常驻要吃掉
 * 90px；1440×800 下会话列表可视高度只剩 332px（≈5.5 条），而 T5 的阈值是 ≥7 条。
 * 会话页根本用不到二级项、扩展页又不渲染会话列表——按需展开两边都不吃亏。
 */
export function groupExpanded(
  state: Readonly<Record<string, boolean>>,
  pathname: string,
  group: NavGroup,
): boolean {
  return state[group.id] ?? isEntryActive(pathname, group)
}
