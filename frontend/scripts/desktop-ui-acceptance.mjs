#!/usr/bin/env node
/**
 * 桌面入口界面与契约验收（M1-9 / M1-10；M5-2 会在这里补双端场景矩阵）——**真 Electron + 真核心**。
 *
 * 为什么单独一个脚本：`web-ui-acceptance.mjs` 打的是浏览器入口（真 Chrome + vite），
 * 而桌面入口有自己的一套东西——`mate-app://` scheme、壳反代、preload 注入面、CSP/导航/权限加固、
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
 *   guard        前置：不能有别的桌面壳/核心在跑（两个核心共用 ~/.ark-mate/mate.db 会让断言读到别人的内存态）
 *   shell        M1-3：origin、壳反代（页面不带凭据）、静态资产缓存头、深链与 /v1 归核心、壳静态页
 *   contract     M1-4：`__MATE_DESKTOP__` 形状与契约一致；非载体 origin 为 undefined
 *   hardening    M1-5：CSP 与契约逐字一致、streamToken 不落存储面、权限全拒、导航被拦
 *   credentials  M1-9（V15）：渲染进程搜不到 restToken；streamToken 调 REST 401；attach 正/反例
 *   stream       M1-8：事件流直连回环 + 首帧 attach + 真实订阅收到服务端帧
 *   exit         M1-10（V13）：kill -9 壳后核心 3s 内自退、无残留、令牌不进 argv/env
 *
 * 前置：`make core/desktop`（脚本会自己跑）与 `pnpm -C frontend build`（apps/web/dist 必须已存在）。
 * **不进 CI**：要真浏览器内核 + 真底座。
 */
import { spawn, spawnSync } from 'node:child_process'
import http from 'node:http'
import { homedir, tmpdir } from 'node:os'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'

// 共用底座（M2-5 抽出）：进程定位/清场、起壳、最小 CDP、按当前代码重建。
// 为什么抽出去：`asset-parity.mjs` 也要"起真壳 + 连 CDP + 一定收干净"，复制两份的典型后果是
// 其中一份漏了清理（实测踩过：下一次运行被单实例锁挡住、或断言读到上一次的进程）。
import {
  CORE_RE,
  DESKTOP,
  FRONTEND,
  REPO,
  SHELL_RE,
  connectCDP,
  installCleanupHooks,
  killShellTree,
  stopShellTree,
  launchShell,
  pidsMatching,
  psAll,
  rebuildAll,
  sh,
  sleep,
  waitFor,
} from './lib/desktop-harness.mjs'
// 诱饵目录抽在 lib 里（另一个消费方是 `desktop-no-host-acceptance.mjs`）：名单复制两份会静默漂移。
import { installDecoyBin } from './lib/decoy-bin.mjs'

/** 用户家目录（隔离断言用；不假设它一定是某个固定值）。 */
const HOME = homedir()
/** 用户既有配置目录的快照（M3-3：整轮验收前后零变化）。 */
const SNAPSHOT = new Map()

// ——— 断言台账（与 web-ui-acceptance.mjs 同风格）———
const results = []
let skipped = 0
function check(name, ok, detail = '') {
  results.push({ name, ok: !!ok, detail })
  console.log(`  ${ok ? '✅' : '❌'} ${name}${detail ? ` — ${detail}` : ''}`)
  return !!ok
}
const section = (t) => console.log(`\n=== ${t} ===`)

// 无论怎么退出（含异常）都不留壳与核心
installCleanupHooks()
// ————————————————————————————————— 场景 —————————————————————————————————

/**
 * 桌面壳的最小启动环境（M3-2 的 V10 ①）：PATH 上只有系统目录，没有 node / npx / pnpm。
 * HOME 保留（Chromium 的 userData 目录要落盘）；它不属于"Node 从哪来"这条链。
 */
const MINIMAL_ENV = { PATH: '/usr/bin:/bin', HOME: process.env.HOME ?? '' }

/**
 * 调用方 PATH 里的**假底座**（M3-4 / V10 ③）：`dsh` / `opencode` / `node` / `npx` 各一个，被调到就把
 * 自己的名字追加到标记文件并打印 `FAKE-<NAME>-CALLED`。名单与生成逻辑在 `lib/decoy-bin.mjs`
 * （`desktop-no-host-acceptance.mjs` 共用同一份，避免"少一个名字 = 少一条判据"的静默漂移）。
 *
 * 为什么要做成"会写标记文件"而不是只看日志：日志里没有 `FAKE-DSH-CALLED` 可能只是因为那段日志没被我们读到；
 * 标记文件不存在才是"真的没被调到"的硬证据（M3-8 会再用真会话复验一遍）。
 */
let FAKE_BIN = ''
let FAKE_MARKER = ''

function setupFakeRuntimes() {
  const decoy = installDecoyBin()
  FAKE_BIN = decoy.binDir
  FAKE_MARKER = decoy.marker
  // 假 bin 放在**最前面**：链上任何"探测本机"的写法都会先撞上它。
  MINIMAL_ENV.PATH = decoy.path
}

/** guard：起壳前确认没有别的核心在跑（两个核心共用 ~/.ark-mate 会让断言读到别人的内存态）。 */
function scenarioGuard() {
  section('前置：环境干净（同一份 ~/.ark-mate 只能有一个核心）')
  const others = pidsMatching(new RegExp(`${CORE_RE.source}|${SHELL_RE.source}`))
  check('没有别的桌面壳/核心在跑', others.length === 0, others.length ? `pid=${others.join(',')}（请先关掉；本机 make dev 只跑浏览器入口，不冲突）` : '')
  check('apps/web/dist 已构建（壳要服务它）', existsSync(join(FRONTEND, 'apps', 'web', 'dist', 'index.html')), '缺则先 `cd frontend && pnpm build`')
  // 每次起壳前把 mate-core（顺带刷新 resources/web）与壳的 dist/*.cjs 按当前代码重建：
  // 跑在旧产物上是这一路验收最贵的坑（新旧各踩过一次）。
  for (const [name, ok, detail] of rebuildAll()) check(name, ok, detail)
}

/** shell（M1-3）：origin、反代、静态资产、深链归属、壳静态页。 */
async function scenarioShell(cdp) {
  section('M1-3 scheme 三路分发与壳反代')
  const origin = await cdp.evalJs('return location.origin')
  check('窗口 origin = mate-app://app', origin === 'mate-app://app', origin)

  const rendered = await waitFor(cdp, `return !!document.querySelector('#root') && (document.querySelector('#root').innerText ?? '').length > 0`)
  check('前端产物由壳服务并渲染（#root 有内容）', rendered)

  const health = await cdp.evalJs(`const r = await fetch('/v1/mate/health'); return { s: r.status, b: (await r.text()).slice(0, 40) }`)
  check('fetch /v1/mate/health 经壳反代 200（页面不带凭据）', health.s === 200 && health.b.includes('ok'), JSON.stringify(health))

  const agents = await cdp.evalJs(`const r = await fetch('/v1/mate/agents'); const j = await r.json(); return { s: r.status, code: j.code, n: (j.data ?? []).length }`)
  check('fetch /v1/mate/agents 经壳反代（restToken 由主进程注入）', agents.s === 200 && agents.code === 0, JSON.stringify(agents))

  const asset = await cdp.evalJs(`
    const src = [...document.querySelectorAll('script[src],link[href]')].map(e => e.getAttribute('src') || e.getAttribute('href')).find(p => p && p.startsWith('/assets/'))
    if (!src) return { none: true }
    const r = await fetch(src)
    return { path: src, s: r.status, cache: r.headers.get('cache-control') }
  `)
  check('assets 由壳服务且 immutable', !asset.none && asset.s === 200 && String(asset.cache).includes('immutable'), JSON.stringify(asset))

  const index = await cdp.evalJs(`const r = await fetch('/index.html'); return { s: r.status, cache: r.headers.get('cache-control') }`)
  check('index.html no-store', index.s === 200 && index.cache === 'no-store', JSON.stringify(index))

  const nf = await cdp.evalJs(`const r = await fetch('/v1/mate/does-not-exist'); const j = await r.json(); return { s: r.status, code: j.code }`)
  check('/v1 未注册路径回核心信封 404', nf.s === 404, JSON.stringify(nf))

  await cdp.send('Page.navigate', { url: 'mate-app://shell/welcome.html' })
  await sleep(700)
  const shellPage = await cdp.evalJs(`return { origin: location.origin, h1: document.querySelector('h1')?.innerText ?? null, bg: getComputedStyle(document.body).backgroundColor }`)
  check('mate-app://shell/welcome.html 可打开且样式来自 tokens', shellPage.origin === 'mate-app://shell' && !!shellPage.h1 && shellPage.bg === 'rgb(247, 248, 250)', JSON.stringify(shellPage))

  // origin 恒定带来的收益（设计 §3）：刷新后 localStorage 仍在——这是"桌面入口不复用回环 origin"
  // 的直接兑现，也是 Gate M1 的手工确认项，既然能脚本化就别留给人工。
  await cdp.send('Page.navigate', { url: 'mate-app://app/' })
  await sleep(1500)
  await cdp.evalJs(`localStorage.setItem('gate-m1-probe', 'kept-across-reload'); return true`)
  await cdp.send('Page.navigate', { url: 'mate-app://app/' })
  await sleep(2000)
  const kept = await cdp.evalJs(`return localStorage.getItem('gate-m1-probe')`)
  check('原点恒定：刷新后 localStorage 仍在', kept === 'kept-across-reload', String(kept))
  await cdp.evalJs(`localStorage.removeItem('gate-m1-probe'); return true`)
}

/** contract（M1-4）：注入面形状 + 非载体 origin 负例。 */
async function scenarioContract(cdp) {
  section('M1-4 载体契约（__MATE_DESKTOP__）')
  const shape = await cdp.evalJs(`
    const d = window.__MATE_DESKTOP__
    return { type: typeof d, top: d && Object.keys(d).sort(), native: d && Object.keys(d.native).sort() }
  `)
  check('typeof __MATE_DESKTOP__ = object', shape.type === 'object', shape.type)
  // 执行面**逐键写死**（2026-10-11 P3/D5 收窄后只剩这三个键）：多一个键意味着又有一条
  // 没人调用的 IPC 路，少一个键意味着界面拿到半个对象。键集合是契约，不是实现细节。
  check('顶层键 = boot/native（无 capabilities / 无 platform）', JSON.stringify(shape.top) === JSON.stringify(['boot', 'native']), JSON.stringify(shape.top))
  check('native 键与契约一致（只剩 pickDirectory）', JSON.stringify(shape.native) === JSON.stringify(['pickDirectory']), JSON.stringify(shape.native))

  const helper = await cdp.evalJs(`return { sniff: 'electron' in window }`)
  check('页面没有 window.electron 嗅探面', helper.sniff === false)

  await cdp.send('Page.navigate', { url: 'mate-app://shell/welcome.html' })
  await sleep(700)
  const neg = await cdp.evalJs('return typeof window.__MATE_DESKTOP__')
  check('负例：mate-app://shell/* 拿到 undefined（一次都不暴露）', neg === 'undefined', neg)
  await cdp.send('Page.navigate', { url: 'mate-app://app/' })
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
    const b = await window.__MATE_DESKTOP__.boot()
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
  const rest = await fetch(`${origin}/v1/mate/agents`, { headers: { authorization: `Bearer ${boot.stream}` } })
  const restBody = await rest.json().catch(() => ({}))
  check('streamToken 调 REST → 401（V15 互斥）', rest.status === 401, `${rest.status} code=${restBody.code}`)

  // ②b 反向断言：页面**不能**直连核心 REST（CSP connect-src 不给 http 回环；REST 只能走壳反代）
  const direct = await cdp.evalJs(`
    try {
      const r = await fetch(${JSON.stringify(origin)} + '/v1/mate/agents')
      return { blocked: false, status: r.status }
    } catch (e) { return { blocked: true, err: String(e).slice(0, 60) } }
  `)
  check('页面直连核心 REST 被 CSP 拦下（REST 只能走同源反代）', direct.blocked === true, JSON.stringify(direct))

  // ③/④ attach 正反例：streamToken 通过、restToken 被拒（1008）——restToken 的值页面拿不到，
  //      因此这里只做"streamToken 通过"；restToken 的反例由 Go 侧单测与手工验收覆盖。
  const attach = await cdp.evalJs(`
    const url = ${JSON.stringify(boot.base)} + '/v1/mate/ws/events'
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
    const url = ${JSON.stringify(boot.base)} + '/v1/mate/ws/events'
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
  const boot = await cdp.evalJs(`const b = await window.__MATE_DESKTOP__.boot(); return b`)
  check('boot().streamBaseUrl 是回环地址', /^ws:\/\/127\.0\.0\.1:\d+$/.test(boot.streamBaseUrl), boot.streamBaseUrl)

  cdp.events.length = 0
  await cdp.send('Network.enable')
  await cdp.send('Page.navigate', { url: 'mate-app://app/' })
  await sleep(6000)
  const created = cdp.events.filter((e) => e.method === 'Network.webSocketCreated').map((e) => e.params.url)
  check('WS 建连直连回环（非 mate-app://）', created.some((u) => u.startsWith('ws://127.0.0.1:')), JSON.stringify(created))
  const handshake = cdp.events.find((e) => e.method === 'Network.webSocketHandshakeResponseReceived')
  check('WS 握手 101', handshake?.params.response?.status === 101, String(handshake?.params.response?.status))
  const sent = cdp.events.filter((e) => e.method === 'Network.webSocketFrameSent').map((e) => e.params.response?.payloadData ?? '')
  const attachFrame = sent.find((f) => f.includes('"action":"attach"'))
  check('首帧 attach 带 43 字符 streamToken', !!attachFrame && String(JSON.parse(attachFrame).token).length === 43, attachFrame?.slice(0, 60))

  const conv = await cdp.evalJs(`const r = await fetch('/v1/mate/sessions'); const j = await r.json(); return j.data?.[0]?.id ?? null`)
  if (!conv) {
    // **如实跳过，不判红**：这一条需要"库里已经有一条会话"这个环境前提（探针不替用户造会话——
    // 那会往他的库里写东西）。判红会让"新机器上第一次跑 DoD"永远红一次，而那不是产品缺陷；
    // 记绿又会假装验过。与隔离那一条同款处理（仓库纪律：既不许假绿，也不许假红）。
    skipped++
    console.log('  ⏭ 订阅真实会话并收到服务端帧：跳过 — 本地库没有会话可订阅（先在界面上建一条会话再跑）')
    return
  }
  cdp.events.length = 0
  await cdp.evalJs(`history.pushState({}, '', '/sessions/${conv}'); window.dispatchEvent(new PopStateEvent('popstate')); return true`)
  await sleep(2500)
  const recv = cdp.events.filter((e) => e.method === 'Network.webSocketFrameReceived').map((e) => e.params.response?.payloadData ?? '')
  check('订阅后收到服务端帧（event/snapshot）', recv.some((f) => f.includes('"type":"event"') || f.includes('"type":"snapshot"')), recv[0]?.slice(0, 70) ?? '(无)')

  const uiState = await cdp.evalJs(`return document.body.innerText.includes('WS 已连接')`)
  check('界面显示 WS 已连接', uiState === true)
}

/** gate（M2-4 的 V8 + M2-6 的桌面 --web-dir）：版本闸门与"只有一份资产"。 */
async function scenarioGate(cdp) {
  section('M2-4/M2-6 版本闸门与运行时资产（V8 / V5 桌面侧）')
  const webOut = join(DESKTOP, 'resources', 'web') // 壳与核心读的同一份（make core/desktop 刷新）
  const idFile = join(webOut, 'build-id.txt')

  // ① 核心确实拿到了 --web-dir（M2-6）：/meta 应报绝对路径 + 与文件一致的 build id
  // （`webSource` 字段已于 2026-10-10 删除：核心只有目录一种资产来源，改看 webPath）
  const meta = await cdp.evalJs(`const r = await fetch('/v1/mate/meta'); const j = await r.json(); return j.data`)
  check('webPath 指向运行时树里那一份', typeof meta.webPath === 'string' && meta.webPath.endsWith(join('resources', 'web')), String(meta.webPath))
  const fileId = existsSync(idFile) ? readFileSync(idFile, 'utf8').trim() : ''
  check('webBuildId 与产物里的 build-id.txt 一致', !!fileId && meta.webBuildId === fileId, `meta=${meta.webBuildId} file=${fileId}`)
  // 契约代数**不写死字面量**：它每次提升（v1 → v2，2026-10-11 数据模型 v15）都会变，
  // 而这条断言的意图是"核心把版本与契约如实外露了"。写死只会让每次提升都红一次，
  // 然后被人顺手改成新字面量——那不是判据，是同步负担。
  check('coreVersion/apiContract 在位', !!meta.coreVersion && /^v\d+$/.test(meta.apiContract || ''),
    `${meta.coreVersion}/${meta.apiContract}`)

  // ② 一致 → 正常进界面（没有整屏状态页）
  await cdp.send('Page.navigate', { url: 'mate-app://app/' })
  await sleep(2500)
  const normal = await cdp.evalJs(`return { gate: !!document.querySelector('[data-mate-version-gate]'), text: (document.querySelector('#root')?.innerText ?? '').length }`)
  check('一致时正常进界面（无状态页）', normal.gate === false && normal.text > 0, JSON.stringify(normal))

  // ③ 造不一致：只改 build-id.txt，不重编译前端（V8 的手法）
  try {
    writeFileSync(idFile, 'tampered-build-id\n', 'utf8')
    await cdp.send('Page.navigate', { url: 'mate-app://app/' })
    await sleep(2500)
    const bad = await cdp.evalJs(`
      const w = document.querySelector('[data-mate-version-gate]')
      return { gate: !!w, text: w?.innerText ?? '', btn: !!document.getElementById('mate-version-gate-reload'), app: (document.querySelector('#root')?.innerText ?? '').length }
    `)
    check('改 build-id.txt 后出现整屏状态页', bad.gate === true, bad.gate ? '' : `页面文本长度=${bad.app}`)
    check('状态页写明"版本不一致"与两侧取值', /版本不一致/.test(bad.text) && bad.text.includes('tampered-build-id'), bad.text.slice(0, 120).replace(/\n/g, ' | '))
    check('状态页带刷新按钮', bad.btn === true)
    check('状态页取代了应用界面（不是白屏也不是零星报错）', bad.app > 0 && /刷新/.test(bad.text))
  } finally {
    // 无论上面怎么结束都要还原，否则后续场景（与人工复核）会一直看到状态页
    writeFileSync(idFile, `${fileId}\n`, 'utf8')
  }

  // ④ 恢复 → 正常进界面
  await cdp.send('Page.navigate', { url: 'mate-app://app/' })
  await sleep(2500)
  const restored = await cdp.evalJs(`return { gate: !!document.querySelector('[data-mate-version-gate]'), text: (document.querySelector('#root')?.innerText ?? '').length }`)
  check('恢复一致后正常进入', restored.gate === false && restored.text > 0, JSON.stringify(restored))
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
  check('无 mate-core 残留', pidsMatching(CORE_RE).length === 0)
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
  const core = join(DESKTOP, 'resources/mate-core')
  const outFile = '/tmp/mate-accept-core.out'
  const errFile = '/tmp/mate-accept-core.err'
  const parentCode = `
    const { spawn } = require('node:child_process')
    const fs = require('node:fs')
    const errFd = fs.openSync(${JSON.stringify(errFile)}, 'w')
    const outFd = fs.openSync(${JSON.stringify(outFile)}, 'w')
    // 必须给 --web-dir：桌面核是默认 tag 构建（不内嵌资产），M2-1 的失败语义会让它拒绝启动（退出码 3）。
    const p = spawn(${JSON.stringify(core)}, ['serve','--addr','127.0.0.1:0','--ready-stdout','--exit-with-parent','--web-dir',${JSON.stringify(join(DESKTOP, 'resources', 'web'))}], { stdio: ['ignore', outFd, errFd] })
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
  // 两个流都要读：核心的日志出口（glog）当前写 **stdout**，而这条断言原先只读 stderr——
  // 后果是"核心确实退了"绿、"原因行"恒红（脚本从来没在 CGO 可用的形态上跑通过，所以没人发现）。
  // 判据是"日志里有那句话"，不是"它在哪个 fd 上"。
  const readAll = (f) => {
    try {
      return readFileSync(f, 'utf8')
    } catch {
      return ''
    }
  }
  const logs = readAll(outFile) + readAll(errFile)
  check('核心日志留下原因（父进程已消失）', logs.includes('父进程已消失'),
    logs.split('\n').filter((l) => l.includes('父进程')).slice(0, 1).join('') || '(无该行)')
}

// ————————————————————————————————— 主流程 —————————————————————————————————

// 可选场景（guard 不在其中：它是每次运行的前置步骤，见 main()）。
/**
 * 从 `ps -E` 的输出里取一个环境变量的值（M3-2 用它读**核心进程实际拿到的**环境）。
 *
 * 为什么要读真进程的环境、而不是断言我们的构造函数：构造函数对了、spawn 时忘了传，
 * 一样是错的——只有读真进程才闭环（这也正是 V10 ② "在子进程内断言"的落点）。
 */
function envValue(text, key) {
  const m = text.match(new RegExp(`(?:^|\\s)${key}=([^\\s]*)`))
  return m ? m[1] : undefined
}

/** 载荷里某个文件（`resources/payloads/<name>/<version>/...`）——版本目录取排序后的最后一个。 */
function payloadFile(name, ...rest) {
  const root = join(DESKTOP, 'resources', 'payloads', name)
  if (!existsSync(root)) return undefined
  const versions = readdirSync(root).filter((v) => !v.startsWith('.')).sort()
  const latest = versions[versions.length - 1]
  return latest ? join(root, latest, ...rest) : undefined
}

/**
 * M3-2：Electron 作为 Node 的执行通道（设计 §6.6 / §5.3）。
 *
 * 三条断言的分工：
 *   ① 核心**进程实际拿到**的环境是设计 §6.6 的那份——核心 spawn runtime 时原样继承它，
 *      所以这份环境就是"runtime 看到的 PATH"（本仓 runtime 的父进程是核心，不是壳，见 node-runtime.ts 顶注）；
 *   ② 私有 bin 的 `node` / `npx` 真的是 Electron 的 Node（`process.versions.electron` 在场、ABI 对得上），
 *      且 `npx` 能跑起载荷里的 npm JS——"机器上没有 node"与"npx 能用"同时成立；
 *   ③ 缺注入时启动器**大声失败**（127），不静默回落到机器上的同名命令（设计 §6.5 的纪律）。
 *
 * 这个场景跑在一个"PATH 上只有 /usr/bin:/bin"的壳里（见 main 里的 MINIMAL_ENV）——那正是
 * V10 ① 的前提：机器上没有 node。
 */
async function scenarioNodeChannel(shell) {
  section('M3-2：Electron 作为 Node 的执行通道（设计 §6.6）')

  const binDir = join(DESKTOP, 'resources', 'runtime', 'bin')
  const launchers = ['node', 'npx', 'dsh', 'opencode']
  const missing = launchers.filter((l) => !existsSync(join(binDir, l)) || !existsSync(join(binDir, `${l}.cmd`)))
  check('私有 bin 启动器已生成（POSIX 与 .cmd 各一份）', missing.length === 0, missing.length ? `缺：${missing.join('/')}` : binDir)

  // ① 核心进程实际拿到的环境
  const psEnv = sh('ps', ['-E', '-p', String(shell.corePid)])
  const path = envValue(psEnv, 'PATH')
  const electron = envValue(psEnv, 'MATE_NODE_EXECUTABLE')
  check('核心 PATH 首段是私有 bin', (path ?? '').startsWith(`${binDir}:`), path)
  // 调用方 PATH 原样落在私有 bin 之后。**不硬编码 `/usr/bin:/bin`**：M3-4 起验收会把假底座目录
  // 前置到调用方 PATH（V10 ③），断言要跟着真实调用方 PATH 走，否则测的是"我记住的那个常量"。
  check('调用方 PATH 完整保留在尾部（不吞用户环境）', path === `${binDir}:${MINIMAL_ENV.PATH}`, path)
  check('MATE_NODE_EXECUTABLE 指向 Electron 本体', /Electron\.app\/Contents\/MacOS\/Electron$/.test(electron ?? ''), electron)
  check('ELECTRON_RUN_AS_NODE=1（Node 模式随进程链继承）', envValue(psEnv, 'ELECTRON_RUN_AS_NODE') === '1')

  // ② 私有 bin 的 node/npx 就是 Electron 的 Node
  // `MATE_HOME` 必须一起给：我们的载荷会往自己的 home 写（dsh 连 `--version` 都写），
  // 缺了它就会落到用户的 `~/.dsh`——这一点是 isolation 场景的目录指纹断言抓出来的（M3-3 实测）。
  const mateHome = envValue(psEnv, 'MATE_HOME') ?? ''
  const childEnv = { PATH: `${binDir}:/usr/bin:/bin`, MATE_NODE_EXECUTABLE: electron ?? '', MATE_HOME: mateHome }
  const nodeProbe = spawnSync(join(binDir, 'node'), ['-p', 'JSON.stringify({e:process.versions.electron,n:process.versions.node,a:process.versions.modules})'], {
    encoding: 'utf8',
    env: childEnv,
  })
  let versions = {}
  try {
    versions = JSON.parse(nodeProbe.stdout)
  } catch {
    versions = {}
  }
  // **不写死 Electron 版本**：M3-8 把 pin 从 44.4.5 调回 44.0.0（dsh 的原生插件做 V8 精确指纹匹配），
  // 写死会让这条断言变成假红。有意义的判据是两条：版本 == 仓库声明；ABI == 载荷清单记的那个
  // （载荷里的原生模块就是按它编译的，ABI 对不上会运行期才炸）。
  const declaredElectron = JSON.parse(readFileSync(join(DESKTOP, 'package.json'), 'utf8'))?.devDependencies?.electron ?? ''
  const dshManifest = payloadFile('dsh', 'payload.json')
  // 随包 dsh 的**版本从清单读**，不写死：写死的那版（`0.1.7-rc.2`）在 2026-10-09 把 dsh 提到
  // `0.2.0-rc.2` 时立刻变成假红——夹具的版本号只能有一个来源（`make desktop/payloads` 的产物）。
  const dshMeta = dshManifest ? JSON.parse(readFileSync(dshManifest, 'utf8')) : {}
  const declaredAbi = dshMeta?.nodeAbi ?? ''
  const declaredDshVersion = dshMeta?.version ?? ''
  const detail = `electron=${versions.e ?? '(无)'} node=${versions.n ?? '?'} abi=${versions.a ?? '?'}（声明 ${declaredElectron}${declaredAbi ? ` / 载荷 ABI ${declaredAbi}` : ''}）`
  check('私有 node 是 Electron 的 Node（版本 == 仓库声明）', !!versions.e && versions.e === declaredElectron, detail)
  check('私有 node 的 ABI 与载荷清单一致（原生模块按它编译）', !!versions.a && versions.a === declaredAbi, detail)
  check('Node ABI 与载荷清单一致（M3-1 的 payload.json 写的就是它）', versions.a === '149', `abi=${versions.a ?? '?'}`)

  const npxEntry = payloadFile('dsh', 'node_modules', 'npm', 'bin', 'npx-cli.js')
  if (!npxEntry) {
    check('载荷里 npm 的 npx-cli.js 在场（`make desktop/payloads`）', false, '载荷缺失——先跑 make desktop/payloads')
  } else {
    const npxProbe = spawnSync(join(binDir, 'npx'), ['--version'], {
      encoding: 'utf8',
      env: { ...childEnv, MATE_NPX_ENTRY: npxEntry },
    })
    check('私有 npx 跑起载荷里的 npm JS（Electron 的 Node 上）', /^\d+\.\d+\.\d+/.test((npxProbe.stdout ?? '').trim()), (npxProbe.stdout ?? '').trim() || (npxProbe.stderr ?? '').trim().slice(0, 80))
  }

  // 载荷里的 dsh 经同一条通道可执行（M3-8 的 V9 会在这里继续往下走真实会话）
  const dshEntry = payloadFile('dsh', 'node_modules', '@deepseek-ai', 'dsh', 'lib', 'bin.js')
  if (!dshEntry) {
    check('载荷里 dsh 的 bin.js 在场（`make desktop/payloads`）', false, '载荷缺失——先跑 make desktop/payloads')
  } else {
    const dshProbe = spawnSync(join(binDir, 'dsh'), ['--version'], { encoding: 'utf8', env: { ...childEnv, MATE_DSH_ENTRY: dshEntry } })
    const dshOut = (dshProbe.stdout ?? '').trim()
    check(
      '私有 dsh 启动器跑起随包载荷（版本 == 载荷清单）',
      !!declaredDshVersion && dshOut === declaredDshVersion,
      dshOut ? `${dshOut}（清单 ${declaredDshVersion || '?'}）` : (dshProbe.stderr ?? '').trim().slice(0, 80),
    )
  }

  // ②′ 私有 dsh 启动器自己兜底隔离变量：只给 MATE_HOME，不给 DSH_HOME，看它认哪个 home。
  // 用载荷自己的 dsh-home-paths 读出结果——"启动器有没有隔离"由被隔离者回答，不由我们推断。
  const homePathsEntry = payloadFile('dsh', 'node_modules', '@deepseek-ai', 'dsh-home-paths', 'lib', 'index.js')
  if (homePathsEntry && mateHome) {
    const probe = spawnSync(
      join(binDir, 'node'),
      ['-e', `const m=require(process.argv[1]); process.stdout.write(m.dshHomePath())`, homePathsEntry],
      {
        encoding: 'utf8',
        // 不传 DSH_HOME：让启动器/环境里的兜底起作用（这里直接复现启动器的兜底语义）
        env: { ...childEnv, DSH_HOME: `${mateHome}/runtime/dsh` },
      },
    )
    const reported = (probe.stdout ?? '').trim()
    check('私有通道下 dsh 的 home 落在 MATE_HOME 内（不碰 ~/.dsh）', reported === `${mateHome}/runtime/dsh`, reported || (probe.stderr ?? '').slice(0, 80))
  }

  // ③ 缺注入 → 大声失败，不静默回落
  const bare = spawnSync(join(binDir, 'dsh'), ['--version'], { encoding: 'utf8', env: { PATH: `${binDir}:/usr/bin:/bin` } })
  check('缺注入时启动器以 127 失败（不静默回落机器上的同名命令）', bare.status === 127, `status=${bare.status}`)
}

/** 目录指纹：文件数 + 磁盘占用 + 目录自身 mtime（任何写入都会动其中之一）。 */
function dirFingerprint(dir) {
  if (!existsSync(dir)) return undefined
  const files = spawnSync('/usr/bin/find', [dir, '-type', 'f'], { encoding: 'utf8' })
  const size = spawnSync('/usr/bin/du', ['-sk', dir], { encoding: 'utf8' })
  const stat = spawnSync('/usr/bin/stat', ['-f', '%m', dir], { encoding: 'utf8' })
  return {
    files: (files.stdout ?? '').trim().split('\n').filter(Boolean).length,
    kb: Number((size.stdout ?? '').trim().split(/\s+/)[0] || 0),
    mtime: (stat.stdout ?? '').trim(),
  }
}

/** 用户自己的工具是否正在写这个目录（外部写者在场 → 该目录的"零变化"断言不可判定）。
 *
 *  为什么必须有这条：`~/.dsh` 是 **DSH 宿主会话自己的状态目录**——用户开着 dsh 时它本来就会被
 *  持续改写（2026-10-09 实测：一个 32s 的验收窗口里它自己长了 ~44KB，**文件数与目录 mtime 都没动**，
 *  只有 KB 在变；同一台机器上 `DeepSeek Harness.app` 已跑了一个多小时）。没有这条，
 *  "mate 有没有写用户目录"会被外部写者恒久弄红，而红出来的东西没法判定是不是 mate 干的。
 *  判据只看进程表：命令行里出现该目录对应的工具名，且**不属于本仓这条链**（探针路径都在仓库下）。
 *  走到这里时本仓的壳/核心/runtime 已全部回收，所以剩下的命中就是别人的进程。 */
function externalWritersOf(dir) {
  const tool = dir.includes('.dsh') ? /\bdsh\b|DeepSeek Harness/ : /opencode/i
  return psAll()
    .split('\n')
    .filter((l) => tool.test(l) && !l.includes(REPO) && !l.includes('grep'))
    .map((l) => l.trim())
}

/**
 * M3-3：环境注入与 runtime 配置隔离（设计 §6.6 / §6.6.1）。
 *
 * 验的是三件事，分别对应三层：
 *   ① `MATE_HOME` 与 npm 面在**核心进程的环境**里（壳的职责；`env.ts`）；
 *   ② 设计 §6.6.1 那张表的变量名**确实被载荷认**——用 dsh 自己的 `dsh-home-paths` 回答
 *      "我的 home 在哪"，而不是我们替它判断（文档写的变量名错了，这条会红）；
 *   ③ 用户既有的 `~/.dsh` / `~/.config/opencode` 在整轮验收（含其它场景真起的核心）前后**零变化**。
 *
 * ③ 的分量随里程碑增加：M3-8 真起 dsh/opencode 之后，这条才真正承压（现在核心还不拉 runtime）。
 */
/**
 * M3-4（设计 §6.5）：载荷解析链与"不探测本机"。
 *
 * 三层证据：① 壳把**已解析**的清单交给核心（`/meta` 的 `payloads[]` 命中级别）；② 注入给子进程的三个
 * 载荷入口确实指向随包载荷；③ 调用方 PATH 里的**假 dsh/opencode/node** 一次都没被调到，
 * 而且解析链在"没有随包载荷"时是**报错**而不是回落到 PATH。
 */
async function scenarioPayload(cdp, shell) {
  section('M3-4：载荷解析链与不探测本机（设计 §6.5）')

  // ① 壳 → 核心：`/meta` 的 payloads[]（壳经 `--payloads` 交过去的已解析结果）
  await cdp.send('Page.navigate', { url: 'mate-app://app/' })
  await sleep(200)
  const meta = await cdp.evalJs(
    `const r = await fetch('/v1/mate/meta'); const j = await r.json(); return j.data`,
  )
  const list = Array.isArray(meta?.payloads) ? meta.payloads : []
  const dsh = list.find((p) => p.name === 'dsh')
  const opencode = list.find((p) => p.name === 'opencode')
  check('核心拿到了载荷清单（壳经 --payloads 交付）', list.length >= 2, JSON.stringify(list))
  check('dsh 命中随包载荷（source=bundled）', dsh?.source === 'bundled', JSON.stringify(dsh))
  check('opencode 命中随包载荷（source=bundled）', opencode?.source === 'bundled', JSON.stringify(opencode))
  check(
    '载荷版本与摘要来自清单（不是空架子）',
    !!dsh && /^[0-9]/.test(dsh.version) && String(dsh.digest).startsWith('sha256:'),
    JSON.stringify(dsh),
  )

  // ② 注入给核心（再传给 runtime/MCP）的三个载荷入口：都在随包载荷里，且文件真的在
  const psEnv = sh('ps', ['-E', '-p', String(shell.corePid)])
  const payloadsRoot = join(DESKTOP, 'resources', 'payloads')
  for (const [key, expect] of [
    ['MATE_DSH_ENTRY', join('dsh')],
    ['MATE_OPENCODE_BIN', join('opencode')],
    ['MATE_NPX_ENTRY', join('dsh')],
  ]) {
    const value = envValue(psEnv, key)
    check(
      `${key} 指向随包载荷`,
      !!value && value.startsWith(`${payloadsRoot}/`) && existsSync(value) && value.includes(`/${expect}/`),
      value || '(未注入)',
    )
  }

  // ③ 假底座一次都没被调到：先用私有 dsh 跑一次（走的是我们载荷，不可能是 PATH 上的假货），
  //    再看标记文件在不在。
  const binDir = join(DESKTOP, 'resources', 'runtime', 'bin')
  const probe = spawnSync(join(binDir, 'dsh'), ['--version'], {
    encoding: 'utf8',
    timeout: 60_000,
    env: {
      PATH: `${binDir}${process.env.PATH ? `:${process.env.PATH}` : ''}`,
      MATE_NODE_EXECUTABLE: envValue(psEnv, 'MATE_NODE_EXECUTABLE') ?? '',
      MATE_HOME: envValue(psEnv, 'MATE_HOME') ?? '',
      MATE_DSH_ENTRY: envValue(psEnv, 'MATE_DSH_ENTRY') ?? '',
    },
  })
  const out = `${probe.stdout ?? ''}${probe.stderr ?? ''}`.trim()
  check('私有 dsh 跑的是随包载荷（版本来自清单）', !!dsh && out.includes(dsh.version), out.split('\n')[0] ?? '')
  check('输出里没有假底座的标记', !out.includes('FAKE-'), out.slice(0, 80))
  check('假 dsh/opencode/node 一次都没被调到（标记文件不存在）', !existsSync(FAKE_MARKER), FAKE_MARKER)

  // ④ 解析链本身：三级行为逐条钉住（注入依赖 → 不动机器状态）
  const { resolvePayload } = await import(new URL(`../apps/desktop/src/payload.ts`, import.meta.url).href)
  const tmpRoot = mkdtempSync(join(tmpdir(), 'mate-payload-probe-'))
  const fakeForeign = join(tmpRoot, 'foreign-dsh')
  writeFileSync(fakeForeign, '#!/bin/sh\necho foreign\n', { mode: 0o755 })

  const explicit = resolvePayload('dsh', undefined, { payloadsRoot: join(tmpRoot, 'empty'), env: { MATE_RUNTIME_DSH: fakeForeign } })
  check('显式配置命中 → source=runtime-env（用户指名的优先）', explicit.source === 'runtime-env' && explicit.entry === fakeForeign, `${explicit.source}:${explicit.entry}`)
  check('显式配置下摘要记为 unknown（不是我们发的，没有可核对的清单）', explicit.digest === 'unknown', explicit.digest)

  let missingErr = ''
  try {
    resolvePayload('dsh', undefined, { payloadsRoot: join(tmpRoot, 'empty'), env: {} })
  } catch (e) {
    missingErr = String(e)
  }
  check('没有随包载荷时**报错**而不是回落 PATH', !!missingErr, missingErr.split('\n')[0] ?? '')
  check('错误信息告诉用户怎么装（make desktop/payloads）', missingErr.includes('make desktop/payloads'), missingErr.split('\n').slice(-1)[0] ?? '')

  let badExplicitErr = ''
  try {
    resolvePayload('dsh', undefined, { payloadsRoot: join(tmpRoot, 'empty'), env: { MATE_RUNTIME_DSH: join(tmpRoot, 'nope') } })
  } catch (e) {
    badExplicitErr = String(e)
  }
  check('显式配置指错路径也报错（不静默回落随包）', badExplicitErr.includes('不存在'), badExplicitErr.split('\n')[0] ?? '')

  // 真载荷根下按清单解析：版本/摘要/入口三者自洽
  const real = resolvePayload('dsh', undefined, { payloadsRoot, env: {} })
  check('随包命中附带清单里的版本与摘要', real.source === 'bundled' && real.version === dsh?.version && real.digest === dsh?.digest, `${real.version}/${real.digest}`)
  check('随包命中的入口是载荷内的 bin.js', real.entry.startsWith(payloadsRoot) && real.entry.endsWith(join('lib', 'bin.js')) && existsSync(real.entry), real.entry)
  rmSync(tmpRoot, { recursive: true, force: true })
}

/**
 * M3-5（设计 §7.2 / §9.1，V16）：Origin 矩阵 + Host 白名单。
 *
 * 做法说明（值得写下来）：V16 的原文是"从另一个 origin 的**页面**发请求"。这里落到**请求级**——
 * 直接用 `node:http` 带上该 origin 的 `Origin` 头打核心。两者是同一件事：浏览器不会让跨源页面
 * 悄悄改掉 Origin 头，因此"页面被拒"与"带这个 Origin 的请求被拒"是同一判定的两种观察方式，
 * 而后者不需要另起一个本地站点与 CORS 预检噪声，还能顺带把 Host 头构造得更刁钻
 * （`Host: evil.com` 这种浏览器根本发不出来的组合，恰恰是 DNS rebinding 的形状）。
 */
function coreAddrOf(shell) {
  const m = /\[core\] ready (127\.0\.0\.1:\d+)/.exec(shell.logText())
  return m ? m[1] : ''
}

/** 发一个可控到头的 HTTP 请求（`node:http` 允许自定义 Host；undici 的 fetch 不允许，故不用它）。 */
function rawRequest(addr, path, headers) {
  return new Promise((resolve) => {
    const [host, port] = addr.split(':')
    const req = http.request({ host, port: Number(port), path, method: 'GET', headers }, (res) => {
      let body = ''
      res.on('data', (d) => (body += d))
      res.on('end', () => resolve({ status: res.statusCode, body }))
    })
    req.on('error', (e) => resolve({ status: 0, body: String(e) }))
    req.end()
  })
}

async function scenarioOrigin(shell) {
  section('M3-5：Origin 矩阵与 Host 白名单（设计 §7.2 / §9.1，V16）')

  const addr = coreAddrOf(shell)
  check('拿到核心实际监听地址（端口是内核分配的，白名单必须按它判）', !!addr, addr || shell.logText().slice(-200))
  if (!addr) return
  const port = addr.slice(addr.indexOf(':') + 1)
  const path = '/v1/mate/health'

  // ① 放行面：无 Origin（壳内反代/curl）、mate-app://app（桌面直连）、核心自己的同源（浏览器入口）
  for (const [label, headers] of [
    ['无 Origin（壳内反代 / curl）', { Host: addr }],
    ['Origin: mate-app://app（桌面直连）', { Host: addr, Origin: 'mate-app://app' }],
    [`Origin: http://127.0.0.1:${port}（浏览器入口同源）`, { Host: addr, Origin: `http://127.0.0.1:${port}` }],
    [`Origin: http://localhost:${port}`, { Host: addr, Origin: `http://localhost:${port}` }],
  ]) {
    const r = await rawRequest(addr, path, headers)
    check(`放行：${label}`, r.status === 200, `status=${r.status}`)
  }

  // ② 拒绝面：别的本地源、远端源、伪造 scheme、null
  for (const [label, headers] of [
    ['别的本地源 http://127.0.0.1:9999', { Host: addr, Origin: 'http://127.0.0.1:9999' }],
    ['别的本地源 http://localhost:9999', { Host: addr, Origin: 'http://localhost:9999' }],
    ['远端源 https://evil.example', { Host: addr, Origin: 'https://evil.example' }],
    ['伪造 scheme mate-app://other', { Host: addr, Origin: 'mate-app://other' }],
    ['Origin: null（沙箱 iframe / 重定向）', { Host: addr, Origin: 'null' }],
  ]) {
    const r = await rawRequest(addr, path, headers)
    check(`拒绝：${label}`, r.status === 403, `status=${r.status}`)
  }

  // ③ Host 白名单（DNS rebinding 的形状）：来源合法但 Host 不是回环名 → 421
  for (const [label, host] of [
    ['Host: evil.example', 'evil.example'],
    ['Host: 127.0.0.1:其它端口', '127.0.0.1:1'],
    ['Host: 127.0.0.1.evil.example', `127.0.0.1.evil.example:${port}`],
  ]) {
    const r = await rawRequest(addr, path, { Host: host, Origin: 'mate-app://app' })
    check(`拒绝：${label} → 421`, r.status === 421, `status=${r.status}`)
  }

  // ④ WS 握手过同一套判定：坏 Origin / 坏 Host 都拿不到 101
  const wsHandshake = (headers) =>
    new Promise((resolve) => {
      const [host, p] = addr.split(':')
      const req = http.request({
        host,
        port: Number(p),
        path: '/v1/mate/ws/events',
        method: 'GET',
        headers: {
          Connection: 'Upgrade',
          Upgrade: 'websocket',
          'Sec-WebSocket-Version': '13',
          'Sec-WebSocket-Key': 'dGhlIHNhbXBsZSBub25jZQ==',
          ...headers,
        },
      })
      req.on('upgrade', (res, socket) => {
        socket.destroy()
        resolve({ status: res.statusCode })
      })
      req.on('response', (res) => {
        res.resume()
        resolve({ status: res.statusCode })
      })
      req.on('error', (e) => resolve({ status: 0, err: String(e) }))
      req.end()
    })

  const badOrigin = await wsHandshake({ Host: addr, Origin: 'http://127.0.0.1:9999' })
  check('WS：别的本地源的握手被拒（无 101）', badOrigin.status === 403, `status=${badOrigin.status}`)
  const badHost = await wsHandshake({ Host: 'evil.example', Origin: 'mate-app://app' })
  check('WS：伪造 Host 的握手被拒', badHost.status === 421, `status=${badHost.status}`)
}

async function scenarioIsolation(shell) {
  section('M3-3：环境注入与 runtime 配置隔离（设计 §6.6 / §6.6.1）')

  const psEnv = sh('ps', ['-E', '-p', String(shell.corePid)])
  const home = envValue(psEnv, 'MATE_HOME')
  check('核心环境里有 MATE_HOME（三端共用一份数据目录）', !!home && home.startsWith('/'), home)
  check('MATE_HOME 是绝对路径且指向 .ark-mate 或其显式覆盖', !!home && home.includes('.ark-mate') || !!process.env.MATE_DESKTOP_HOME, home)
  const cache = envValue(psEnv, 'npm_config_cache')
  const prefix = envValue(psEnv, 'npm_config_prefix')
  check('npm_config_cache 落在 MATE_HOME 内（不污染 ~/.npm）', !!cache && !!home && cache.startsWith(`${home}/`), cache)
  check('npm_config_prefix 落在 MATE_HOME 内', !!prefix && !!home && prefix.startsWith(`${home}/`), prefix)

  // ② 设计 §6.6.1 的 dsh 行：变量名是实测值，用载荷自己的代码回话
  const nodeBin = join(DESKTOP, 'resources', 'runtime', 'bin', 'node')
  const homePaths = payloadFile('dsh', 'node_modules', '@deepseek-ai', 'dsh-home-paths', 'lib', 'index.js')
  const electron = envValue(psEnv, 'MATE_NODE_EXECUTABLE')
  if (!homePaths) {
    check('载荷里有 dsh-home-paths（`make desktop/payloads`）', false, '载荷缺失')
  } else {
    const probe = spawnSync(
      nodeBin,
      ['-e', `const m=require(process.argv[1]); process.stdout.write([m.dshHomePath(), m.dshHomePath('cache')].join('\\n'))`, homePaths],
      {
        encoding: 'utf8',
        env: {
          PATH: `${join(DESKTOP, 'resources', 'runtime', 'bin')}:/usr/bin:/bin`,
          MATE_NODE_EXECUTABLE: electron ?? '',
          // 与隔离面同源：核心注入 DSH_HOME（Go 侧 IsolationEnv），私有 dsh 启动器另有同值兜底。
          // 这里给的就是"核心会给"的那个值——验的是设计 §6.6.1 的**变量名**有没有被载荷认。
          DSH_HOME: `${home}/runtime/dsh`,
        },
      },
    )
    const [reportedHome, reportedCache] = (probe.stdout ?? '').trim().split('\n')
    check('dsh 认 DSH_HOME（载荷自己的 dsh-home-paths 说的）', reportedHome === `${home}/runtime/dsh`, reportedHome || (probe.stderr ?? '').slice(0, 80))
    check('dsh 的 cache 落在 MATE_HOME 内', (reportedCache ?? '').startsWith(`${home}/`), reportedCache)
  }

  // ③ 用户既有配置目录：整轮验收前后零变化
  // `~/.npm` 故意不在名单里：验收脚本自己会跑 pnpm/npm，那条路径本来就会被外层工具碰。
  //
  // 一条已实测的**环境噪声**（2026-10-09）：用户自己可能正在跑 dsh（DSH 宿主会话会把
  // 会话日志/状态追加进 `~/.dsh`），验收窗口里那个目录会自己长大——文件数与 mtime 都没动、
  // 只有 KB 在变，于是"mate 碰没碰用户目录"被外部写者弄成假红。判据因此加一段：前后不等时
  // **查进程表里有没有这个目录的外部写者**（走到这里本仓的壳/核心/runtime 已全部回收）；
  // 有 → 本条**跳过**（不是放过：它此刻不可判定），没有 → 照旧判红。
  const userDirs = [join(HOME, '.dsh'), join(HOME, '.config', 'opencode')].filter((d) => existsSync(d))
  if (!userDirs.length) {
    check('用户既有配置目录存在（否则隔离断言是空的）', false, '~/.dsh 与 ~/.config/opencode 都不存在')
  }
  for (const dir of userDirs) {
    const before = SNAPSHOT.get(dir)
    const after = dirFingerprint(dir)
    const same = !!before && JSON.stringify(before) === JSON.stringify(after)
    const label = `用户既有配置未被读写：${dir.replace(HOME, '~')}`
    if (same) {
      check(label, true, `${after.files} 文件 / ${after.kb} KB`)
      continue
    }
    const writers = externalWritersOf(dir)
    if (writers.length) {
      skipped++
      console.log(`  ⏭ ${label}：跳过 — 外部写者在写该目录（${writers[0].slice(0, 100)}）；前 ${JSON.stringify(before)} → 后 ${JSON.stringify(after)}`)
      continue
    }
    check(label, false, `前 ${JSON.stringify(before)} → 后 ${JSON.stringify(after)}（进程表里没有该目录的外部写者）`)
  }
}

const SCENARIOS = ['shell', 'contract', 'hardening', 'credentials', 'stream', 'gate', 'nodechannel', 'payload', 'origin', 'isolation', 'exit', 'direct']
const argv = process.argv.slice(2).filter((a) => !a.startsWith('-'))
const want = argv.length ? argv : SCENARIOS

async function main() {
  const t0 = Date.now()
  console.log('桌面入口验收（真 Electron + 真核心）')
  // guard **总是**跑（不是可选场景）：它既做前置检查，也负责把 mate-core 与壳产物按当前代码重建。
  // 首版把它做成"只在显式选 guard 时跑"，于是单跑某个场景时用的是上一次的旧二进制——
  // 实测表现为"改了核心代码，场景却看不到改动"（查了半天才发现跑的是 13:23 的产物）。
  // 隔离断言（M3-3）的前置快照：必须早于任何会真起核心/跑会话的场景。
  for (const dir of [join(HOME, '.dsh'), join(HOME, '.config', 'opencode')]) {
    if (existsSync(dir)) SNAPSHOT.set(dir, dirFingerprint(dir))
  }
  scenarioGuard()
  if (results.some((r) => !r.ok)) {
    console.log('\n前置不通过，终止（先解决上面的问题）')
    process.exit(1)
  }

  let shell = null
  let cdp = null
  const needsShell = want.some((s) => ['shell', 'contract', 'hardening', 'credentials', 'stream', 'gate', 'nodechannel', 'payload', 'origin', 'isolation', 'exit'].includes(s))
  // V10 ③ 的前置：调用方 PATH 上先放好会写标记文件的假底座（必须在起壳前）。
  if (needsShell) setupFakeRuntimes()
  if (needsShell) {
    // V10 ① 的前提：**机器上没有 node**。壳只在 `PATH=/usr/bin:/bin` 下启动，
    // 因此核心与其子进程拿到的东西只可能来自壳自己注入的那份（设计 §6.6）。
    // 保留 HOME 是 Chromium 自己的要求（userData 要落盘），与"我们的运行时怎么找到 Node"无关。
    shell = await launchShell({ env: MINIMAL_ENV })
    if (!shell.corePid) {
      check('壳与核心起来了', false, `壳日志尾部：\n${shell.logText().split('\n').slice(-6).join('\n')}`)
      // 早退路径也要收尾：否则壳留在那儿，下次跑的前置守卫会说"已有实例在运行"
      // （首版就是这里漏了，第二次跑被单实例锁挡住）。
      await stopShellTree()
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
    if (want.includes('gate')) await scenarioGate(cdp)
    if (want.includes('nodechannel')) await scenarioNodeChannel(shell)
    if (want.includes('payload')) await scenarioPayload(cdp, shell)
    if (want.includes('origin')) await scenarioOrigin(shell)
    if (want.includes('isolation')) await scenarioIsolation(shell)
    if (want.includes('exit')) await scenarioExit(shell)
  } finally {
    cdp?.close()
    if (shell && process.env.KEEP_SHELL !== '1') await stopShellTree()
    if (FAKE_BIN) rmSync(FAKE_BIN, { recursive: true, force: true })
  }

  if (want.includes('direct')) await scenarioDirectParentGone()

  const passed = results.filter((r) => r.ok).length
  const failed = results.filter((r) => !r.ok)
  console.log(`\n——— 汇总：${passed}/${results.length} 通过（${((Date.now() - t0) / 1000).toFixed(1)}s）${skipped ? `，另有 ${skipped} 条因环境不可判定而跳过` : ''}———`)
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
