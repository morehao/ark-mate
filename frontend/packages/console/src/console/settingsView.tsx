/**
 * 设置（左栏 footer 的「设置」按钮 + 那一个**大弹窗**）的唯一事实源：新增一节只改本文件。
 *
 * 交互形态（2026-10-10 定稿）：
 *  ① 左栏左下「设置」固定在 footer，点一下**直接开弹窗**（没有二级菜单那一跳）；
 *  ② 弹窗是"标题 设置 + 左侧分节列表 + 右侧内容"的一体式面板
 *     （`components/settings/SettingsDialog.tsx`），换节在弹窗内完成，不换页、不改 URL。
 *
 * **为什么不是页面、也不进左栏主导航**：设置是「同一配置面下的若干动作」，不是并列的内容面。
 * workdsh 的对应做法是 `plugins/admin/src/client/sections/*`（管理面按分节组织，且能力中心
 * 明确保持"单个导航项"以免重复导航，见 `plugins/experts/src/client.tsx:32`）。做成页面会额外
 * 占掉左栏高度与会话列表的宽度预算（T5 的 ≥7 条），做成主导航条目则会和「扩展」形成两套
 * 含义不同的手风琴。一体式弹窗还有个直接好处：从任何页面都能就地改设置，且分节之间一步切换。
 */
import type { ReactNode } from 'react'
import { ApiOutlined, KeyOutlined, SafetyCertificateOutlined, SettingOutlined } from '@ant-design/icons'
import { isDesktopEntry } from '../capabilityFacts'

export interface SettingsSection {
  readonly id: string
  readonly label: string
  /** 一句话说明这一节管什么（弹窗左栏第二行）。 */
  readonly hint: string
  readonly icon: ReactNode
  readonly order: number
}

/** 左栏 footer 的触发器（固定左下）。 */
export const SETTINGS_MENU = {
  label: '设置',
  icon: <SettingOutlined />,
} as const satisfies { label: string; icon: ReactNode }

/**
 * 分节的**命名纪律**（A13 守卫钉住）：这一节面向用户的标题里不出现 `provider`/`mcp`/`skill`
 * 这类代码词，也不出现任何底座名（`dsh`/`runtime`/`底座`）。「模型」这一节的标题与
 * 两块小标题只说「供应商」「模型」——用户视角的词，而不是资源层的词。
 */
// v19 删掉的是「Agent 管理」那一节（注册 / 启停 / 凭据引用）：底座由构建期固定、用户不可自定义，
// 那一节里的每个动作都没有接收方。**「模型」那一节的两个标题一个字都没动**——
// 它的文案由守卫 `TestSettingsModelCopyAvoidsProvider` 钉着（用户词是「供应商」「模型」）。
export const SETTINGS_SECTIONS: readonly SettingsSection[] = [
  { id: 'general', label: '通用', hint: '工作区目录白名单', icon: <SettingOutlined />, order: 10 },
  { id: 'model', label: '模型', hint: '供应商 / 模型清单 / 默认模型 / 生效结论', icon: <ApiOutlined />, order: 15 },
  { id: 'command', label: '命令规则', hint: '哪些命令可直接执行 / 必须确认 / 禁止', icon: <SafetyCertificateOutlined />, order: 20 },
  { id: 'token', label: '访问令牌', hint: '后端启用 MATE_TOKEN 时必填', icon: <KeyOutlined />, order: 30 },
]

export function settingsSectionOf(id: string): SettingsSection | undefined {
  return SETTINGS_SECTIONS.find((s) => s.id === id)
}

/**
 * 本次入口**实际该显示的分节**。
 *
 * `token`（访问令牌）在**桌面壳里是死 UI**：壳的反代转发前会**覆盖**任何客户端带来的
 * `Authorization`（`apps/desktop/src/scheme.ts`：`headers.set('authorization', ...)`），
 * 所以用户在这一节里粘的令牌根本不会生效——留着它就是"看着能配、其实无效"的那种界面。
 * 判据取服务端声明 `entries.desktop`（**不嗅探客户端**：UA / `window.electron` 一律不许当判据），
 * 声明没拉到（`UNKNOWN`）时**照常渲染**——少一块功能比多一块无效界面更糟。
 *
 * 注意这**不是**"桌面就该少一块"的通用许可：只有"在这个入口里必然无效"的界面才允许这样收起来。
 */
export function settingsSections(): readonly SettingsSection[] {
  return isDesktopEntry() ? SETTINGS_SECTIONS.filter((s) => s.id !== 'token') : SETTINGS_SECTIONS
}
