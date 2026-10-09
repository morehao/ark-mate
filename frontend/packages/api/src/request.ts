import axios from 'axios'
import type { AxiosError, AxiosRequestConfig } from 'axios'
import type { Envelope } from '@ark/types'
import { BizCode } from '@ark/types'

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
 * 单操作员令牌（A6）：后端配置 MATE_TOKEN 后，除 /health 外所有接口都要求 Bearer token。
 * 令牌只存在浏览器本地（localStorage），不写进 URL/日志；面板顶栏可设置/清除。
 */
const TOKEN_KEY = 'mate.token'

export function getToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? ''
  } catch {
    return ''
  }
}

export function setToken(token: string): void {
  try {
    const v = token.trim()
    if (v) localStorage.setItem(TOKEN_KEY, v)
    else localStorage.removeItem(TOKEN_KEY)
  } catch {
    // localStorage 不可用（隐私模式）时静默降级：本次会话内仍可通过请求头传递。
  }
}

instance.interceptors.request.use((cfg) => {
  const t = getToken()
  if (t) cfg.headers.set('Authorization', `Bearer ${t}`)
  return cfg
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
}

/** 把任意异常归一为可展示文案。 */
export function toErrorMessage(err: unknown): string {
  if (err instanceof HttpError) return err.message
  if (err instanceof Error) return err.message
  return String(err)
}
