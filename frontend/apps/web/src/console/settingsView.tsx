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
import { ClusterOutlined, KeyOutlined, SettingOutlined } from '@ant-design/icons'

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

export const SETTINGS_SECTIONS: readonly SettingsSection[] = [
  { id: 'general', label: '通用', hint: '工作区目录白名单', icon: <SettingOutlined />, order: 10 },
  { id: 'agents', label: 'Agent 管理', hint: '注册 / 启停 / provider 档案', icon: <ClusterOutlined />, order: 20 },
  { id: 'token', label: '访问令牌', hint: '后端启用 MATE_TOKEN 时必填', icon: <KeyOutlined />, order: 30 },
]

export function settingsSectionOf(id: string): SettingsSection | undefined {
  return SETTINGS_SECTIONS.find((s) => s.id === id)
}
