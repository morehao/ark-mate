#!/usr/bin/env node
/**
 * M3-8｜V9 第一轮收口：**桌面态真实起 runtime 的 smoke**。
 *
 * ## 这个脚本回答什么
 *
 * 桌面架构的核心假设是："runtime 的父进程是**核心**（不是壳），而 runtime 用的是**壳随包带的那份
 * Node**（Electron 的 Node），机器上装没装 node 与它无关"。M1–M3 的其余验收都停在"启动器能转发、
 * `--version` 能打出来"这一层（M3-2 的 `scenarioNodeChannel`）；本脚本往下走一步：
 * 在**真壳 + 真核心**的前提下，用核心进程**实际拿到的那份环境**（`ps -E` 读出来的，不是我们猜的）
 * 经私有 bin 启动器把两份随包载荷的 ACP 模式真拉起来，各走一遍最小握手
 * （`initialize` → `session/new`）——**不需要任何模型凭据**。
 *
 * ## 与计划 §M3-8 的对应
 *
 * 计划的退出判据是"设计 §6.3 的三条回退触发条件各一个 smoke 用例；全过 → 不启用独立 Node 载荷"：
 *
 * | # | 触发条件 | 本脚本 |
 * | --- | --- | --- |
 * | ① | 某 runtime 需要**写自己所在目录**（包内只读 → 不行就物化） | **验到**：跑前/跑后对载荷目录取指纹，断言零变化（`fingerprint` 场景） |
 * | ② | 平台签名 / 公证 / hardened runtime 不允许包内执行 | **源码态不判**；**打包态判到 hardened runtime 这半边**（`MATE_SMOKE_APP=…`，M4-2a）；公证那半边本期不做（§12.27） |
 * | ③ | Windows SmartScreen / AV 拦 | **不判**：需要 Windows 测试机与出包，属 M4 |
 *
 * ②③ 的状态每次运行都会**显式打印一行**：免得读日志的人把"全绿"当成"三条件已验完"。
 *
 * ## 目录指纹口径（触发条件 ① 的取证方式）
 *
 * 为什么不是"跑一轮真会话然后看目录变没变"：真会话要模型凭据（计划明确"不进 CI"），而"往自己所在
 * 目录里写"与模型无关——**进程启动本身**就会碰那条路径（dsh 连 `--version` 都写自己的 home，M3-3
 * 实测过）。所以这里用"起进程 + 走一遍 ACP（能握到底就握到底）"当载荷**真正跑起来**的证据，用
 * **目录指纹**当"没写自己目录"的证据，两者都不需要凭据。**握手失败不允许跳过指纹**：进程起过一次
 * 就已经是对"会不会写自己目录"的有效施压（本轮 dsh 正是这种情形——它 boot 失败但仍然没写载荷目录）。
 *
 * 指纹的粒度（**不读内容**）：
 *   - `find <dir> -type f -exec stat -f '%z\t%m\t%N' {} +` 取全部文件的 (size, mtime, 路径)，
 *     按路径排序后取 sha256 作为**元数据摘要**；
 *   - 另记**文件数**与**总字节**（人读得懂的两位）；
 *   - 另记**目录自身 mtime**（"创建后立刻删除"的临时文件只会留下这一项）。
 *
 * 口径为什么够用：写入必然表现为①新增/删除文件（计数与摘要都变）或②改写文件（size 或 mtime 变）；
 * 反过来**读取不会动其中任何一项**（读不更新 mtime）。全量内容 hash 也能发现写入，但要把 ~490MB
 * 读两遍/次且收益为零。载荷规模：dsh 15164 文件 / ~345MB，opencode 3 文件 / ~145MB；
 * 一次元数据扫描实测 <1s。（`payload.json` 里的 `files` 是**闭包自身**的计数、不含那份清单；
 * 所以指纹的文件数会比它多 1——这不是不一致。）
 *
 * 已知的"载荷目录之外"的写点（**故意不在指纹范围内**，但要说清，免得被当成漏报）：
 * `node-addon-require-builtin` 会把预编译 `.node` 物化到系统临时目录
 * （`$TMPDIR/node-addon-native-custom-loader-<uid>/native-cache/`）再加载——那是 tmpdir，
 * 不是载荷目录，也正是设计 §6.3"可写面只有 MATE_HOME"允许的形状。
 *
 * ## 当前实测状态（2026-09-28，本机 Electron 44.4.5）
 *
 * - **opencode**：握手绿（`initialize` + `session/new`），载荷目录指纹零变化 → 触发条件 ① 对它是**已验**。
 * - **dsh**：**握手红**，原因**不在本脚本也不在环境**——dsh 的 profile boot 依赖原生插件
 *   `node-addon-require-builtin@0.1.6`，它对 Electron 做 **V8 版本精确指纹**匹配，只接受
 *   43.0.0 / 44.0.0 / 45.0.0-alpha.6；本仓 pin 的 **44.4.5**（V8 15.2.124.28）不在表里 → 直接抛错。
 *   本脚本会贴出原始 stderr 并打印可执行的诊断（见 `printDshBlockerDiagnosis`）。
 *   **但触发条件 ① 对 dsh 仍然取证到了**：它的进程确实起来了、确实是跑在 **Electron 的 Node
 *   （24.21.0 / V8 …-electron.0，进程内自述）** 上，而载荷目录指纹零变化。
 *   （即：dsh 的"会不会写自己所在目录"这个问题答完了；"能不能在 Electron 的 Node 上 boot"是新暴露的、
 *   §6.3 三条之外的另一条形态约束。）
 * - `dsh-diag` / `dsh-alt-node` 是**定位工具**（不计入 V9 判据）：前者把同一份 dsh 换本机 Node 起
 *   （握手绿 → 协议面没问题），后者用 `MATE_SMOKE_ALT_NODE` 指一份别处的 Electron 起
 *   （实测 **Electron 44.0.0 → 握手绿**，而设计 §5.1 记的本来就是 44.0.0）。
 *
 * ## 用法
 *
 *   node scripts/desktop-runtime-smoke.mjs            # 全部场景（默认档：不联网、不跑模型）
 *   node scripts/desktop-runtime-smoke.mjs dsh        # 只跑指定场景
 *   MATE_SMOKE_APP=/abs/ark-mate.app node scripts/desktop-runtime-smoke.mjs
 *                                                     # **打包态**（M4-2a）：起 .app 里的壳与核心，
 *                                                     # 判"签名 + hardened runtime 之后包内载荷仍能执行"（条件 ② 的 hardened runtime 半边）
 *   node scripts/desktop-runtime-smoke.mjs --help     # 本说明
 *   MATE_SMOKE_ALT_NODE=/abs/Electron node scripts/desktop-runtime-smoke.mjs dsh-alt-node
 *                                                     # 候选修复验证（换 Node 再起 dsh）
 *
 * 场景：
 *   guard        前置（**总是**跑）：单实例检查、`make core/desktop` 级重建、载荷与私有 bin 在位
 *   dsh          dsh 的 ACP 握手（私有 bin → Electron 的 Node → 随包载荷）
 *   opencode     opencode 的 ACP 握手（私有 bin → 随包原生二进制）
 *   fingerprint  触发条件 ①：两份载荷目录指纹在整轮前后零变化
 *   live         可选 LIVE 档：真发一次 `session/prompt`（要凭据，**不进 CI**）
 *   dsh-diag     诊断：同一份 dsh 载荷换**本机 Node** 起（定位阻塞点；不计入判据）
 *   dsh-alt-node 候选修复验证：换 `MATE_SMOKE_ALT_NODE` 指的 Electron 再起 dsh（不计入判据）
 *
 * 环境变量：
 *   KEEP_SHELL=1                 跑完不杀壳（人工接管用；下次跑会被 guard 挡下）
 *   RUNTIME_SMOKE_LIVE=1         开启 LIVE 档
 *   MATE_SMOKE_DSH_API_KEY      LIVE 档 dsh 的凭据（透传成子进程的 DEEPSEEK_API_KEY）
 *   MATE_SMOKE_OPENCODE_API_KEY LIVE 档 opencode 的凭据（透传成子进程的 OPENCODE_API_KEY）
 *   MATE_SMOKE_ALT_NODE         `dsh-alt-node` 用的候选 Node/Electron 可执行文件绝对路径
 *   MATE_SMOKE_APP              打包态：`.app` 的绝对路径（起包内的壳与核心；缺省的源码态不判条件 ②）
 *
 * **不进 CI**：要真 Electron + 真核心 + 随包载荷（`make desktop/payloads`）。
 * 零新依赖：ACP 客户端就是 NDJSON over stdio（协议帧形状对齐
 * `backend/pkg/runtime/acp_testserver_test.go` 与 `acpsdk` 的 line-delimited JSON 约定）。
 */
import { createHash } from 'node:crypto'
import { spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

/**
 * 读出仓库**声明**的 Electron 版本（`apps/desktop/package.json` 的 devDependencies）。
 *
 * 为什么不硬编码：M3-8 实测把 pin 从 44.4.5 调回 44.0.0（dsh 的原生插件做 V8 精确指纹匹配），
 * 硬编码断言当场变成一条假红。断言"私有 bin 报的版本 == 仓库声明"才有意义。
 */
function declaredElectronVersion() {
  try {
    const pkg = JSON.parse(readFileSync(join(DESKTOP, 'package.json'), 'utf8'))
    return pkg?.devDependencies?.electron ?? ''
  } catch {
    return ''
  }
}

// 共用底座（M2-5 抽出）：进程定位/清场、起壳、按当前代码重建。
import {
  CORE_RE,
  DESKTOP,
  REPO,
  SHELL_RE,
  coreAddrOf,
  installCleanupHooks,
  launchPackagedShell,
  launchShell,
  packagedProcessRes,
  parseProcessEnv,
  pidsMatching,
  psAll,
  rebuildAll,
  sh,
  sleep,
  stopPackagedShell,
  stopShellTree,
} from './lib/desktop-harness.mjs'

/** 用户家目录（起壳的最小环境要保留它：Chromium 的 userData 要落盘）。 */
const HOME = homedir()
/**
 * 打包态（M4-2a）：`MATE_SMOKE_APP=/abs/path/ark-mate.app` → 起来的是**打出来的包**里的壳与核心。
 *
 * 这是设计 §6.3 回退条件 ② 的唯一判据形态：源码态的壳没有签名、也没有 hardened runtime，
 * 只有真包（ad-hoc 签名 + `--options runtime`）才能回答"平台允不允许执行包内载荷"。
 */
const PACKAGED_APP = process.env.MATE_SMOKE_APP ? resolve(process.env.MATE_SMOKE_APP) : null
if (PACKAGED_APP && !existsSync(join(PACKAGED_APP, 'Contents', 'Resources', 'mate-core'))) {
  console.error(`MATE_SMOKE_APP 指向的不是一个打包好的 .app：${PACKAGED_APP}`)
  process.exit(1)
}

/** 运行时树的根：未打包 = 仓库 `apps/desktop/resources`；已打包 = `.app/Contents/Resources`。 */
const RESOURCES_ROOT = PACKAGED_APP ? join(PACKAGED_APP, 'Contents', 'Resources') : join(DESKTOP, 'resources')
/** 私有 bin 目录（启动器是生成物：`apps/desktop/scripts/gen-runtime-bin.mjs`）。 */
const BIN_DIR = join(RESOURCES_ROOT, 'runtime', 'bin')
/** 随包载荷根（`payloads/<name>/<version>/…`）。 */
const PAYLOAD_ROOT = join(RESOURCES_ROOT, 'payloads')
/** 两种形态各自的壳/核心进程匹配（残留与前置检查都按它判）。 */
const PROC_RES = PACKAGED_APP ? packagedProcessRes(PACKAGED_APP) : { shell: SHELL_RE, core: CORE_RE }
/** 展示用：把绝对路径缩成可读（打包态缩 `.app` 之前那一段）。 */
const ELIDE_FROM = PACKAGED_APP ?? DESKTOP

/** 两份随包载荷。runtime id 与 `backend/pkg/runtime/{deepseekharness,opencode}` 的 ID 一致。 */
const RUNTIMES = {
  dsh: {
    label: 'dsh（deepseekharness）',
    // 形态来自 backend/pkg/runtime/deepseekharness：Command="dsh"，Args=["--profile","acp"]。
    args: ['--profile', 'acp'],
  },
  opencode: {
    label: 'opencode',
    // 形态来自 backend/pkg/runtime/opencode：Command="opencode"，Args=["acp"]。
    args: ['acp'],
  },
}

// ——— 断言台账（与 desktop-ui-acceptance.mjs 同风格）———
const results = []
function check(name, ok, detail = '') {
  results.push({ name, ok: !!ok, detail })
  console.log(`  ${ok ? '✅' : '❌'} ${name}${detail ? ` — ${detail}` : ''}`)
  return !!ok
}
const section = (t) => console.log(`\n=== ${t} ===`)
const note = (t) => console.log(`  · ${t}`)

// 无论怎么退出（含异常）都不留壳与核心、不留我们起的 runtime
installCleanupHooks()

/** 我们起的 runtime 子进程 pid（收尾时用它 + 载荷路径双条件判定残留，不动用户自己的进程）。 */
const spawnedRuntimePids = new Set()
/** 临时目录（MATE_HOME / 工作区 / 指纹缓存），收尾统一删。 */
const tempDirs = []

// ————————————————————————————— 小工具 —————————————————————————————

/**
 * 读一份随包载荷：版本目录取排序后的最后一个 + `payload.json`（`entry` 是交给运行时的入口）。
 *
 * 清单是**载荷形态的唯一事实源**（M3-1）：入口不在这里猜，脚本也不去 `which`。
 */
function readPayload(name) {
  const root = join(PAYLOAD_ROOT, name)
  if (!existsSync(root)) return undefined
  const versions = readdirSync(root)
    .filter((v) => !v.startsWith('.'))
    .sort()
  const version = versions[versions.length - 1]
  if (!version) return undefined
  const dir = join(root, version)
  const manifestPath = join(dir, 'payload.json')
  if (!existsSync(manifestPath)) return undefined
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
  return { name, version, root, dir, manifest, entry: join(dir, manifest.entry) }
}

/** 建一个临时目录并登记（收尾统一删）。 */
function makeTemp(prefix) {
  const dir = mkdtempSync(join(tmpdir(), prefix))
  tempDirs.push(dir)
  return dir
}

/**
 * 载荷目录指纹（口径见文件头）：文件数 + 总字节 + (路径,size,mtime) 摘要 + 目录自身 mtime。
 *
 * 用系统 `find`/`stat` 而不是 Node 逐个 `statSync`：15164 个文件在 Node 侧是 15164 次 syscall +
 * 一次 JS 循环；`find -exec stat {} +` 一次批量完成（实测整轮 <1s）。
 *
 * **不能走 harness 的 `sh()`**：它内部是 `execFileSync`，而默认 `maxBuffer` 只有 1MB ——
 * dsh 载荷 15164 个文件的 stat 输出约 1.8MB，会 `ENOBUFS` 然后被 `catch` 静默吞成空串，
 * 表现为"dsh 目录 = 0 文件"。而前后两次都读到 0，断言就成了**假绿**（实测踩到过）。
 * 所以这里显式给 64MB，并且**把失败当失败**（返回带 `error` 的对象，由调用方报红）。
 * 路径含换行的载荷在这套口径下不可用——npm/pnpm 生成的路径不含换行，且下面按行数计数。
 */
function payloadFingerprint(dir) {
  if (!existsSync(dir)) return undefined
  const probe = spawnSync(
    '/usr/bin/find',
    [dir, '-type', 'f', '-exec', '/usr/bin/stat', '-f', '%z\t%m\t%N', '{}', '+'],
    { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 },
  )
  if (probe.error || probe.status !== 0) {
    return { error: String(probe.error ?? `find 退出码 ${probe.status}`).slice(0, 160) }
  }
  const lines = String(probe.stdout ?? '').split('\n').filter(Boolean)
  let bytes = 0
  const meta = []
  for (const line of lines) {
    const [size, mtime, ...rest] = line.split('\t')
    const path = rest.join('\t')
    bytes += Number(size)
    meta.push(`${path}\t${size}\t${mtime}`)
  }
  meta.sort() // 稳定性：同一份内容必须得到同一个摘要（find 的顺序不保证）
  return {
    files: lines.length,
    bytes,
    digest: createHash('sha256').update(meta.join('\n')).digest('hex').slice(0, 16),
    dirMtime: String(sh('/usr/bin/stat', ['-f', '%m', dir]) ?? '').trim(),
  }
}

const fingerprintText = (f) => {
  if (!f) return '(目录不存在)'
  if (f.error) return `(取指纹失败：${f.error})`
  return `${f.files} 文件 / ${f.bytes} 字节 / ${f.digest} / dirMtime=${f.dirMtime}`
}

/**
 * 起一个 ACP server 子进程并接上最小 NDJSON 客户端（协议：line-delimited JSON-RPC 2.0）。
 *
 * 返回的 `request` 只认"带 id 且没有 method"的帧当响应（通知/反向请求落在 `notifications`）——
 * 子进程退出时把在途请求直接 reject，免得脚本卡在超时上（"进程早就死了却在等 60s"是最难查的形状）。
 */
function startAcpServer({ command, args, env, cwd }) {
  const child = spawn(command, args, { cwd, env, stdio: ['pipe', 'pipe', 'pipe'] })
  spawnedRuntimePids.add(child.pid)
  const err = []
  child.stderr.on('data', (d) => err.push(String(d)))
  const notifications = []
  const pending = new Map()
  const exited = { code: null, signal: null, at: null }
  let buf = ''
  child.stdout.on('data', (d) => {
    buf += String(d)
    let i
    while ((i = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, i).trim()
      buf = buf.slice(i + 1)
      if (!line) continue
      let msg
      try {
        msg = JSON.parse(line)
      } catch {
        notifications.push({ raw: line })
        continue
      }
      if (msg.id != null && msg.method === undefined) {
        const p = pending.get(msg.id)
        if (p) {
          pending.delete(msg.id)
          clearTimeout(p.timer)
          p.resolve(msg)
        }
      } else {
        notifications.push(msg)
      }
    }
  })
  child.on('exit', (code, signal) => {
    exited.code = code
    exited.signal = signal
    exited.at = Date.now()
    for (const [id, p] of pending) {
      clearTimeout(p.timer)
      pending.delete(id)
      p.reject(new Error(`ACP 进程在等待响应时退出（code=${code} signal=${signal}）`))
    }
  })
  let nextId = 0
  const request = (method, params, timeoutMs = 60_000) =>
    new Promise((resolve, reject) => {
      // 进程早退了就**立刻**失败：否则会白等满 60s（"进程早就死了却在等超时"是最难查的形状，
      // 实测 dsh 就是这样白等了 60s）。
      if (exited.at !== null) {
        reject(new Error(`ACP 进程已退出（code=${exited.code} signal=${exited.signal}），未发出 ${method}`))
        return
      }
      const id = ++nextId
      const timer = setTimeout(() => {
        pending.delete(id)
        reject(new Error(`ACP 请求超时（${timeoutMs}ms）：${method}`))
      }, timeoutMs)
      pending.set(id, { resolve, reject, timer })
      try {
        child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', id, method, params })}\n`)
      } catch (e) {
        pending.delete(id)
        clearTimeout(timer)
        reject(e)
      }
    })
  /**
   * 收一个 ACP server：**先关 stdin**（dsh 的帮助原文："serve ACP until the client disconnects"），
   * 给它一个体面的退出机会，超时才强杀。强杀只针对这一个 pid；孙进程由下面的残留清扫兜底。
   */
  const stop = async () => {
    try {
      child.stdin.end()
    } catch {}
    for (let i = 0; i < 30; i++) {
      if (exited.at !== null) return
      await sleep(100)
    }
    try {
      child.kill('SIGKILL')
    } catch {}
    await sleep(200)
  }
  return {
    child,
    pid: child.pid,
    request,
    stop,
    exited,
    notifications,
    stderrText: () => err.join(''),
  }
}

/** ACP initialize 参数：与 `pkg/acl` 的握手一致（protocolVersion=1 + 客户端能力/身份）。 */
const INITIALIZE_PARAMS = {
  protocolVersion: 1,
  clientCapabilities: {
    fs: { readTextFile: false, writeTextFile: false },
    terminal: false,
  },
  clientInfo: { name: 'ark-mate-desktop-runtime-smoke', version: '0.1.0' },
}

/**
 * 从**核心进程实际拿到的环境**里造出 runtime 子进程环境。
 *
 * 三条纪律：
 *  1. **PATH 前置私有 bin**（宁可重复校准也不假设）：设计 §6.6 的私有 bin 必须是 PATH 首段，
 *     否则机器上恰好装过的同名命令会顶掉随包那份；
 *  2. **载荷入口来自核心环境**（`MATE_DSH_ENTRY` / `MATE_OPENCODE_BIN` / `MATE_NODE_EXECUTABLE`
 *     / `MATE_HOME`）——本脚本不自己拼这几条路径，拼了就等于在验"我记住的那个常量"；
 *  3. **配置隔离按 runtime 叠加**（`backend/pkg/runtime/isolation.go` 的 `IsolationEnv` 逐条对齐）。
 *     为什么要在这里补：核心进程自己的环境里**没有**这些变量——它们是核心在 `Connect` 时按
 *     runtime 注入子进程的。不补的话 dsh/opencode 会去写用户的 `~/.dsh`、`~/.config/opencode`。
 */
/**
 * 探针 runtime 的**可写面**（dsh / opencode 的 home 与 npm 面都挂在它下面）。
 *
 * 刻意**不**沿用 `coreEnv.MATE_HOME`：那是壳按默认注入的**用户真实** `~/.ark-mate`，拿它当可写面
 * 会让探针往用户真实数据目录里写——2026-10-09 实测 `~/.ark-mate/runtime/dsh/sessions/` 里留下一批
 * `--var-folders-*mate-smoke-dsh-ws-*`，而载荷指纹那两条断言只看载荷目录、**看不见这里**；
 * 更隐蔽的是它还会**读**用户真实的 dsh profile / session 状态，让"干净机器上是什么样"失真。
 *
 * 载荷**入口**（`MATE_*_ENTRY` / `MATE_HOME`）仍从核心进程环境取——那是本脚本的纪律 2
 * （不自己拼常量、不 `which`）；这里只是不拿它当可写面。整轮共用一个临时 home（收尾统一删）。
 */
let smokeProbeHomeDir
function smokeProbeHome() {
  smokeProbeHomeDir ??= makeTemp('mate-smoke-home-')
  return smokeProbeHomeDir
}

function runtimeEnvOf(coreEnv, runtimeId) {
  const env = { ...coreEnv }
  const path = env.PATH ?? '/usr/bin:/bin'
  env.PATH = path.startsWith(`${BIN_DIR}:`) ? path : `${BIN_DIR}:${path}`

  const home = smokeProbeHome()
  // npm 面：所有本地 runtime 都给（MCP server 常经 npx 拉起，不隔离就写用户的 ~/.npm）。
  const isolation = {
    npm_config_cache: join(home, 'npm', 'cache'),
    npm_config_prefix: join(home, 'npm', 'global'),
  }
  if (runtimeId === 'dsh') {
    isolation.DSH_HOME = join(home, 'runtime', 'dsh')
    // 刻意**不**给 DSH_AGENTS_HOME：它是**用户全局**的 skill/agents 面（平台投影根 = `${HOME}/.agents/skills`），
    // 而 dsh 按 `config.agentsHome ?? $DSH_AGENTS_HOME ?? ~/.agents` 解析它——注入了就把 dsh 的读根
    // 从平台写根上撬走、skill 静默不投放（2026-10-09 桌面态验收实测）。核心侧（isolation.go）与
    // 桌面私有启动器（apps/desktop/scripts/gen-runtime-bin.mjs）同批修掉。
  } else {
    isolation.XDG_CONFIG_HOME = join(home, 'runtime', 'opencode', 'config')
    isolation.XDG_DATA_HOME = join(home, 'runtime', 'opencode', 'data')
    isolation.XDG_CACHE_HOME = join(home, 'runtime', 'opencode', 'cache')
    // v2 的服务发现面（`<XDG_STATE_HOME 或 ~/.local/state>/opencode/service.json`）：探针不隔离它就会
    // **读/替换用户自己的后台服务登记**——那正是"探针动了本机"（本脚本 2026-10-09 起把可写面收进临时
    // home，这一条必须跟着核心 isolation.go 的表走）。
    isolation.XDG_STATE_HOME = join(home, 'runtime', 'opencode', 'state')
    isolation.OPENCODE_CONFIG_DIR = join(home, 'runtime', 'opencode', 'config', 'opencode')
    isolation.OPENCODE_DB = join(home, 'runtime', 'opencode', 'data', 'opencode.db')
  }
  // "已有键不覆盖"（isolation.go 的 ApplyIsolationEnv 语义）：显式配置优先。
  for (const [k, v] of Object.entries(isolation)) if (env[k] === undefined) env[k] = v
  return env
}

/**
 * 私有 bin 的 `node` 报的 Node 版本 —— 这是"哪份 Node"的**可复现证据**。
 *
 * 为什么要单独探一次：ACP 握手响应里没有 Node 版本；而"runtime 跑在 Electron 的 Node 上"这条
 * 恰恰是 V10 ① 的核心。私有 bin 的 `node` 与私有 bin 的 `dsh` 走**同一个 `exec` 目标**
 * （`ELECTRON_RUN_AS_NODE=1 "$MATE_NODE_EXECUTABLE" …`），因此它报的版本就是 runtime 拿到的版本。
 */
function probeNodeChannel(env) {
  const probe = spawnSync(join(BIN_DIR, 'node'), ['-p', 'JSON.stringify(process.versions)'], {
    encoding: 'utf8',
    env,
    timeout: 30_000,
  })
  try {
    return JSON.parse(probe.stdout)
  } catch {
    return { error: (probe.stderr ?? '').trim().slice(0, 200) || '探针无输出' }
  }
}

/** 观察一个 pid 的命令行（`/bin/sh` 的 shim 与 exec 之后的真身都从这读；进程已退返回空串）。 */
function commandOf(pid) {
  return sh('ps', ['-o', 'command=', '-p', String(pid)]).trim()
}

/**
 * 轮询同一个 pid 的命令行，直到它变成**载荷入口那条**（私有 bin 的 shim `exec` 之后）。
 *
 * 为什么不能只采一次：启动器是 `#!/bin/sh` 脚本，spawn 之后有十几毫秒 argv 还是
 * `/bin/sh …/runtime/bin/dsh --profile acp`；只采一次会读到 shim（实测踩到）。而 dsh 是"起来就死"，
 * 所以这里是小步快跑地采，采到就返回、进程消失就收工——**这条只是加强证据**（把"runtime 跑在哪份
 * 解释器上"变成肉眼可见），判据本身由启动器生成物与进程内指纹承担。
 */
async function waitForExecArgv(pid, entry, timeoutMs = 4000) {
  const t0 = Date.now()
  let last = ''
  while (Date.now() - t0 < timeoutMs) {
    const c = commandOf(pid)
    if (c) {
      last = c
      if (c.includes(entry)) return { observed: true, argv: c }
    } else if (last) {
      return { observed: false, argv: last } // 进程已退
    }
    await sleep(40)
  }
  return { observed: false, argv: last }
}

// ————————————————————————————— 场景 —————————————————————————————

/** guard：起壳前的前置（单实例、重建、载荷与启动器在位）。 */
function scenarioGuard() {
  section(
    PACKAGED_APP
      ? '前置：环境干净 + 包内运行时树在位（M4-2a：加固运行时下包内执行）'
      : '前置：环境干净 + 载荷/启动器在位（计划 §M3-8 的前提）',
  )
  const others = pidsMatching(new RegExp(`${PROC_RES.core.source}|${PROC_RES.shell.source}`))
  check(
    '没有别的桌面壳/核心在跑',
    others.length === 0,
    others.length ? `pid=${others.join(',')}（两个核心共用 ~/.ark-mate/mate.db，请先关掉）` : '',
  )
  if (PACKAGED_APP) {
    // 打包态**故意不重建**：起的就是盘上那个包，重建等于把"判的是这个包"这件事悄悄换掉。
    for (const [rel, why] of [
      ['mate-core', '壳要 spawn 它'],
      ['web/index.html', '壳与核心共用这一份资产'],
      ['release.json', '唯一发布契约随包'],
    ]) {
      check(`包内 ${rel} 在位（${why}）`, existsSync(join(RESOURCES_ROOT, rel)), PACKAGED_APP)
    }
  } else {
    check(
      'apps/web/dist 已构建（壳要服务它）',
      existsSync(join(DESKTOP, '..', 'web', 'dist', 'index.html')),
      existsSync(join(DESKTOP, '..', 'web', 'dist', 'index.html')) ? '' : '缺则先 `cd frontend && pnpm build`',
    )
    // 每次起壳前按当前代码重建 mate-core（顺带刷新 resources/web）与壳的产物：
    // 跑在旧产物上是这一路验收最贵的坑（`desktop-ui-acceptance.mjs` 的 guard 注释里记着实测）。
    for (const [name, ok, detail] of rebuildAll()) check(name, ok, detail)
  }

  for (const name of ['dsh', 'opencode']) {
    const payload = readPayload(name)
    check(
      `载荷 ${name} 在位（payloads/*/*/payload.json）`,
      !!payload && existsSync(payload.entry),
      payload
        ? `${payload.version} / entry=${payload.entry.replace(ELIDE_FROM, '…')} / ${payload.manifest.files} 文件`
        : PACKAGED_APP
          ? `包内没有这份载荷：${PAYLOAD_ROOT}`
          : '缺——先 `make desktop/payloads`',
    )
  }

  const launchers = ['node', 'npx', 'dsh', 'opencode']
  const missing = launchers.filter((l) => !existsSync(join(BIN_DIR, l)) || !existsSync(join(BIN_DIR, `${l}.cmd`)))
  check('私有 bin 启动器已生成（POSIX 与 .cmd 各一份）', missing.length === 0, missing.length ? `缺：${missing.join('/')}` : BIN_DIR.replace(REPO, '.'))
}

/**
 * 一份载荷的 ACP 握手用例（dsh / opencode 共用）。
 *
 * 断言的分工：
 *  1. 载荷入口在位、注入变量在场（启动器只转发，目标缺失就 127 大声失败）；
 *  2. 进程**经私有 bin** 起来了；
 *  3. 运行它的那份解释器**就是壳注入的那份**（dsh：Electron 的 Node；opencode：随包原生二进制）——
 *     见下面"跑在哪份解释器上"的三条证据；
 *  4. `initialize` / `session/new` 握手成功（不需要模型凭据）；
 *  5. （可选）LIVE 档真发一次 prompt。
 */
async function scenarioHandshake(coreEnv, runtimeId, { live }) {
  const rt = RUNTIMES[runtimeId]
  section(`M3-8：${rt.label} 的 ACP 握手（桌面态 · 私有 bin · 核心里那份环境）`)

  const payload = readPayload(runtimeId)
  if (!payload || !existsSync(payload.entry)) {
    check(`${runtimeId} 载荷入口在位`, false, '缺——先 `make desktop/payloads`')
    return
  }
  check(`${runtimeId} 载荷入口在位`, true, `${payload.version} → ${payload.entry.replace(ELIDE_FROM, '…')}`)

  const env = runtimeEnvOf(coreEnv, runtimeId)
  // 工作区给临时目录：session/new 要求绝对 cwd，且**不能污染仓库**（opencode 会在 cwd 下落状态）。
  const workspace = makeTemp(`mate-smoke-${runtimeId}-ws-`)

  // 私有 bin 的 dsh/opencode 启动器只做转发；目标没注入就 127。先静态核对要交出去的那几件套。
  const launcher = join(BIN_DIR, runtimeId)
  const needed = runtimeId === 'dsh' ? ['MATE_NODE_EXECUTABLE', 'MATE_HOME', 'MATE_DSH_ENTRY'] : ['MATE_HOME', 'MATE_OPENCODE_BIN']
  for (const key of needed) {
    check(`注入变量在场：${key}`, !!env[key], env[key] ? env[key].replace(DESKTOP, '…') : '(空)')
  }

  // ——— "跑在哪份解释器上"的三条证据 ———
  //
  // ① 启动器脚本的 exec 目标（生成物，逐字节确定）：dsh 是 `ELECTRON_RUN_AS_NODE=1 exec "$MATE_NODE_EXECUTABLE" "$MATE_DSH_ENTRY"`，
  //    opencode 是 `exec "$MATE_OPENCODE_BIN"`（原生二进制，不经 Node）。
  // ② 私有 bin 的 `node` 报的 `process.versions`：与 dsh 走同一个 exec 目标，所以它报的版本
  //    就是 dsh 拿到的版本。**本机自己的 node 是另一个版本**（互为反证：24.21.0 不可能是本机那份）。
  // ③ 进程内自述（dsh 特有）：它的 boot 会打印 `native runtime fingerprint: Node x.y.z, V8 …`；
  //    成功路径下没有这行，所以这条是"有则必须自洽"的加强证据，不是唯一证据。
  const launcherScript = readFileSync(launcher, 'utf8')
  const versions = probeNodeChannel(env)
  const localNode = process.versions.node
  if (runtimeId === 'dsh') {
    check(
      '私有 bin 的 dsh 启动器把参数交给 Electron 的 Node（读生成物）',
      /ELECTRON_RUN_AS_NODE=1 exec "\$MATE_NODE_EXECUTABLE" "\$MATE_DSH_ENTRY"/.test(launcherScript),
      `launcher=${launcher.replace(DESKTOP, '…')}`,
    )
    check(
      `私有 bin 的 node 是 Electron 的 Node（不是本机的 ${localNode}）`,
      // 不硬编码 Electron 版本：读 apps/desktop/package.json 的 pin（M3-8 实测把 44.4.5 调回 44.0.0 之后，
      // 硬编码立刻变成一条假红——断言"我记住的那个常量"没有价值）。
      versions.electron === declaredElectronVersion() && /^24\./.test(versions.node ?? '') && versions.node !== localNode,
      `electron=${versions.electron ?? '(无)'} node=${versions.node ?? '?'} abi=${versions.modules ?? '?'}（本机 node=${localNode}）`,
    )
  } else {
    check(
      '私有 bin 的 opencode 启动器直接 exec 随包原生二进制（不经 Node）',
      /exec "\$MATE_OPENCODE_BIN" "\$@"$/.test(launcherScript.trim()),
      `launcher=${launcher.replace(DESKTOP, '…')}`,
    )
  }

  // ——— 起进程 ———
  const srv = startAcpServer({ command: launcher, args: rt.args, env, cwd: workspace })
  // 加强证据（**不是判据**）：等 argv 从 `/bin/sh <shim>` 变成载荷入口那条，肉眼可见"exec 到哪去了"。
  const argv = await waitForExecArgv(srv.pid, payload.entry, 4000)
  if (argv.observed) {
    note(`pid ${srv.pid} 的 argv = ${argv.argv.replace(DESKTOP, '…').slice(0, 160)}`)
  } else {
    note(`pid ${srv.pid} 在观察到 exec 后的 argv 之前就退了（最后读到：${(argv.argv || '(空)').replace(DESKTOP, '…').slice(0, 120)}）`)
  }
  // 等进程把 stderr 吐完（dsh 的失败信息就一行，很快；这里只是给它一点时间）
  await sleep(200)
  const earlyExit = srv.exited.at !== null
  const stderrNow = srv.stderrText()
  check(
    `${runtimeId} 进程经私有 bin 起来（不是 127 缺注入）`,
    !/未设置/.test(stderrNow),
    earlyExit ? `pid=${srv.pid}，已退出 code=${srv.exited.code}（不是启动器的 127）` : `pid=${srv.pid}（仍活着）`,
  )

  // ——— initialize ———
  let init = null
  let initErr = ''
  try {
    init = await srv.request('initialize', INITIALIZE_PARAMS, 60_000)
  } catch (e) {
    initErr = String(e?.message ?? e)
  }
  const handshakeOk = init?.result?.protocolVersion === 1
  check(`${runtimeId} ACP initialize 握手成功（protocolVersion=1）`, handshakeOk, handshakeOk ? JSON.stringify(init.result.agentInfo ?? {}) : initErr || JSON.stringify(init?.error ?? {}))
  if (handshakeOk) {
    check(
      `${runtimeId} 广告了 agentCapabilities（不是空架子）`,
      !!init.result.agentCapabilities,
      Object.keys(init.result.agentCapabilities ?? {}).join('/') || '(无)',
    )
  }

  // ——— session/new ———
  let sess = null
  let sessErr = ''
  if (handshakeOk) {
    try {
      sess = await srv.request('session/new', { cwd: workspace, mcpServers: [] }, 60_000)
    } catch (e) {
      sessErr = String(e?.message ?? e)
    }
    const sid = sess?.result?.sessionId
    check(`${runtimeId} session/new 建出会话（不需要模型凭据）`, !!sid, sid ? `sessionId=${sid}` : sessErr || JSON.stringify(sess?.error ?? {}))
  } else {
    check(`${runtimeId} session/new 建出会话（不需要模型凭据）`, false, '握手未成功，跳过')
  }

  // ——— ②/③ 的加强证据：运行时进程内的 Node 指纹 ———
  // dsh 的 boot 会打印它自己看到的运行时指纹（`native runtime fingerprint: Node %u.%u.%u, V8 %s`）。
  // 启动失败时，这行 stderr 恰好就是"进程当时跑在哪份 Node 上"的**进程内自述**——比任何外部推断都硬。
  const fp = /Node (\d+\.\d+\.\d+), V8 ([^\s)]+)/.exec(srv.stderrText())
  if (fp && runtimeId === 'dsh') {
    check(
      `${runtimeId} 进程内自述的 Node 就是 Electron 那份（${versions.node}，V8 带 electron 标记）`,
      fp[1] === versions.node && fp[2].includes('electron'),
      `${fp[1]} / V8 ${fp[2]}`,
    )
  }

  // ——— 可选 LIVE 档 ———
  if (live) await scenarioLive(runtimeId, { srv, handshakeOk, sessionId: sess?.result?.sessionId, env })

  // 进程日志（无论成败都留痕：失败时它是唯一的现场）
  const stderrTail = srv.stderrText().trim()
  if (stderrTail) {
    console.log(`  ── ${runtimeId} stderr（截断 1500 字符）──`)
    for (const line of stderrTail.slice(0, 1500).split('\n')) console.log(`    | ${line}`)
  }
  await srv.stop()
  spawnedRuntimePids.delete(srv.pid)

  // 失败时给一条可执行的诊断，而不是让人对着 ❌ 猜。
  if (!handshakeOk && runtimeId === 'dsh' && /node-addon-require-builtin unsupported/.test(stderrTail)) {
    printDshBlockerDiagnosis(versions)
  }
}

/**
 * dsh 在 Electron 44.4.5 上起不来的**阻塞诊断**（实测结论，逐条可复现）。
 *
 * 关键点：这不是环境/配置问题，也不是本脚本的问题——dsh 的 profile boot 依赖一个对 Electron 做
 * **V8 版本精确指纹**匹配的原生插件，而本仓 pin 的 Electron 的 V8 不在它的表里。
 */
function printDshBlockerDiagnosis(versions) {
  console.log('\n  ⛔ 阻塞诊断（dsh 起不来，**不是**本脚本或环境配置的问题）')
  console.log('     dsh 的 profile boot 依赖原生插件 node-addon-require-builtin@0.1.6 的预编译件，')
  console.log('     该插件对 Electron 做的是 **V8 版本精确指纹**匹配，只接受：')
  console.log('       43.0.0        → V8 15.0.245.13-electron.0')
  console.log('       44.0.0        → V8 15.2.124.13-electron.0')
  console.log('       45.0.0-alpha.6 → V8 15.4.80-electron.0')
  console.log(`     本仓 pin 的 Electron 是 44.4.5 → V8 ${versions.v8 ?? '15.2.124.28-electron.0'}，不在表里 → 拒绝 requireBuiltin。`)
  console.log('     纯 Node（插件里叫 "untagged Node 20/22/24 family"）不受影响，所以 `dsh --version` 一直能跑。')
  console.log('     要解掉它（三条都在本脚本的改动范围之外，属装配/版本决策）：')
  console.log('       a. 把 Electron pin 到 44.0.x（其 V8 在表里）——改 apps/desktop/package.json 并重跑 pnpm install；')
  console.log('       b. 等 node-addon-require-builtin 上游补 44.4.5 的指纹（0.1.6 已是 npm 最新）；')
  console.log('       c. 让 dsh 跑在**独立 Node 载荷**上——但那正是 §6.3 三条回退条件要判的事，')
  console.log('          而本条件（原生插件的 V8 指纹）**不在**那三条里，属新增的形态约束。')
  console.log('     定位用的旁证：`node scripts/desktop-runtime-smoke.mjs dsh-diag` —— ')
  console.log('     同一份 dsh 载荷换成本机 Node 起，握手是**绿的**，说明 ACP/协议面没问题，')
  console.log('     卡住的只是"Electron 的 Node"这一条通道。')
}

/**
 * 可选 LIVE 档：真发一次 `session/prompt`（要凭据、要联网、**不进 CI**）。
 *
 * 默认档（不设 RUNTIME_SMOKE_LIVE）**完全不联网、不跑模型**：本函数不会被调用。
 * 凭据只经环境透传（`DEEPSEEK_API_KEY` / `OPENCODE_API_KEY`），不进 argv、不落盘。
 */
async function scenarioLive(runtimeId, { srv, handshakeOk, sessionId, env }) {
  if (!handshakeOk || !sessionId) {
    check(`${runtimeId} LIVE：真发一次 prompt`, false, '握手未成功，跳过（LIVE 档以握手为前提）')
    return
  }
  const credential = runtimeId === 'dsh' ? env.DEEPSEEK_API_KEY : env.OPENCODE_API_KEY
  if (!credential) {
    check(`${runtimeId} LIVE：真发一次 prompt`, false, '凭据缺失')
    return
  }
  let res = null
  let err = ''
  try {
    res = await srv.request(
      'session/prompt',
      { sessionId, prompt: [{ type: 'text', text: 'Reply with the single word: pong' }] },
      180_000,
    )
  } catch (e) {
    err = String(e?.message ?? e)
  }
  const stopReason = res?.result?.stopReason
  check(
    `${runtimeId} LIVE：真发一次 prompt 并收到 stopReason`,
    !!stopReason,
    stopReason ? `stopReason=${stopReason}（流式帧 ${srv.notifications.length} 条）` : err || JSON.stringify(res?.error ?? {}),
  )
}

/** 触发条件 ①：两份载荷目录的指纹在整轮前后零变化。 */
function scenarioFingerprint(before) {
  section('触发条件 ①（设计 §6.3）：runtime 不写自己所在的载荷目录')
  note('口径：文件数 + 总字节 + (路径,size,mtime) 的 sha256 摘要 + 目录自身 mtime（不读文件内容）')
  for (const name of ['dsh', 'opencode']) {
    const dir = before[name]?.dir
    if (!dir) {
      check(`载荷 ${name} 目录指纹零变化`, false, '前置没取到目录（载荷缺失？）')
      continue
    }
    const b = before[name].fingerprint
    const a = payloadFingerprint(dir)
    note(`跑前基线：${name} ${fingerprintText(b)}`)
    // 任一侧"取指纹失败"都必须报红：`files=0` 与"取不到"在 JSON 上是两种形状，但都要当失败看
    // （正是"输出被 maxBuffer 截断 → 前后都读成 0 文件 → 假绿"这个坑教出来的）。
    const same = !b?.error && !a?.error && JSON.stringify(b) === JSON.stringify(a)
    check(
      `载荷 ${name} 目录指纹零变化（${dir.replace(ELIDE_FROM, '…')}）`,
      same,
      same ? fingerprintText(a) : `前 ${fingerprintText(b)} → 后 ${fingerprintText(a)}`,
    )
    if (!same) {
      // 如实报红：变化点就是"需要物化"的直接证据（§6.3 的回退触发条件）。
      console.log('    ⛔ 变化点（这是"该载荷需要物化"的证据，不是脚本噪声）：')
      console.log('       请对比上面的"前/后"三项：文件数变 = 新增/删除；字节数变 = 内容长度变；')
      console.log('       摘要变而字节数不变 = 有文件被改写（含 mtime-only）；dirMtime 变 = 目录内建过临时文件。')
    }
  }
}

/**
 * 候选修复验证场景（**不属于 V9 退出判据**）：换一个**别的 Electron/Node 可执行文件**当
 * `MATE_NODE_EXECUTABLE`，看 dsh 的 ACP 握手能不能起来。
 *
 * 为什么值得单独一条：dsh 的阻塞点（`printDshBlockerDiagnosis`）是原生插件对 Electron 做的
 * **V8 版本精确指纹**匹配，而设计 §5.1 记的本来就是 Electron **44.0.0**（V8 15.2.124.13，在表里），
 * 仓库实际 pin 的是 44.4.5（V8 15.2.124.28，不在表里）。要判断"把 pin 调回 44.0.x 就能解掉"这个
 * 假设成不成立，不必先改 `apps/desktop/package.json` —— 用 `MATE_SMOKE_ALT_NODE` 指一份别处的
 * Electron 就能当场验：**同一条私有 bin、同一份载荷、同一套环境**，只换那一个变量。
 *
 * 用法：`MATE_SMOKE_ALT_NODE=/abs/path/to/Electron node scripts/desktop-runtime-smoke.mjs dsh-alt-node`
 */
async function scenarioAltNodeExecutable(coreEnv) {
  const alt = process.env.MATE_SMOKE_ALT_NODE ?? ''
  section('候选修复验证（不计入 V9 判据）：换 MATE_NODE_EXECUTABLE 再起一次 dsh')
  if (!alt || !existsSync(alt)) {
    check('已有 MATE_SMOKE_ALT_NODE 指向一个存在的可执行文件', false, `当前：${alt || '(未设)'}`)
    return
  }
  const versions = probeNodeChannel({ ...runtimeEnvOf(coreEnv, 'dsh'), MATE_NODE_EXECUTABLE: alt, PATH: `${BIN_DIR}:/usr/bin:/bin` })
  note(`候选：${alt} → node=${versions.node ?? '?'} v8=${versions.v8 ?? '?'} abi=${versions.modules ?? '?'} electron=${versions.electron ?? '(非 Electron)'}`)

  const payload = readPayload('dsh')
  if (!payload) {
    check('dsh 载荷在位', false, '缺——先 `make desktop/payloads`')
    return
  }
  const env = { ...runtimeEnvOf(coreEnv, 'dsh'), MATE_NODE_EXECUTABLE: alt }
  const workspace = makeTemp('mate-smoke-alt-node-ws-')
  const srv = startAcpServer({ command: join(BIN_DIR, 'dsh'), args: RUNTIMES.dsh.args, env, cwd: workspace })
  let init = null
  let err = ''
  try {
    init = await srv.request('initialize', INITIALIZE_PARAMS, 60_000)
  } catch (e) {
    err = String(e?.message ?? e)
  }
  check(
    `候选修复：换 ${versions.electron ?? versions.node ?? alt} 后 dsh 握手成功`,
    init?.result?.protocolVersion === 1,
    init?.result ? JSON.stringify(init.result.agentInfo ?? {}) : err,
  )
  let sid = ''
  if (init?.result?.protocolVersion === 1) {
    try {
      sid = (await srv.request('session/new', { cwd: workspace, mcpServers: [] }, 60_000))?.result?.sessionId ?? ''
    } catch {}
  }
  check('候选修复：session/new 也成功', !!sid, sid || '(无)')
  const stderrTail = srv.stderrText().trim()
  if (stderrTail) for (const line of stderrTail.slice(0, 800).split('\n')) console.log(`    | ${line}`)
  await srv.stop()
  spawnedRuntimePids.delete(srv.pid)
}

/**
 * 诊断场景（**不属于 V9 退出判据**，只用于定位 dsh 的阻塞点）：用**本机 Node** 起同一份 dsh 载荷。
 *
 * 为什么要它：dsh 在 Electron 44.4.5 上 boot 失败（见 `printDshBlockerDiagnosis`），但那**不能**说明
 * "ACP 那部分有问题"。把同一条载荷、同一份环境换成本机 Node 起，握手若是绿的，就把问题精确地钉在
 * "Electron 的 Node"这一条通道上——这正是父任务要据此做的装配决策所需的证据。
 */
async function scenarioDshUnderLocalNode(coreEnv) {
  section('诊断（不计入 V9 判据）：同一份 dsh 载荷换**本机 Node** 起')
  note('它证明的是 ACP/协议面没问题，**不是**桌面态的证据（桌面态必须跑 Electron 的 Node）。')
  const payload = readPayload('dsh')
  if (!payload) {
    check('诊断：dsh 载荷在位', false, '缺——先 `make desktop/payloads`')
    return
  }
  const env = runtimeEnvOf(coreEnv, 'dsh')
  // 不给 MATE_NODE_EXECUTABLE：这条诊断走的就是本机 node，不碰私有 bin 的转发。
  const workspace = makeTemp('mate-smoke-dsh-diag-ws-')
  const srv = startAcpServer({
    command: process.execPath,
    args: [payload.entry, '--profile', 'acp'],
    env,
    cwd: workspace,
  })
  let init = null
  let err = ''
  try {
    init = await srv.request('initialize', INITIALIZE_PARAMS, 60_000)
  } catch (e) {
    err = String(e?.message ?? e)
  }
  check(
    `诊断：本机 Node ${process.versions.node} 起 dsh 能握手（protocolVersion=1）`,
    init?.result?.protocolVersion === 1,
    init?.result ? JSON.stringify(init.result.agentInfo ?? {}) : err,
  )
  let sid = ''
  if (init?.result?.protocolVersion === 1) {
    try {
      const sess = await srv.request('session/new', { cwd: workspace, mcpServers: [] }, 60_000)
      sid = sess?.result?.sessionId ?? ''
    } catch {}
  }
  check('诊断：本机 Node 下 session/new 也能建会话', !!sid, sid || '(无)')
  const stderrTail = srv.stderrText().trim()
  if (stderrTail) for (const line of stderrTail.slice(0, 800).split('\n')) console.log(`    | ${line}`)
  await srv.stop()
  spawnedRuntimePids.delete(srv.pid)
}

/**
 * 触发条件 ②③ 的口径：**显式打印**当前这一轮到底判了什么（免得把"全绿"当成三条件已验完）。
 *
 * M4-2a 起 ② 拆成两半（计划 §12.27）：hardened runtime 这一半在**打包态**（`MATE_SMOKE_APP=…`）
 * 被本脚本判到（起的就是签名 + `--options runtime` 的那个包）；公证那一半**本期不做**（不付费、不上架）。
 * ③ 仍需要 Windows 出包。
 */
function printUnverifiableConditions() {
  if (PACKAGED_APP) {
    section('触发条件 ②：本脚本**判到了** hardened runtime 这一半（打包态运行）')
    console.log('  ✅ ②（hardened runtime 半边）：起的是包内那份 ad-hoc 签名 + `--options runtime` 的壳与核心，')
    console.log('     下面每个 runtime 的握手都是"签名后的包内二进制真的被执行了"的直接证据（make verify/signature 判签名形态）。')
    console.log('  ⚠ ②（公证半边）**本期不做**（不上架、不付费，§12.27）：异地机器 + staple 的验收已撤销，')
    console.log('     替换为"tar.gz 解到干净目录后首启无阻断"（make desktop/dist 收尾的 verify-release-json --launch）。')
    console.log('  ⚠ ③ Windows SmartScreen / AV 是否拦 —— 需要 Windows 测试机与出包（M4-3）。')
    console.log('     ⇒ 因此"全绿"= ① 已验 + ② 的 hardened runtime 半边已验；不等于 §6.3 三条件全验完。')
    return
  }
  section('触发条件 ②③：本脚本（源码态）不判')
  console.log('  ⚠ ②③ 需要 M4 的签名/公证/Windows 出包，源码态跑的本脚本判不了（见计划 §M3-6/§M3-8、设计 §6.3）：')
  console.log('     ② 平台签名 / hardened runtime 是否允许包内执行该二进制 —— 用 `MATE_SMOKE_APP=<app> pnpm smoke:desktop`（M4-2a）；')
  console.log('     ③ Windows SmartScreen / AV 是否拦 —— 需要 Windows 测试机与出包。')
  console.log('     ⇒ 因此"全绿"只等于**触发条件 ①** 已验完，不等于 §6.3 的三条件已验完。')
}

/**
 * 可选 LIVE 档的**门禁说明**：无论跑不跑都打印一行，说清"为什么没跑"。
 *
 * 默认档**完全不联网、不跑模型**（`scenarioLive` 根本不会被调用）。计划要求"需要模型时用一次性 key，
 * 不进 CI"——所以这里只接受环境变量、只经环境透传（不进 argv/不落盘），并且在没开时明确打印跳过原因，
 * 免得读日志的人以为"跑了但没输出"。
 */
function printLiveGate() {
  section('可选 LIVE 档（真模型；默认跳过）')
  const on = process.env.RUNTIME_SMOKE_LIVE === '1'
  const creds = {
    dsh: !!process.env.MATE_SMOKE_DSH_API_KEY,
    opencode: !!process.env.MATE_SMOKE_OPENCODE_API_KEY,
  }
  if (!on) {
    console.log('  ⏭ 跳过：未设 RUNTIME_SMOKE_LIVE=1（默认档不联网、不跑模型、不需要任何凭据）')
    console.log('     要跑：RUNTIME_SMOKE_LIVE=1 MATE_SMOKE_DSH_API_KEY=… MATE_SMOKE_OPENCODE_API_KEY=… \\')
    console.log('           node scripts/desktop-runtime-smoke.mjs live')
    console.log('     ⚠ 它**不进 CI**（要凭据 + 真模型 + 真出网）；本机从未用凭据验过这条路径——')
    console.log('       本轮的 LIVE 档只做了形状实现与门禁，**没有**真跑过。')
    return
  }
  console.log(`  ▶ 已开启：凭据 dsh=${creds.dsh ? '有' : '无'} opencode=${creds.opencode ? '有' : '无'}`)
  console.log('     ⚠ 不进 CI；凭据只经环境透传（不进 argv、不落盘）。')
}

/** 收尾核对：我们起的壳/核心/runtime 都不在了（只按"本仓路径"判，不动用户自己的进程）。 */
async function scenarioResidue() {
  section('收尾：无残留（本仓的壳/核心/载荷进程）')
  await sleep(300)
  check('无 mate-core 残留', pidsMatching(PROC_RES.core).length === 0)
  check('无桌面壳残留', pidsMatching(PROC_RES.shell).length === 0)
  // 只认"命令行里带我们载荷目录"的进程：用户自己开的 `dsh web` / 全局 `opencode` 不在此列
  // （这正是不能用 `pkill -f dsh` 的原因——那会误杀别人的会话）。
  const ours = psAll()
    .split('\n')
    .filter((l) => l.includes(PAYLOAD_ROOT) && !l.includes('grep'))
  check('无本仓载荷进程残留', ours.length === 0, ours.slice(0, 2).map((l) => l.slice(0, 120)).join(' | '))
  // 我们显式记过 pid 的那几个也必须都没了（双保险：argv 读不到时也能判）。
  const alive = [...spawnedRuntimePids].filter((pid) => {
    try {
      process.kill(pid, 0)
      return true
    } catch {
      return false
    }
  })
  check('脚本起过的 runtime pid 都已退出', alive.length === 0, alive.join(',') || '')
}

// ————————————————————————————— 主流程 —————————————————————————————

function printHelp() {
  const text = readFileSync(new URL(import.meta.url), 'utf8')
  const head = text.slice(0, text.indexOf('*/') + 2)
  console.log(head.replace(/^#!.*\n/, ''))
  console.log('用法：node scripts/desktop-runtime-smoke.mjs [场景…]')
  console.log('  场景（默认全部，guard 总是跑）：')
  console.log('    guard       前置：单实例检查、make core/desktop 重建、载荷与私有 bin 在位')
  console.log('    dsh         dsh 的 ACP 握手（私有 bin → Electron 的 Node → 随包载荷）')
  console.log('    opencode    opencode 的 ACP 握手（私有 bin → 随包原生二进制）')
  console.log('    fingerprint 触发条件 ①：两份载荷目录指纹整轮前后零变化')
  console.log('    live        可选 LIVE 档：真发一次 session/prompt（要凭据，**不进 CI**）')
  console.log('    dsh-diag    诊断（**不计入** V9 判据）：同一份 dsh 载荷换本机 Node 起，用于定位阻塞点')
  console.log('    dsh-alt-node 候选修复验证（**不计入** V9 判据）：换 MATE_SMOKE_ALT_NODE 指的 Electron 再起 dsh')
  console.log('  环境变量：KEEP_SHELL=1 跑完不杀壳；RUNTIME_SMOKE_LIVE=1 开 LIVE 档；')
  console.log('           MATE_SMOKE_DSH_API_KEY / MATE_SMOKE_OPENCODE_API_KEY 为 LIVE 档凭据；')
  console.log('           MATE_SMOKE_ALT_NODE 为 dsh-alt-node 的候选 Node/Electron 可执行文件绝对路径')
}

/** 默认场景集（不含 `dsh-diag` / `dsh-alt-node`：它们是定位工具，不是退出判据）。 */
const SCENARIOS = ['dsh', 'opencode', 'fingerprint', 'live']
/** 全部可指定的场景（含两个诊断/验证工具）。 */
const ALL_SCENARIOS = [...SCENARIOS, 'dsh-diag', 'dsh-alt-node']
const argv = process.argv.slice(2)
if (argv.includes('--help') || argv.includes('-h')) {
  printHelp()
  process.exit(0)
}
const want = argv.filter((a) => !a.startsWith('-'))
const unknown = want.filter((s) => !ALL_SCENARIOS.includes(s))
if (unknown.length) {
  console.error(`未知场景：${unknown.join(', ')}（可选：${ALL_SCENARIOS.join(' / ')}；--help 看说明）`)
  process.exit(2)
}
const selected = want.length ? want : SCENARIOS
const liveWanted = selected.includes('live') && process.env.RUNTIME_SMOKE_LIVE === '1'
// 两个诊断/验证场景都是「起一次壳拿核心环境 → 换解释器起 dsh」，但它们**不参与** V9 判据；
// 只有默认场景才跑触发条件 ① 的指纹与残留核对（诊断场景的壳同样会被 finally 收掉）。
const DIAGNOSTIC_ONLY = selected.every((s) => s === 'dsh-diag' || s === 'dsh-alt-node')

async function main() {
  const t0 = Date.now()
  console.log('桌面态 runtime smoke（真 Electron + 真核心 + 随包载荷；M3-8 / V9 第一轮收口）')

  scenarioGuard()
  if (results.some((r) => !r.ok)) {
    console.log('\n前置不通过，终止（先解决上面的问题）')
    process.exit(1)
  }

  // 触发条件 ① 的"跑前"快照：必须**早于任何 runtime 进程**（连探针都不跑）。
  const before = {}
  if (!DIAGNOSTIC_ONLY) {
    for (const name of ['dsh', 'opencode']) {
      const payload = readPayload(name)
      if (payload) before[name] = { dir: payload.dir, fingerprint: payloadFingerprint(payload.dir) }
    }
  }

  let shell = null
  let coreEnv = {}
  try {
    if (!DIAGNOSTIC_ONLY) {
      // V10 ① 的前提：**机器上没有 node**。壳只在 `PATH=/usr/bin:/bin` 下启动，因此核心及其子进程
      // 拿到的 Node 只可能来自壳自己注入的那份（设计 §6.6）。HOME 保留是 Chromium 自己的要求。
      shell = PACKAGED_APP
        ? await launchPackagedShell({
            appPath: PACKAGED_APP,
            env: { PATH: '/usr/bin:/bin', HOME, MATE_DESKTOP_HOME: makeTemp('mate-smoke-home-') },
          })
        : await launchShell({ env: { PATH: '/usr/bin:/bin', HOME } })
      if (!shell.corePid) {
        check('壳与核心起来了', false, `壳日志尾部：\n${shell.logText().split('\n').slice(-6).join('\n')}`)
        await (PACKAGED_APP ? stopPackagedShell(shell) : stopShellTree())
        process.exit(1)
      }
      check('壳与核心起来了', true, `shell=${shell.shellPid} core=${shell.corePid}`)

      // 核心的实际监听地址（端口是内核分配的）：证明核心真在服务，也是"壳真的把核起全了"的证据。
      const addr = coreAddrOf(shell)
      check('拿到核心实际监听地址（内核分配的端口）', /^127\.0\.0\.1:\d+$/.test(addr), addr || shell.logText().slice(-200))
      if (addr) {
        const health = await fetch(`http://${addr}/v1/mate/health`).then((r) => r.status).catch(() => 0)
        check('核心 /v1/mate/health 200（壳→核这段是通的）', health === 200, `status=${health}`)
      }

      // —— 核心进程**实际拿到**的环境（不是我们构造的）：runtime 看见的 PATH 就是它。
      const psEnv = sh('ps', ['-E', '-p', String(shell.corePid)])
      coreEnv = parseProcessEnv(psEnv)
      if (!psEnv.trim() || Object.keys(coreEnv).length === 0) {
        console.log('  ⚠ 本机 ps -E 读不到核心环境（macOS 权限）：下面的用例退化为"用我们构造的环境"')
      }
      check('核心 PATH 首段是私有 bin', String(coreEnv.PATH ?? '').startsWith(`${BIN_DIR}:`), String(coreEnv.PATH ?? '').replace(DESKTOP, '…'))
      check(
        PACKAGED_APP ? 'MATE_NODE_EXECUTABLE 指向包内那份壳可执行文件' : 'MATE_NODE_EXECUTABLE 指向 Electron 本体',
        PACKAGED_APP
          ? String(coreEnv.MATE_NODE_EXECUTABLE ?? '').startsWith(join(PACKAGED_APP, 'Contents', 'MacOS'))
          : /Electron\.app\/Contents\/MacOS\/Electron$/.test(coreEnv.MATE_NODE_EXECUTABLE ?? ''),
        coreEnv.MATE_NODE_EXECUTABLE ?? '(未注入)',
      )
      check('MATE_HOME 是绝对路径（可写面只有它）', String(coreEnv.MATE_HOME ?? '').startsWith('/'), coreEnv.MATE_HOME ?? '(未注入)')
      // 上一条判的是"核心自己被注入了 MATE_HOME"；这一条判**探针**别把它当可写面：
      // 核心的 MATE_HOME 在开发机上就是用户真实的 `~/.ark-mate`，探针往那儿写会污染真机状态
      // （见 smokeProbeHome 的注释），而载荷指纹断言看不出来。
      check(
        '探针的可写面是临时目录（不是核心的 MATE_HOME）',
        smokeProbeHome().startsWith(tmpdir()) && smokeProbeHome() !== coreEnv.MATE_HOME,
        `probe=${smokeProbeHome().replace(DESKTOP, '…')}`,
      )

      // 壳给核心的三个载荷入口：都指向随包载荷，且文件真的在。
      for (const key of ['MATE_DSH_ENTRY', 'MATE_OPENCODE_BIN', 'MATE_NPX_ENTRY']) {
        const v = coreEnv[key] ?? ''
        check(`${key} 指向随包载荷`, v.startsWith(`${PAYLOAD_ROOT}/`) && existsSync(v), v.replace(DESKTOP, '…') || '(未注入)')
      }

      // LIVE 档的凭据注入（默认档完全不碰）
      if (process.env.MATE_SMOKE_DSH_API_KEY) coreEnv.DEEPSEEK_API_KEY = process.env.MATE_SMOKE_DSH_API_KEY
      if (process.env.MATE_SMOKE_OPENCODE_API_KEY) coreEnv.OPENCODE_API_KEY = process.env.MATE_SMOKE_OPENCODE_API_KEY

      for (const id of ['dsh', 'opencode']) {
        if (!selected.includes(id)) continue
        // eslint-disable-next-line no-await-in-loop -- 两个 runtime 串行：日志可读、且避免两条 ACP 互相盖住现场
        await scenarioHandshake(coreEnv, id, { live: liveWanted })
      }

      if (selected.includes('fingerprint')) scenarioFingerprint(before)
    } else {
      // 只跑诊断时也起一次壳：诊断要的"核心那份环境"只能从真核心里读（否则就成了"我记住的常量"）。
      shell = PACKAGED_APP
        ? await launchPackagedShell({
            appPath: PACKAGED_APP,
            env: { PATH: '/usr/bin:/bin', HOME, MATE_DESKTOP_HOME: makeTemp('mate-smoke-home-') },
          })
        : await launchShell({ env: { PATH: '/usr/bin:/bin', HOME } })
      check('壳与核心起来了（诊断需要读核心的真实环境）', !!shell.corePid, `core=${shell.corePid}`)
      coreEnv = parseProcessEnv(sh('ps', ['-E', '-p', String(shell.corePid)]))
    }

    if (selected.includes('dsh-diag')) await scenarioDshUnderLocalNode(coreEnv)
    if (selected.includes('dsh-alt-node')) await scenarioAltNodeExecutable(coreEnv)
  } catch (e) {
    console.error(`\nsmoke 异常：${e?.stack ?? e}`)
    results.push({ name: '脚本异常', ok: false, detail: String(e?.message ?? e) })
  } finally {
    // 一定收干净（异常路径也走这里）：这个仓库被"上次跑剩的壳挡住下次运行"坑过。
    if (shell && process.env.KEEP_SHELL !== '1') {
      const stats = PACKAGED_APP ? await stopPackagedShell(shell) : await stopShellTree()
      note(`stopShellTree：graceful=${stats.graceful} ${stats.ms}ms reaped=${stats.reaped}`)
    } else if (shell) {
      note('KEEP_SHELL=1：壳保留（人工接管；下次跑会被 guard 挡下）')
    }
    for (const dir of tempDirs) rmSync(dir, { recursive: true, force: true })
  }

  if (!DIAGNOSTIC_ONLY) await scenarioResidue()
  printUnverifiableConditions()
  printLiveGate()

  const passed = results.filter((r) => r.ok).length
  const failed = results.filter((r) => !r.ok)
  console.log(`\n——— 汇总：${passed}/${results.length} 通过（${((Date.now() - t0) / 1000).toFixed(1)}s）———`)
  if (failed.length) {
    console.log('失败项：')
    for (const f of failed) console.log(`  ❌ ${f.name}${f.detail ? ` — ${f.detail}` : ''}`)
    process.exit(1)
  }
  console.log('桌面态 runtime smoke：全部通过')
  process.exit(0)
}

main().catch((e) => {
  console.error(`smoke 脚本异常：${e?.stack ?? e}`)
  process.exit(1)
})
