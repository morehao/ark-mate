#!/usr/bin/env node
/**
 * 极简静态文件服务（仅测试用，给 Playwright e2e 提供 dist 产物）。
 *
 * 为什么不引 `serve`/http-server：本仓前端零测试依赖纪律还热着，能用一个 Node 内置模块
 * （node:http + node:fs）解决的事不再加包。SPA fallback：非静态资源、非 /assets 的路径回落 index.html。
 *
 * 用法：node scripts/static-server.mjs [port] [dir]
 *   port 默认 4173，dir 默认 apps/web/dist。
 */
import { createServer } from 'node:http'
import { readFile, stat } from 'node:fs/promises'
import { extname, join, normalize, resolve } from 'node:path'

const PORT = Number(process.argv[2] || 4173)
const ROOT = resolve(process.argv[3] || 'apps/web/dist')

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.webmanifest': 'application/manifest+json',
  '.txt': 'text/plain; charset=utf-8',
  '.woff2': 'font/woff2',
}

async function serve(res, filePath) {
  const body = await readFile(filePath)
  res.writeHead(200, { 'content-type': MIME[extname(filePath)] || 'application/octet-stream' })
  res.end(body)
}

const server = createServer(async (req, res) => {
  try {
    const url = new URL(req.url, `http://127.0.0.1:${PORT}`)
    // 防目录穿越：normalize 后必须以 ROOT 开头。
    const safe = normalize(decodeURIComponent(url.pathname)).replace(/^(\.\.[/\\])+/, '')
    let filePath = join(ROOT, safe)
    if (!filePath.startsWith(ROOT)) {
      res.writeHead(403).end('forbidden')
      return
    }
    try {
      const s = await stat(filePath)
      if (s.isDirectory()) filePath = join(filePath, 'index.html')
      await serve(res, filePath)
    } catch {
      // SPA fallback
      await serve(res, join(ROOT, 'index.html'))
    }
  } catch (e) {
    res.writeHead(500).end(String(e))
  }
})

server.listen(PORT, '127.0.0.1', () => {
  console.log(`static-server: http://127.0.0.1:${PORT} → ${ROOT}`)
})
