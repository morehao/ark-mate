/**
 * 桌面态子进程环境（M3-3，设计 §6.6 的机械落地）。
 *
 * 与契约里的 `desktopNodeEnvironment`（纯函数，管"继承 + Node 模式 + PATH 前置"）分工：
 * 那边只管**与平台无关的三条**，这边管**本仓自己的路径变量**——它们要问 Electron 拿路径
 * （用户数据目录、私有 bin、载荷根），因此不可能进契约包（契约 dependencies 为空、
 * 零 Electron 引用是硬约束）。
 *
 * 设计 §6.6 的六项在这里齐了：`ELECTRON_RUN_AS_NODE` / `PATH` / `MATE_NODE_EXECUTABLE`
 * （契约函数给）、`MATE_HOME` / `npm_config_cache` / `npm_config_prefix`（本文件给）。
 * 各 runtime 的 home/config 变量不在这里——它们要**按 runtime**注入到对应子进程，
 * 落在 Go 侧的 `pkg/runtime.IsolationEnv`（设计 §6.6.1）。
 */
import { delimiter, join, resolve } from 'node:path'

import { desktopNodeEnvironment } from '@ark/desktop-contract'

import { runtimeBinDir } from './paths'

/**
 * 数据目录（`MATE_HOME`）：CLI / 浏览器 / 桌面**三端共用一份**（设计 §481 的红线——
 * 按入口分裂会出现"桌面打开后会话不见了"）。
 *
 * 解析顺序：
 *   1. `MATE_DESKTOP_HOME`——**只给开发与验收用**的显式覆盖（打包态也读；它不改变任何名字，
 *      只是把整棵数据目录指到别处，验收靠它证明"隔离真的发生在 MATE_HOME 里"）；
 *   2. 用户环境里已有的 `MATE_HOME`——它是本仓公开的配置键（配置优先级：进程 `MATE_*` 最高），
 *      桌面入口**照用**，不偷偷改写用户的选择；
 *   3. 默认 `~/.ark-mate`。
 *
 * 把 2 与 3 都显式写出来（而不是留给核心自己默认）的意义：`MATE_HOME` 因此出现在**整条进程链**
 * 的环境里，runtime / MCP 子进程也能看到自己的数据目录在哪——而不是只有核心知道。
 */
export function mateHome(): string {
  const override = process.env.MATE_DESKTOP_HOME?.trim()
  if (override) return resolve(override)
  const fromEnv = process.env.MATE_HOME?.trim()
  if (fromEnv) return resolve(fromEnv)
  const home = process.env.HOME?.trim() || process.env.USERPROFILE?.trim()
  if (!home) {
    // 家目录都拿不到时**明确失败**：数据目录落到别处（如 cwd）会让"我的会话去哪了"变成谜案。
    throw new Error('无法确定数据目录：HOME 未设置，且没有 MATE_HOME / MATE_DESKTOP_HOME')
  }
  return join(resolve(home), '.ark-mate')
}

/** npm 面在数据目录下的落点（设计 §6.6：不污染用户的 `~/.npm`）。 */
export const npmCacheDir = (home = mateHome()) => join(home, 'npm', 'cache')
export const npmPrefixDir = (home = mateHome()) => join(home, 'npm', 'global')

export type ChildEnvironmentExtras = {
  /** 追加/覆盖的键（如载荷入口 `MATE_DSH_ENTRY`，M3-4 填）。**不覆盖** §6.6 的那六项。 */
  extras?: Record<string, string | undefined>
  /** 私有 bin 目录（默认取 `paths.runtimeBinDir()`；测试可显式传）。 */
  binDir?: string
}

/**
 * 构造交给核心（以及经它继承给全部 runtime / MCP 子进程）的环境。
 *
 * 纪律：`extras` 在最后叠加，但 §6.6 的六项**先写、且不允许被覆盖**——否则一个笔误的
 * `extras.PATH` 就能把私有 bin 顶掉，症状是"机器上恰好有 node 时一切正常，干净的机器上起不来"。
 */
export function coreEnvironment(opts: ChildEnvironmentExtras = {}): Record<string, string> {
  const home = mateHome()
  const env = desktopNodeEnvironment({
    executable: process.execPath,
    binDir: opts.binDir ?? runtimeBinDir(),
    base: process.env,
    delimiter,
  })
  env.MATE_HOME = home
  env.npm_config_cache = npmCacheDir(home)
  env.npm_config_prefix = npmPrefixDir(home)
  for (const [key, value] of Object.entries(opts.extras ?? {})) {
    if (key in PROTECTED_ENV_KEYS) continue
    if (typeof value === 'string' && value !== '') env[key] = value
  }
  return env
}

/** §6.6 里由壳负责的键：`extras` 不许改（PATH 尤其）。 */
const PROTECTED_ENV_KEYS: Record<string, true> = {
  ELECTRON_RUN_AS_NODE: true,
  PATH: true,
  MATE_NODE_EXECUTABLE: true,
  MATE_HOME: true,
  npm_config_cache: true,
  npm_config_prefix: true,
}
