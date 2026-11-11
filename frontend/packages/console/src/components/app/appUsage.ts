import type { App as AppType } from '@ark-mate/types'

/**
 * 应用「使用说明」的纯函数层（v42）——与 UI 解耦，便于 `pnpm test:console`（node --test）直接校验。
 *
 * 这些函数只做「文本派生的兜底与解析」：
 *   - `appTaglineOf`：卡片主文案（tagline → description → 从成员派生），保证老应用不空转；
 *   - `parseGuide`：把 usage_guide 自由文本按「场景/需要/产出/边界」四段轻量解析；
 *   - `defaultCompose`：「开始用」输入框的默认话术。
 *
 * **用法（usage_guide）是给人看的**，不是模型 system prompt。
 */

export interface ParsedGuide {
  scene?: string
  need?: string
  out?: string
  limit?: string
  /** 四段都没命中时，整段文本原样返回（不硬拆）。 */
  plain?: string
}

/** 卡片上的能力句：tagline 优先，其次 description，最后从 members 派生一句兜底。 */
export function appTaglineOf(app: {
  name: string
  tagline?: string
  description?: string
  members?: AppType['members']
}): string {
  if (app.tagline && app.tagline.trim()) return app.tagline.trim()
  if (app.description && app.description.trim()) return app.description.trim()
  const m = app.members
  const parts: string[] = []
  if (m?.experts?.length) parts.push(`专家 ${m.experts.length}`)
  if (m?.automations?.length) parts.push(`定时 ${m.automations.length}`)
  if (m?.skills?.length) parts.push(`技能 ${m.skills.length}`)
  if (m?.mcps?.length) parts.push(`连接器 ${m.mcps.length}`)
  const combo = parts.length ? `它带了${parts.join('、')}` : '它还没配置成员'
  return `还没有使用说明——先描述你想要的「${app.name}」结果，助手会按它的能力来帮你（${combo}）。`
}

/** 把 usage_guide 自由文本按四段标签解析；全无命中 → { plain: 原文 }。 */
export function parseGuide(raw?: string): ParsedGuide | null {
  const s = (raw ?? '').trim()
  if (!s) return null
  const pick = (labels: string[]): string | undefined => {
    for (const lb of labels) {
      const re = new RegExp(`${lb}\\s*[：:]\\s*([^\\n；;]+)`)
      const m = s.match(re)
      if (m) return m[1].trim()
    }
    return undefined
  }
  const scene = pick(['适用场景', '场景'])
  const need = pick(['需要提供', '需要准备', '输入'])
  const out = pick(['产出', '输出'])
  const limit = pick(['边界', '不包含', '限制'])
  if (!scene && !need && !out && !limit) return { plain: s }
  return { scene, need, out, limit }
}

/** 「开始用」输入框默认内容：优先第一个示例，其次 tagline，最后兜底一句。 */
export function defaultCompose(name: string, tagline?: string, examples?: string[]): string {
  if (examples && examples.length > 0 && examples[0].trim()) return examples[0]
  if (tagline && tagline.trim()) return tagline.trim()
  return `用「${name}」帮我处理一个任务`
}
