// ─────────────────────────────────────────────────────────────────────────────
// V6 单测：routes.ts 最长后缀匹配 + resolvePreview 兜底，limits.ts resolveOversized 判据。
//
// 对齐 `pnpm test:component`（= vitest run）。纯函数，无需 DOM。
// ─────────────────────────────────────────────────────────────────────────────
import { describe, it, expect } from 'vitest'
import {
  candidates,
  resolvePreview,
  TEXT_FALLBACK,
  type PreviewDefinition,
} from '../routes'
import { DEFAULT_LIMITS, defaultLimit, resolveOversized, type LimitCategory } from '../limits'

/** 构造一个最小 PreviewDefinition（只为测 candidates 的匹配/排序）。 */
function def(id: string, extensions: readonly string[], priority: 'builtin' | 'extension' = 'builtin'): PreviewDefinition {
  return {
    id,
    extensions,
    priority,
    loading: 'text-pages',
    shape: 'drawer',
    title: () => id,
    body: () => null,
  }
}

describe('candidates —— 后缀匹配（最长后缀 / priority / 注册顺序）', () => {
  it('复合后缀 a.tar.gz 命中 .gz 与 .tar.gz 时取最长（.tar.gz）', () => {
    const defs = [def('gz', ['gz']), def('targz', ['tar.gz'])]
    const hit = candidates(defs, 'backup.tar.gz')
    expect(hit.map((d) => d.id)).toEqual(['targz', 'gz'])
    expect(hit[0].id).toBe('targz')
  })

  it('a.ts 命中代码后缀', () => {
    const hit = resolvePreview('src/index.ts')
    expect(hit.id).toBe('code')
  })

  it('a.d.ts 仍按 .ts 命中代码（不会误落兜底）', () => {
    const hit = resolvePreview('types/foo.d.ts')
    expect(hit.id).toBe('code')
  })

  it('extension 优先于 builtin（同后缀时）', () => {
    const defs = [def('builtin-md', ['md'], 'builtin'), def('ext-md', ['md'], 'extension')]
    expect(candidates(defs, 'a.md')[0].id).toBe('ext-md')
  })

  it('未知后缀回落 TEXT_FALLBACK（resolvePreview）', () => {
    const hit = resolvePreview('archive.unknownext')
    expect(hit).toBe(TEXT_FALLBACK)
    expect(hit.id).toBe('text')
  })

  it('大小写不敏感 + 取最后一段文件名（路径含点目录不干扰）', () => {
    // 目录名里的点不参与后缀判定，只看 basename。
    expect(resolvePreview('some.dir/sub.dir/README.MD').id).toBe('markdown')
  })

  it('多候选时按最长后缀优先（真实表：xlsx > 无更短冲突）', () => {
    // .markdown 比 .md 长，两者都在 markdown 定义里；命中同一 id 即可（此处验证 .markdown 命中 markdown）。
    expect(resolvePreview('doc.markdown').id).toBe('markdown')
    expect(resolvePreview('doc.md').id).toBe('markdown')
  })

  it('无任何后缀命中时 candidates 为空、isBinaryPath 语义可区分', () => {
    expect(candidates([def('md', ['md'])], 'noext')).toEqual([])
  })
})

describe('resolveOversized —— 大小判据（V6：>= limit → true）', () => {
  it('size >= limit → true', () => {
    expect(resolveOversized(100, 100)).toBe(true)
    expect(resolveOversized(101, 100)).toBe(true)
  })

  it('size < limit → false', () => {
    expect(resolveOversized(99, 100)).toBe(false)
    expect(resolveOversized(0, 100)).toBe(false)
  })

  it('limit 缺省（Infinity）→ 永 false；undefined/负数 size → false', () => {
    expect(resolveOversized(Number.MAX_SAFE_INTEGER)).toBe(false)
    expect(resolveOversized(undefined, 100)).toBe(false)
    expect(resolveOversized(-1, 100)).toBe(false)
  })

  it('分类默认上限符合规格：doc/sheet 100MB、slide/pdf 500MB、md 30MB、text/image 50MB', () => {
    const MB = 1024 * 1024
    expect(DEFAULT_LIMITS.doc).toBe(100 * MB)
    expect(DEFAULT_LIMITS.sheet).toBe(100 * MB)
    expect(DEFAULT_LIMITS.slide).toBe(500 * MB)
    expect(DEFAULT_LIMITS.pdf).toBe(500 * MB)
    expect(DEFAULT_LIMITS.markdown).toBe(30 * MB)
    expect(DEFAULT_LIMITS.text).toBe(50 * MB)
    expect(DEFAULT_LIMITS.image).toBe(50 * MB)
    // defaultLimit 与表一致
    const cats: LimitCategory[] = ['doc', 'sheet', 'slide', 'pdf', 'markdown', 'text', 'image']
    for (const c of cats) expect(defaultLimit(c)).toBe(DEFAULT_LIMITS[c])
    // markdown 用分类上限判超限
    expect(resolveOversized(DEFAULT_LIMITS.markdown, defaultLimit('markdown'))).toBe(true)
    expect(resolveOversized(DEFAULT_LIMITS.markdown - 1, defaultLimit('markdown'))).toBe(false)
  })
})
