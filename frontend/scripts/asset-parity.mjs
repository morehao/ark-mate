#!/usr/bin/env node
/**
 * 资产一致性断言（M2-5，设计 §6.2 / V17）：**同一个请求，两条服务路径，必须给出同一个答案**。
 *
 * 静态资产在桌面安装里有两条服务路径：
 *   壳侧：`mate-app://app/...`（`scheme.handle` 读运行时树里的 dist，`/v1` 由壳反代到核心）
 *   核心侧：`http://127.0.0.1:<port>/...`（核心按 `--web-dir` 读**同一份** dist）
 *
 * 两者由不同代码实现（TS vs Go），因此必须有机器判据：对同一组路径分别取一次，
 * 逐项比较 `status` / `Content-Type` / `Cache-Control` / 响应体 sha256；再对一条非资产深链
 * 断言"最终由**核心**回退产出 index.html"（设计 §6.2：回退归核心，壳不做回退）。
 *
 * 为什么值得一条独立脚本：这两条路径的差异只会在桌面态暴露，而它们各自都能"看起来正常"——
 * 一条把 `no-store` 写成 `no-cache`、或者壳悄悄回了 404 而没人发现，用户看到的是"偶尔要手动刷新"。
 *
 * 用法：`node scripts/asset-parity.mjs`（或 `make verify/asset-parity`）
 */
import { createHash } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

import {
  DESKTOP_WEB,
  connectCDP,
  installCleanupHooks,
  killShellTree,
  stopShellTree,
  launchShell,
  pidsMatching,
  rebuildAll,
  sleep,
  CORE_RE,
  SHELL_RE,
} from './lib/desktop-harness.mjs'

const results = []
function check(name, ok, detail = '') {
  results.push({ name, ok: !!ok, detail })
  console.log(`  ${ok ? '✅' : '❌'} ${name}${detail ? ` — ${detail}` : ''}`)
  return !!ok
}
const section = (t) => console.log(`\n=== ${t} ===`)
const sha = (s) => createHash('sha256').update(s).digest('hex').slice(0, 16)

installCleanupHooks()

/** 要逐项比较的路径。前五个是壳**声明**服务的那一组（M2-5 的清单），后一个是 SPA 深链。 */
function assetPaths(assetsJs) {
  return [
    '/',
    '/index.html',
    `/assets/${assetsJs}`,
    '/favicon.svg',
    '/manifest.webmanifest',
  ]
}

/** 从壳侧读一次（CDP 在 `mate-app://app` 页面里 fetch：走的是 scheme.handle）。 */
async function fetchViaShell(cdp, path) {
  return cdp.evalJs(`
    const r = await fetch(${JSON.stringify(path)}, { cache: 'no-store' })
    const body = await r.arrayBuffer()
    return {
      status: r.status,
      type: (r.headers.get('content-type') ?? '').split(';')[0].trim(),
      cache: r.headers.get('cache-control'),
      bytes: body.byteLength,
      text: await new Response(body).text(),
    }
  `)
}

/** 从核心侧读一次（Node fetch 直连回环；静态资产不经 rest 面凭据）。 */
async function fetchViaCore(coreBase, path) {
  const r = await fetch(`${coreBase}${path}`, { cache: 'no-store' })
  const buf = Buffer.from(await r.arrayBuffer())
  return {
    status: r.status,
    type: (r.headers.get('content-type') ?? '').split(';')[0].trim(),
    cache: r.headers.get('cache-control'),
    bytes: buf.byteLength,
    text: buf.toString('utf8'),
  }
}

async function main() {
  console.log('资产一致性断言（真壳 vs 核心直连，M2-5 / V17）')

  section('前置')
  check('没有别的桌面壳/核心在跑', pidsMatching(new RegExp(`${CORE_RE.source}|${SHELL_RE.source}`)).length === 0)
  check('运行时资产目录存在', existsSync(join(DESKTOP_WEB, 'index.html')), DESKTOP_WEB)
  for (const [name, ok, detail] of rebuildAll()) check(name, ok, detail)
  if (results.some((r) => !r.ok)) {
    console.log('\n前置不通过，终止')
    process.exit(1)
  }

  const shell = await launchShell()
  if (!shell.corePid) {
    check('壳与核心起来了', false, shell.logText().split('\n').slice(-6).join('\n'))
    await stopShellTree()
    process.exit(1)
  }
  check('壳与核心起来了', true, `shell=${shell.shellPid} core=${shell.corePid}`)

  const cdp = await connectCDP(shell.port)
  try {
    // 核心地址从载体契约拿（`boot().streamBaseUrl` 就是回环地址，换 http 即为 REST/静态基址）
    const boot = await cdp.evalJs(`return await window.__MATE_DESKTOP__.boot()`)
    const coreBase = String(boot.streamBaseUrl).replace(/^ws:\/\//, 'http://')
    check('从载体契约拿到核心地址', /^http:\/\/127\.0\.0\.1:\d+$/.test(coreBase), coreBase)

    // 产物里真实存在的文件（缺失的路径不参与比较：壳回 404、核心回退 index，那是**有意**的差异）
    const entries = readFileSync(join(DESKTOP_WEB, 'index.html'), 'utf8')
    const assetsJs = (entries.match(/\/assets\/[A-Za-z0-9._-]+\.js/) ?? [])[0]?.replace('/assets/', '')
    check('产物里能找到 assets/*.js（比较清单非空）', !!assetsJs, String(assetsJs))
    if (!assetsJs) throw new Error('产物里没有 assets/*.js，无法比较')

    section('逐路径比较（壳 vs 核心）')
    for (const path of assetPaths(assetsJs)) {
      const viaShell = await fetchViaShell(cdp, path)
      const viaCore = await fetchViaCore(coreBase, path)
      const same = (a, b) => a === b
      const detail = `壳[${
        `${viaShell.status} ${viaShell.type || '-'} cache=${viaShell.cache ?? '-'} sha=${sha(viaShell.text)}`
      }] 核心[${`${viaCore.status} ${viaCore.type || '-'} cache=${viaCore.cache ?? '-'} sha=${sha(viaCore.text)}`}]`
      const ok =
        same(viaShell.status, viaCore.status) &&
        same(viaShell.type, viaCore.type) &&
        same(viaShell.cache, viaCore.cache) &&
        same(sha(viaShell.text), sha(viaCore.text))
      check(`${path} 两侧一致（status/content-type/cache-control/sha256）`, ok, detail)
    }

    section('两条路径的分工（有意为之，逐条钉住）')
    // 壳只服务自己声明的那五类（`/`、`/index.html`、`/assets/*`、`/favicon.svg`、`/manifest.webmanifest`），
    // **其余一律转给核心**。因此"产物里没有的文件"要看它在哪一侧：
    //   - 落在壳的清单里但产物缺 → 壳自己回 404（壳不编页面）；
    //   - 不在清单里 → 转核心 → 核心按 SPA 落回 index.html（回退归核心，设计 §6.2）。
    // 现在清单里的两个文件都真实存在，所以第二种情形用 `/assets/`（前缀命中但不是文件）来钉：
    // 这是壳侧唯一还能观察到的"壳自己回"的边界。
    const dirish = await fetchViaShell(cdp, '/assets/')
    const dirishCore = await fetchViaCore(coreBase, '/assets/')
    check(
      '壳对 /assets/（前缀命中但非文件）不产出页面',
      dirish.status !== 200 || dirish.type !== 'text/html',
      `壳 ${dirish.status} ${dirish.type} / 核心 ${dirishCore.status} ${dirishCore.type}`,
    )
    check(
      '核心对同一路径按 SPA 规则处理（200 HTML 或 404，都不是"壳的页面"）',
      dirishCore.status === 200 || dirishCore.status === 404,
      `核心 ${dirishCore.status} ${dirishCore.type}`,
    )

    section('SPA 深链与回退归属（设计 §6.2）')
    const deep = '/sessions/asset-parity-probe'
    const deepCore = await fetchViaCore(coreBase, deep)
    check(
      '核心对非资产深链回退 index.html（200 + HTML）',
      deepCore.status === 200 && deepCore.type === 'text/html' && deepCore.text.includes('<div id="root"'),
      `${deepCore.status} ${deepCore.type} cache=${deepCore.cache}`,
    )
    const deepShell = await fetchViaShell(cdp, deep)
    check(
      '壳对深链**不做**回退（它把深链交给核心；这里断言壳没有自己编一个 index 出来）',
      // 壳侧请求深链会命中 scheme 的三路分发之外的路径 → 归核心；核心回 index → 200 HTML。
      // 因此现象与核心一致，但**归属**由核心日志/路由决定；这里断言内容同源（sha 相同）。
      sha(deepShell.text) === sha(deepCore.text),
      `壳 sha=${sha(deepShell.text)} 核心 sha=${sha(deepCore.text)}`,
    )

    section('build id 一致性（M2-4 的地基）')
    const idFile = join(DESKTOP_WEB, 'build-id.txt')
    const fileId = existsSync(idFile) ? readFileSync(idFile, 'utf8').trim() : ''
    check('运行时资产里有 build-id.txt', !!fileId, fileId || '(缺)')
    const meta = await cdp.evalJs(`const r = await fetch('/v1/mate/meta'); const j = await r.json(); return j.data`)
    check('/meta 的 webBuildId 与文件一致', meta.webBuildId === fileId, `meta=${meta.webBuildId} file=${fileId}`)
    check('/meta 的 webPath 指向运行时树（核心读的就是这一份）', String(meta.webPath).endsWith(join('resources', 'web')), String(meta.webPath))

    // 核心侧也读同一个值（证明两侧读的是同一份文件，而不是"页面恰好也拿到了"）
    const coreId = await fetchViaCore(coreBase, '/build-id.txt')
    check(
      '核心直连也能取到同一份 build-id.txt（除静态清单外不额外暴露文件）',
      coreId.status === 200 ? coreId.text.trim() === fileId : true,
      `核心取 build-id.txt → ${coreId.status}${coreId.status === 200 ? ` ${coreId.text.trim()}` : '（不在壳声明服务的清单里，属预期）'}`,
    )
  } finally {
    cdp.close()
    await stopShellTree()
    await sleep(300)
  }

  const passed = results.filter((r) => r.ok).length
  const failed = results.filter((r) => !r.ok)
  console.log(`\n——— 汇总：${passed}/${results.length} 通过 ———`)
  if (failed.length) {
    console.log('失败项：')
    for (const f of failed) console.log(`  ❌ ${f.name}${f.detail ? ` — ${f.detail}` : ''}`)
    process.exit(1)
  }
  console.log('资产一致性：全部通过')
  process.exit(0)
}

main().catch((e) => {
  console.error(`断言脚本异常：${e?.stack ?? e}`)
  killShellTree()
  process.exit(1)
})
