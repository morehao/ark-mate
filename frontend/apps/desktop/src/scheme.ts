/**
 * `mate-app://` 自定义 scheme 的注册与**三路分发**（M1-3，设计 §3 / §6.2 / §9.2）。
 *
 * 为什么需要它：桌面窗口的 origin 必须与随机端口解耦（`mate-app://app` 恒定，
 * 磁盘缓存与页面存储跨启动稳定复用），因此页面发出的 `/v1/mate/*` 请求由本模块**反代**给核心。
 *
 * 三路（顺序即优先级）：
 *   1. `mate-app://shell/<page>` → 壳自己的静态页（welcome / boot-failed / 更新对话框）；
 *   2. `mate-app://app/<静态路径>` → 前端产物：只服务 `/`、`/index.html`、`/assets/*`、
 *      `/favicon.svg`、`/manifest.webmanifest` 这五类，**不含 SPA 回退**（回退归核心，设计 §6.2）；
 *   3. 其余（`mate-app://app/` 下的深链与全部 `/v1/*`）→ 反代核心。
 *
 * 已知机制限制（不是取舍）：`protocol.handle` **不承载 WebSocket upgrade**，因此事件流由
 * 渲染进程直连回环（设计 §7.2）——壳在 boot 时把 `streamBaseUrl` 交给页面（M1-8）。
 */
import { net, protocol } from 'electron'
import { statSync } from 'node:fs'
import { resolve, sep } from 'node:path'
import { pathToFileURL } from 'node:url'

import { MATE_SCHEME, CSP_POLICY, assetContentType } from '@ark-mate/desktop-contract'
import type { CoreHandle } from './core'

/** 壳服务的静态路径集合（核心侧静态规则的**子集**，M2-5 断言两者响应头逐字一致）。 */
const APP_STATIC_EXACT = new Set(['/', '/index.html', '/favicon.svg', '/manifest.webmanifest'])

/** 必须剥掉的逐跳头（反代两侧都剥：请求侧剥客户端带来的，响应侧剥核心回的）。 */
const HOP_BY_HOP = new Set([
  'connection',
  'upgrade',
  'keep-alive',
  'transfer-encoding',
  'te',
  'trailer',
  'host',
])

/** 注册 scheme 特权。**必须在 `app.ready` 之前调用**（Electron 的硬要求）。 */
export function registerSchemePrivileges(): void {
  protocol.registerSchemesAsPrivileged([
    {
      scheme: MATE_SCHEME,
      privileges: {
        standard: true, // 有 origin 语义（否则 location.origin 不是 mate-app://app）
        secure: true, // 与 https 同级的安全上下文（crypto.subtle / service worker 等）
        supportFetchAPI: true, // 页面里的 fetch 能打这个 scheme
        corsEnabled: true,
        stream: true,
        codeCache: true,
      },
    },
  ])
}

export type SchemeDeps = {
  /** 前端产物目录（未打包 = 工作区里的 apps/web/dist；打包 = resources/web）。 */
  webDir: string
  /** 壳静态页目录（本包的 renderer/）。 */
  rendererDir: string
  /** 取当前核心句柄（可能尚未就绪：此时反代返回 503，页面显示明确状态而不是白屏）。 */
  core: () => CoreHandle | null
}

/** 安装 scheme 处理器（`app.ready` 之后调用）。 */
export function installSchemeHandler(deps: SchemeDeps): void {
  protocol.handle(MATE_SCHEME, async (request) => {
    const url = new URL(request.url)
    try {
      if (url.hostname === 'shell') {
        const page = url.pathname === '/' || url.pathname === '' ? '/welcome.html' : url.pathname
        // 壳页不加契约 CSP：它们自带更严的 meta CSP（无脚本、样式仅同目录）。
        return await serveFile(deps.rendererDir, page)
      }
      if (url.hostname === 'app') {
        if (isAppStaticPath(url.pathname)) {
          const p = url.pathname === '/' ? '/index.html' : url.pathname
          // app 文档与脚本都下发契约里的 CSP（M1-5）：与核心服务浏览器入口时**同一个头**，
          // 由 archtest 守卫断言两处字符串逐字相同。
          return await serveFile(deps.webDir, p, { csp: true })
        }
        return await proxyToCore(request, deps.core())
      }
      return textResponse(404, `未知宿主：${url.hostname}（期望 app 或 shell）`)
    } catch (e) {
      return textResponse(500, `壳处理 ${request.url} 失败：${String(e)}`)
    }
  })
}

/** 壳服务的前端静态路径判定（设计 §6.2 的五类）。 */
export function isAppStaticPath(pathname: string): boolean {
  return APP_STATIC_EXACT.has(pathname) || pathname.startsWith('/assets/')
}

/**
 * 静态文件响应 + 缓存语义（设计 §6.2，与核心侧同一条规则）：
 * `index.html` → `no-store`；带内容 hash 的 `assets/*` → `immutable` 一年；其余 → 可重验证。
 *
 * `csp` 为真时额外下发契约里的 CSP（app 文档与脚本；壳自己的静态页有自己的 meta CSP，
 * 那份比契约更严，不需要也不该被覆盖）。
 */
async function serveFile(root: string, pathname: string, opts: { csp?: boolean } = {}): Promise<Response> {
  let rel: string
  try {
    rel = decodeURIComponent(pathname).replace(/^\/+/, '')
  } catch {
    return textResponse(400, '路径编码非法')
  }
  const full = resolve(root, rel)
  // 目录穿越防护：解出来的绝对路径必须仍在 root 之内。
  if (full !== root && !full.startsWith(root + sep)) {
    return textResponse(403, '路径越界')
  }
  // 只服务**文件**：`/assets/` 这种前缀命中但不是文件的请求，`net.fetch(file://dir)` 会炸成 500，
  // 而它语义上就是"壳没有这个文件"（回退归核心，不由壳编页面）。
  let isFile = false
  try {
    isFile = statSync(full).isFile()
  } catch {
    isFile = false
  }
  if (!isFile) {
    return textResponse(404, `壳没有这个文件：${pathname}`)
  }
  const res = await net.fetch(pathToFileURL(full).toString())
  const headers = new Headers(res.headers)
  headers.set('cache-control', cacheControlFor(pathname))
  // 显式 Content-Type：Chromium 的嗅探认不出 .webmanifest（会给 text/plain），
  // 而核心侧按同一张映射表给 application/manifest+json —— 两侧头必须一致（M2-5）。
  const ct = assetContentType(pathname)
  if (ct) headers.set('content-type', ct)
  if (opts.csp) headers.set('content-security-policy', CSP_POLICY)
  return new Response(res.body, { status: res.status, statusText: res.statusText, headers })
}

function cacheControlFor(pathname: string): string {
  if (pathname.startsWith('/assets/')) return 'public, immutable, max-age=31536000'
  if (pathname === '/' || pathname === '/index.html') return 'no-store'
  return 'no-cache'
}

/**
 * 反代核心：剥掉逐跳头，保留方法与请求体。
 */
async function proxyToCore(request: Request, core: CoreHandle | null): Promise<Response> {
  if (!core) {
    return textResponse(503, '核心尚未就绪')
  }
  const url = new URL(request.url)
  const target = `http://${core.addr}${url.pathname}${url.search}`

  const headers = new Headers(request.headers)
  stripHopByHop(headers)
  // 入口标识（M1-7）：核心据此回答"这次请求走的是桌面窗口还是浏览器"
  // （`/capabilities` 的 available 与 `entries.desktop` 都看它）。浏览器入口的请求不经过这里，自然没有它。
  headers.set('x-mate-carrier', 'desktop')

  const init: RequestInit & { duplex?: 'half' } = { method: request.method, headers }
  if (request.method !== 'GET' && request.method !== 'HEAD') {
    init.body = request.body
    init.duplex = 'half' // 流式请求体（Node 的 fetch 要求显式声明）
  }

  const res = await net.fetch(target, init)
  const outHeaders = new Headers(res.headers)
  stripHopByHop(outHeaders)
  return new Response(res.body, { status: res.status, statusText: res.statusText, headers: outHeaders })
}

function stripHopByHop(headers: Headers): void {
  const drop: string[] = []
  headers.forEach((_v, k) => {
    const name = k.toLowerCase()
    if (HOP_BY_HOP.has(name) || name.startsWith('proxy-')) drop.push(name)
  })
  for (const name of drop) headers.delete(name)
}

function textResponse(status: number, msg: string): Response {
  return new Response(`${msg}\n`, {
    status,
    headers: { 'content-type': 'text/plain; charset=utf-8', 'cache-control': 'no-store' },
  })
}

/** 壳静态页的绝对地址（供加载 boot-failed 之类的恢复页）。 */
export function shellPageUrl(page: string, shellOrigin: string): string {
  return `${shellOrigin}/${page.replace(/^\/+/, '')}`
}
