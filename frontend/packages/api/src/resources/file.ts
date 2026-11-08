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

/** 拿某 file_id 的签名 url（会过期，别长期缓存）。 */
export const filePresignUrl = (fileId: string): Promise<PresignResp> =>
  http.post<PresignResp>(`/files/${encodeURIComponent(fileId)}/presign-url`)

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
