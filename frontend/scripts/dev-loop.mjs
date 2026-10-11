#!/usr/bin/env node
/**
 * 开发循环：**构建一次 → 常驻 watcher 重建 → 真实宿主服务同一份构建产物**。
 *
 * 阶段顺序（对齐参照实现 DSH 的 `scripts/dev-web.ts`，那里给了同样的三段式与理由）：
 *   1. `pnpm run build`（`--skip-build` 可跳过）：**必须先跑完**——它与阶段 2 写的是同一棵 `dist/`；
 *   2. 两个常驻 watcher：`tsc -b --watch`（类型）与 `vite build --watch --no-emptyOutDir`（改写 dist）；
 *   3. 核心 `serve --web-dir <dist> --asset-watch 500ms`：它服务的就是阶段 2 那份产物，
 *      并在产物**内容真的变了**时通知页面刷新（P6-A），浏览器开 `:10001`。
 *
 * 为什么不再用 vite dev server（这个文件存在的全部理由）：
 *   dev server 从**源码现编译**，与交付的 `apps/web/dist` 不是同一次构建——人判断"有没有问题"时
 *   所依据的资产，因此与用户加载的不是同一份：CSP 三条限制不会被下发、`build-id.txt` 不存在（版本
 *   闸门只能判"不可判定"）、antd 会多打 `css-dev-only-*` 类名、React 的开发期警告还在。
 *   人看的必须是产物；否则其它一切措施只是让漂移**可见**，而不是**消除**。
 *   `apps/web/vite.config.ts` 已在 `serve` 上直接报错兜住这条路（照抄 DSH 的 `rejectStandaloneServe`）。
 *
 * fail-loud：DSH 点名过一个静默形态——"A missing watcher does not fail, it silently shows the
 * previous artifact, so an edit appears to do nothing"。所以**任一阶段自己退出即停掉全部、以 1
 * 退出，并打印是哪一阶段死的**；不许出现"前端已经死了、循环还活着"这种看起来正常的假活。
 *
 * **父进程看护**（2026-10-11 实测加的一层）：上面收尾全靠"信号到达本进程"，而 `make dev` 里本进程是
 * make 的子进程。实测"父死"有**两种**：**`kill -TERM make` → make 会把信号转发下来**（于是这条看护
 * 是冗余的）；**`kill -9 make` → 没有任何信号会到本进程**，本进程被过继给 launchd、继续端着 :10001
 * ——那正是另一种假活：用户以为停了，下一次 `make dev` 撞端口，或者浏览器还在跟一个"已经没有主人
 * 的"老核说话。所以每 2 s 查一次 `process.ppid`：一旦变成 1（父死被过继），按 fail-loud 收尾。
 *
 * 用法：`node scripts/dev-loop.mjs [--skip-build] [--no-serve]`
 *   --skip-build  复用上一次完整构建的产物树（两个 watcher 都是增量，不会替你补一棵缺失的树）
 *   --no-serve    只跑 watcher（核心在别处起）
 *
 * **不要与 `pnpm build` / `make web` 并行**：它们与阶段 2 写同一棵 `dist/`。
 */
import { spawn } from 'node:child_process'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, join, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const FRONTEND_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const WEB_APP = join(FRONTEND_ROOT, 'apps', 'web')

/** 产物目录：与 `--web-dir` 指的是同一个（Makefile 传进来，独立运行时有默认）。 */
const WEB_DIR = process.env.MATE_DEV_WEB_DIR ?? join(WEB_APP, 'dist')
/** 核心源码目录（`go run` 的 CWD）。 */
const CORE_DIR = process.env.MATE_DEV_CORE_DIR ?? resolve(FRONTEND_ROOT, '..', 'backend')
/** `-ldflags` 的**内层**字符串（Makefile 的 `CORE_LDFLAG_ARGS`）。空串表示不注入。 */
const CORE_LDFLAG_ARGS = (process.env.MATE_DEV_CORE_LDFLAG_ARGS ?? '').trim()
const HEALTH_URL = process.env.MATE_DEV_HEALTH_URL ?? 'http://127.0.0.1:10001/v1/mate/health'
const APP_URL = process.env.MATE_DEV_APP_URL ?? 'http://127.0.0.1:10001'
const RUNTIME = process.env.MATE_DEV_RUNTIME ?? ''
/** 显式覆盖那份底座可执行文件（Makefile 的 `OPENCODE_BIN`；空串 = 未指定）。 */
const RUNTIME_BIN = (process.env.MATE_DEV_RUNTIME_BIN ?? '').trim()
/** 上面那个值的来源：`命令行` / `环境变量` / `.env` / `未指定`（Makefile 的 `$(origin)` 取得）。 */
const RUNTIME_BIN_SOURCE = (process.env.MATE_DEV_RUNTIME_BIN_SOURCE ?? '').trim()
/** 随包运行时载荷的根（`resources/payloads/<name>/<ver>/`），只读。 */
const PAYLOADS_ROOT = process.env.MATE_DEV_PAYLOADS_ROOT ?? join(FRONTEND_ROOT, 'apps', 'desktop', 'resources', 'payloads')

/** 健康检查与收尾的时间预算（与 Makefile 原先的 300×0.3s 一致）。 */
const HEALTH_ATTEMPTS = Number(process.env.MATE_DEV_HEALTH_ATTEMPTS ?? 300)
const HEALTH_INTERVAL_MS = 300
/** 收到信号后先 SIGTERM、再 SIGKILL 的宽限。 */
const STOP_GRACE_MS = 4000
/** 子进程退出的巡逻间隔。 */
const WATCH_INTERVAL_MS = 200
/** 父进程看护间隔：`make` 被杀而信号没到我们身上时，靠它发现"自己已经没有主人了"。 */
const ORPHAN_CHECK_MS = 2000

const stageDeaths = []
let stopping = false
let requested = null
/**
 * dev 态「命中哪一份底座」的唯一判定（P4 / 设计 D3，2026-08-11 起的最初缺口）。
 *
 * 为什么这件事必须有：dev 态的核心是按**写死的名字**（`opencode` / `dsh`）经 `exec` 走 PATH 解析，
 * 所以默认命中的是**本机装的那份**；交付态（桌面壳）用的是随包载荷。两者完全可以不同代
 * （2026-10-10 实测：本机 opencode 1.18.35 / 随包 2.0.26）——而此前这件事**完全静默**：回显里的
 * 底座名字写死成 `opencode`，可构建期固定的 runtime 早就是 `dsh` 了。
 *
 * 三条纪律（都是既有的，别在这里破）：
 *   ① **不探测 PATH**（`which` / `command -v`）：与 `apps/desktop/src/payload.ts` 同一条口径——
 *      探测会让「机器上恰好装过这个底座」静默顶掉你以为在跑的那份。未指定就是「PATH 上解析」。
 *   ② **不跑 `<底座> --version`**：底座连 `--version` 都写自己的 home（dsh / opencode 都实测过），
 *      而 dev 态那份是**用户本机**的安装——探一下就是动用户的环境（本机隔离纪律）。
 *      于是版本**只在能从路径证明时**给出：路径落在 `resources/payloads/<name>/<ver>/` 内，
 *      就报那份 `payload.json` 的版本；落在别处如实报「外部指定（版本未知）」。
 *   ③ 同一份事实**交给核心**（`--payloads`，`/meta.payloads[]` 的唯一来源）：于是「这次 dev 跑的是
 *      哪一份」在 `/meta` 上可查、验收能比——不再只活在这一行横幅里（`source: path` 正是那份契约里
 *      为「核心在非桌面形态下从 PATH 找」预留的取值）。
 *
 * 返回 null 表示连 runtime 都不知道（`--no-serve` 或环境不完整时的独立运行）：那时什么都不报。
 */
function resolveRuntimeHit() {
  if (RUNTIME === '') return null
  const bundled = bundledPayloadVersions(RUNTIME)
  const base = { name: RUNTIME, bundled }

  if (RUNTIME_BIN === '') {
    return { ...base, source: 'path', version: 'unknown', digest: 'unknown', inert: false, origin: 'PATH 上解析' }
  }

  // 覆盖是否**真的会生效**：转发 shim 的名字写死是 `opencode`（Makefile 的 dev/opencode-bin），
  // 所以本构建固定的是别的 runtime 时，这个覆盖只是让 PATH 上多了一个没人调用的文件。
  //
  // 不生效时**如实报成 `path`**（而不是那份被忽略的覆盖）：`/meta.payloads[]` 是机器读的事实面，
  // 报 `runtime-env` 就等于把"我覆盖过了"写进诊断面，而核心实际拉的是 PATH 上那份——正是本项要消灭的
  // 那类"回显骗人"。人的那一半由 `inert` 在横幅里单独说清。
  const sourceTxt = RUNTIME_BIN_SOURCE === '' ? '显式覆盖' : `显式覆盖 [${RUNTIME_BIN_SOURCE}]`
  const effective = RUNTIME === 'opencode'
  if (!effective) {
    return {
      ...base,
      source: 'path',
      version: 'unknown',
      digest: 'unknown',
      inert: true,
      origin: `${RUNTIME_BIN} [${sourceTxt}]`,
    }
  }

  // 路径落在随包载荷里 → 版本是**可证明的**（读那份清单，不猜、不执行）
  const abs = resolve(RUNTIME_BIN)
  for (const v of bundled) {
    const dir = join(PAYLOADS_ROOT, RUNTIME, v)
    if (abs === dir || abs.startsWith(dir + sep)) {
      const m = readPayloadManifest(dir)
      return {
        ...base,
        source: 'runtime-env',
        version: m?.version ?? v,
        digest: m?.digest ?? 'unknown',
        inert: false,
        origin: `${RUNTIME_BIN} [${sourceTxt}]`,
      }
    }
  }
  return { ...base, source: 'runtime-env', version: 'external', digest: 'unknown', origin: `${RUNTIME_BIN} [${sourceTxt}]` }
}

/** 读一份载荷清单（`payload.json`）；缺了或不是合法 JSON 都返回 null（调用方如实报未知）。 */
function readPayloadManifest(dir) {
  const file = join(dir, 'payload.json')
  if (!existsSync(file)) return null
  try {
    return JSON.parse(readFileSync(file, 'utf8'))
  } catch {
    return null
  }
}

/** 仓库里这份 runtime 的随包载荷版本（可能一份都没有：载荷是几百 MB 的生成物）。 */
function bundledPayloadVersions(name) {
  const root = join(PAYLOADS_ROOT, name)
  if (!existsSync(root)) return []
  return readdirSync(root)
    .filter((v) => existsSync(join(root, v, 'payload.json')))
    .sort()
}

/** 把命中事实打成横幅（含「与随包是否同代」），返回同一份值的 `--payloads` 形状。 */
function reportRuntimeHit(hit) {
  if (hit === null) return null
  const bundledTxt = hit.bundled.length
    ? `随包 ${hit.bundled.join(' / ')}（${join('apps/desktop/resources/payloads', hit.name)}）`
    : `随包 无（仓库里没有 ${hit.name} 载荷；要它先 \`make desktop/payloads\`）`
  log(`   底座  ${hit.name}（构建期固定；改它要重新构建并重启本循环）`)
  log(`   命中  ${hit.origin}；版本 ${hit.version}`)
  log(`         ${bundledTxt}`)

  if (hit.inert) {
    log('   ⚠️  上面那个覆盖**不会生效**：转发 shim 只叫 `opencode`，而本构建固定的是 ' +
      `${hit.name}——核心照旧拉 PATH 上的 ${hit.name}。`)
  }
  if (hit.source === 'path') {
    log('   ℹ️  跑的是 **PATH 上本机安装的那份**，与随包是否同代**无从判定**——本仓刻意不探测' +
      '（底座连 `--version` 都写自己的 home）。')
    // 这个提示只对 opencode 成立（`OPENCODE_BIN` 的转发 shim 名字写死是它）；dsh 今天**没有**等价的
    // 显式覆盖口——如实说，别给一条走不通的路（那比不提示更糟，见 P4 记录）。
    log('       要跑随包那一代：' + (hit.name === 'opencode'
      ? `OPENCODE_BIN=frontend/apps/desktop/resources/payloads/${hit.name}/<版本>/bin/opencode`
      : '`dsh` 今天没有等价的覆盖口（核心按写死的名字走 PATH）'))
  } else if (hit.version === 'external') {
    log('   ℹ️  显式指定的是**仓库外**那份：版本无从判定（不探测），与随包是否同代同样无从判定。')
  } else if (hit.bundled.length > 1) {
    log(`   ⚠️  仓库里有 ${hit.bundled.length} 份 ${hit.name} 载荷（${hit.bundled.join(' / ')}）：` +
      `dev 命中 ${hit.version}，而壳按 \`resolvePayload\` 取读到的最后一份——` +
      '多于一份时"dev 的那一份"与"交付的那一份"可能不是同一份（不判红，这里只是让你看见）。')
  } else if (hit.bundled.length && !hit.bundled.includes(hit.version)) {
    // 兜底：清单里的 version 与它所在目录名不一致时（罕见但真实），不能报成"同一代"。
    log(`   ⚠️  dev 命中的是 ${hit.name} ${hit.version}，而随包目录是 ${hit.bundled.join(' / ')}——` +
      '**两代底座**：dev 会通过、交付会跑在另一代上（不判红，这里只是让你看见）。')
  } else {
    log('   ✅  命中的就是随包那一份（与交付同一代）。')
  }
  return [{ name: hit.name, version: hit.version, digest: hit.digest, source: hit.source }]
}

/** 名 → { child, longLived }；长驻阶段死亡会拖垮整个循环，一次性阶段不会。 */
const stages = new Map()

const log = (msg) => console.log(msg)
const options = parseArgs(process.argv.slice(2))

// ————————————————————————————————————————————————————————————————
// 阶段
// ————————————————————————————————————————————————————————————————

/**
 * 起一个子进程。
 *
 * `detached: true` 不是为了"后台"——是为了**给每个阶段一个独立进程组**，收尾时按组发信号。
 * 否则 `go run` 只杀掉自己，它编译出来的那个核（真正端着 10001 的那个）会活下来，
 * 下一次 `make dev` 撞端口；vite/tsc 同理。
 */
function launch(name, command, args, opts = {}) {
  const child = spawn(command, args, {
    cwd: opts.cwd ?? FRONTEND_ROOT,
    env: process.env,
    stdio: 'inherit',
    detached: true,
  })
  child.on('error', (err) => {
    log(`==> 阶段 ${name} 起不来：${err.message}`)
    fail(name, 'spawn-error')
  })
  child.on('exit', (code, signal) => {
    if (stopping) return
    fail(name, signal ? `被信号 ${signal} 终止` : `自行退出（退出码 ${code}）`)
  })
  stages.set(name, { child, longLived: opts.longLived === true })
  if (opts.longLived) log(`==> 阶段 ${name} 已起（pid ${child.pid}）`)
  return child
}

/** 一次性阶段：等它跑完，非 0 直接抛。 */
function runOnce(name, command, args, opts = {}) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(command, args, {
      cwd: opts.cwd ?? FRONTEND_ROOT,
      env: process.env,
      stdio: 'inherit',
      detached: true,
    })
    child.on('error', (err) => rejectPromise(new Error(`${name}: ${err.message}`)))
    child.on('exit', (code) => {
      if (code === 0) resolvePromise()
      else rejectPromise(new Error(`${name} 失败（退出码 ${code}）`))
    })
  })
}

/** 收尾：先给每个进程组 SIGTERM，宽限后仍在的 SIGKILL。 */
async function stopAll() {
  const alive = [...stages.values()].map((s) => s.child).filter((c) => c.exitCode === null && c.signalCode === null)
  for (const child of alive) signalGroup(child, 'SIGTERM')
  if (alive.length === 0) return
  const deadline = Date.now() + STOP_GRACE_MS
  while (Date.now() < deadline) {
    if (alive.every((c) => c.exitCode !== null || c.signalCode !== null)) return
    await sleep(100)
  }
  for (const child of alive) {
    if (child.exitCode === null && child.signalCode === null) {
      log(`==> 阶段 pid ${child.pid} 未在 ${STOP_GRACE_MS}ms 内退出，SIGKILL`)
      signalGroup(child, 'SIGKILL')
    }
  }
}

function signalGroup(child, signal) {
  if (child.pid === undefined) return
  try {
    process.kill(-child.pid, signal) // 负 pid = 整个进程组
  } catch {
    try {
      child.kill(signal)
    } catch {
      /* 已经没了 */
    }
  }
}

function fail(name, why) {
  if (stopping) return
  stopping = true
  stageDeaths.push(`${name}: ${why}`)
  log('')
  log(`==> 开发循环停下：阶段 ${name} ${why}。`)
  log('    循环不会替你重启它——这是刻意的：某个 watcher 悄悄死掉时，界面会停在上一份产物上，')
  log('    改代码看起来"没反应"，那正是最难查的一类假活。请查看上方该阶段的输出后重跑 `make dev`。')
  finish(1)
}

async function finish(code) {
  stopping = true
  await stopAll()
  process.exit(code)
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms))
}

function parseArgs(argv) {
  const out = { build: true, serve: true }
  for (const arg of argv) {
    if (arg === '--skip-build') out.build = false
    else if (arg === '--no-serve') out.serve = false
    else if (arg === '--help' || arg === '-h') {
      log('用法：node scripts/dev-loop.mjs [--skip-build] [--no-serve]')
      process.exit(0)
    } else {
      log(`未知参数：${arg}`)
      process.exit(2)
    }
  }
  return out
}

/** 轮询健康检查；核心先死则立刻失败（不等到超时才知道）。 */
async function waitHealthy(name) {
  log(`==> 等待后端健康检查 ${HEALTH_URL} ...`)
  for (let i = 0; i < HEALTH_ATTEMPTS; i += 1) {
    if (stopping) return false
    const stage = stages.get(name)
    if (stage && stage.child.exitCode !== null) return false // 死亡由 exit 钩子报，这里只需让路
    try {
      const res = await fetch(HEALTH_URL)
      if (res.ok) return true
    } catch {
      /* 还没起来 */
    }
    await sleep(HEALTH_INTERVAL_MS)
  }
  log(`==> 等待健康检查超时（约 ${Math.round((HEALTH_ATTEMPTS * HEALTH_INTERVAL_MS) / 1000)}s）`)
  return false
}

// ————————————————————————————————————————————————————————————————
// 主流程
// ————————————————————————————————————————————————————————————————

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => {
    if (requested !== null) return // 第二次 Ctrl-C 不再客气
    requested = signal
    log('')
    log(`==> 收到 ${signal}，收尾（先 SIGTERM 后 SIGKILL，按进程组）...`)
    finish(signal === 'SIGINT' ? 130 : 0)
  })
}

log('==> 开发循环  唯一资产路径：vite build --watch 改写产物，核心服务同一份产物')
log(`   产物  ${WEB_DIR}`)
log(`   打开  ${APP_URL}   （不要另起 vite dev server：apps/web/vite.config.ts 会在 serve 上直接报错）`)
const runtimeHit = reportRuntimeHit(resolveRuntimeHit())

if (options.build) {
  log('==> 阶段 build（一次性）：tsc -b && vite build')
  try {
    await runOnce('build', 'pnpm', ['run', 'build'], { cwd: WEB_APP })
  } catch (err) {
    log(`==> ${err.message}`)
    await finish(1)
  }
} else {
  log('==> 跳过一次性构建（--skip-build）：假定产物树已经完整')
}

// 两个 watcher 都在复用阶段 1 的产物树：增量，但不会替你补一棵缺失的树。
const typeArgs = ['exec', 'tsc', '-b', '--watch', '--preserveWatchOutput']
launch('types', 'pnpm', typeArgs, { cwd: WEB_APP, longLived: true })

const assetArgs = ['exec', 'vite', 'build', '--watch', '--no-emptyOutDir']
launch('assets', 'pnpm', assetArgs, { cwd: WEB_APP, longLived: true })

if (options.serve) {
  const coreArgs = ['run']
  if (CORE_LDFLAG_ARGS !== '') coreArgs.push('-ldflags', CORE_LDFLAG_ARGS)
  coreArgs.push('./cmd/mate', 'serve', '--web-dir', WEB_DIR)
  // 开发期资产重建通知（P6-A）：核心盯自己正在服务的那份 dist，内容真变了就经
  // `/dev/assets/events`（SSE）通知页面刷新——省掉"改完再手动刷一次"。
  // **只在这一处传**：`make server` 与桌面壳都不传，于是交付形态下那条路由根本不存在
  // （见 router.New 的注册点与 archtest 的开关守卫）。间隔与 DSH 的 poll 默认值一致。
  coreArgs.push('--asset-watch', '500ms')
  // 命中事实交给核心：`/meta.payloads[]` 在 dev 态因此不再恒为空数组（D3 的可见性缺口）。
  // 不传 = 与今天逐字相同（非桌面态报空数组）；`source: path` 是那份契约里为本形态预留的取值。
  if (runtimeHit !== null) coreArgs.push('--payloads', JSON.stringify(runtimeHit))
  // 核心在 `--web-dir` 目录没有 index.html 时**拒绝启动（退出码 3）**：这正是阶段 1 必须先跑完的原因。
  launch('serve', 'go', coreArgs, { cwd: CORE_DIR, longLived: true })
  const healthy = await waitHealthy('serve')
  if (stopping) await finish(1)
  if (!healthy) {
    log('==> 后端没能就绪，收尾。请查看上方核心输出（`--web-dir` 缺 index.html 会以退出码 3 拒启）。')
    await finish(1)
  }
  log(`==> 就绪：${APP_URL}`)
} else {
  log('==> 跳过核心（--no-serve）：只跑 watcher')
}

// 巡逻：长驻阶段已经由 exit 钩子接管，这里只保证"主流程不空转退出"。
await new Promise(() => {
  setInterval(() => {
    if (stopping) return
    for (const [name, stage] of stages) {
      if (stage.longLived && stage.child.exitCode !== null) fail(name, `自行退出（退出码 ${stage.child.exitCode}）`)
    }
  }, WATCH_INTERVAL_MS)

  // 父进程看护：被过继给 launchd（ppid 变 1）= make 已经死在别处，而信号没到我们身上。
  if (process.env.MATE_DEV_KEEP_ALIVE !== '1') {
    setInterval(() => {
      if (stopping) return
      if (process.ppid === 1) fail('父进程', '已退出（本进程被过继）——大概率是 `make` 被单独杀掉而信号没到这一层')
    }, ORPHAN_CHECK_MS)
  }
})
