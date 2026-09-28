#!/usr/bin/env node
/**
 * 桌面入口界面与契约验收（M1-9 / M1-10；M5-2 会在这里补双端场景矩阵）——**真 Electron + 真核心**。
 *
 * 为什么单独一个脚本：`web-ui-acceptance.mjs` 打的是浏览器入口（真 Chrome + vite），
 * 而桌面入口有自己的一套东西——`forge-app://` scheme、壳反代、preload 注入面、CSP/导航/权限加固、
 * 双凭据分层、父进程消失自保。这些只能在**真壳**里验。
 *
 * 零新依赖：CDP 用 Node 24 自带的全局 WebSocket 直连；壳用 node:child_process 起。
 *
 * 用法：
 *   node scripts/desktop-ui-acceptance.mjs                # 全部场景（exit 场景会杀掉壳，放最后）
 *   node scripts/desktop-ui-acceptance.mjs shell contract # 只跑指定场景
 *   KEEP_SHELL=1 ...                                      # 跑完不杀壳，便于人工接管
 *
 * 场景：
 *   guard        前置：不能有别的桌面壳/核心在跑（两个核心共用 ~/.forge/forge.db 会让断言读到别人的内存态）
 *   shell        M1-3：origin、壳反代（页面不带凭据）、静态资产缓存头、深链与 /v1 归核心、壳静态页
 *   contract     M1-4：`__FORGE_DESKTOP__` 形状与契约一致；非载体 origin 为 undefined
 *   hardening    M1-5：CSP 与契约逐字一致、streamToken 不落存储面、权限全拒、导航被拦
 *   credentials  M1-9（V15）：渲染进程搜不到 restToken；streamToken 调 REST 401；attach 正/反例
 *   stream       M1-8：事件流直连回环 + 首帧 attach + 真实订阅收到服务端帧
 *   exit         M1-10（V13）：kill -9 壳后核心 3s 内自退、无残留、令牌不进 argv/env
 *
 * 前置：`make core/desktop`（脚本会自己跑）与 `pnpm -C frontend build`（apps/web/dist 必须已存在）。
 * **不进 CI**：要真浏览器内核 + 真底座。
 */
import { execFileSync, spawn } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

// 路径按脚本位置定位（`pnpm desk:accept` 的 cwd 是 frontend/，用相对路径会把临时文件写错地方）
const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url)) // frontend/scripts
const FRONTEND = join(SCRIPT_DIR, '..') // frontend
const REPO = join(FRONTEND, '..') // 仓库根
const DESKTOP = join(FRONTEND, 'apps', 'desktop') // frontend/apps/desktop

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
// 进程匹配式：Electron 会派生 Helper 子进程，它们的命令行同样含 `electron@<ver>...MacOS/`，
// 因此壳只认**主进程**形态（`Electron .`，末尾那个点是要启动的应用目录）。
const SHELL_RE = /electron@44\.4\.5.*MacOS\/Electron \./
const CORE_RE = /resources\/forge-core serve/

const sh = (cmd, args, opts = {}) => {
  try {
    return execFileSync(cmd, args, { encoding: 'utf8', ...opts })
  } catch {
    return ''
  }
}

// ——— 断言台账（与 web-ui-acceptance.mjs 同风格）———
const results = []
function check(name, ok, detail = '') {
  results.push({ name, ok: !!ok, detail })
  console.log(`  ${ok ? '✅' : '❌'} ${name}${detail ? ` — ${detail}` : ''}`)
  return !!ok
}
const section = (t) => console.log(`\n=== ${t} ===`)

// ——— 进程探针 ———
const psAll = () => sh('ps', ['-Ao', 'pid,ppid,command'])
const pidsMatching = (re) =>
  psAll()
    .split('\n')
    .filter((l) => re.test(l) && !l.includes('grep'))
    .map((l) => Number(l.trim().split(/\s+/)[0]))

// ——— 收尾：脚本无论怎么退出（含异常）都不能留下壳与核心 ———
//
// 首版踩过：CDP 回调里抛异常把进程直接打挂，`finally` 没跑到，于是下一次跑的前置守卫
// 报"还有壳/核心在跑"。这里把清理挂到进程级钩子上，兜住所有退出路径。
function killShellTree() {
  for (const re of [SHELL_RE, CORE_RE]) {
    for (const pid of pidsMatching(re)) {
      try {
        process.kill(pid, 'SIGKILL')
      } catch {}
    }
  }
}
process.on('uncaughtException', (e) => {
  console.error(`未捕获异常：${e?.stack ?? e}`)
  killShellTree()
  process.exit(1)
})
process.on('unhandledRejection', (e) => {
  console.error(`未处理的 rejection：${e?.stack ?? e}`)
  killShellTree()
  process.exit(1)
})
process.on('SIGINT', () => {
  killShellTree()
  process.exit(130)
})

// ——— 最小 CDP 客户端 ———
async function connectCDP(port) {
  const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()
  const page = list.find((t) => t.type === 'page' && t.url.startsWith('forge-app://'))
  if (!page) throw new Error(`没有 forge-app:// 的 page target：${JSON.stringify(list.map((t) => t.url))}`)
  const ws = new WebSocket(page.webSocketDebuggerUrl)
  let id = 0
  const pending = new Map()
  const events = []
  ws.onmessage = (ev) => {
    const m = JSON.parse(ev.data)
    if (m.id != null && pending.has(m.id)) {
      const { res } = pending.get(m.id)
      pending.delete(m.id)
      res(m)
      return
    }
    events.push(m)
  }
  await new Promise((res, rej) => {
    ws.onopen = res
    ws.onerror = () => rej(new Error('CDP 连接失败'))
  })
  const send = (method, params = {}) =>
    new Promise((res, rej) => {
      const mid = ++id
      pending.set(mid, { res }) // 统一存对象：onmessage 里按 { res } 取（首版存的是函数本身，取值炸了）
      ws.send(JSON.stringify({ id: mid, method, params }))
      setTimeout(() => pending.has(mid) && (pending.delete(mid), rej(new Error(`CDP 超时: ${method}`))), 25000)
    })
  const evalJs = async (body) => {
    const m = await send('Runtime.evaluate', {
      expression: `(async () => { ${body} })()`,
      awaitPromise: true,
      returnByValue: true,
    })
    if (m.result?.exceptionDetails) {
      throw new Error(`页面内异常：${m.result.exceptionDetails.exception?.description ?? m.result.exceptionDetails.text}`)
    }
    return m.result.result.value
  }
  return { send, evalJs, events, close: () => ws.close() }
}

// ——— 起壳 ———
async function launchShell() {
  const port = 9400 + Math.floor(Math.random() * 100)
  const log = []
  const child = spawn(join(DESKTOP, 'node_modules/.bin/electron'), ['.', `--remote-debugging-port=${port}`], {
    cwd: DESKTOP,
    env: { ...process.env, ELECTRON_RUN_AS_NODE: undefined },
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
  })
  child.stdout.on('data', (d) => log.push(String(d)))
  child.stderr.on('data', (d) => log.push(String(d)))
  const logText = () => log.join('')
  for (let i = 0; i < 100; i++) {
    if (logText().includes('core ready')) break
    await sleep(250)
  }
  const shellPid = pidsMatching(SHELL_RE)[0]
  const corePid = pidsMatching(CORE_RE)[0]
  return { child, port, logText, shellPid, corePid }
}

// ————————————————————————————————— 场景 —————————————————————————————————

/** guard：起壳前确认没有别的核心在跑（两个核心共用 ~/.forge 会让断言读到别人的内存态）。 */
function scenarioGuard() {
  section('前置：环境干净（同一份 ~/.forge 只能有一个核心）')
  const others = pidsMatching(new RegExp(`${CORE_RE.source}|${SHELL_RE.source}`))
  check('没有别的桌面壳/核心在跑', others.length === 0, others.length ? `pid=${others.join(',')}（请先关掉；本机 make dev 只跑浏览器入口，不冲突）` : '')
  check('apps/web/dist 已构建（壳要服务它）', existsSync(join(FRONTEND, 'apps', 'web', 'dist', 'index.html')), '缺则先 `cd frontend && pnpm build`')
  // 核心二进制每次重建：壳传的 flag（--exit-with-parent 等）必须与当前代码一致，
  // 否则测的是旧二进制（M1-6 首跑就踩到过：旧核心不认新 flag，直接退出码 2）。
  try {
    execFileSync('make', ['core/desktop'], { cwd: REPO, stdio: 'ignore' })
    check('forge-core 已按当前代码重建', true)
  } catch (e) {
    check('forge-core 已按当前代码重建', false, String(e).slice(0, 120))
  }
}

/** shell（M1-3）：origin、反代、静态资产、深链归属、壳静态页。 */
async function scenarioShell(cdp) {
  section('M1-3 scheme 三路分发与壳反代')
  const origin = await cdp.evalJs('return location.origin')
  check('窗口 origin = forge-app://app', origin === 'forge-app://app', origin)

  const rendered = await cdp.evalJs(`return !!document.querySelector('#root') && document.body.innerHTML.length > 500`)
  check('前端产物由壳服务并渲染（#root 有内容）', rendered)

  const health = await cdp.evalJs(`const r = await fetch('/v1/forge/health'); return { s: r.status, b: (await r.text()).slice(0, 40) }`)
  check('fetch /v1/forge/health 经壳反代 200（页面不带凭据）', health.s === 200 && health.b.includes('ok'), JSON.stringify(health))

  const agents = await cdp.evalJs(`const r = await fetch('/v1/forge/agents'); const j = await r.json(); return { s: r.status, code: j.code, n: (j.data ?? []).length }`)
  check('fetch /v1/forge/agents 经壳反代（restToken 由主进程注入）', agents.s === 200 && agents.code === 0, JSON.stringify(agents))

  const asset = await cdp.evalJs(`
    const src = [...document.querySelectorAll('script[src],link[href]')].map(e => e.getAttribute('src') || e.getAttribute('href')).find(p => p && p.startsWith('/assets/'))
    if (!src) return { none: true }
    const r = await fetch(src)
    return { path: src, s: r.status, cache: r.headers.get('cache-control') }
  `)
  check('assets 由壳服务且 immutable', !asset.none && asset.s === 200 && String(asset.cache).includes('immutable'), JSON.stringify(asset))

  const index = await cdp.evalJs(`const r = await fetch('/index.html'); return { s: r.status, cache: r.headers.get('cache-control') }`)
  check('index.html no-store', index.s === 200 && index.cache === 'no-store', JSON.stringify(index))

  const nf = await cdp.evalJs(`const r = await fetch('/v1/forge/does-not-exist'); const j = await r.json(); return { s: r.status, code: j.code }`)
  check('/v1 未注册路径回核心信封 404', nf.s === 404, JSON.stringify(nf))

  await cdp.send('Page.navigate', { url: 'forge-app://shell/welcome.html' })
  await sleep(700)
  const shellPage = await cdp.evalJs(`return { origin: location.origin, h1: document.querySelector('h1')?.innerText ?? null, bg: getComputedStyle(document.body).backgroundColor }`)
  check('forge-app://shell/welcome.html 可打开且样式来自 tokens', shellPage.origin === 'forge-app://shell' && !!shellPage.h1 && shellPage.bg === 'rgb(247, 248, 250)', JSON.stringify(shellPage))
  await cdp.send('Page.navigate', { url: 'forge-app://app/' })
  await sleep(1500)
}

/** contract（M1-4）：注入面形状 + 非载体 origin 负例。 */
async function scenarioContract(cdp) {
  section('M1-4 载体契约（__FORGE_DESKTOP__）')
  const shape = await cdp.evalJs(`
    const d = window.__FORGE_DESKTOP__
    return { type: typeof d, top: d && Object.keys(d).sort(), native: d && Object.keys(d.native).sort(), platform: d && Object.keys(d.platform).sort() }
  `)
  check('typeof __FORGE_DESKTOP__ = object', shape.type === 'object', shape.type)
  check('顶层键 = boot/native/platform（无 capabilities）', JSON.stringify(shape.top) === JSON.stringify(['boot', 'native', 'platform']), JSON.stringify(shape.top))
  check('native 键与契约一致', JSON.stringify(shape.native) === JSON.stringify(['openExternal', 'openPath', 'pathFor', 'pickDirectory']), JSON.stringify(shape.native))
  check('platform 键与契约一致', JSON.stringify(shape.platform) === JSON.stringify(['close', 'setBounds']), JSON.stringify(shape.platform))

  const helper = await cdp.evalJs(`
    const f = new File(['x'], 'pasted.txt')
    return { path: window.__FORGE_DESKTOP__.native.pathFor(f), sniff: 'electron' in window }
  `)
  check('pathFor(粘贴字节) 返回空串（无真实路径）', helper.path === '')
  check('页面没有 window.electron 嗅探面', helper.sniff === false)

  await cdp.send('Page.navigate', { url: 'forge-app://shell/welcome.html' })
  await sleep(700)
  const neg = await cdp.evalJs('return typeof window.__FORGE_DESKTOP__')
  check('负例：forge-app://shell/* 拿到 undefined（一次都不暴露）', neg === 'undefined', neg)
  await cdp.send('Page.navigate', { url: 'forge-app://app/' })
  await sleep(1500)
}

/** hardening（M1-5）：CSP、存储面、权限、导航。 */
async function scenarioHardening(cdp) {
  section('M1-5 渲染进程加固基线')
  const csp = await cdp.evalJs(`
    const r = await fetch('/index.html')
    const raw = r.headers.get('content-security-policy')
    return raw
  `)
  // 直接从契约源码取 CSP 常量：跨语言比对要用**同一份真源**，不在这里抄一遍字符串。
  const { CSP_POLICY } = await import(new URL('../packages/desktop-contract/src/index.ts', import.meta.url).href)
  check('文档响应头 CSP 与契约逐字一致', csp === CSP_POLICY, csp === CSP_POLICY ? '' : `头=${JSON.stringify(csp)}`)

  const perm = await cdp.evalJs(`return { n: await Notification.requestPermission() }`)
  check('权限请求默认被拒（Notification → denied）', perm.n === 'denied', perm.n)

  const before = await cdp.evalJs('return location.href')
  await cdp.evalJs(`try { location.href = 'file:///etc/hosts' } catch (e) {} return true`)
  await sleep(500)
  const after = await cdp.evalJs('return location.href')
  check('file:// 导航被拦（窗口未跳转）', after === before, `${before} → ${after}`)

  const opened = await cdp.evalJs(`return { opened: window.open('about:blank') !== null }`)
  check('window.open 被 deny（不开新窗口）', opened.opened === false, JSON.stringify(opened))
}

/** credentials（M1-9，V15）：双凭据分层的四项。 */
async function scenarioCredentials(cdp) {
  section('M1-9 凭据分层（V15）')
  const boot = await cdp.evalJs(`
    const b = await window.__FORGE_DESKTOP__.boot()
    return { base: b.streamBaseUrl, stream: b.streamToken }
  `)
  check('boot() 下发回环基址与 streamToken', /^ws:\/\/127\.0\.0\.1:\d+$/.test(boot.base) && boot.stream.length === 43, boot.base)

  // ① 渲染进程里搜不到 restToken（它的值只有主进程有；这里搜的是"任何 43 字符令牌形状"）
  const leak = await cdp.evalJs(`
    const tok = ${JSON.stringify(boot.stream)}
    const hits = []
    const scan = (store, name) => { for (let i = 0; i < store.length; i++) { const k = store.key(i); if (String(store.getItem(k) ?? '').includes(tok)) hits.push(name + ':' + k) } }
    try { scan(localStorage, 'localStorage') } catch {}
    try { scan(sessionStorage, 'sessionStorage') } catch {}
    if (document.cookie.includes(tok)) hits.push('cookie')
    if (document.documentElement.outerHTML.includes(tok)) hits.push('html')
    return { hits, htmlHasRest: /restToken/.test(document.documentElement.outerHTML) }
  `)
  check('streamToken 不在 localStorage/sessionStorage/cookie/HTML', leak.hits.length === 0, JSON.stringify(leak.hits))
  check('页面 HTML 里没有 restToken 字样', leak.htmlHasRest === false)

  // ② streamToken 调 REST → 401。**在 Node 侧发**：页面里根本打不到核心的 REST 面
  //    （CSP 的 connect-src 只放开 `'self'` 与 `ws://127.0.0.1:*`），这本身就是要断言的性质。
  const origin = boot.base.replace('ws://', 'http://')
  const rest = await fetch(`${origin}/v1/forge/agents`, { headers: { authorization: `Bearer ${boot.stream}` } })
  const restBody = await rest.json().catch(() => ({}))
  check('streamToken 调 REST → 401（V15 互斥）', rest.status === 401, `${rest.status} code=${restBody.code}`)

  // ②b 反向断言：页面**不能**直连核心 REST（CSP connect-src 不给 http 回环；REST 只能走壳反代）
  const direct = await cdp.evalJs(`
    try {
      const r = await fetch(${JSON.stringify(origin)} + '/v1/forge/agents')
      return { blocked: false, status: r.status }
    } catch (e) { return { blocked: true, err: String(e).slice(0, 60) } }
  `)
  check('页面直连核心 REST 被 CSP 拦下（REST 只能走同源反代）', direct.blocked === true, JSON.stringify(direct))

  // ③/④ attach 正反例：streamToken 通过、restToken 被拒（1008）——restToken 的值页面拿不到，
  //      因此这里只做"streamToken 通过"；restToken 的反例由 Go 侧单测与手工验收覆盖。
  const attach = await cdp.evalJs(`
    const url = ${JSON.stringify(boot.base)} + '/v1/forge/ws/events'
    return await new Promise((resolve) => {
      const ws = new WebSocket(url)
      const out = { opened: false, closed: null, frames: [] }
      ws.onopen = () => { out.opened = true; ws.send(JSON.stringify({ action: 'attach', token: ${JSON.stringify(boot.stream)} })); setTimeout(() => ws.send(JSON.stringify({ action: 'attach', agg_id: 'accept-probe' })), 100) }
      ws.onmessage = (ev) => { out.frames.push(JSON.parse(String(ev.data)).type); if (out.frames.includes('snapshot')) { ws.close(); resolve(out) } }
      ws.onclose = (ev) => { out.closed = ev.code; resolve(out) }
      setTimeout(() => resolve({ ...out, timeout: true }), 5000)
    })
  `)
  check('streamToken 走首帧 attach → 正常订阅（收到 snapshot）', attach.frames?.includes('snapshot'), JSON.stringify(attach))

  const bad = await cdp.evalJs(`
    const url = ${JSON.stringify(boot.base)} + '/v1/forge/ws/events'
    return await new Promise((resolve) => {
      const ws = new WebSocket(url)
      ws.onopen = () => ws.send(JSON.stringify({ action: 'attach', token: 'definitely-wrong-token' }))
      ws.onclose = (ev) => resolve({ code: ev.code })
      setTimeout(() => resolve({ timeout: true }), 5000)
    })
  `)
  check('错 token 走 attach → 服务端 1008 关闭', bad.code === 1008, JSON.stringify(bad))
}

/** stream（M1-8）：事件流真的通。 */
async function scenarioStream(cdp) {
  section('M1-8 事件流（直连回环 + 首帧 attach）')
  const boot = await cdp.evalJs(`const b = await window.__FORGE_DESKTOP__.boot(); return b`)
  check('boot().streamBaseUrl 是回环地址', /^ws:\/\/127\.0\.0\.1:\d+$/.test(boot.streamBaseUrl), boot.streamBaseUrl)

  cdp.events.length = 0
  await cdp.send('Network.enable')
  await cdp.send('Page.navigate', { url: 'forge-app://app/' })
  await sleep(6000)
  const created = cdp.events.filter((e) => e.method === 'Network.webSocketCreated').map((e) => e.params.url)
  check('WS 建连直连回环（非 forge-app://）', created.some((u) => u.startsWith('ws://127.0.0.1:')), JSON.stringify(created))
  const handshake = cdp.events.find((e) => e.method === 'Network.webSocketHandshakeResponseReceived')
  check('WS 握手 101', handshake?.params.response?.status === 101, String(handshake?.params.response?.status))
  const sent = cdp.events.filter((e) => e.method === 'Network.webSocketFrameSent').map((e) => e.params.response?.payloadData ?? '')
  const attachFrame = sent.find((f) => f.includes('"action":"attach"'))
  check('首帧 attach 带 43 字符 streamToken', !!attachFrame && String(JSON.parse(attachFrame).token).length === 43, attachFrame?.slice(0, 60))

  const conv = await cdp.evalJs(`const r = await fetch('/v1/forge/conversations'); const j = await r.json(); return j.data?.[0]?.id ?? null`)
  if (!conv) {
    check('订阅真实会话并收到服务端帧', false, '本地库没有会话可订阅')
    return
  }
  cdp.events.length = 0
  await cdp.evalJs(`history.pushState({}, '', '/conversations/${conv}'); window.dispatchEvent(new PopStateEvent('popstate')); return true`)
  await sleep(2500)
  const recv = cdp.events.filter((e) => e.method === 'Network.webSocketFrameReceived').map((e) => e.params.response?.payloadData ?? '')
  check('订阅后收到服务端帧（event/snapshot）', recv.some((f) => f.includes('"type":"event"') || f.includes('"type":"snapshot"')), recv[0]?.slice(0, 70) ?? '(无)')

  const uiState = await cdp.evalJs(`return document.body.innerText.includes('WS 已连接')`)
  check('界面显示 WS 已连接', uiState === true)
}

/** exit（M1-10，V13）：退出顺序、孤儿、令牌不进 env。 */
async function scenarioExit(shell) {
  section('M1-10 退出与孤儿（V13）')
  const { shellPid, corePid, logText } = shell
  if (!shellPid || !corePid) {
    check('壳与核心进程都在（前置）', false, `shellPid=${shellPid} corePid=${corePid}`)
    return
  }
  // ④ 令牌不进 argv / env（runtime 子进程按设计 §6.6 继承调用方环境 → 核心没有，它们也不可能有）
  const argv = sh('ps', ['-o', 'args=', '-p', String(corePid)])
  check('核心 argv 无令牌字样（只经 stdin）', !/restToken|streamToken|--token/.test(argv), argv.slice(0, 90))
  const env = sh('ps', ['-E', '-p', String(corePid)])
  if (env.trim()) {
    check('核心 env 无令牌键名', !/restToken|streamToken/.test(env))
  } else {
    console.log('  ⚠ 本机 ps -E 读不到环境（macOS 权限）：该条以"令牌只经 stdin"的代码路径为准')
  }

  // ② kill -9 壳 → 核心 3s 内自退（--exit-with-parent）
  process.kill(shellPid, 'SIGKILL')
  const t0 = Date.now()
  let gone = null
  while (Date.now() - t0 < 5000) {
    if (pidsMatching(CORE_RE).length === 0) {
      gone = Date.now() - t0
      break
    }
    await sleep(100)
  }
  check('kill -9 壳后核心自行退出（<3s）', gone !== null && gone < 3000, gone === null ? '5s 内仍存活' : `${gone}ms`)

  // ③ 退出后无残留
  await sleep(400)
  check('无 forge-core 残留', pidsMatching(CORE_RE).length === 0)
  check('无 electron 残留', pidsMatching(SHELL_RE).length === 0)
  const dangling = psAll()
    .split('\n')
    .filter((l) => /(opencode|dsh)\b.*(acp|--profile acp)/.test(l) && !/dsh web|grep/.test(l))
  check('无 opencode/dsh 的 acp 子进程残留', dangling.length === 0, dangling.slice(0, 2).join(' | '))

  // ① 有在途轮次退出 → 二次确认：需要真实在途轮次（会真跑模型），默认跳过。
  if (process.env.V13_LIVE === '1') {
    check('有在途轮次退出 → 二次确认（V13_LIVE=1）', false, '该场景需要真在途轮次：请手工建一条会话并提问，然后在轮次进行中按 ⌘Q 观察二次确认对话框')
  } else {
    console.log('  ⏭ 有在途轮次退出 → 二次确认：跳过（V13_LIVE=1 时提示手工步骤；该场景要真跑模型，不进 CI）')
  }

  // 核心日志留痕（父进程消失的原因行会写在壳的 stdout 里？不会——壳已死，管道无读者。
  // 因此这里只断言"核心确实退了"，原因行由 Go 侧单测 + direct 场景覆盖）
  check('壳日志里有核心就绪行（前置证据）', logText().includes('core ready'))
}

// ——— 直测 --exit-with-parent（父进程被强杀；核心 stderr 直接落文件才留得下原因行）———
async function scenarioDirectParentGone() {
  section('M1-6 直测：父进程被 SIGKILL 后核心自退（--exit-with-parent）')
  const core = join(DESKTOP, 'resources/forge-core')
  const outFile = '/tmp/forge-accept-core.out'
  const errFile = '/tmp/forge-accept-core.err'
  const parentCode = `
    const { spawn } = require('node:child_process')
    const fs = require('node:fs')
    const errFd = fs.openSync(${JSON.stringify(errFile)}, 'w')
    const outFd = fs.openSync(${JSON.stringify(outFile)}, 'w')
    const p = spawn(${JSON.stringify(core)}, ['serve','--addr','127.0.0.1:0','--ready-stdout','--exit-with-parent'], { stdio: ['ignore', outFd, errFd] })
    console.log(p.pid)
    setTimeout(() => {}, 120000)
  `
  const parent = spawn(process.execPath, ['-e', parentCode], { stdio: ['ignore', 'pipe', 'inherit'] })
  const corePid = await new Promise((resolve) => {
    let buf = ''
    parent.stdout.on('data', (d) => {
      buf += String(d)
      const first = buf.split('\n')[0].trim()
      if (/^\d+$/.test(first)) resolve(Number(first))
    })
  })
  await sleep(2500)
  const alive = pidsMatching(new RegExp(`^\\s*${corePid}\\s`)).length > 0
  process.kill(parent.pid, 'SIGKILL')
  const t0 = Date.now()
  let gone = null
  while (Date.now() - t0 < 5000) {
    if (pidsMatching(new RegExp(`^\\s*${corePid}\\s`)).length === 0) {
      gone = Date.now() - t0
      break
    }
    await sleep(100)
  }
  check('父进程被 SIGKILL 后核心自退', alive && gone !== null, alive ? `${gone ?? '>5000'}ms` : '核心没起来')
  const err = (() => {
    try {
      return readFileSync(errFile, 'utf8')
    } catch {
      return ''
    }
  })()
  check('核心日志留下原因（父进程已消失）', err.includes('父进程已消失'), err.split('\n').filter((l) => l.includes('父进程')).slice(0, 1).join('') || '(无该行)')
}

// ————————————————————————————————— 主流程 —————————————————————————————————

const SCENARIOS = ['guard', 'shell', 'contract', 'hardening', 'credentials', 'stream', 'exit', 'direct']
const argv = process.argv.slice(2).filter((a) => !a.startsWith('-'))
const want = argv.length ? argv : SCENARIOS

async function main() {
  const t0 = Date.now()
  console.log('桌面入口验收（真 Electron + 真核心）')
  if (want.includes('guard')) scenarioGuard()
  if (results.some((r) => !r.ok)) {
    console.log('\n前置不通过，终止（先解决上面的问题）')
    process.exit(1)
  }

  let shell = null
  let cdp = null
  const needsShell = want.some((s) => ['shell', 'contract', 'hardening', 'credentials', 'stream', 'exit'].includes(s))
  if (needsShell) {
    shell = await launchShell()
    if (!shell.corePid) {
      check('壳与核心起来了', false, `壳日志尾部：\n${shell.logText().split('\n').slice(-6).join('\n')}`)
      process.exit(1)
    }
    check('壳与核心起来了', true, `shell=${shell.shellPid} core=${shell.corePid}`)
    cdp = await connectCDP(shell.port)
  }

  try {
    if (want.includes('shell')) await scenarioShell(cdp)
    if (want.includes('contract')) await scenarioContract(cdp)
    if (want.includes('hardening')) await scenarioHardening(cdp)
    if (want.includes('credentials')) await scenarioCredentials(cdp)
    if (want.includes('stream')) await scenarioStream(cdp)
    if (want.includes('exit')) await scenarioExit(shell)
  } finally {
    cdp?.close()
    if (shell && process.env.KEEP_SHELL !== '1') killShellTree()
  }

  if (want.includes('direct')) await scenarioDirectParentGone()

  const passed = results.filter((r) => r.ok).length
  const failed = results.filter((r) => !r.ok)
  console.log(`\n——— 汇总：${passed}/${results.length} 通过（${((Date.now() - t0) / 1000).toFixed(1)}s）———`)
  if (failed.length) {
    console.log('失败项：')
    for (const f of failed) console.log(`  ❌ ${f.name}${f.detail ? ` — ${f.detail}` : ''}`)
    process.exit(1)
  }
  console.log('桌面入口验收：全部通过')
  process.exit(0)
}

main().catch((e) => {
  console.error(`验收脚本异常：${e?.stack ?? e}`)
  process.exit(1)
})
