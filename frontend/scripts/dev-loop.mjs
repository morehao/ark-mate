#!/usr/bin/env node
/**
 * 开发循环：**构建一次 → 常驻 watcher 重建 → 真实宿主服务同一份构建产物**。
 *
 * **阶段表是声明出来的**（T3）：顺序、critical、超时、依赖、降级动作都在 `dev-stages.json` 里，
 * 本文件只负责**执行**那张表。分档口径与后端的启动序列（`internal/bootstrap/assembly` 的
 * `Step` / `ValidateSteps`）同形同判据：critical 必须有超时、非 critical 必须写降级动作、
 * `dependsOn` 只能指向前面的阶段。同一份 JSON 也被 archtest 读去校验（`TestDevStageTableIsDeclared`）
 * ——两边各写一份就是两个真相。
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
/** 显式覆盖那份底座可执行文件（Makefile 的 `RUNTIME_BIN`；空串 = 未指定）。 */
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

/** 阶段声明表（唯一事实源；见文件头）。 */
const STAGE_TABLE_PATH = new URL('./dev-stages.json', import.meta.url)
const stageDeaths = []
/** 已经降级的阶段（非 critical 挂了）：收尾时汇总，免得"少了一个 watcher"只留在刷过去的日志里。 */
const degradations = []
let stopping = false
let requested = null
/**
 * dev 态「命中哪一份底座」的唯一判定（P4 / 设计 D3，2026-08-11 起的最初缺口）。
 *
 * 为什么这件事必须有：dev 态的核心是按**写死的名字**（构建期固定的那个 runtime 的 `Command`）
 * 经 `exec` 走 PATH 解析，所以默认命中的是**本机装的那份**；交付态（桌面壳）用的是随包载荷。
 * 两者完全可以不同代（2026-10-10 实测：dsh 本机那份与随包那份不是一代）——而此前这件事
 * **完全静默**：回显里的底座名字写死成某个具体底座，可构建期固定的 runtime 是另一个。
 *
 * 三条纪律（都是既有的，别在这里破）：
 *   ① **不探测 PATH**（`which` / `command -v`）：与 `apps/desktop/src/payload.ts` 同一条口径——
 *      探测会让「机器上恰好装过这个底座」静默顶掉你以为在跑的那份。未指定就是「PATH 上解析」。
 *   ② **不跑 `<底座> --version`**：底座连 `--version` 都写自己的 home（dsh 实测过），
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
    return { ...base, source: 'path', version: 'unknown', digest: 'unknown', origin: 'PATH 上解析' }
  }

  // 覆盖**一定会生效**（2026-10-12 起）：转发 shim 按**构建期固定的 runtime** 命名（Makefile 的
  // dev/runtime-bin 生成 `<DEV_BIN_DIR>/$(RUNTIME)`），而核心正是按那个写死的名字走 PATH 解析，
  // 所以这里不再存在"填了但没人调用"那一态。
  //
  // 此前 shim 名写死某个具体底座名，固定成另一个的构建上这个覆盖是个**死文件**，于是这里得
  // 绕开来报（报成 `path` + 横幅补一句"不会生效"）——那套分流（`inert`）随根因修复整体删除。现在
  // `/meta.payloads[]` 报 `runtime-env` **就是**核心实际拉的那份，不存在"回显骗人"。
  const sourceTxt = RUNTIME_BIN_SOURCE === '' ? '显式覆盖' : `显式覆盖 [${RUNTIME_BIN_SOURCE}]`

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

/**
 * 随包载荷里**可直接当 `RUNTIME_BIN` 用**的那个入口（`<版本>` 位置让调用方按实际目录填）。
 *
 * 入口名**一律从清单读**（`payload.json` 的 `entry`），不按底座名分支、也不写死包路径：
 * 这里曾经有一个 `if (hit.name === 'opencode') return base + '/bin/opencode'` 的分支——那是
 * "dev 循环认识某个底座"的形状，接一个新底座就得回来加一条，而漏加只会给出一条指向不存在
 * 文件的提示（比不给提示更糟）。清单本来就是载荷形态的唯一事实源。
 */
function packagedBinHint(hit) {
  const base = `frontend/apps/desktop/resources/payloads/${hit.name}/<版本>`
  const v = hit.bundled[hit.bundled.length - 1]
  const entry = v ? readPayloadManifest(join(PAYLOADS_ROOT, hit.name, v))?.entry : null
  return entry ? `${base}/${entry}` : `${base}/<入口见该版本 payload.json 的 entry>`
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

  if (hit.source === 'path') {
    log('   ℹ️  跑的是 **PATH 上本机安装的那份**，与随包是否同代**无从判定**——本仓刻意不探测' +
      '（底座连 `--version` 都写自己的 home）。')
    // 显式覆盖口是**可用的**（转发 shim 名随构建期 runtime，见 Makefile 的 dev/runtime-bin）。
    // 给具体路径，不给一句口号——指一条走不通的路比不提示更糟（P4 记录）。
    log(`       要跑随包那一代：RUNTIME_BIN=${packagedBinHint(hit)}`)
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
    const why = signal ? `被信号 ${signal} 终止` : `自行退出（退出码 ${code}）`
    // critical 与非 critical 的分档与后端启动序列同一套：前者拖垮整个循环，后者如实降级。
    if (opts.critical === true) fail(name, why)
    else degrade(name, why)
  })
  stages.set(name, { child, longLived: opts.longLived === true, critical: opts.critical === true })
  if (opts.longLived) log(`==> 阶段 ${name} 已起（pid ${child.pid}）`)
  return child
}

/**
 * 一次性阶段：等它跑完，非 0 直接抛；给了 `timeoutMs` 就**有上界**（超时按进程组杀掉再抛）。
 *
 * 上界不是可选项：一次 `tsc -b && vite build` 挂住时，没有超时的开发循环会永远停在那一行
 * ——既没有产物也没有报错，而人只会以为"还在编译"。
 */
function runOnce(name, command, args, opts = {}) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(command, args, {
      cwd: opts.cwd ?? FRONTEND_ROOT,
      env: process.env,
      stdio: 'inherit',
      detached: true,
    })
    let timer = null
    if (opts.timeoutMs > 0) {
      timer = setTimeout(() => {
        signalGroup(child, 'SIGKILL')
        rejectPromise(new Error(`${name} 超过 ${Math.round(opts.timeoutMs / 1000)}s 未完成（已杀掉进程组）`))
      }, opts.timeoutMs)
    }
    const settle = (fn) => (arg) => {
      if (timer !== null) clearTimeout(timer)
      fn(arg)
    }
    child.on('error', settle((err) => rejectPromise(new Error(`${name}: ${err.message}`))))
    child.on('exit', settle((code) => {
      if (code === 0) resolvePromise()
      else rejectPromise(new Error(`${name} 失败（退出码 ${code}）`))
    }))
  })
}

/** 常驻阶段的**就绪窗口**：起起来之后 `windowMs` 内没退出才算起来了。 */
async function waitAlive(name, windowMs) {
  const stage = stages.get(name)
  const deadline = Date.now() + windowMs
  while (Date.now() < deadline) {
    if (!stage || stage.child.exitCode !== null || stage.child.signalCode !== null) return false
    await sleep(100)
  }
  return !(stage && (stage.child.exitCode !== null || stage.child.signalCode !== null))
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

/**
 * 非 critical 阶段挂了：**不拖垮循环**，但要留下一条显式的降级（判据在 `dev-stages.json` 的
 * degrade 里：这一步没了之后开发循环长什么样，只该有一个答案）。
 */
function degrade(name, why) {
  const declared = (stageTable.stages.find((st) => st.name === name) ?? {}).degrade
  // 同一阶段只记一次：exit 钩子与就绪窗口可能先后发现同一件事（"它死了"），
  // 报两遍会让"到底降级了几件事"读不清。
  if (degradations.some((d) => d.name === name)) return
  degradations.push({ name, why })
  log('')
  log(`==> 降级：阶段 ${name} ${why}。`)
  log(`    ${declared === undefined || declared === '' ? '（这一步没声明降级动作——阶段表验证本该拦住它）' : declared}`)
  log('    其余阶段继续跑；`make dev` 不必重跑。')
}

async function finish(code) {
  stopping = true
  await stopAll()
  if (degradations.length > 0) {
    log('')
    log(`==> 本次开发循环有 ${degradations.length} 个阶段降级：${degradations.map((d) => `${d.name}（${d.why}）`).join('；')}`)
  }
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

/**
 * 读阶段声明表并**校验**它（fail-closed：表不存在、字段缺失、依赖指错、critical 没超时、
 * 非 critical 没降级 —— 一律当场退出 2）。
 *
 * 判据与后端启动序列的 `assembly.ValidateSteps` 同形同规则，理由也一样：这些错法都不会让任何
 * 测试变红，只会让"少了一个 watcher"看起来像"一切都好"。
 */
function loadStageTable(url) {
  const raw = readFileSync(url, 'utf8')
  const table = JSON.parse(raw)
  const stages = Array.isArray(table.stages) ? table.stages : []
  if (stages.length === 0) throw new Error('阶段表为空：这张表就是开发循环的唯一事实源')
  const index = new Map()
  for (const [i, st] of stages.entries()) {
    if (typeof st.name !== 'string' || st.name.trim() === '') throw new Error(`第 ${i + 1} 个阶段没有名字`)
    if (index.has(st.name)) throw new Error(`阶段名 ${st.name} 重复`)
    index.set(st.name, i)
    if (st.kind !== 'once' && st.kind !== 'daemon') throw new Error(`阶段 ${st.name} 的 kind=${st.kind} 非法（once|daemon）`)
    if (typeof st.timeoutMs !== 'number' || st.timeoutMs <= 0) throw new Error(`阶段 ${st.name} 没有超时（timeoutMs）`)
    if (st.critical !== true && (typeof st.degrade !== 'string' || st.degrade.trim() === '')) {
      throw new Error(`非 critical 阶段 ${st.name} 没有声明降级动作（degrade）`)
    }
    if (st.readyProbe !== undefined && st.readyProbe !== 'http-health') {
      throw new Error(`阶段 ${st.name} 的 readyProbe=${st.readyProbe} 非法（http-health）`)
    }
    if (!Array.isArray(st.dependsOn)) throw new Error(`阶段 ${st.name} 的 dependsOn 必须是数组`)
  }
  for (const [i, st] of stages.entries()) {
    for (const dep of st.dependsOn) {
      if (!index.has(dep)) throw new Error(`阶段 ${st.name} 依赖 ${dep}，但表里没有这一步`)
      if (index.get(dep) >= i) throw new Error(`阶段 ${st.name} 依赖 ${dep}，而后者排在它后面（依赖只能指向前面的阶段）`)
    }
  }
  return { stages }
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

// ——— 阶段表：读 + 校验（fail-closed；表不自洽就别起任何东西）———
let stageTable
try {
  stageTable = loadStageTable(STAGE_TABLE_PATH)
} catch (err) {
  log(`==> 阶段表 ${fileURLToPath(STAGE_TABLE_PATH)} 不自洽：${err.message}`)
  process.exit(2)
}

log('==> 开发循环  唯一资产路径：vite build --watch 改写产物，核心服务同一份产物')
log(`   产物  ${WEB_DIR}`)
log(`   打开  ${APP_URL}   （不要另起 vite dev server：apps/web/vite.config.ts 会在 serve 上直接报错）`)
const runtimeHit = reportRuntimeHit(resolveRuntimeHit())

// 命令行选择：`--skip-build` / `--no-serve` 只是把对应阶段标成"用户显式跳过"，
// 依赖它的阶段据此判断（跳过 ≠ 失败：这是用户的选择，但要**说出来**）。
const skipped = new Set()
if (!options.build) skipped.add('build')
if (!options.serve) skipped.add('serve')

/**
 * 阶段 → 启动配方。**声明在 JSON 里，配方在这里**：命令与参数依赖运行时事实（产物目录、
 * ldflags、命中的底座），塞进声明表只会让那一份事实源带上环境变量。两边的对应关系由
 * archtest 双向钉住（表里的阶段必须有配方、配方必须有登记），漏一个就红。
 */
const recipes = {
  build: (st) =>
    runOnce('build', 'pnpm', ['run', 'build'], { cwd: WEB_APP, timeoutMs: st.timeoutMs }),
  types: (st) =>
    // 两个 watcher 都复用阶段 1 的产物树：增量，但不会替你补一棵缺失的树。
    launch('types', 'pnpm', ['exec', 'tsc', '-b', '--watch', '--preserveWatchOutput'],
      { cwd: WEB_APP, longLived: true, critical: st.critical }),
  assets: (st) =>
    launch('assets', 'pnpm', ['exec', 'vite', 'build', '--watch', '--no-emptyOutDir'],
      { cwd: WEB_APP, longLived: true, critical: st.critical }),
  serve: () => {
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
    // 核心在 `--web-dir` 目录没有 index.html 时**拒绝启动（退出码 3）**：这正是 `build` 阶段
    // 必须先跑完的原因（这条依赖是**声明**出来的，见 dev-stages.json 的 dependsOn）。
    return launch('serve', 'go', coreArgs, { cwd: CORE_DIR, longLived: true, critical: true })
  },
}

// 按表执行（顺序 = 声明顺序；每条依赖都指向前面，所以单趟就够）。
const started = new Set()
for (const st of stageTable.stages) {
  if (skipped.has(st.name)) {
    log(`==> 跳过阶段 ${st.name}（命令行的选择，不是失败）`)
    continue
  }
  const recipe = recipes[st.name]
  if (typeof recipe !== 'function') {
    log(`==> 阶段表里的 ${st.name} 没有启动配方（recipes 与 dev-stages.json 对不上）`)
    await finish(1)
  }
  const missing = st.dependsOn.filter((dep) => !started.has(dep) && !skipped.has(dep))
  if (missing.length > 0) {
    const why = `依赖的 ${missing.join('、')} 没有起来`
    if (st.critical) {
      log(`==> 阶段 ${st.name} ${why}：critical 步骤不能跳过它的依赖`)
      await finish(1)
    }
    degrade(st.name, why)
    continue
  }

  log(`==> 阶段 ${st.name}（${st.kind}${st.critical ? '，critical' : ''}）`)
  try {
    await recipe(st)
  } catch (err) {
    if (st.critical) {
      log(`==> ${err.message}`)
      await finish(1)
    }
    degrade(st.name, err.message)
    continue
  }

  // 就绪判据：常驻阶段要么过完整健康检查，要么过就绪窗口（起起来后不能立刻死）。
  if (st.readyProbe === 'http-health') {
    const healthy = await waitHealthy(st.name)
    if (stopping) await finish(1)
    if (!healthy) {
      log('==> 后端没能就绪，收尾。请查看上方核心输出（`--web-dir` 缺 index.html 会以退出码 3 拒启）。')
      await finish(1)
    }
    log(`==> 就绪：${APP_URL}`)
  } else if (st.kind === 'daemon') {
    if (!(await waitAlive(st.name, st.timeoutMs))) {
      const why = `起起来后 ${st.timeoutMs}ms 内就退出了`
      if (st.critical) {
        log(`==> 阶段 ${st.name} ${why}`)
        await finish(1)
      }
      degrade(st.name, why)
      continue
    }
  }
  started.add(st.name)
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
