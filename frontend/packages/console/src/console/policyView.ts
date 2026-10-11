import type { PermissionAction, PermissionPolicy } from '@ark-mate/types'

// ——— 权限策略视图模型（C1：设置在「Agent 管理」节展示/编辑；纯函数，便于 Node 侧校验） ———

/** 解析工具项 JSON（对象且值 ∈ ask|allow|deny）；空串 → {}；非法 → 抛错供表单校验展示。 */
export function parsePolicyTools(raw?: string): Record<string, PermissionAction> {
  const t = (raw ?? '').trim()
  if (!t) return {}
  let obj: unknown
  try {
    obj = JSON.parse(t)
  } catch {
    throw new Error('工具项需为 JSON 对象，如 {"bash":"deny"}')
  }
  if (typeof obj !== 'object' || obj === null || Array.isArray(obj)) throw new Error('工具项需为 JSON 对象')
  const out: Record<string, PermissionAction> = {}
  for (const [k, v] of Object.entries(obj as Record<string, unknown>)) {
    if (v !== 'ask' && v !== 'allow' && v !== 'deny') throw new Error(`工具项 ${k} 的动作需为 ask|allow|deny`)
    out[k] = v
  }
  return out
}

/** 权限策略一行摘要（Agent 管理节展示）：`ask` / `allow` / `deny` + 工具项数 + 原生模式。 */
export function policySummary(p?: PermissionPolicy): string {
  if (!p) return '未声明（全 ask）'
  const base = p.default || 'ask'
  const n = Object.keys(p.tools ?? {}).length
  const native = p.native_mode && p.native_mode !== 'auto' ? ` · 原生 ${p.native_mode}` : ''
  return `${base}${n > 0 ? ` · ${n} 个工具项` : ''}${native}`
}

/** 策略动作 → antd Tag 颜色（allow 绿 / deny 红 / ask 金）。 */
export function policyColor(action?: string): string {
  switch (action) {
    case 'allow':
      return 'green'
    case 'deny':
      return 'red'
    default:
      return 'gold'
  }
}
