/**
 * 桌面态子进程环境（设计 §6.6 的**机械落地**，M3-2）。
 *
 * 放在契约包里而不是 `apps/desktop`：这条规则既是"壳怎么起核心"的依据，也是"私有 bin 到底前置了什么"
 * 的判定标准——两侧（壳的启动路径 / 验收脚本的断言）引用同一份实现，才不会出现"代码是 A、断言是 B"。
 * 纯函数、零 Electron、零 Node API 引用，故可被 `node --test` 直接覆盖（本仓前端没有测试框架，
 * 纯逻辑一律落到这里）。
 *
 * 三条纪律（设计 §6.6）：
 *   1. **继承调用方环境为基线**——不做白名单。MCP server 与用户工具链（git/python/uv/代理/
 *      `SSH_AUTH_SOCK`）都靠它；白名单化的症状是"某些工具在桌面态莫名找不到"。
 *   2. `PATH` 只做**前置**：`<私有 bin><分隔符><原 PATH>`。用户原有的 PATH 尾部必须保留。
 *   3. `ELECTRON_RUN_AS_NODE` 与 `MATE_NODE_EXECUTABLE` 成对出现：前者让 Electron 以 Node 模式启动，
 *      后者让私有 `node`/`npx` 启动器知道该转发到哪个可执行文件（对应参照实现的
 *      `DSH_DESKTOP_NODE_EXECUTABLE`，这里按本仓命名前缀为 `MATE_`）。
 */

/** 桌面态子进程环境里本仓注入的变量名（唯一真源：壳、启动器脚本、验收脚本都引用它）。 */
export const NODE_ENV_VARS = {
  /** 让 Electron 以 Node 模式启动（不加载 Chromium）。 */
  runAsNode: 'ELECTRON_RUN_AS_NODE',
  /** Electron 可执行文件的绝对路径；私有 `node`/`npx` 启动器据此转发。 */
  nodeExecutable: 'MATE_NODE_EXECUTABLE',
} as const

export type NodeModeEnvironmentInput = {
  /** Electron 自身可执行文件的绝对路径（`process.execPath`）。 */
  executable: string
  /** 私有 bin 目录；`undefined` = 本次不前置 PATH（只切 Node 模式）。 */
  binDir?: string
  /** 调用方环境（原样继承）。 */
  base: Record<string, string | undefined>
  /** PATH 分隔符：POSIX `:`、Windows `;`。默认 `:`。 */
  delimiter?: string
}

/**
 * 构造"交给 Electron 的 Node 执行"的子进程环境。
 *
 * 注意 `binDir === undefined` 与 `binDir === ''` 的区别：前者**不动 PATH**（只切 Node 模式，
 * 适合"不需要私有 bin"的直接子进程），后者会产出一个以分隔符开头的奇怪 PATH——因此空串按
 * "不前置"处理，调用方不必自己判空。
 */
export function desktopNodeEnvironment(input: NodeModeEnvironmentInput): Record<string, string> {
  const { executable, binDir, base, delimiter = ':' } = input
  const env: Record<string, string> = {}
  // 继承：只保留有值的项（`process.env` 里 undefined 的键不该被写成字符串 "undefined"）。
  for (const [key, value] of Object.entries(base)) {
    if (typeof value === 'string') env[key] = value
  }
  env[NODE_ENV_VARS.runAsNode] = '1'
  env[NODE_ENV_VARS.nodeExecutable] = executable
  if (binDir) {
    const rest = env.PATH ?? ''
    env.PATH = rest ? `${binDir}${delimiter}${rest}` : binDir
  }
  return env
}

/**
 * 判定一个子进程 PATH 是否满足设计 §6.6（私有 bin 前置 + 调用方 PATH 保留）。
 *
 * 这是**验收判据**而不是实现的一部分：V10 ② 要求"子进程内断言 PATH 首段是私有 bin、
 * 其后仍含调用方 PATH"。写成函数是为了让验收脚本与将来的诊断导出共用同一个口径——
 * 手写字符串前缀比较很容易在"私有 bin 路径里本身含分隔符"这类情况下给出错判。
 */
export function pathPrecedesCaller(
  childPath: string | undefined,
  binDir: string,
  callerPath: string | undefined,
  delimiter = ':',
): boolean {
  if (!childPath) return false
  if (childPath === binDir) return !callerPath // 调用方本来没有 PATH：PATH 就等于私有 bin
  const prefix = binDir + delimiter
  if (!childPath.startsWith(prefix)) return false
  const rest = childPath.slice(prefix.length)
  if (!callerPath) return rest === ''
  // 调用方 PATH 必须**原样**保留在尾部（`env -i PATH=/usr/bin:/bin` 时这里就是 `/usr/bin:/bin`）。
  // 允许前置段不止私有 bin 一项：宿主（Electron / 平台 shell）偶尔会在最前面再插一条，
  // 那不是我们破坏契约，但尾部被改写就是。
  return rest === callerPath || rest.endsWith(delimiter + callerPath)
}
