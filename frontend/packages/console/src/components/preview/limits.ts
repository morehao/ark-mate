// ─────────────────────────────────────────────────────────────────────────────
// limits.ts —— 预览尺寸上限（按分类给默认上限）+ resolveOversized
//
// 超过上限不进入渲染器，直接落 Oversized 状态页（含下载出口）。
// 分类默认上限（FE-P3 §3.3）：
//   doc    100 MB   —— 一般文档（Office 等非 PDF/幻灯片的文档）
//   sheet  100 MB   —— 表格（xlsx/xls/csv…）
//   slide  500 MB   —— 幻灯片（PPT 系，最宽松）
//   pdf    500 MB   —— PDF（最宽松）
//   markdown 30 MB  —— Markdown（解析成本最高，最严）
//   text   50 MB    —— 代码 / 纯文本
//   image  50 MB    —— 图片
// ─────────────────────────────────────────────────────────────────────────────

/** 分类默认上限（字节）。 */
export const DEFAULT_LIMITS = {
  /** 文档类（Office 等）：100 MB。 */
  doc: 100 * 1024 * 1024,
  /** 表格：100 MB。 */
  sheet: 100 * 1024 * 1024,
  /** 幻灯片（PPT）：500 MB。 */
  slide: 500 * 1024 * 1024,
  /** PDF：500 MB。 */
  pdf: 500 * 1024 * 1024,
  /** Markdown：30 MB。 */
  markdown: 30 * 1024 * 1024,
  /** 代码 / 纯文本：50 MB。 */
  text: 50 * 1024 * 1024,
  /** 图片：50 MB。 */
  image: 50 * 1024 * 1024,
} as const

export type LimitCategory = keyof typeof DEFAULT_LIMITS

/** 该分类的默认上限。 */
export function defaultLimit(category: LimitCategory): number {
  return DEFAULT_LIMITS[category]
}

/**
 * 是否超出上限（size >= limit 即视为超限；limit 缺省为 Infinity 时永 false）。
 * 判据取 `>=`：单测 V6 要求 size>=limit → true。
 */
export function resolveOversized(size: number | undefined, limit: number = Number.POSITIVE_INFINITY): boolean {
  if (size === undefined || size < 0) return false
  return size >= limit
}
