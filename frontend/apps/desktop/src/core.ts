/**
 * 核心子进程的启动与握手（M1-3 起外壳需要它；M1-6 补退出语义、M1-10 补孤儿验收）。
 *
 * 启动链（设计 §7.1）：壳生成两个令牌 → spawn 核心（`:0` + `--auth-stdin` + `--ready-stdout`）
 * → 令牌只经 **stdin 首行** 交付 → 读 stdout 的 `FORGE_READY {...}` 拿到实际地址
 * → 再用 `/v1/forge/health` 复核一次（就绪行给出地址，health 证明真的在服务）。
 *
 * 令牌的落点（设计 §7.2 / §9.1）：只在**本进程内存**里——不进 argv、不进 env、不写文件；
 * 子进程环境按设计 §6.6 原样继承调用方环境，因此更不能把令牌塞进 env（runtime 子进程会继承）。
 */
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { createInterface } from 'node:readline'

/** 就绪行的行首前缀（与核心侧 `app.ReadyPrefix` 同值）。 */
export const READY_PREFIX = 'FORGE_READY '

/** 就绪行等待预算（设计 §7.1：超时 → health 轮询兜底 → 再超时走恢复页）。 */
const READY_TIMEOUT_MS = 10_000
/** health 复核预算（就绪行已到，这一步只是确认"真的在服务"）。 */
const HEALTH_TIMEOUT_MS = 5_000

export type CoreHandle = {
  /** 实际监听地址（`127.0.0.1:<内核分配端口>`）。 */
  addr: string
  port: number
  /** 只给壳的反代用；**从不**进渲染进程。 */
  restToken: string
  /** 事件流通道级凭据；经 M1-8 的 boot() 下发。 */
  streamToken: string
  /** 进程句柄（M1-6 的退出语义要用）。 */
  child: ChildProcessWithoutNullStreams
}

export type StartCoreOptions = {
  exe: string
  /** 就绪日志回调（壳的日志前缀统一在 main.ts）。 */
  onLog?: (line: string) => void
}

/** 生成 32 字节随机 base64url 令牌（与核心侧 `--auth-stdin` 的约定一致）。 */
export function newToken(): string {
  return randomBytes(32).toString('base64url')
}

/**
 * 启动核心并完成握手。失败即抛错（调用方据此显示 `boot-failed` 壳页，而不是留一个白窗口）。
 */
export async function startCore(opts: StartCoreOptions): Promise<CoreHandle> {
  const restToken = newToken()
  const streamToken = newToken()

  const child = spawn(
    opts.exe,
    // 桌面态的核心：内核分配端口 + 双凭据经 stdin + 就绪行回写。**不带** `-tags release`
    // 的内嵌资产（资产由壳服务，核心只在需要时按 `--web-dir` 读同一份，M2-6 接线）。
    ['serve', '--addr', '127.0.0.1:0', '--auth-stdin', '--ready-stdout'],
    { stdio: ['pipe', 'pipe', 'pipe'] },
  ) as ChildProcessWithoutNullStreams

  const log = opts.onLog ?? (() => {})

  // spawn 的失败是**异步**的（'error' 事件：ENOENT / EACCES…）。不接它就会冒泡成主进程的
  // uncaughtException——实测就是这样：二进制缺失时窗口根本没开，只剩一行异常（M1-3 自证抓到）。
  const spawnErr = new Promise<never>((_, reject) => {
    child.once('error', (err) => reject(new Error(`核心进程启动失败（${opts.exe}）：${err.message}`)))
  })

  // 核心只读首行；写完后关掉写端（"读完后不再读 stdin"，也不会因为管道悬着而挂住）。
  child.stdin.write(JSON.stringify({ restToken, streamToken }) + '\n')
  child.stdin.end()

  createInterface({ input: child.stderr }).on('line', (l) => log(`[core] ${l}`))

  const addr = await Promise.race([waitReady(child, log), spawnErr])
  await Promise.race([waitHealthy(addr, log), spawnErr])

  const port = Number(addr.slice(addr.lastIndexOf(':') + 1))
  return { addr, port, restToken, streamToken, child }
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
      const r = await fetch(`http://${addr}/v1/forge/health`)
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

/** 请求核心退出（M1-6 会补"等核心退出 → 宽限 → 进程组 SIGKILL"的完整语义）。 */
export function stopCore(handle: CoreHandle | null): void {
  if (!handle) return
  try {
    handle.child.kill('SIGTERM')
  } catch {
    /* 已退出 */
  }
}
