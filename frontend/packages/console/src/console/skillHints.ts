import type { ComposerSkill } from '@ark-mate/types'

/**
 * composer `/` 技能选择器的**纯逻辑**（v4 P1；无 React、无 IO）。
 *
 * 与斜杠**命令**补全（commandHints.ts）刻意分开：
 *   - 命令是**底座自报**的（ADR-17；dsh 不报，永远为空）；
 *   - 技能是**平台内置**的（`GET /composer/skills`，一次性注入的协作技能）。
 * 两者语义不同（命令由底座执行、技能是平台拼进本轮前缀的契约），不混列同一个弹层。
 *
 * 触发口径与命令一致：草稿以 `/` 开头且**尚未出现空白**才算选择上下文——一旦进参数区就收起。
 */

/** 判断草稿是否处于技能选择上下文。返回待匹配前缀（可空串）；null = 不触发。 */
export function skillHintQuery(draft: string): string | null {
  if (!draft.startsWith('/')) return null
  const rest = draft.slice(1)
  if (/\s/.test(rest)) return null
  return rest.toLowerCase()
}

/** 按前缀筛选技能（大小写不敏感，保留后端顺序）；前缀空 → 全部。 */
export function matchSkills(skills: ComposerSkill[] | undefined, query: string): ComposerSkill[] {
  if (!skills || skills.length === 0) return []
  return skills.filter((s) => s.id.toLowerCase().startsWith(query) || s.name.toLowerCase().startsWith(query))
}
