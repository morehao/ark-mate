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
import { mkdtempSync, readdirSync, rmSync, statSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
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
// **不写死 Electron 版本**：M3-8 把 pin 从 44.4.5 调回 44.0.0（dsh 的原生插件做 V8 精确指纹匹配），
// 写死版本会让"残留壳检测"当场静默失效——表现为下一次运行被单实例锁挡住，
// 而日志里只有一句"已有实例在运行"（实测踩过）。
export const SHELL_RE = /electron@\d+\.\d+\.\d+.*MacOS\/Electron \./
export const CORE_RE = /resources\/mate-core serve/

/**
 * 从 **apps/desktop** 出发解析裸模块（Electron 二进制路径等）。
 *
 * 不能用本文件自己的 `import.meta.url`：本文件在 `frontend/scripts/`，而 `electron` 是
 * `apps/desktop` 的依赖，pnpm 的隔离布局下 `frontend/node_modules/electron` 并不存在。
 */
const require = createRequire(join(DESKTOP, 'package.json'))

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

/**
 * 从 `ps -E` 的输出里取一个环境变量的值。
 *
 * 为什么读真进程的环境、而不是断言我们的构造函数：构造函数对了、spawn 时忘了传，一样是错的——
 * 只有读真进程才闭环（V10 ② "在子进程内断言"的落点）。macOS 权限不足时 `ps -E` 会返回空串，
 * 调用方要按"读不到"分支处理，不能把空串当成"变量没注入"。
 */
export function envValue(text, key) {
  const m = text.match(new RegExp(`(?:^|\\s)${key}=([^\\s]*)`))
  return m ? m[1] : undefined
}

/**
 * 把 `ps -E -p <pid>` 的输出解析成环境变量表（键 → 值）。
 *
 * 口径与 {@link envValue} 一致：按空白切分、`KEY=VALUE`；`VALUE` 不能含空白（真实环境里这几个
 * 变量都是路径/枚举，含空白的环境变量本就不在名单上）。读不到（权限/进程已退）返回空对象——
 * 因此调用方要用 `Object.keys(env).length` 判"读不到"，不要用某个键是否存在来判。
 */
export function parseProcessEnv(text) {
  const out = {}
  if (!text) return out
  const re = /(?:^|\s)([A-Za-z_][A-Za-z0-9_]*)=([^\s]*)/g
  let m
  while ((m = re.exec(text)) !== null) out[m[1]] = m[2]
  return out
}

/**
 * 从壳日志里取核心**实际监听**地址（`[core] ready 127.0.0.1:<port>`）。
 *
 * 端口是内核分配的随机值，所以调用方不能硬编码：Host 白名单、Origin 矩阵、直连断言都要按它判。
 */
export function coreAddrOf(shell) {
  const m = /\[core\] ready (127\.0\.0\.1:\d+)/.exec(shell.logText())
  return m ? m[1] : ''
}

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
 * 列出一组进程的**全部后代**（按 ppid 做 BFS）。
 *
 * 用途：收场时要精确清掉"我们起的核心手里的 runtime 子进程"，而不是 `pkill -f dsh`——
 * 后者会把用户自己开的 dsh 会话一起杀掉（那是别人的进程，不是我们的残留）。
 */
function descendantsOf(rootPids) {
  const rows = psAll()
    .split('\n')
    .map((l) => l.trim().split(/\s+/))
    .filter((a) => a.length >= 2 && /^\d+$/.test(a[0]) && /^\d+$/.test(a[1]))
    .map((a) => [Number(a[0]), Number(a[1])])
  const children = new Map()
  for (const [pid, ppid] of rows) {
    if (!children.has(ppid)) children.set(ppid, [])
    children.get(ppid).push(pid)
  }
  const out = new Set()
  const queue = [...rootPids]
  while (queue.length) {
    for (const child of children.get(queue.shift()) ?? []) {
      if (!out.has(child)) {
        out.add(child)
        queue.push(child)
      }
    }
  }
  return out
}

/**
 * 优雅收场（默认路径）：**先杀壳，让核心自己收尾**。
 *
 * 为什么不能一律 SIGKILL：核心的 `--exit-with-parent` 会走优雅停机（关闭编排器 → 收掉 runtime
 * 子进程）。直接强杀核心会把底座那些 ACP 子进程变成孤儿（ppid=1）——实测一轮验收漏过 6 个，
 * 而断言看不到它们（父进程已消失）。
 *
 * 三层保障（缺一层就会漏进程）：
 *   1. 先给宽限（默认 6s）让核心自己停机——正常路径下这一步就干净了；
 *   2. 超时仍活着的核心才强杀；
 *   3. **再兜一层**：把"起手时记录下来的核心后代"里仍活着的收掉——它们只可能来自我们起的核心
 *      （不用 `pkill -f dsh`：那会误杀用户自己的 dsh 会话）。
 */
export async function stopShellTree({ graceMs = 6000 } = {}) {
  const cores = pidsMatching(CORE_RE)
  const offspring = descendantsOf(cores)
  for (const pid of pidsMatching(SHELL_RE)) {
    try {
      process.kill(pid, 'SIGKILL')
    } catch {}
  }
  const t0 = Date.now()
  let graceful = true
  while (Date.now() - t0 < graceMs) {
    if (pidsMatching(CORE_RE).length === 0) break
    await sleep(100)
  }
  if (pidsMatching(CORE_RE).length !== 0) {
    graceful = false
    killShellTree()
  }
  // 兜底：只清我们记录下来的后代
  let reaped = 0
  for (const pid of offspring) {
    try {
      process.kill(pid, 0) // 还活着才杀
      process.kill(pid, 'SIGKILL')
      reaped++
    } catch {}
  }
  return { graceful, ms: Date.now() - t0, reaped }
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
export async function connectCDP(port, urlPrefix = 'mate-app://') {
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
 * 把当前代码重建一遍：mate-core（make core/desktop，顺带刷新 resources/web）与壳的
 * dist/*.cjs。**每次起壳前都要做**——跑在旧产物上是这一路验收最贵的坑（实测踩过两次）。
 */
export function rebuildAll() {
  const out = []
  try {
    execFileSync('make', ['core/desktop'], { cwd: REPO, stdio: 'ignore' })
    out.push(['mate-core + resources/web 已重建', true, ''])
  } catch (e) {
    out.push(['mate-core + resources/web 已重建', false, String(e).slice(0, 160)])
  }
  try {
    execFileSync(process.execPath, ['scripts/build.mjs'], { cwd: DESKTOP, stdio: 'ignore' })
    out.push(['壳的主进程/preload 产物已重建', true, ''])
  } catch (e) {
    out.push(['壳的主进程/preload 产物已重建', false, String(e).slice(0, 160)])
  }
  return out
}

/**
 * 起一个真壳（真 Electron + 真核心），等核心就绪行，返回句柄。
 *
 * 两个刻意的选择：
 *   - 直接 spawn **Electron 二进制本体**（`require('electron')` 给的路径），不走 `node_modules/.bin/electron`
 *     那个 shim——shim 是 `#!/usr/bin/env node` 的脚本，PATH 上没有 node 时它自己就起不来，
 *     而 M3-2 的验收恰恰要在"机器上没有 node"的前提下起壳（设计 §6.6）。
 *   - `env` 可整体覆盖：调用方给什么就是什么（验收用它构造 `PATH=/usr/bin:/bin` 的干净起点）。
 */
export async function launchShell({ remoteDebugPort, env } = {}) {
  const port = remoteDebugPort ?? 9400 + Math.floor(Math.random() * 200)
  const log = []
  const electron = require('electron')
  const child = spawn(electron, ['.', `--remote-debugging-port=${port}`], {
    cwd: DESKTOP,
    env: env ?? { ...process.env, ELECTRON_RUN_AS_NODE: undefined },
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

// ————————————————— 打包态（.app）的壳：M4-2a 的"加固运行时下包内执行"用 —————————————————
//
// 与 `launchShell` 的唯一区别是"壳从哪来"：那边是仓库源码（`electron .`），这里是**打出来的包**
// （`<app>/Contents/MacOS/<productName>`）。为什么要专门有一条：设计 §6.3 的回退条件 ② 问的不是
// "源码态能不能跑"，而是"**签名 + hardened runtime 之后的包**能不能跑包内的载荷"——只有真起包里
// 那份可执行文件才能回答。

/** 打包态壳/核心的进程匹配（核心路径在 `.app/Contents/Resources/mate-core`）。 */
export function packagedProcessRes(appPath) {
  const macos = join(appPath, 'Contents', 'MacOS')
  const core = join(appPath, 'Contents', 'Resources', 'mate-core')
  const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return { shell: new RegExp(`${esc(macos)}/`), core: new RegExp(`${esc(core)} serve`) }
}

/** 打包态壳的主可执行文件（`Contents/MacOS/<productName>`；按"可执行"挑，不猜名字）。 */
export function packagedExecutable(appPath) {
  const dir = join(appPath, 'Contents', 'MacOS')
  const found = readdirSync(dir)
    .map((n) => join(dir, n))
    .find((abs) => {
      try {
        return statSync(abs).isFile() && (statSync(abs).mode & 0o111) !== 0
      } catch {
        return false
      }
    })
  if (!found) throw new Error(`${dir} 里没有可执行文件`)
  return found
}

/**
 * 起一个**打包态**的壳（真 .app，hardened runtime 已生效）。
 *
 * `--user-data-dir` 指到临时目录：打包态与开发态共用同一个 appId，单实例锁按 userData 判，
 * 不隔离就会因为"开发态壳在跑"直接退出（症状是"壳起来了但没有 core ready"，极难查）。
 */
export async function launchPackagedShell({ appPath, remoteDebugPort, env, userDataDir } = {}) {
  const port = remoteDebugPort ?? 9600 + Math.floor(Math.random() * 200)
  const userData = userDataDir ?? mkdtempSync(join(tmpdir(), 'mate-packaged-userdata-'))
  const exe = packagedExecutable(appPath)
  const log = []
  const child = spawn(exe, [`--remote-debugging-port=${port}`, `--user-data-dir=${userData}`], {
    env: env ?? { ...process.env, ELECTRON_RUN_AS_NODE: undefined },
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
  const res = packagedProcessRes(appPath)
  return {
    child,
    port,
    logText,
    userData,
    appPath,
    shellPid: child.pid,
    corePid: pidsMatching(res.core)[0],
  }
}

/**
 * 收掉打包态的壳：先 SIGKILL 壳（核心的 `--exit-with-parent` 会带它一起走），
 * 等核心消失，最后兜底把壳的整棵后代树收掉。
 */
export async function stopPackagedShell(shell, { graceMs = 6000 } = {}) {
  const res = packagedProcessRes(shell.appPath)
  const cores = pidsMatching(res.core)
  const offspring = descendantsOf(cores)
  try {
    process.kill(-shell.child.pid, 'SIGKILL')
  } catch {
    try {
      shell.child.kill('SIGKILL')
    } catch {}
  }
  const t0 = Date.now()
  let graceful = true
  while (Date.now() - t0 < graceMs) {
    if (pidsMatching(res.core).length === 0) break
    await sleep(100)
  }
  if (pidsMatching(res.core).length !== 0) {
    graceful = false
    for (const pid of pidsMatching(res.core)) {
      try {
        process.kill(pid, 'SIGKILL')
      } catch {}
    }
  }
  let reaped = 0
  for (const pid of offspring) {
    try {
      process.kill(pid, 0)
      process.kill(pid, 'SIGKILL')
      reaped++
    } catch {}
  }
  rmSync(shell.userData, { recursive: true, force: true })
  return { graceful, ms: Date.now() - t0, reaped }
}
