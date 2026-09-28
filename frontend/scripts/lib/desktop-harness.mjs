/**
 * 桌面验收脚本的共用底座（M2-5 抽出）：进程定位/清场、起壳、最小 CDP 客户端。
 *
 * 为什么抽出来：`desktop-ui-acceptance.mjs` 与 `asset-parity.mjs` 都要"起一个真壳、连 CDP、
 * 抓真响应、最后**一定**收干净"。这类代码复制两份的典型后果是其中一份漏了清理，
 * 于是下一次运行被单实例锁挡住、或断言读到上一次的进程（两种都真实踩过）。
 *
 * 零依赖：CDP 用 Node 24 自带的全局 WebSocket。
 */
import { execFileSync, spawn } from 'node:child_process'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const LIB_DIR = dirname(fileURLToPath(import.meta.url)) // frontend/scripts/lib
export const SCRIPT_DIR = join(LIB_DIR, '..') // frontend/scripts
export const FRONTEND = join(SCRIPT_DIR, '..') // frontend
export const REPO = join(FRONTEND, '..') // 仓库根
export const DESKTOP = join(FRONTEND, 'apps', 'desktop') // frontend/apps/desktop
export const WEB_DIST = join(FRONTEND, 'apps', 'web', 'dist') // 构建产物
export const DESKTOP_WEB = join(DESKTOP, 'resources', 'web') // 壳与核心共用的那一份（M2-6）

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

/**
 * 进程匹配式：只认**主进程**。
 *
 * Electron 会派生一堆 Helper 子进程，它们的命令行同样含 `electron@<ver>...MacOS/`，
 * 用宽匹配会把它们当成"还有壳在跑"（而它们会随主进程一起消失）。
 */
export const SHELL_RE = /electron@44\.4\.5.*MacOS\/Electron \./
export const CORE_RE = /resources\/forge-core serve/

/** 跑一条命令并把 stdout 作为字符串返回（失败返回空串，供断言"读不到"分支使用）。 */
export const sh = (cmd, args, opts = {}) => {
  try {
    return execFileSync(cmd, args, { encoding: 'utf8', ...opts })
  } catch {
    return ''
  }
}

/** 全量进程列表（pid/ppid/command）。 */
export const psAll = () => sh('ps', ['-Ao', 'pid,ppid,command'])

/** 按正则找出匹配的 pid 列表（自动排除 grep 自身）。 */
export function pidsMatching(re) {
  return psAll()
    .split('\n')
    .filter((l) => re.test(l) && !l.includes('grep'))
    .map((l) => Number(l.trim().split(/\s+/)[0]))
}

/** 强杀本仓起的壳与核心（**只匹配本仓的路径**，不动别人的进程）。 */
export function killShellTree() {
  for (const re of [SHELL_RE, CORE_RE]) {
    for (const pid of pidsMatching(re)) {
      try {
        process.kill(pid, 'SIGKILL')
      } catch {}
    }
  }
}

/**
 * 优雅收场（默认路径）：**先杀壳，让核心自己收尾**。
 *
 * 为什么不能一律 SIGKILL：核心的 `--exit-with-parent` 会走优雅停机（关闭编排器 → 收掉 runtime
 * 子进程）。直接强杀核心会把 dsh/opencode 的 ACP 进程变成孤儿（ppid=1）——实测一次验收就漏了
 * 6 个，而它们**不会被后续断言发现**（父进程已消失，ps 里只剩一个 ppid=1 的 node）。
 *
 * 顺序：SIGKILL 壳 → 轮询等核心自行消失（预算 graceMs）→ 仍活着的核心才强杀。
 */
export async function stopShellTree({ graceMs = 3000 } = {}) {
  for (const pid of pidsMatching(SHELL_RE)) {
    try {
      process.kill(pid, 'SIGKILL')
    } catch {}
  }
  const t0 = Date.now()
  while (Date.now() - t0 < graceMs) {
    if (pidsMatching(CORE_RE).length === 0) return { graceful: true, ms: Date.now() - t0 }
    await sleep(100)
  }
  killShellTree()
  return { graceful: false, ms: Date.now() - t0 }
}

/** 把清理挂到进程级钩子上：无论怎么退出（含异常）都不留壳与核心。 */
export function installCleanupHooks(onFatal) {
  const fail = (e) => {
    console.error(`未捕获异常：${e?.stack ?? e}`)
    killShellTree()
    onFatal?.()
    process.exit(1)
  }
  process.on('uncaughtException', fail)
  process.on('unhandledRejection', fail)
  process.on('SIGINT', () => {
    killShellTree()
    process.exit(130)
  })
}

/** 最小 CDP 客户端（Node 24 全局 WebSocket；零依赖）。 */
export async function connectCDP(port, urlPrefix = 'forge-app://') {
  const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()
  const page = list.find((t) => t.type === 'page' && t.url.startsWith(urlPrefix))
  if (!page) throw new Error(`没有 ${urlPrefix} 的 page target：${JSON.stringify(list.map((t) => t.url))}`)
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
      pending.set(mid, { res })
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

/** 轮询一个页面断言直到为真。 */
export async function waitFor(cdp, expr, timeoutMs = 8000, stepMs = 300) {
  const t0 = Date.now()
  while (Date.now() - t0 < timeoutMs) {
    if (await cdp.evalJs(expr)) return true
    await sleep(stepMs)
  }
  return false
}

/**
 * 把当前代码重建一遍：forge-core（make core/desktop，顺带刷新 resources/web）与壳的
 * dist/*.cjs。**每次起壳前都要做**——跑在旧产物上是这一路验收最贵的坑（实测踩过两次）。
 */
export function rebuildAll() {
  const out = []
  try {
    execFileSync('make', ['core/desktop'], { cwd: REPO, stdio: 'ignore' })
    out.push(['forge-core + resources/web 已重建', true, ''])
  } catch (e) {
    out.push(['forge-core + resources/web 已重建', false, String(e).slice(0, 160)])
  }
  try {
    execFileSync(process.execPath, ['scripts/build.mjs'], { cwd: DESKTOP, stdio: 'ignore' })
    out.push(['壳的主进程/preload 产物已重建', true, ''])
  } catch (e) {
    out.push(['壳的主进程/preload 产物已重建', false, String(e).slice(0, 160)])
  }
  return out
}

/** 起一个真壳（真 Electron + 真核心），等核心就绪行，返回句柄。 */
export async function launchShell({ remoteDebugPort } = {}) {
  const port = remoteDebugPort ?? 9400 + Math.floor(Math.random() * 200)
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
  for (let i = 0; i < 120; i++) {
    if (logText().includes('core ready')) break
    await sleep(250)
  }
  return {
    child,
    port,
    logText,
    shellPid: pidsMatching(SHELL_RE)[0],
    corePid: pidsMatching(CORE_RE)[0],
    close: killShellTree,
  }
}
