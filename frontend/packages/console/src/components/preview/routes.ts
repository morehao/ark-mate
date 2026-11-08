// ─────────────────────────────────────────────────────────────────────────────
// routes.ts —— 预览渲染器注册表（唯一事实源）
//
// 参照 dsh DocumentPreviewDefinition，裁剪到 React 19 + 无 cordis：
// 注册表 + 后缀匹配（priority + 最长后缀）+ 加载模式声明 + 懒加载 body。
//
// ⚠ 本文件是**唯一注册表**：body 渲染器由各 owner 只写自己文件（renderers/XxxRenderer.tsx，
//   默认导出），注册条目统一在这里追加。改注册表请只改本文件，避免多处注册漂移。
// ─────────────────────────────────────────────────────────────────────────────
import { lazy, type ComponentType } from 'react'

/** 内容如何被读取（与渲染解耦；由 PreviewProvider 依据它去 fetch 签名 url）。 */
export type LoadMode =
  | 'text-pages' // 分页读文本（Markdown / 代码 / 纯文本）
  | 'bytes-complete' // 整块字节（PDF / 表格 / 图片）
  | 'renderer' // 渲染器自行取数据（如 Office 先转 PDF）

/** 预览外壳形态：文档走右侧侧栏，图片走居中大图（§4.0 的唯一事实源）。 */
export type Shape = 'drawer' | 'modal'

/** 每个 body 组件收到的 props（外壳把已取好的内容注入）。 */
export interface PreviewBodyProps {
  /** 文件名（展示 + 决定标题）。 */
  fileName: string
  /** 已按 loadMode 取好的内容：text 类给 string，bytes 类给 Uint8Array，renderer 类给签名 url。 */
  content: string | Uint8Array | { url: string }
  /** 原始字节大小（给状态页展示）。 */
  sizeBytes?: number
  /**
   * mate 体系 file_id（= core_file_upload.id），由 PreviewProvider 注入。
   * renderer 模式的渲染器（如 Office 先经服务端转 PDF）需要它作转换入参——
   * content 里的 { url } 是存储 key 签名链接，无法可靠还原 file_id。
   */
  fileId?: string
}

/** 一条渲染器定义。 */
export interface PreviewDefinition {
  /** 唯一标识，也是 body 的 key。 */
  id: string
  /** 后缀（无前导点）；支持复合后缀如 tar.gz。 */
  extensions: readonly string[]
  /** extensions 中「非文本」的那些（用于排除纯文本兜底，可选）。 */
  binaryExtensions?: readonly string[]
  /** extension 胜 builtin（能力升级时用）。 */
  priority: 'builtin' | 'extension'
  /** 加载模式。 */
  loading: LoadMode
  /** 外壳形态（§4.0）。 */
  shape: Shape
  /** 显示名（工具栏/类型行）。 */
  title: () => string
  /** 正文组件（懒加载）。 */
  body: ComponentType<PreviewBodyProps>
}

/** 取路径最后一段的文件名，小写。 */
function fileNameOf(path: string): string {
  // 用 split/join 而非 String.replaceAll（target=ES2020 不含 ES2021 的 replaceAll）。
  const normalized = path.split('\\').join('/').toLowerCase()
  return normalized.slice(normalized.lastIndexOf('/') + 1)
}

/** 规整后缀（去前导点、小写）。 */
function normalizeSuffix(ext: string): string {
  return ext.toLowerCase().replace(/^\./u, '')
}

/** 文件名命中的最长声明后缀长度（0 表示不命中）。 */
function matchedSuffixLength(name: string, extensions: readonly string[]): number {
  return Math.max(
    0,
    ...extensions
      .map(normalizeSuffix)
      .filter((ext) => name.endsWith(`.${ext}`))
      .map((ext) => ext.length),
  )
}

/**
 * 返回所有命中该路径的实现，按自动选择顺序：
 * 先 priority（extension > builtin），再最长后缀，再注册顺序。
 */
export function candidates(defs: readonly PreviewDefinition[], path: string): PreviewDefinition[] {
  const name = fileNameOf(path)
  return defs
    .map((definition, order) => ({
      definition,
      order,
      rank: definition.priority === 'builtin' ? 0 : 1,
      length: matchedSuffixLength(name, definition.extensions),
    }))
    .filter((c) => c.length > 0)
    .sort((a, b) => b.rank - a.rank || b.length - a.length || a.order - b.order)
    .map((c) => c.definition)
}

/** 是否命中任何声明的 binary 后缀（用于纯文本兜底的排除，可选）。 */
export function isBinaryPath(defs: readonly PreviewDefinition[], path: string): boolean {
  const name = fileNameOf(path)
  return defs.some((d) => matchedSuffixLength(name, d.binaryExtensions ?? []) > 0)
}

// ── 内置注册表（body 用 React.lazy 懒加载，重库不进主包）─────────────────────────
const CODE_EXTS = [
  'ts', 'tsx', 'js', 'jsx', 'mjs', 'cjs', 'go', 'py', 'rb', 'rs', 'java', 'kt', 'c', 'cc', 'cpp', 'h', 'hpp',
  'cs', 'php', 'swift', 'dart', 'lua', 'sh', 'bash', 'zsh', 'sql', 'json', 'jsonl', 'yaml', 'yml', 'toml',
  'ini', 'xml', 'css', 'scss', 'less', 'log', 'env', 'dockerfile',
] as const

export const BUILTIN_PREVIEWS: readonly PreviewDefinition[] = [
  {
    id: 'pdf',
    extensions: ['pdf'],
    binaryExtensions: ['pdf'],
    priority: 'builtin',
    loading: 'bytes-complete',
    shape: 'drawer',
    title: () => 'PDF',
    body: lazy(() => import('./renderers/PdfRenderer')),
  },
  {
    id: 'sheet',
    extensions: ['xlsx', 'xls', 'csv', 'tsv'],
    binaryExtensions: ['xlsx', 'xls'],
    priority: 'builtin',
    loading: 'bytes-complete',
    shape: 'drawer',
    title: () => '表格',
    body: lazy(() => import('./renderers/SheetRenderer')),
  },
  {
    id: 'markdown',
    extensions: ['md', 'markdown'],
    priority: 'builtin',
    loading: 'text-pages',
    shape: 'drawer',
    title: () => 'Markdown',
    body: lazy(() => import('./renderers/MarkdownRenderer')),
  },
  {
    // HTML 页面：按 bytes-complete 取整块字节，交给 HtmlRenderer 用受控 sandboxed iframe
    // 真正「渲染页面」。——注意 html/htm 已从 CODE_EXTS 移除，否则会被 code 先命中显示源码。
    id: 'html',
    extensions: ['html', 'htm'],
    priority: 'builtin',
    loading: 'bytes-complete',
    shape: 'drawer',
    title: () => 'HTML',
    body: lazy(() => import('./renderers/HtmlRenderer')),
  },
  {
    id: 'code',
    extensions: CODE_EXTS,
    priority: 'builtin',
    loading: 'text-pages',
    shape: 'drawer',
    title: () => '代码',
    body: lazy(() => import('./renderers/CodeRenderer')),
  },
  {
    id: 'image',
    extensions: ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'avif'],
    binaryExtensions: ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'ico', 'avif'],
    priority: 'builtin',
    loading: 'bytes-complete',
    shape: 'modal',
    title: () => '图片',
    body: lazy(() => import('./renderers/ImageRenderer')),
  },
  // Word/PPT：服务端经 libreoffice-kit 转 PDF，再由 OfficeRenderer 复用 PdfRenderer 渲染。
  // loading='renderer'：Provider 只给签名 url（此处其实用不上），真正的转换由 OfficeRenderer
  // 用 fileId 调 POST /files/:id/convert-pdf 完成——故 fileId 必须由 Provider 注入（见 PreviewProvider）。
  {
    id: 'office',
    extensions: ['doc', 'docx', 'ppt', 'pptx'],
    binaryExtensions: ['doc', 'docx', 'ppt', 'pptx'],
    priority: 'builtin',
    loading: 'renderer',
    shape: 'drawer',
    title: () => 'Office',
    body: lazy(() => import('./renderers/OfficeRenderer')),
  },
]

/** 纯文本兜底：无任何定义命中时用它（等价 dsh 的 fallback）。 */
export const TEXT_FALLBACK: PreviewDefinition = {
  id: 'text',
  extensions: [],
  priority: 'builtin',
  loading: 'text-pages',
  shape: 'drawer',
  title: () => '文本',
  body: lazy(() => import('./renderers/TextRenderer')),
}

/** 解析某文件名应使用的预览定义（含兜底）。 */
export function resolvePreview(path: string, defs: readonly PreviewDefinition[] = BUILTIN_PREVIEWS): PreviewDefinition {
  return candidates(defs, path)[0] ?? TEXT_FALLBACK
}
