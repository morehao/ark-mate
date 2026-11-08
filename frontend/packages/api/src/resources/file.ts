// ─────────────────────────────────────────────────────────────────────────────
// api/resources/file.ts —— 全局唯一「文件取数」入口（§3.5.7 / §3.5.8）
//
// 所有模块（附件/产物/资料）只返回 file_id；前端统一经这里：先拿签名 url，再 fetch。
// 服务端不经业务接口直出内容；唯一吐字节的是 ginupload 的签名对象端点（presign-url → objects）。
// 依赖：现有 http（baseURL '/v1'，Envelope 解包）。
// ─────────────────────────────────────────────────────────────────────────────
import { http } from '../request'

/** `POST /files/:id/presign-url` 的响应（ginupload presignURLResponse）。 */
export interface PresignResp {
  url: string
  method: string
  headers: Record<string, string>
  expiresIn: number
}

/**
 * 判断一个 file_id 是否是 DSH 原生 id（而不是本仓 mate 的 `core_file_upload.id`）。
 *
 * why：mate 的 file_id = `core_file_upload.id`（golib 里 `StringID.BeforeCreate` 生成的
 * UUID v7，形如 `0190f2c4-7a1b-7c3d-8e9f-0123456789ab`）：varchar(36)、连字符分段、
 * **不含冒号**、也不以 `file_` 之类业务前缀打头。
 *
 * 而 DSH Web 的 file id 是另一套体系（常见形态带 `dsh:` / `dsh-file:` 冒号命名空间前缀，
 * 或 `file_...` 之类的自有前缀）。两套 id 混用时，这个相对 `/v1` 的 presign 请求会打到
 * 当前宿主 origin（DSH Web 62005）而不是 mate 核心（10001），DSH 对该路径回 405，
 * 前端最终只能看到一句含糊的「接口不存在」。这里前置挡掉明显非 mate 体系的 id，
 * 给出可直接定位来源的文案。判据刻意宽松（只要「非空且不含冒号、不以 file_ 打头」即视为
 * mate id），避免过度约束后续可能的 id 形态演进。
 */
const DSH_LIKE_FILE_ID = /[:]|^file_/i

/** file_id 是否明显来自其它宿主（DSH），而非 mate 的 `core_file_upload.id`。 */
export function isDshFileId(fileId: string): boolean {
  return DSH_LIKE_FILE_ID.test(fileId)
}

/**
 * 校验 file_id 属于 mate 体系（`core_file_upload.id`）。
 *
 * - 非空字符串 → 否则抛「文件 id 为空」；
 * - 明显是 DSH 原生 id（`dsh:` / `dsh-file:` / `file_…` 等）→ 抛跨宿主文案。
 *
 * 校验失败抛 `Error`（会被调用方/上层 `toErrorMessage` 原样展示）。
 */
export function assertFileId(fileId: string): void {
  if (typeof fileId !== 'string' || fileId.trim() === '') {
    throw new Error('预览取字节失败：文件 id 为空，无法换签名 url')
  }
  if (isDshFileId(fileId)) {
    throw new Error(
      `该文件来自其它宿主（DSH），应走 DSH 自己的取字节通路，不能用 mate presign-url（file_id=${fileId}）`,
    )
  }
}

/**
 * 从异常里尽量还原「请求方法 + 完整 URL（含 origin）」。
 *
 * 相对 `/v1` 请求被宿主页面加载时会打到当前 origin，还原出 origin 才能一眼看出打错了宿主。
 * axios 的错误里 `config.baseURL + config.url` 是相对路径，浏览器运行时可拼 `location.origin`；
 * 非浏览器环境（单测 / SSR）退化用 `http://<host>` 之类兜底，避免直接抛二次异常。
 */
function resolveRequestTarget(err: unknown): { method: string; url: string } {
  const cfg = (err as { config?: { method?: string; url?: string; baseURL?: string } })?.config
  const method = (cfg?.method || 'post').toUpperCase()
  const baseURL = cfg?.baseURL || ''
  const path = cfg?.url || ''
  const rel = `${baseURL}${path}`
  if (/^https?:\/\//i.test(rel)) return { method, url: rel }
  const origin =
    typeof globalThis.location?.origin === 'string' && globalThis.location.origin !== 'null'
      ? globalThis.location.origin
      : ''
  const prefixed = rel.startsWith('/') ? rel : `/${rel}`
  return { method, url: origin ? `${origin}${prefixed}` : prefixed }
}

/** 从 axios 错误里取 HTTP 状态码（无则 -1）。 */
function statusOf(err: unknown): number {
  const status = (err as { response?: { status?: number } })?.response?.status
  return typeof status === 'number' ? status : -1
}

/**
 * 拿某 file_id 的签名 url（会过期，别长期缓存）。
 *
 * 前置校验 file_id 属 mate 体系；请求失败时把「完整请求 URL（含 origin）+ HTTP 状态」
 * 拼进错误信息——这样页面挂在别的宿主 origin 上时能立刻看出「405 打到的是谁」。
 */
export async function filePresignUrl(fileId: string): Promise<PresignResp> {
  assertFileId(fileId)
  try {
    // 注意 baseURL 是 '/v1'（见 request.ts），而控制面所有路由都挂在 `/v1/mate` 下
    // （ginupload.Register 挂的是 `/files` 到 v1 版本组，即 `/v1/mate/files`）。所以这里必须写
    // `/mate/files/...`——漏 `/mate` 会拼成 `/v1/files/...`，打到核心后命中误路由守卫 405，
    // 在浏览器带 DSH cookie 的跨层场景下还会被 CORS 吞成"网络错误"，表现为预览一直失败。
    return await http.post<PresignResp>(`/mate/files/${encodeURIComponent(fileId)}/presign-url`)
  } catch (err) {
    const { method, url } = resolveRequestTarget(err)
    const status = statusOf(err)
    // 区分两类失败，别把"网络抖动"也硬说成"打错后端"（那样会误导排查方向）：
    //  - 有 HTTP 状态码（如 405）：说明请求确实到了某个服务，只是路径不是本应用的 → 多半是打错了后端 origin。
    //  - 无状态码（网络错误 / 超时 / 后端重启连接被重置）：请求没拿到响应，与"打错 origin"无关。
    if (status >= 0) {
      throw new Error(
        `预览取字节失败（HTTP ${status}，${method} ${url}）：请求已到达某个服务，但该路径不是本应用接口——` +
          `预览页可能挂在其它宿主 origin 上，请改用核心 origin`,
      )
    }
    throw new Error(
      `预览取字节失败（网络错误，${method} ${url}）：请求未拿到响应——后端可能未启动、正在重启或网络中断，` +
        `请确认核心服务（默认 127.0.0.1:10001）可用后重试`,
    )
  }
}

/** 按签名 url 取整块字节（bytes-complete / 需要 blob 下载时用）。 */
export async function fetchFileBytes(fileId: string): Promise<Uint8Array> {
  const { url, headers } = await filePresignUrl(fileId)
  const res = await fetch(url, { headers }) // 同源回环，无 CORS
  if (!res.ok) throw new Error(`读取文件失败：HTTP ${res.status}`)
  return new Uint8Array(await res.arrayBuffer())
}

/** 按签名 url 取文本（text-pages 用）。 */
export async function fetchFileText(fileId: string): Promise<string> {
  const { url, headers } = await filePresignUrl(fileId)
  const res = await fetch(url, { headers })
  if (!res.ok) throw new Error(`读取文件失败：HTTP ${res.status}`)
  return res.text()
}

/** 取签名 url 字符串（renderer 自取 / 图片直接作 src 时用）。 */
export async function fetchFileUrl(fileId: string): Promise<string> {
  return (await filePresignUrl(fileId)).url
}
