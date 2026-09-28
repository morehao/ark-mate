/**
 * Electron 作为 Node 的执行通道（M3-2，设计 §5.1–§5.3 / §6.6）。
 *
 * ## 本仓与参照实现的一处**结构差异**（决定了这个模块比参照的 12 行要多想几步）
 *
 * 参照实现里，"直接子进程"就是 ACP runtime——由 Electron 自己 spawn。本仓不是：**ACP runtime 由
 * Go 核心 spawn**（`acl.NewStdioTransport` → `exec.Command(Launch.Command, …)`），核心才是 runtime 的父进程。
 * 于是"让 runtime 跑在 Electron 的 Node 上"这件事，落点不是"壳 spawn runtime"，而是：
 *
 *   1. 壳 spawn 核心时，把设计 §6.6 的环境交给它（本模块 `coreEnvironment()`）；
 *   2. 核心 spawn runtime 时**原样继承**那份环境（Go 侧 `os.Environ()` 的语义），
 *      `PATH` 首段因此是私有 bin、`FORGE_NODE_EXECUTABLE` 因此在场；
 *   3. 私有 bin 里的 `dsh` / `opencode` 启动器把"名字"翻译成"随包载荷 + Electron 的 Node"。
 *
 * 因此 `utilityProcess` 在本仓**没有用武之地**：它是给"Electron 自己 fork 一个 Node 子进程并走
 * Electron IPC"设计的，而我们的 Node 模式子进程（探针 / M3-8 的 smoke）只需要 stdio，
 * 且都要能被同一条 `spawn` 路径驱动以便测试。这是对计划 §7 M3-2 "默认 utilityProcess" 的**有意偏离**，
 * 记在计划 §12.21（D66）。
 */
import { spawn, type ChildProcess } from 'node:child_process'
import { existsSync } from 'node:fs'
import { delimiter } from 'node:path'

import { NODE_ENV_VARS, desktopNodeEnvironment } from '@ark/desktop-contract'

import { runtimeBinDir } from './paths'

/**
 * `resources/runtime/bin` 必须已生成：缺了它，子进程 `PATH` 首段就是一条不存在的路径，
 * 表现会退化成"运行时莫名起不来"。宁可在启动链上明确失败。
 */
export function resolveRuntimeBinDir(): string {
  const dir = runtimeBinDir()
  if (!existsSync(dir)) {
    throw new Error(
      `私有 bin 目录不存在：${dir}\n  先构建：make desktop/runtime-bin（make core/desktop 会自动带上）`,
    )
  }
  return dir
}

/**
 * 起核心用的环境：切 Node 模式 + 私有 bin 前置 + 注入回环地址/资产目录等（设计 §6.6）。
 *
 * `extras` 用来叠加本仓自己的路径变量（载荷入口等，M3-4 填）——**不覆盖** §6.6 的四项。
 */
export function coreEnvironment(extras: Record<string, string | undefined> = {}): Record<string, string> {
  const env = desktopNodeEnvironment({
    executable: process.execPath,
    binDir: resolveRuntimeBinDir(),
    base: process.env,
    delimiter,
  })
  for (const [key, value] of Object.entries(extras)) {
    if (typeof value === 'string' && value !== '') env[key] = value
  }
  return env
}

/**
 * 以 Electron 的 Node 起一个直接子进程（探针 / M3-8 的 runtime smoke）。
 *
 * 与"私有 bin 启动器"是两条等价通道：这条用于**我们自己的**、需要拿到句柄与 stdio 的子进程；
 * 启动器那条用于**别人的**命令（MCP server 通过 `PATH` 上的 `node`/`npx` 找到我们）。
 */
export function spawnNodeMode(entry: string, args: readonly string[] = [], env?: Record<string, string>): ChildProcess {
  return spawn(process.execPath, [entry, ...args], {
    env: env ?? coreEnvironment(),
    stdio: ['ignore', 'pipe', 'pipe'],
  })
}

/** 环境里注入的 Node 可执行文件路径（诊断 / `/meta` 用；未注入时回落到当前进程）。 */
export function nodeExecutableFromEnv(env: Record<string, string | undefined> = process.env): string {
  return env[NODE_ENV_VARS.nodeExecutable] ?? process.execPath
}
