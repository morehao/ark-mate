import axios from 'axios'
import type { AxiosError, AxiosRequestConfig } from 'axios'
import type { Envelope } from '@ark-mate/types'
import { BizCode } from '@ark-mate/types'

/**
 * mate 控制面 HTTP 客户端。
 *
 * - baseURL '/v1'，dev 走 vite proxy（127.0.0.1:10001），release 内嵌同源；
 * - 后端统一信封 {code, requestID, msg, data}：code=0 时返回 data，
 *   否则抛出带 code 的 HttpError（业务错误仍为 HTTP 200，网络层错误 code=-1）。
 */

export class HttpError extends Error {
  code: number
  constructor(code: number, msg: string) {
    super(msg || `请求失败（code ${code}）`)
    this.name = 'HttpError'
    this.code = code
  }
}

const instance = axios.create({ baseURL: '/v1', timeout: 30_000 })

/**
 * 控制面认证（决策 v38）：access token 提供者。
 *
 * 本包**不依赖** ark-iam 的 SDK（保持与 @morehao/ark-iam-react 解耦、web/desktop 两形态同一份
 * api 包）；由登录门禁（`console/src/auth`）在启动时 `setAccessTokenProvider(() => token)` 注入。
 * 未注入（桌面本地态降级 / 未启用认证）→ 不附带 Authorization，行为与旧版一致。
 */
let accessTokenProvider: (() => string | null | undefined) | null = null

/** 设置 access token 提供者（登录门禁调用）。传 null 清除。 */
export function setAccessTokenProvider(provider: (() => string | null | undefined) | null): void {
  accessTokenProvider = provider
}

/**
 * 读取当前 access token（供 WS 等**无法走 axios 拦截器**的通道自取）。
 *
 * 浏览器原生 WebSocket 不能自定义 Authorization 头，令牌只能放进 WS URL 的查询参数（决策 v38、
 * 设计 §C2：SLO 按 sid 撤连接需要 WS 握手时能验出 sid）。与 axios 拦截器同一份 provider——每请求
 * 现取、不缓存，静默续期后的新值对 WS 重连同样生效。未注入（未启用认证）→ 返回 null。
 */
export function getAccessToken(): string | null {
  return accessTokenProvider?.() ?? null
}

// 每个请求按当前 token 附带 Bearer（token 由门禁维护，可能是静默续期后的新值——每请求现取，
// 不缓存）。enabled=false 时 provider 为 null，直接跳过。
instance.interceptors.request.use((config) => {
  const token = accessTokenProvider?.()
  if (token) {
    config.headers = config.headers ?? {}
    ;(config.headers as Record<string, string>).Authorization = `Bearer ${token}`
  }
  return config
})

function unwrap<T>(env: Envelope<T> | undefined): T {
  if (!env || env.code !== BizCode.Success) {
    throw new HttpError(env?.code ?? -1, env?.msg || '服务响应异常')
  }
  return env.data
}

function normalize(err: unknown): unknown {
  if (err instanceof HttpError) return err
  if (axios.isAxiosError(err)) {
    const e = err as AxiosError<Envelope<unknown>>
    const env = e.response?.data
    if (env && typeof env.code === 'number') return new HttpError(env.code, env.msg)
    const msg = e.code === 'ECONNABORTED' ? '请求超时' : e.message ? `网络错误：${e.message}` : '网络错误'
    return new HttpError(-1, msg)
  }
  if (err instanceof Error) return new HttpError(-1, err.message)
  return new HttpError(-1, String(err))
}

async function raw<T>(p: Promise<{ data: Envelope<T> }>): Promise<T> {
  try {
    return unwrap((await p).data)
  } catch (err) {
    throw normalize(err)
  }
}

export const http = {
  get: <T>(url: string, config?: AxiosRequestConfig): Promise<T> =>
    raw<T>(instance.get<Envelope<T>>(url, config)),
  post: <T>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<T> =>
    raw<T>(instance.post<Envelope<T>>(url, data, config)),
  put: <T>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<T> =>
    raw<T>(instance.put<Envelope<T>>(url, data, config)),
  patch: <T>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<T> =>
    raw<T>(instance.patch<Envelope<T>>(url, data, config)),
  delete: <T>(url: string, config?: AxiosRequestConfig): Promise<T> =>
    raw<T>(instance.delete<Envelope<T>>(url, config)),
  // postForm 上传 multipart（FormData）：由 axios 自动带 boundary，不手设 Content-Type。
  postForm: <T>(url: string, form: FormData, config?: AxiosRequestConfig): Promise<T> =>
    raw<T>(instance.post<Envelope<T>>(url, form, config)),
}

/** 把任意异常归一为可展示文案。 */
export function toErrorMessage(err: unknown): string {
  if (err instanceof HttpError) return err.message
  if (err instanceof Error) return err.message
  return String(err)
}
