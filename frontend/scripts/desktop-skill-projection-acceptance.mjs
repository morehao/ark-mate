#!/usr/bin/env node
/**
 * 桌面态 **skill 投放**验收（真 Electron 壳 + 真 mate-core + 真 dsh 子进程）。
 *
 * 为什么单独一条：`desktop-ui-acceptance.mjs` 打的是壳/契约/加固/凭据/流/退出六个场景，
 * **不含能力投放**——skill 到底有没有落到底座真正读的那个根，它一概看不见。而这条链恰恰
 * 有个"平台自报一切正常、底座什么都没收到"的失效形态（2026-10-09 实测踩到两次），
 * 只能用"真底座 + 真请求体"验，不能靠单测：两次缺陷都在单测全绿的情况下存在。
 *
 * 验两条（都来自 2026-10-09 的实测）：
 *   A `fix(runtime)` 配置隔离**不注入** `DSH_AGENTS_HOME` ⇒ dsh 的读根 == 平台写根
 *     （`${HOME}/.agents/skills`）；若注入，底座会静默去读 `<MATE_HOME>/runtime/dsh/agents/skills`。
 *   B `fix(capability)` 物化**自建投影根** ⇒ 全新 home 上"第一条 skill"也能落盘；
 *     此前 staging 目录建在根里面而根不存在，直接 `no such file or directory`、只留一条 WARN。
 *
 * 历史（不构成本脚本的判据）：这里原有第三条 opencode v2（2.0.26；`OPENCODE_DISABLE_EXTERNAL_SKILLS` 与
 *   `debug skill` 那套 v1 取证手段在 v2 已失效）的 skill 目录块判据——v2 只把 skill 放进模型目录块，
 *   只能看真模型请求体。**该适配已于 v18 下线**（`pkg/runtime/` 下那个 adapter、桌面随包载荷、注册表
 *   与 `builtinAgents` 里的那一行均已删除，`AllBuiltinSpecs()` 只剩 dsh），判据与取证函数一并删除。
 *
 * 隔离与代价（都是刻意的）：
 *   - 用**自己的 `HOME` 与 `MATE_HOME`**（<repo>/.dsh/tmp/desktop-skill-accept）：既不碰用户的
 *     `~/.ark-mate/mate.db`，也让 `${HOME}/.agents/skills` 从**不存在**开始（那正是 B 的前提）。
 *     脚本收尾会核对用户真实的 `~/.agents/skills` 与 `~/.ark-mate` 指纹**零变化**。
 *   - 模型端点是**共用 lib 的假 LLM**（`lib/fake-llm.mjs`，Anthropic `/v1/messages` 流式）：零额度、零凭证，
 *     只回答一句话，并把底座的真实请求体留在内存里给断言用（dsh 侧的端点由核心注入的
 *     `DEEPSEEK_BASE_URL` 指过来，密钥走 `DEEPSEEK_API_KEY`）。
 *   - 只重建 `mate-core`（Go 侧改动）。壳产物与载荷是既有生成物，脚本只断言它们在位。
 *
 * 两档靶子（同一份断言，壳从哪来不同）：
 *   - **源码态**（缺省）：壳是 `electron .`，核心是 `apps/desktop/resources/mate-core`，载荷与私有
 *     启动器取自 `apps/desktop/resources/`。
 *   - **打包态**（`MATE_SMOKE_APP=<abs .app>`，与 `pnpm smoke:desktop:packaged` 同一个开关）：
 *     壳与核心都来自**打出来的包**（ad-hoc 签名 + `--options runtime`）。为什么值得单独一档——发布
 *     形态有四件东西是源码态看不见的：① 私有运行时启动器在**打包时重新生成**进
 *     `Contents/Resources/runtime/bin/`（隔离表就在那个生成物里，源码态那份读的是另一个路径）；
 *     ② 包内核心同样从包内拉起载荷（`MATE_DSH_ENTRY` 指包内那份）；
 *     ③ hardened runtime 真的生效；④ 探针的 `HOME`/`MATE_HOME` 重定向在包内壳上同样成立。
 *     打包态**不重建任何东西**——被测的就是磁盘上那份包，重建等于把靶子换掉。
 *
 * 用法：
 *   `node scripts/desktop-skill-projection-acceptance.mjs`（源码态；`KEEP_PROBE=1` 保留探针目录）
 *   `MATE_SMOKE_APP=apps/desktop/release/mac-arm64/ark-mate.app node scripts/desktop-skill-projection-acceptance.mjs`
 *     （打包态；等价 `pnpm ui:accept:desktop-skills:packaged`，需要先 `make desktop/dist`）
 * **不进 CI**：要真 Electron + 真底座。
 */
import { execFileSync, spawn } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
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
// 假 LLM 抽在 lib 里（另一个消费方是 `desktop-no-host-acceptance.mjs`）：两份复制会静默漂移。
import { startFakeLLM } from './lib/fake-llm.mjs'

const PROBE = join(REPO, '.dsh', 'tmp', 'desktop-skill-accept')
const PROBE_HOME = join(PROBE, 'home')
const PROBE_MATE_HOME = join(PROBE_HOME, '.ark-mate')
const SKILL_ID = 'probe-desktop'
const BODY_MARK = 'DESKTOP-ONLY-MARK'
const PROJECTION_ROOT = join(PROBE_HOME, '.agents', 'skills')
const LLM_PORT = 10078

/** 打包态靶子（`MATE_SMOKE_APP=<abs .app>`，与 `pnpm smoke:desktop:packaged` 同一个开关）。
 *  设了它就**不重建任何东西**——被测的就是磁盘上那份包，重新构建等于把靶子换掉。 */
const PACKAGED_APP = process.env.MATE_SMOKE_APP ? resolve(process.env.MATE_SMOKE_APP) : null
/** 壳与核心共用的资产根：源码态 `apps/desktop/resources/`，打包态 `.app/Contents/Resources/`。
 *  私有运行时启动器（隔离表在仓库里的第二份）与随包载荷都在这下面，且**打包时是重新生成的**——
 *  这正是"源码态全绿 ≠ 发布形态全绿"的那一处，也是本脚本要能打打包态的理由。 */
const ASSETS = PACKAGED_APP ? join(PACKAGED_APP, 'Contents', 'Resources') : join(DESKTOP, 'resources')
const LAUNCHER_DIR = join(ASSETS, 'runtime', 'bin')
const PAYLOADS_DIR = join(ASSETS, 'payloads')
const MODE = PACKAGED_APP ? `打包态（${PACKAGED_APP}）` : '源码态（electron .）'

/** 本档要判的壳/核心进程匹配：打包态的壳是 `Contents/MacOS/<productName>`，不在 `SHELL_RE` 面上
 *  （核心两档都落在 `resources/mate-core serve`，所以 `CORE_RE` 通用）。 */
const shellRe = PACKAGED_APP ? packagedProcessRes(PACKAGED_APP).shell : SHELL_RE
const coreRe = PACKAGED_APP ? packagedProcessRes(PACKAGED_APP).core : CORE_RE
const runningShells = () => pidsMatching(shellRe)
const runningCores = () => pidsMatching(coreRe)

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

let pass = 0
let fail = 0
const section = (t) => console.log(`\n—— ${t} ——`)
const check = (label, ok, detail = '') => {
  if (ok) pass++
  else fail++
  console.log(`${ok ? '✓' : '✗'} ${label}${detail ? ` — ${detail}` : ''}`)
}

/** 读进程环境（`ps -E`；权限不足返回空对象，调用方按"读不到"处理）。 */
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
 * 桌面态核心：直接重建（Go 侧改动；不拖 make core/desktop 的 pnpm 与 runtime-bin）。
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

/** 从请求体里取 dsh 看到的 skill 目录块（它在**用户消息**的 <system-reminder> 里，不在 system）。 */
function availableSkillsOf(body) {
  for (const m of body?.messages ?? []) {
    const text =
      typeof m.content === 'string'
        ? m.content
        : (m.content ?? []).map((c) => c?.text ?? '').join('\n')
    const hit = /<available_skills>([\s\S]*?)<\/available_skills>/.exec(text)
    if (hit) return hit[1].trim()
  }
  return ''
}

const realAgentsRoot = join(homedir(), '.agents', 'skills')
const realMateRoot = join(homedir(), '.ark-mate')
/** 用户自己的 `~/.local/state/opencode`：**宿主环境面，不是本仓的适配面**——本仓今天不在册这个底座，
 *  这条判据的语义是"宿主机上装过什么与本产品无关"：mate 全程一个字节都不该碰它。
 *  （历史：该底座下线前，v2 的服务发现面 `service.json` 与 v1 的锁/历史都落在这儿，2026-10-09 补
 *  XDG_STATE_HOME 隔离之前这里正是会漏的地方；适配已于 v18 下线，这条宿主环境判据保留。） */
const realOpencodeState = join(homedir(), '.local', 'state', 'opencode')

async function main() {
  section('前置：清场与产物')
  console.log(`   靶子：${MODE}`)
  if (runningShells().length || runningCores().length) killOurTree()
  await sleep(500)
  check(
    '没有别的桌面壳/核心在跑（两个核心共用数据目录会让断言读到别人的内存态）',
    runningShells().length === 0 && runningCores().length === 0,
  )

  const payloadsReady = existsSync(PAYLOADS_DIR) && readdirSync(PAYLOADS_DIR).length > 0
  if (PACKAGED_APP) {
    // 打包态**一个字节都不重建**：被测的就是磁盘上这份包。这里只断言四件套齐（缺一件就说明包本身不完整，
    // 而不是"验收失败"），并把载荷版本打出来——"投放成立"这个结论必须锚在具体版本上。
    check(
      '打包态靶子是一个完整的包（壳可执行 / 包内核心 / 包内 web / 私有 runtime bin / 包内载荷）',
      existsSync(join(PACKAGED_APP, 'Contents', 'MacOS')) &&
        existsSync(join(ASSETS, 'mate-core')) &&
        existsSync(join(ASSETS, 'web', 'index.html')) &&
        existsSync(LAUNCHER_DIR) &&
        payloadsReady,
      PACKAGED_APP,
    )
    const versions = payloadsReady
      ? readdirSync(PAYLOADS_DIR).map((name) => {
          const vs = readdirSync(join(PAYLOADS_DIR, name))
          return `${name}@${vs.sort()[vs.length - 1] ?? '?'}`
        })
      : []
    check('随包载荷清单（本次判的就是这些版本）', versions.length > 0, versions.join(' / '))
  } else {
    const built = rebuildCore()
    check('mate-core 已按当前代码重建', built.ok, built.detail ?? '')
    check(
      '壳与载荷产物在位（壳 dist / resources/web / 私有 runtime bin / resources/payloads）',
      existsSync(join(DESKTOP, 'dist', 'main.cjs')) &&
        existsSync(join(ASSETS, 'web', 'index.html')) &&
        existsSync(LAUNCHER_DIR) &&
        payloadsReady,
      payloadsReady ? '' : '载荷不在场：先跑 `make desktop/payloads`（壳在启动期解析随包载荷，缺了直接拒启）',
    )
  }

  rmSync(PROBE, { recursive: true, force: true })
  mkdirSync(PROBE_HOME, { recursive: true })
  if (!PACKAGED_APP) {
    const binGen = regenerateRuntimeBin()
    check('私有运行时启动器已按当前代码重建（隔离表就在生成物里）', binGen.ok, binGen.detail ?? '')
  }
  const dshLauncherPath = join(LAUNCHER_DIR, 'dsh')
  const dshLauncher = existsSync(dshLauncherPath) ? readFileSync(dshLauncherPath, 'utf8') : ''
  check(
    `私有启动器（${PACKAGED_APP ? '包内那份生成物' : '本仓生成物'}）不再兜底 DSH_AGENTS_HOME` +
      '（它会把 dsh 的读根从平台写根上撬走）',
    dshLauncher.length > 0 && !dshLauncher.includes('DSH_AGENTS_HOME'),
    PACKAGED_APP ? dshLauncherPath : '',
  )
  check('投影根从"不存在"开始（修复 B 的前提）', !existsSync(PROJECTION_ROOT))

  const beforeAgents = fingerprint(realAgentsRoot)
  const beforeMate = fingerprint(realMateRoot)
  // 宿主环境面（不是本仓的适配面）：宿主机上装过什么与本产品无关，探针全程不该碰用户真实目录。
  const beforeHostState = fingerprint(realOpencodeState)

  const llm = await startFakeLLM({ port: LLM_PORT })
  section(`真壳 + 真核心（${MODE}）`)
  const launchEnv = {
    ...process.env,
    HOME: PROBE_HOME,
    MATE_HOME: PROBE_MATE_HOME,
    DEEPSEEK_API_KEY: 'probe-key',
    DEEPSEEK_BASE_URL: `http://127.0.0.1:${LLM_PORT}`,
    ELECTRON_RUN_AS_NODE: undefined,
  }
  // 两档只换"壳从哪来"：打包态起的是包内那份 hardened runtime 的壳（它会 spawn 包内核心、
  // 并用包内重新生成的私有启动器拉起包内载荷）。
  const shell = PACKAGED_APP
    ? await launchPackagedShell({ appPath: PACKAGED_APP, env: launchEnv })
    : await launchShell({ env: launchEnv })
  check(
    '壳起来了，核心打印了就绪行',
    shell.logText().includes('core ready'),
    PACKAGED_APP ? `包内壳 pid=${shell.shellPid} / 包内核心 pid=${shell.corePid}` : '',
  )
  const coreEnv = envOf(shell.corePid)
  const envReadable = Object.keys(coreEnv).length > 0
  check(
    '核心进程环境确实是探针 HOME/MATE_HOME（不是用户真实目录）',
    !envReadable || (coreEnv.HOME === PROBE_HOME && coreEnv.MATE_HOME === PROBE_MATE_HOME),
    envReadable ? `HOME=${coreEnv.HOME} MATE_HOME=${coreEnv.MATE_HOME}` : 'ps -E 读不到（按"读不到"放过）',
  )

  const cdp = await connectCDP(shell.port)
  await waitFor(cdp, "return typeof window.__MATE_DESKTOP__ === 'object'", 20000)

  // 构建期固定的底座（**服务端事实**，不猜构建参数）：底座由构建期固定，所以本脚本必须先问一句
  // "这个构建跑的是哪个底座"，而不是假定某个 id。这份事实只用在一个地方——下面建 skill 时声明的
  // `runtimes`（投放到本构建的那个底座，而不是写死 id 字符串），并在这里如实回显。
  //
  // v19：读面从 `GET /agents`（一族路由，已删）换成 `GET /meta` 的 `runtime` 字段。
  // 判据强度不变：仍然是"跟服务端要一句事实"，仍然不写死任何底座名。
  const metaEnv = await cdp.evalJs(`
    const r = await fetch('/v1/mate/meta')
    return await r.json()
  `)
  const runtimeID = metaEnv?.data?.runtime?.id
  const registeredRuntimes = runtimeID ? [runtimeID] : []
  if (!runtimeID) {
    check('GET /meta.runtime 给出了构建期固定的底座 id', false, JSON.stringify(metaEnv).slice(0, 200))
  } else {
    console.log(`   构建期固定的底座：${runtimeID}（${metaEnv?.data?.runtime?.version ?? '版本未知'}）`)
  }

  section('修复 B：全新 home 上第一条 skill 要落盘')
  const created = await cdp.evalJs(`
    const r = await fetch('/v1/mate/skills', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: ${JSON.stringify(
        JSON.stringify({
          id: SKILL_ID,
          name: SKILL_ID,
          description: '桌面态投放探针',
          // 投放到**本构建固定**的那个底座（取自上面 `GET /meta.runtime` 的事实，不写死 id）。
          runtimes: registeredRuntimes,
          body: `# ${SKILL_ID}\n\n正文标记：${BODY_MARK}\n`,
        }),
      )},
    })
    return await r.json()
  `)
  check('经壳反代建 skill 成功（渲染进程不带凭据）', created?.code === 0, JSON.stringify(created).slice(0, 160))
  const skillPath = join(PROJECTION_ROOT, SKILL_ID, 'SKILL.md')
  const skillText = existsSync(skillPath) ? readFileSync(skillPath, 'utf8') : ''
  check(`物化落盘到 ${skillPath}`, skillText.length > 0)
  check(
    'SKILL.md 内容合规（frontmatter 的 name/description + 正文标记）',
    skillText.includes(`name: "${SKILL_ID}"`) && skillText.includes(BODY_MARK),
    skillText.replace(/\n/g, ' ⏎ ').slice(0, 120),
  )
  check('核心日志没有物化失败的 WARN', !shell.logText().includes('skill 物化跳过'))

  // 平台**自报**的物化体检：在册底座都在同一处 root 上投影，归属标记里应登记它们且状态 in_sync。
  // 这条是"平台侧"的说法，与下面那条"底座侧"的真请求体互为独立来源——两者不一致正是要抓的形态。
  const mat = await cdp.evalJs(`
    const r = await fetch('/v1/mate/skills/${SKILL_ID}/materialization')
    return await r.json()
  `)
  const matStates = mat?.data?.states ?? []
  const dshState = matStates.find((s) => (s.owners ?? []).includes('dsh') || s.runtime === 'dsh')
  check(
    '平台自报的物化体检：dsh 那一处 in_sync',
    dshState?.status === 'in_sync',
    JSON.stringify(matStates).slice(0, 240),
  )

  section('修复 A · 底座侧：平台拉起的 dsh 要能读到这条 skill')
  const conv = await cdp.evalJs(`
    const r = await fetch('/v1/mate/sessions', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: ${JSON.stringify(JSON.stringify({ prompt: '报告你可见的 skill 名称。' }))},
    })
    return await r.json()
  `)
  check('经壳反代起 dsh 会话成功', conv?.code === 0, JSON.stringify(conv).slice(0, 160))

  // 注意：dsh 会先发一次**会话标题生成**调用（`dsh-session-title-first-prompt-llm`），那一轮**没有**
  // skill 目录块——要等的是**轮次请求**（带工具表与 `<available_skills>` 的那一个）。
  let modelReq = null
  let catalogReq = null
  for (let i = 0; i < 160; i++) {
    modelReq ??= llm.captured.find((c) => c.path.includes('/messages'))
    catalogReq = llm.captured.find((c) => c.path.includes('/messages') && availableSkillsOf(c.body))
    if (catalogReq) break
    await sleep(250)
  }
  llm.captured.forEach((c, i) => writeFileSync(join(PROBE, `llm-req-${String(i + 1).padStart(2, '0')}.json`), c.raw))
  check(
    '假 LLM 收到了 dsh 的真实模型请求（零额度）',
    Boolean(modelReq),
    `收到 ${llm.captured.length} 个请求：${llm.captured.map((c) => c.path).join(', ')}`,
  )
  const skills = availableSkillsOf(catalogReq?.body ?? {})
  check(
    `dsh 轮次请求的 <available_skills> 里出现 ${SKILL_ID}`,
    skills.includes(SKILL_ID),
    skills ? skills.replace(/\n/g, ' / ') : '（没有任何请求带 skill 目录块）',
  )
  // 次证（第二来源）：底层子进程环境里**不该**有 DSH_AGENTS_HOME，且 DSH_HOME 指向探针 home。
  const dshPids = pidsMatching(/--profile\s+acp/)
  const dshEnv = envOf(dshPids[0])
  const dshEnvReadable = Object.keys(dshEnv).length > 0
  check(
    'dsh 子进程环境里没有 DSH_AGENTS_HOME（读根不被撬走）',
    !dshEnvReadable || dshEnv.DSH_AGENTS_HOME === undefined,
    dshEnvReadable
      ? `DSH_HOME=${dshEnv.DSH_HOME} DSH_AGENTS_HOME=${dshEnv.DSH_AGENTS_HOME ?? '（未注入）'}`
      : 'ps -E 读不到（以请求体那条为准）',
  )

  section('收尾')
  // 打包态的壳不在 `SHELL_RE` 面上（它是 `Contents/MacOS/<productName>`），所以走 harness 那条
  // 打包态收场：SIGKILL 壳 → 等核心自行停机（`--exit-with-parent`）→ 兜底清后代 → 删临时 userData。
  const stopped = PACKAGED_APP ? await stopPackagedShell(shell) : await stopShellTree()
  await sleep(500)
  check(
    '壳/核心全部退出，无残留',
    runningShells().length === 0 && runningCores().length === 0,
    JSON.stringify(stopped),
  )
  check('dsh 子进程也随核心回收', pidsMatching(/--profile\s+acp/).length === 0)
  check('用户真实 ~/.agents/skills 零变化', fingerprint(realAgentsRoot) === beforeAgents)
  check('用户真实 ~/.ark-mate 零变化', fingerprint(realMateRoot) === beforeMate)
  // 宿主环境面（不是本仓的适配面）：宿主机上装过什么底座与本产品无关，探针全程不该碰用户真实目录。
  check(
    '用户真实 ~/.local/state/opencode 零变化（宿主环境面：探针全程不该碰它）',
    fingerprint(realOpencodeState) === beforeHostState,
  )

  cdp.close()
  llm.server.close()
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
