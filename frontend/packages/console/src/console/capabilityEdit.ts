import type { CapabilityWarning, SessionCapabilities, SessionCapabilitiesPatch } from '@ark-mate/types'
import type { CapabilityChoice } from './capabilityView'

// ——— 会话内改能力的**请求语义**（M3-8；纯函数，便于 Node 侧单测） ———
//
// 与输入区的能力入口（`CapabilityBar`）共用同一套三态：`null` = 未声明（回落默认）、`[]` = 明确一个
// 都不要、非空 = 白名单；专家 `''` = 明确不用、非空 = 专家 id。
//
// 这里只回答两个问题：**读面怎么折成三态**、**某一格改动要发什么补丁**。两者分开写是因为
// 服务端的补丁语义是「键缺失 = 不改这一格」——整体替换会让"只换技能"顺手把专家清掉。

/** 读面（会话快照的 `capabilities`）→ 三态选择。整个对象缺失 = 三格都未声明。 */
export function choiceFromCapabilities(caps?: SessionCapabilities | null): CapabilityChoice {
  if (!caps) return { expert: null, skills: null, mcps: null }
  return {
    // `undefined`（键缺失）与 `null` 同义；`''` 必须原样保留（明确不用专家）。
    expert: caps.expert === undefined ? null : caps.expert,
    skills: caps.skills === undefined ? null : caps.skills,
    mcps: caps.mcps === undefined ? null : caps.mcps,
  }
}

/**
 * 技能/MCP 的勾选变化 → **只带这一格**的补丁。
 *
 * `next === null` 是「跟随默认」（回未声明），`[]` 是「一个都不要」——两者在服务端是两个
 * 代际，所以这里不合并、不省略。
 */
export function selectionPatchFor(
  kind: 'skills' | 'mcps',
  next: string[] | null,
): SessionCapabilitiesPatch {
  return kind === 'skills' ? { skills: next } : { mcps: next }
}

/**
 * 把一次三态补丁就地合并进**本地**三态选择（新建会话面板用：那边没有服务端回执可读）。
 *
 * 语义与 wire 一致：**键缺失 = 不改这一格**，`null` = 回未声明，`[]` = 明确一个都不要。
 * 之所以必须按"键在不在"判而不是按"值是不是 null"，是因为 `null` 本身就是一个要落下去的值。
 */
export function mergeCapabilityPatch(
  current: CapabilityChoice,
  patch: SessionCapabilitiesPatch,
): CapabilityChoice {
  return {
    expert: 'expert' in patch ? (patch.expert ?? null) : current.expert,
    skills: 'skills' in patch ? (patch.skills ?? null) : current.skills,
    mcps: 'mcps' in patch ? (patch.mcps ?? null) : current.mcps,
  }
}

/**
 * 换专家 → 换成**它的能力预设**：显式技能/MCP 一并交回「未声明」，由服务端按新专家重新物化。
 *
 * 这是「专家 = 能力预设」那条产品口径的落点（与创建时 `{expert}` 不带 skills/mcps 同义）。
 * `null`（跟随默认）只动专家这一格：用户只取消了专家，没要求把手选的技能也清掉。
 */
export function expertSwitchPatch(expert: string | null): SessionCapabilitiesPatch {
  if (expert === null) return { expert: null }
  return { expert, skills: null, mcps: null }
}

/** 输入区要显示的一个 chip（`count` 只有技能/MCP 才有意义）。 */
export interface CapabilityChip {
  kind: 'expert' | 'skills' | 'mcps'
  label: string
  /** 专家 chip 的头像字（名字首字）；技能/MCP 不用。 */
  avatar?: string
  count?: number
  title: string
}

/**
 * 三态选择 → 输入区要显示哪几个 chip。
 *
 * 规则与原型一致：**只有被声明过的那一格才出现 chip**（未声明 = 跟随默认，界面上什么都不加）。
 * 专家用「名字首字」当头像；`''`（明确不用专家）显示成「通用」——它也是一种声明，
 * 只是没有具体专家，藏起来会让"跟随默认"与"明确不用"重新变成同一个样子。
 */
export function capabilityChips(choice: CapabilityChoice, expertName?: string): CapabilityChip[] {
  const out: CapabilityChip[] = []
  if (choice.expert !== null) {
    const label = choice.expert === '' ? '通用' : expertName || choice.expert
    out.push({
      kind: 'expert',
      label,
      avatar: label.slice(0, 1),
      title:
        choice.expert === ''
          ? '专家：通用（明确不用专家）· 点击更换'
          : `专家：${label} · 点击更换（会换成它的技能/MCP 预设）`,
    })
  }
  if (choice.skills !== null) {
    out.push({
      kind: 'skills',
      label: '技能',
      count: choice.skills.length,
      title:
        choice.skills.length === 0
          ? '技能：一个都不要 · 点击更改'
          : `技能：只用这 ${choice.skills.length} 个 · 点击更改`,
    })
  }
  if (choice.mcps !== null) {
    out.push({
      kind: 'mcps',
      label: '连接器',
      count: choice.mcps.length,
      title:
        choice.mcps.length === 0
          ? '连接器：一个都不要 · 点击更改'
          : `连接器：只注入这 ${choice.mcps.length} 个 · 点击更改`,
    })
  }
  return out
}

/**
 * 改完能力后的提示文案（toast）。
 *
 * 主句固定是「下一条消息生效」——它同时覆盖两种处境：空闲时下一条就是下一轮；忙时当前轮
 * 不打断，也是下一条。**不做 pending 横幅/代际号**（原型第 6 条）。
 * 服务端回带的 `warnings`（推荐被跳过、换专家不重注入人格…）缀在后面，不另开提示。
 */
export function capabilityToastText(warnings?: CapabilityWarning[] | null): string {
  const base = '已更新能力 · 下一条消息生效'
  const reasons = (warnings ?? []).map((w) => (w.reason ?? '').trim()).filter((r) => r !== '')
  return reasons.length === 0 ? base : `${base}（${reasons.join('；')}）`
}
