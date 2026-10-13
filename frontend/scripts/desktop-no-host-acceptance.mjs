#!/usr/bin/env node
/**
 * 桌面「不依赖宿主机环境」验收（设计 R2 的判据面，2026-10-10）。
 *
 * ## 它判什么（一句话）
 *
 * 在**最小 `PATH`（`<诱饵目录>:/usr/bin:/bin`）+ 私有 `HOME`/`MATE_HOME`** 下，起真 Electron 壳 + 真核心 +
 * **真 dsh 子进程**，跑完「建会话 → 一轮 → 退出 → 重开 → 续聊一轮」，并断言**诱饵一次都没被调到**。
 *
 * ## 为什么值得单独一条（与既有靶子的分工）
 *
 *   1. `desktop-ui-acceptance.mjs`：有诱饵 + 最小 `PATH`，但**从不建会话**（只打壳/契约/加固/凭据/事件流/
 *      退出/载荷/来源/隔离）——"最小环境下真能跑完一轮"它看不见；
 *   2. `desktop-skill-projection-acceptance.mjs`：有真 dsh 会话 + 假 LLM，但它 `spread process.env`、
 *      **不洗 `PATH`**——"宿主环境不参与"它没判；
 *   3. 本脚本把两者**合起来**，并补上全仓此前没有的两件事：**跨重启续聊**与**"从未咨询宿主"的端到端证据**。
 *
 * 与静态那半配对：`backend/internal/archtest/desktop_host_path_test.go`（第 21 条）管"代码里**有没有**
 * 绑宿主工具这条路"，本脚本管"**今天这条路径没被走到**"。少了任何一半，结论都不成立：
 * 只静态 → 动态分支可能绕开；只动态 → 没走到的分支里可能写着 `/opt/homebrew/bin/node`。
 *
 * ## 判据口径（刻意这么定）
 *
 *   - **续聊**只看平台侧：会话还在 + 第二轮成功 + 两轮回复都落在历史里。**不断言** dsh 侧 native session id
 *     不变——ADR-4 的"可续聊性只看会话是否存在"，绑定实现细节会让判据过脆。
 *   - **假 LLM 零额度、零凭证**：它只回一句 `PROBE-REPLY-OK`。因此本脚本**触发不了权限卡/提问卡**
 *     （那要 `tool_use`），这是**已知且刻意**的边界，见计划 §5 的开放问题。
 *   - **不 spread `process.env`**：环境就是上面那几项。这正是"不依赖宿主机环境"的字面含义——
 *     连宿主 `PATH` 都不给。
 *
 * ## 前置
 *
 *   - `make desktop/payloads`（壳启动期解析随包载荷，缺了**拒启**）与 `core/desktop`；
 *     源码态脚本自己重建核心与私有启动器，只断言载荷在位。
 *   - 跑之前别让 `make dev` / `make desktop/dev` 的核同时活着（两个核心共用 `~/.ark-mate/mate.db`）。
 *
 * ## 两档靶子（同一份断言）
 *
 *   - **源码态**（缺省）：`pnpm verify:no-host` → `electron .` + `apps/desktop/resources/` 下的核心与载荷。
 *   - **打包态**（`MATE_SMOKE_APP=<abs .app>`）：`pnpm ui:accept:desktop-nohost:packaged`
 *     → 包内 hardened runtime 壳 + 包内核心 + 包内私有启动器与载荷。打包态**不重建任何东西**。
 *
 * **不进 CI**：要真 Electron + 真载荷（数百 MB，冷缓存需联网）。
 */
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join, resolve } from 'node:path'
import {
  CORE_RE,
  DESKTOP,
  REPO,
  SHELL_RE,
  connectCDP,
  installCleanupHooks,
  killShellTree,
  launchPackagedShell,
  launchShell,
  packagedProcessRes,
  pidsMatching,
  parseProcessEnv,
  sleep,
  stopPackagedShell,
  stopShellTree,
  waitFor,
} from './lib/desktop-harness.mjs'
// 诱饵目录与假 LLM 都抽在 lib 里（与 ui / skills 两条验收共用同一份，避免静默漂移）。
import { DECOY_NAMES, installDecoyBin } from './lib/decoy-bin.mjs'
import { FAKE_LLM_REPLY, startFakeLLM } from './lib/fake-llm.mjs'

const PROBE = join(REPO, '.dsh', 'tmp', 'desktop-no-host-accept')
const PROBE_HOME = join(PROBE, 'home')
const PROBE_MATE_HOME = join(PROBE_HOME, '.ark-mate')
/** 假 LLM 端口：与 skills 验收的 10078 **刻意错开**——两条脚本可能被同一次 DoD 连着跑。 */
const LLM_PORT = 10079
const FIRST_PROMPT = '第一轮：请只回一句话。'
const SECOND_PROMPT = '第二轮：同一会话，再回一句话。'

/** 打包态靶子（`MATE_SMOKE_APP=<abs .app>`，与 `pnpm smoke:desktop:packaged` 同一个开关）。 */
const PACKAGED_APP = process.env.MATE_SMOKE_APP ? resolve(process.env.MATE_SMOKE_APP) : null
/** 壳与核心共用的资产根：源码态 `apps/desktop/resources/`，打包态 `.app/Contents/Resources/`。 */
const ASSETS = PACKAGED_APP ? join(PACKAGED_APP, 'Contents', 'Resources') : join(DESKTOP, 'resources')
const PAYLOADS_DIR = join(ASSETS, 'payloads')
const MODE = PACKAGED_APP ? `打包态（${PACKAGED_APP}）` : '源码态（electron .）'

const shellRe = PACKAGED_APP ? packagedProcessRes(PACKAGED_APP).shell : SHELL_RE
const coreRe = PACKAGED_APP ? packagedProcessRes(PACKAGED_APP).core : CORE_RE
const runningShells = () => pidsMatching(shellRe)
const runningCores = () => pidsMatching(coreRe)

/** 用户真实目录（隔离断言用）：本脚本全程不该碰它们。 */
const realAgentsRoot = join(homedir(), '.agents', 'skills')
const realMateRoot = join(homedir(), '.ark-mate')
// 宿主上"另一个底座"的状态目录：**本脚本与产品都不该碰它**（哪怕本仓已不再随包那个底座——
// 用户自己的安装仍然是用户的环境，这正是"平台不写用户目录"这条硬约束的独立证据面）。
const realOpencodeState = join(homedir(), '.local', 'state', 'opencode')

// ——— 断言台账 ———
let pass = 0
let fail = 0
const section = (t) => console.log(`\n—— ${t} ——`)
const check = (label, ok, detail = '') => {
  if (ok) pass++
  else fail++
  console.log(`${ok ? '✓' : '✗'} ${label}${detail ? ` — ${detail}` : ''}`)
}

/** 读进程环境（`ps -E`；权限不足返回空对象，调用方按"读不到"处理，不假绿也不假红）。 */
function envOf(pid) {
  if (!pid) return {}
  try {
    return parseProcessEnv(execFileSync('ps', ['-E', '-p', String(pid)], { encoding: 'utf8' }))
  } catch {
    return {}
  }
}

/** 用户既有配置的指纹（M3-3 的"整轮验收前后零变化"）：只取顶层名 + mtime + 大小。 */
function fingerprint(dir) {
  try {
    return readdirSync(dir)
      .sort()
      .map((n) => {
        const st = statSync(join(dir, n))
        return `${n}:${st.size}:${st.mtimeMs}`
      })
      .join('|')
  } catch {
    return '<absent>'
  }
}

/**
 * 桌面态核心：直接重建（Go 侧改动；不拖 `make core/desktop` 的 pnpm 与 runtime-bin）。
 *
 * `CGO_ENABLED=0` 与 `make core/desktop` 一致：golib v1.32.22 起 SQLite dialect 是纯 Go 的，
 * 不需要 C 工具链（2026-10-11 之前那版驱动是 CGO 的，0 会编出运行期连不上库的 stub——
 * 本脚本当时正是为了避免它才写死 1）。
 */
function rebuildCore() {
  const out = join(DESKTOP, 'resources', 'mate-core')
  try {
    execFileSync('go', ['build', '-o', out, './cmd/mate'], {
      cwd: join(REPO, 'backend'),
      env: { ...process.env, CGO_ENABLED: '0' },
      stdio: 'ignore',
    })
    return { ok: existsSync(out) }
  } catch (e) {
    return { ok: false, detail: String(e).slice(0, 200) }
  }
}

/** 重建私有运行时启动器（`resources/runtime/bin/*`，生成物；隔离表就在它里面）。 */
function regenerateRuntimeBin() {
  try {
    execFileSync(process.execPath, ['scripts/gen-runtime-bin.mjs'], { cwd: DESKTOP, stdio: 'ignore' })
    return { ok: existsSync(join(DESKTOP, 'resources', 'runtime', 'bin', 'dsh')) }
  } catch (e) {
    return { ok: false, detail: String(e).slice(0, 200) }
  }
}

/** 清场（含异常路径）：源码态走 harness 的 killShellTree，打包态的壳要按 .app 路径再来一遍。 */
function killOurTree() {
  killShellTree()
  if (!PACKAGED_APP) return
  const res = packagedProcessRes(PACKAGED_APP)
  for (const re of [res.shell, res.core]) {
    for (const pid of pidsMatching(re)) {
      try {
        process.kill(pid, 'SIGKILL')
      } catch {}
    }
  }
}

// ——— 经壳反代说话（restToken 由壳的主进程注入，脚本拿不到也不需要）———

const apiGet = (cdp, path) =>
  cdp.evalJs(`const r = await fetch(${JSON.stringify(path)}); return await r.json()`)

const apiPost = (cdp, path, body) =>
  cdp.evalJs(`
    const r = await fetch(${JSON.stringify(path)}, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: ${JSON.stringify(JSON.stringify(body))},
    })
    return await r.json()
  `)

/** 会话终态闭集取自前端契约 `SessionStatus`（`in_progress` = 有轮次在跑，不是终态）。 */
const TERMINAL = new Set(['completed', 'cancelled', 'failed', 'interrupted'])

async function sessionStatus(cdp, id) {
  try {
    const j = await apiGet(cdp, `/v1/mate/sessions/${id}`)
    return j?.data?.status ?? 'unknown'
  } catch {
    return 'unknown'
  }
}

/** 等终态：**不写死 sleep**，否则等于把断言押在模型速度上（假 LLM 也会先发一次标题生成调用）。 */
async function waitTerminal(cdp, id, timeoutMs = 120000) {
  const t0 = Date.now()
  let last = 'unknown'
  while (Date.now() - t0 < timeoutMs) {
    last = await sessionStatus(cdp, id)
    if (TERMINAL.has(last)) return last
    await sleep(1000)
  }
  return `timeout(${last})`
}

/** 会话历史里假 LLM 的回复出现了几次（两轮都落地 = 2）。 */
async function replyEvidence(cdp, id) {
  const j = await apiGet(cdp, `/v1/mate/sessions/${id}/history?limit=1000`)
  const events = Array.isArray(j?.data?.events) ? j.data.events : []
  // wire 上事件负载字段叫 `payload`（`eventbus.Event`，与 transcript 落盘一致）；`data` 是旧名，
  // 两种都认一下以免契约改名时静默判成"没回复"。
  const text = events
    .filter((e) => e?.type === 'session.message')
    .map((e) => (e?.payload ?? e?.data ?? {}).text ?? '')
    .join('')
  return { events: events.length, text, count: text.split(FAKE_LLM_REPLY).length - 1 }
}

/** 起壳 + 连 CDP + 等前端壳标志；返回 `{ shell, cdp }`（`env` 原样传给壳，不再补宿主环境）。 */
async function launchAndConnect(env) {
  const shell = PACKAGED_APP
    ? await launchPackagedShell({ appPath: PACKAGED_APP, env })
    : await launchShell({ env })
  const cdp = await connectCDP(shell.port)
  await waitFor(cdp, "return typeof window.__MATE_DESKTOP__ === 'object'", 20000)
  return { shell, cdp }
}

async function stopShell(shell) {
  if (PACKAGED_APP) await stopPackagedShell(shell)
  else await stopShellTree()
  await sleep(500)
}

async function main() {
  // ————————————————————— 前置 —————————————————————
  section(`前置：产物在位、环境干净（${MODE}）`)
  check(
    '没有别的桌面壳/核心在跑（同一份 ~/.ark-mate 只能有一个核心）',
    runningShells().length === 0 && runningCores().length === 0,
    `shell=${runningShells().join(',') || '无'} core=${runningCores().join(',') || '无'}`,
  )
  if (fail > 0) {
    console.log('\n前置不通过，终止（先解决上面的问题）')
    return
  }

  // 只判**本仓随包的那几份**载荷：宿主上有没有别的底座与本脚本无关。
  const payloadsReady = existsSync(join(PAYLOADS_DIR, 'dsh'))
  check(
    `随包载荷在位（${PAYLOADS_DIR}）`,
    payloadsReady,
    payloadsReady ? '' : '载荷不在场：先跑 `make desktop/payloads`（壳在启动期解析随包载荷，缺了直接拒启）',
  )
  if (!PACKAGED_APP) {
    const built = rebuildCore()
    check('mate-core 已按当前代码重建', built.ok, built.detail ?? '')
    const binGen = regenerateRuntimeBin()
    check('私有运行时启动器已按当前代码重建', binGen.ok, binGen.detail ?? '')
    check(
      '壳的 dist 与 resources/web 在位',
      existsSync(join(DESKTOP, 'dist', 'main.cjs')) && existsSync(join(ASSETS, 'web', 'index.html')),
      '缺则先 `cd frontend && pnpm build` 与 `make core/desktop`',
    )
  } else {
    check(
      '包内壳/核心/载荷/启动器都在位',
      existsSync(join(ASSETS, 'mate-core')) &&
        existsSync(join(ASSETS, 'web', 'index.html')) &&
        existsSync(join(ASSETS, 'runtime', 'bin', 'dsh')) &&
        payloadsReady,
    )
  }
  if (fail > 0) {
    console.log('\n前置不通过，终止（先解决上面的问题）')
    return
  }

  rmSync(PROBE, { recursive: true, force: true })
  mkdirSync(PROBE_HOME, { recursive: true })

  // 诱饵目录：**一次装、两段共用**——标记文件跨"退出 → 重开"累积，第二段也证明没被调到。
  const decoy = installDecoyBin()
  const beforeAgents = fingerprint(realAgentsRoot)
  const beforeMate = fingerprint(realMateRoot)
  const beforeHostState = fingerprint(realOpencodeState)

  // 最小环境：**刻意不 spread `process.env`**。PATH 上只有诱饵 + 系统目录；两个凭据给假 LLM 用。
  const launchEnv = {
    PATH: decoy.path,
    HOME: PROBE_HOME,
    MATE_HOME: PROBE_MATE_HOME,
    DEEPSEEK_API_KEY: 'probe-key',
    DEEPSEEK_BASE_URL: `http://127.0.0.1:${LLM_PORT}`,
  }
  console.log(`  探针 HOME=${PROBE_HOME}`)
  console.log(`  探针 PATH=${launchEnv.PATH}`)

  const llm = await startFakeLLM({ port: LLM_PORT })
  /** 探针取证：把假 LLM 收到的请求体留档（失败时最有用的一份证据）；两段各留一次快照。 */
  const dumpLlm = () =>
    llm.captured.forEach((c, i) =>
      writeFileSync(join(PROBE, `llm-req-${String(i + 1).padStart(2, '0')}.json`), c.raw || ''),
    )
  let sessionId = null

  // ————————————————————— 第一段：建会话 + 一轮 —————————————————————
  section('第一段：最小 PATH + 诱饵下，起壳 → 建会话 → 跑完一轮')
  let shell = null
  let cdp = null
  try {
    ;({ shell, cdp } = await launchAndConnect(launchEnv))
    check(
      '壳起来了、核心打印了就绪行',
      shell.logText().includes('core ready'),
      shell.corePid ? `shell=${shell.shellPid} core=${shell.corePid}` : shell.logText().split('\n').slice(-4).join(' / '),
    )
    const coreEnv = envOf(shell.corePid)
    const envReadable = Object.keys(coreEnv).length > 0
    check(
      '核心进程环境是探针 HOME/MATE_HOME（不是用户真实目录）',
      !envReadable || (coreEnv.HOME === PROBE_HOME && coreEnv.MATE_HOME === PROBE_MATE_HOME),
      envReadable ? `HOME=${coreEnv.HOME} MATE_HOME=${coreEnv.MATE_HOME}` : 'ps -E 读不到（按"读不到"放过）',
    )
    // 注意：壳会把**自己的私有启动器目录**（`resources/runtime/bin`）前插到 PATH 上——那是设计
    // （随包启动器优先，设计 §6.5），不是"咨询宿主"。所以判据是"诱饵排在系统目录**之前**"：
    // 链上任何"探测本机 node/dsh"的写法都会先撞上诱饵，而不是 `/usr/bin` 或宿主目录。
    const pathParts = (coreEnv.PATH ?? '').split(':')
    const decoyIdx = pathParts.indexOf(decoy.binDir)
    const sysIdx = pathParts.indexOf('/usr/bin')
    check(
      '核心的 PATH 里诱饵排在系统目录之前（最小 PATH 真的传下去了）',
      !envReadable || (decoyIdx >= 0 && (sysIdx === -1 || decoyIdx < sysIdx)),
      envReadable ? `PATH=${coreEnv.PATH}` : 'ps -E 读不到（按"读不到"放过）',
    )

    const conv = await apiPost(cdp, '/v1/mate/sessions', { agent_id: 'dsh', prompt: FIRST_PROMPT })
    sessionId = conv?.data?.id ?? null
    check('经壳反代建会话成功（agent=dsh）', conv?.code === 0 && !!sessionId, JSON.stringify(conv).slice(0, 200))

    // 第一轮的终态：不写死 sleep，轮询 status。
    const firstStatus = sessionId ? await waitTerminal(cdp, sessionId) : 'no-session'
    check('第一轮终态 completed', firstStatus === 'completed', `status=${firstStatus}`)
    check(
      '假 LLM 收到了 dsh 的真实模型请求（零额度、零凭证）',
      llm.captured.some((c) => c.path.includes('/messages')),
      `收到 ${llm.captured.length} 个请求：${llm.captured.map((c) => c.path).join(', ') || '（一个都没有）'}`,
    )
    const firstEvidence = sessionId ? await replyEvidence(cdp, sessionId) : { count: 0, events: 0, text: '' }
    check(
      `会话历史里出现假 LLM 的回复（${FAKE_LLM_REPLY}）`,
      firstEvidence.count >= 1,
      `events=${firstEvidence.events} 命中 ${firstEvidence.count} 次`,
    )
    // 载荷来源：最小 PATH 下如果解析链退化成"咨询本机"，`/meta` 会报成 `path` 而不是 `bundled`
    // （设计 §6.5 的解析链是显式的；静态守卫 `desktop_payload_test.go` 只管"代码里没有探测"）。
    const meta = await apiGet(cdp, '/v1/mate/meta')
    const payloads = Array.isArray(meta?.data?.payloads) ? meta.data.payloads : []
    // **数量不写死**：这条原本写的是 `>= 2`（当时随包两份载荷），opencode 下线后随包只剩 dsh 一份，
    // 于是"清单全部命中随包"永远不成立（2026-10-13 实测：清单里只有 `dsh=bundled`，断言却要两条）。
    // 本意是"清单**非空**、且每条都真的来自随包"——"够不够"由上面那条「随包载荷在位」按构建期那个
    // 底座判，接第二个底座时两边都不必回来改这一行。
    check(
      '载荷清单全部命中随包（source=bundled）',
      payloads.length >= 1 && payloads.every((p) => p.source === 'bundled'),
      payloads.length ? payloads.map((p) => `${p.name}=${p.source}`).join(' ') : '（没有载荷清单）',
    )
  } finally {
    dumpLlm()
  }

  // ————————————————————— 退出 —————————————————————
  section('退出：壳与核心收干净')
  if (cdp) cdp.close()
  if (shell) await stopShell(shell)
  check('壳/核心全部退出，无残留', runningShells().length === 0 && runningCores().length === 0)
  check('dsh 子进程也随核心回收', pidsMatching(/--profile\s+acp/).length === 0)

  // ————————————————————— 第二段：重开 + 续聊 —————————————————————
  section('第二段：同一个 HOME/MATE_HOME 重开 → 会话仍在 → 续聊一轮')
  let shell2 = null
  let cdp2 = null
  try {
    ;({ shell: shell2, cdp: cdp2 } = await launchAndConnect(launchEnv))
    check('重开后壳与核心又起来了', shell2.logText().includes('core ready'), `shell=${shell2.shellPid} core=${shell2.corePid}`)

    const again = sessionId ? await apiGet(cdp2, `/v1/mate/sessions/${sessionId}`) : { code: -1 }
    check(
      '重开后会话仍在（同一个 id 可读）',
      again?.code === 0 && again?.data?.id === sessionId,
      `id=${sessionId} status=${again?.data?.status ?? '?'}`,
    )

    const before = llm.captured.length
    const turn2 = sessionId
      ? await apiPost(cdp2, `/v1/mate/sessions/${sessionId}/prompt`, { prompt: SECOND_PROMPT })
      : { code: -1 }
    check('续聊投递成功（POST /sessions/:id/prompt）', turn2?.code === 0, JSON.stringify(turn2).slice(0, 200))

    const secondStatus = sessionId ? await waitTerminal(cdp2, sessionId) : 'no-session'
    check('第二轮终态 completed', secondStatus === 'completed', `status=${secondStatus}`)
    check(
      '第二轮同样到了模型（新请求数 > 0）',
      llm.captured.length > before,
      `本轮新增 ${llm.captured.length - before} 个请求`,
    )
    const finalEvidence = sessionId ? await replyEvidence(cdp2, sessionId) : { count: 0, events: 0, text: '' }
    check(
      '两轮回复都落在同一个会话的历史里（续聊真落地）',
      finalEvidence.count >= 2,
      `events=${finalEvidence.events} 命中 ${finalEvidence.count} 次`,
    )
  } finally {
    if (cdp2) cdp2.close()
    if (shell2) await stopShell(shell2)
    dumpLlm()
    await llm.close()
  }

  // ————————————————————— 收尾判据 —————————————————————
  section('收尾：从未咨询宿主 + 用户目录零变化')
  const called = decoy.calledNames()
  check(
    '诱饵一次都没被调到（= 从未咨询宿主命令：' + DECOY_NAMES.join('/') + '）',
    called.length === 0,
    called.length ? `被调到的：${called.join(', ')}（标记文件 ${decoy.marker}）` : `诱饵目录 ${decoy.binDir}`,
  )
  check('壳/核心全部退出，无残留', runningShells().length === 0 && runningCores().length === 0)
  check('dsh 子进程也随核心回收', pidsMatching(/--profile\s+acp/).length === 0)
  check('用户真实 ~/.agents/skills 零变化', fingerprint(realAgentsRoot) === beforeAgents)
  check('用户真实 ~/.ark-mate 零变化', fingerprint(realMateRoot) === beforeMate)
  check(
    '用户真实 ~/.local/state/opencode 零变化',
    fingerprint(realOpencodeState) === beforeHostState,
  )

  decoy.cleanup()
}

installCleanupHooks()
// 打包态的壳不在 harness 的 `SHELL_RE` 面上，异常/中断路径得自己再兜一层（否则包内壳会留在后台）。
if (PACKAGED_APP) {
  for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
    process.on(sig, () => {
      killOurTree()
      process.exit(130)
    })
  }
}
let keepProbe = false
process.on('exit', () => {
  if (!keepProbe && !process.env.KEEP_PROBE) rmSync(PROBE, { recursive: true, force: true })
})

try {
  await main()
} catch (e) {
  console.error(`\n未捕获异常：${e?.stack ?? e}`)
  fail++
} finally {
  killOurTree()
  await sleep(300)
  console.log(`\n结果：${pass} 通过 / ${fail} 失败（靶子：${MODE}）`)
  keepProbe = fail > 0
  if (fail) console.log(`失败，探针目录保留在 ${PROBE} 便于取证`)
  process.exit(fail ? 1 : 0)
}
