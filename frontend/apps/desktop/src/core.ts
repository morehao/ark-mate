/**
 * 核心子进程的启动与握手（M1-3 起外壳需要它；M1-6 补退出语义、M1-10 补孤儿验收）。
 *
 * 启动链（设计 §7.1）：spawn 核心（`:0` + `--ready-stdout`）→ 读 stdout 的
 * `MATE_READY {...}` 拿到实际地址 → 再用 `/v1/mate/health` 复核一次
 * （就绪行给出地址，health 证明真的在服务）。
 */
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { createInterface } from 'node:readline'
import { coreEnvironment } from './env'
import { payloadEnvironmentExtras, payloadInfoList, resolveRuntimePayloads } from './payload'
import { payloadsDir, webDir } from './paths'

/** 就绪行的行首前缀（与核心侧 `app.ReadyPrefix` 同值）。 */
export const READY_PREFIX = 'MATE_READY '

/** 就绪行等待预算（设计 §7.1：超时 → health 轮询兜底 → 再超时走恢复页）。 */
const READY_TIMEOUT_MS = 10_000
/** health 复核预算（就绪行已到，这一步只是确认"真的在服务"）。 */
const HEALTH_TIMEOUT_MS = 5_000

export type CoreHandle = {
  /** 实际监听地址（`127.0.0.1:<内核分配端口>`）。 */
  addr: string
  port: number
  /** 进程句柄（M1-6 的退出语义要用）。 */
  child: ChildProcessWithoutNullStreams
}

export type StartCoreOptions = {
  exe: string
  /** 就绪日志回调（壳的日志前缀统一在 main.ts）。 */
  onLog?: (line: string) => void
}

/**
 * 启动核心并完成握手。失败即抛错（调用方据此显示 `boot-failed` 壳页，而不是留一个白窗口）。
 */
export async function startCore(opts: StartCoreOptions): Promise<CoreHandle> {
  // 解析随包载荷（M3-4，设计 §6.5）。**在 spawn 之前**：缺载荷时宁可走 boot-failed 恢复页，
  // 也不让核心起来后在第一次会话里才报"runtime 起不来"——那时用户已经等了一轮。
  // 依赖显式注入：载荷根来自 `paths`（那是唯一知道"随包资源在哪"的地方），环境取本进程环境。
  const payloads = resolveRuntimePayloads({ payloadsRoot: payloadsDir(), env: process.env })

  const child = spawn(
    opts.exe,
    // 桌面态的核心：内核分配端口 + 就绪行回写 + 父进程消失自退（M1-6）。
    // 核心从不自带前端资产（2026-10-10 起 `-tags release` / `go:embed` 已删）：资产由壳服务，核心按 `--web-dir` 读同一份（M2-6 接线）。
    [
      'serve',
      '--addr', '127.0.0.1:0',
      '--ready-stdout',
      '--exit-with-parent',
      // 资产目录：核心不内嵌，壳把自己服务的那一份显式指过去（M2-6）。
      // 两边读**同一份**是硬约束——页面里的 build id 与核心读到的 build-id.txt 必须同源，
      // 否则版本闸门（M2-4）会把"两份资产"误判成"版本不一致"。
      '--web-dir', webDir(),
      // 载荷清单（M3-4）：核心不解析载荷（那是壳的事），它只把这份**已解析**的结果透出到 `/meta`，
      // 让诊断面能看到"这次用的是哪一份、来自哪一级"。
      '--payloads', JSON.stringify(payloadInfoList(payloads)),
    ],
    {
      stdio: ['pipe', 'pipe', 'pipe'],
      // 独立进程组（POSIX）：退出时对**整组**发信号，核心手里的 runtime 子进程一并收掉。
      // 它同时意味着"壳崩了核心不会自动收到信号"——那由核心自己的 --exit-with-parent 兜底。
      detached: true,
      // 设计 §6.6 的那份环境（M3-2）：**继承调用方** + Node 模式 + 私有 bin 前置 + 载荷入口注入。
      // 核心 spawn runtime 时原样继承它——这就是"runtime 跑在 Electron 的 Node 上"的全部机关
      // （本仓的 runtime 父进程是核心，不是壳，见 node-runtime.ts 顶注）。
      env: coreEnvironment({ extras: payloadEnvironmentExtras(payloads) }),
    },
  ) as ChildProcessWithoutNullStreams

  const log = opts.onLog ?? (() => {})

  // spawn 的失败是**异步**的（'error' 事件：ENOENT / EACCES…）。不接它就会冒泡成主进程的
  // uncaughtException——实测就是这样：二进制缺失时窗口根本没开，只剩一行异常（M1-3 自证抓到）。
  const spawnErr = new Promise<never>((_, reject) => {
    child.once('error', (err) => reject(new Error(`核心进程启动失败（${opts.exe}）：${err.message}`)))
  })

  // 核心不读 stdin；直接关掉写端，避免管道悬着。
  child.stdin.end()

  createInterface({ input: child.stderr }).on('line', (l) => log(`[core] ${l}`))

  const addr = await Promise.race([waitReady(child, log), spawnErr])
  await Promise.race([waitHealthy(addr, log), spawnErr])

  const port = Number(addr.slice(addr.lastIndexOf(':') + 1))
  return { addr, port, child }
}

/** 读 stdout 直到就绪行（按**行首**匹配：gin 的路由转储等日志同样落在 stdout）。 */
function waitReady(child: ChildProcessWithoutNullStreams, log: (l: string) => void): Promise<string> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      cleanup()
      reject(new Error(`核心 ${READY_TIMEOUT_MS / 1000}s 内未回写 ${READY_PREFIX.trim()}（就绪行缺失）`))
    }, READY_TIMEOUT_MS)

    const rl = createInterface({ input: child.stdout })
    const onLine = (line: string) => {
      if (line.startsWith(READY_PREFIX)) {
        clearTimeout(timer)
        rl.close()
        try {
          const payload = JSON.parse(line.slice(READY_PREFIX.length)) as { addr?: string; port?: number }
          if (!payload.addr) throw new Error('就绪行缺少 addr')
          log(`[core] ready ${payload.addr}`)
          resolve(payload.addr)
        } catch (e) {
          reject(new Error(`就绪行解析失败：${String(e)}（原始行：${line.slice(0, 200)}）`))
        }
        return
      }
      log(`[core] ${line}`)
    }
    rl.on('line', onLine)
    const cleanup = () => {
      clearTimeout(timer)
      rl.close()
    }
    child.once('exit', (code, signal) => {
      cleanup()
      reject(new Error(`核心提前退出（code=${code} signal=${signal}）`))
    })
  })
}

/** 用 health 复核（免鉴权端点），确认"地址存在"确实等于"在服务"。 */
async function waitHealthy(addr: string, log: (l: string) => void): Promise<void> {
  const deadline = Date.now() + HEALTH_TIMEOUT_MS
  let lastErr = ''
  while (Date.now() < deadline) {
    try {
      const r = await fetch(`http://${addr}/v1/mate/health`)
      if (r.ok) {
        log('[core] health ok')
        return
      }
      lastErr = `HTTP ${r.status}`
    } catch (e) {
      lastErr = String(e)
    }
    await new Promise((r) => setTimeout(r, 100))
  }
  throw new Error(`核心 health 复核失败（${HEALTH_TIMEOUT_MS / 1000}s 内未就绪）：${lastErr}`)
}

/** 向**整个进程组**发信号（POSIX：detached 子进程的 pgid == pid，负号即整组）。 */
function signalGroup(pid: number, sig: NodeJS.Signals): void {
  try {
    process.kill(-pid, sig)
  } catch {
    // 组信号在个别平台/时机可能不可用，退化为只发直接子进程（不静默失败）。
    try {
      process.kill(pid, sig)
    } catch {
      /* 已退出 */
    }
  }
}

/**
 * 优雅停机（M1-6）：`SIGTERM` → 等核心退出（宽限 `graceMs`，默认 3s）→ 对**进程组** `SIGKILL`。
 *
 * 为什么必须补 SIGKILL：核心自己会优雅回收 runtime 子进程，但它可能卡住（底座无响应、
 * 权限确认悬着）。宽限期一到就整组杀掉——桌面用户点了"退出"，应用就必须真的退出。
 */
export async function stopCoreGracefully(
  handle: CoreHandle | null,
  opts: { graceMs?: number; log?: (msg: string) => void } = {},
): Promise<void> {
  if (!handle) return
  const pid = handle.child.pid
  if (!pid) return
  const grace = opts.graceMs ?? 3000
  const log = opts.log ?? (() => {})

  if (handle.child.exitCode !== null || handle.child.signalCode !== null) return

  const exited = new Promise<'exited'>((resolve) => handle.child.once('exit', () => resolve('exited')))
  signalGroup(pid, 'SIGTERM')
  log(`已向核心进程组发 SIGTERM（pid=${pid}），宽限 ${grace}ms`)

  const winner = await Promise.race([
    exited,
    new Promise<'timeout'>((resolve) => setTimeout(() => resolve('timeout'), grace)),
  ])
  if (winner === 'timeout') {
    log(`核心 ${grace}ms 内未退出，对进程组 SIGKILL`)
    signalGroup(pid, 'SIGKILL')
  } else {
    log('核心已退出')
  }
}

/**
 * 向核心询问"有没有在途轮次"（M1-6 退出前二次确认的依据）。
 *
 * 用既有读模型：`GET /v1/mate/sessions` 的 `status=running` 即"这一轮还没收尾"。
 * **不新增端点**——这个判断是"退出前问一句"，不值得为它改对外路由表。
 *
 * 超时/出错时返回 `known:false`：调用方按**有任务**保守处理（设计 §7.4），
 * 因为"以为没在跑"地退出会真的打断一轮对话。
 */
export async function probeRunningRuns(
  handle: CoreHandle | null,
  timeoutMs = 2000,
): Promise<{ known: boolean; running: number }> {
  if (!handle) return { known: true, running: 0 }
  const ctl = new AbortController()
  const timer = setTimeout(() => ctl.abort(), timeoutMs)
  try {
    const r = await fetch(`http://${handle.addr}/v1/mate/sessions?limit=200`, {
      signal: ctl.signal,
    })
    if (!r.ok) return { known: false, running: -1 }
    const j = (await r.json()) as { code?: number; data?: Array<{ status?: string }> }
    if (j.code !== 0 || !Array.isArray(j.data)) return { known: false, running: -1 }
    return { known: true, running: j.data.filter((c) => c.status === 'running').length }
  } catch {
    return { known: false, running: -1 }
  } finally {
    clearTimeout(timer)
  }
}
