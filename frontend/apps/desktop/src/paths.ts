/**
 * 桌面运行时树里各类路径的解析（M1-3；打包期布局在 M4-1 落地）。
 *
 * 两种形态的差别只有"根在哪"，**不改变任何名字**（设计 §6.1 最后一条：显式覆盖只出现在
 * 「未打包」与「打包层」，核心自己既不推测也不搜索）：
 *
 *   - 未打包（`electron .` 跑源码）：一切都在仓库里——壳的 renderer 在 `renderer/`，
 *     前端产物在 `../web/dist`，核心二进制看 `MATE_DESKTOP_CORE` 或 `resources/mate-core`；
 *   - 已打包：`process.resourcesPath` 下是 `web/`（= apps/web/dist 的那一份）、`mate-core`、
 *     `renderer/`（随 asar 内的文件，路径按 `app.getAppPath()` 解）。
 *
 * 为什么不用"多级回落搜索"：路径算错的代价必须是**明确失败**，而不是悄悄用了别处的资产——
 * 那正是"用户看到旧界面却以为新版本已生效"的来源（设计 §6.1 的失败语义）。
 */
import { app } from 'electron'
import { existsSync } from 'node:fs'
import { join, resolve } from 'node:path'

/** 壳静态页目录（welcome / boot-failed / 后续的更新对话框）。 */
export function rendererDir(): string {
  return join(app.getAppPath(), 'renderer')
}

/**
 * 前端产物目录：壳服务 `/`、`/index.html`、`/assets/*` 时读它，**核心也用 `--web-dir` 读同一份**。
 *
 * 两份资产是设计 §6.1 的红线：核心不内嵌（桌面骨架不带 `-tags release`），壳也不在运行时树里
 * 另放一份——否则"页面里的 build id"与"核心实际读到的 build_id"就来自两处，版本闸门（M2-4）
 * 也就失去了意义。
 *
 * 未打包态默认指向运行时树里的 `resources/web`（`make core/desktop` 从 apps/web/dist 刷新），
 * 可用 `MATE_DESKTOP_WEB_DIR` 覆盖（只给开发用；打包态不读任何环境变量）。
 */
export function webDir(): string {
  if (!app.isPackaged) {
    const override = process.env.MATE_DESKTOP_WEB_DIR
    if (override) return resolve(override)
    return resolve(app.getAppPath(), 'resources', 'web')
  }
  return join(process.resourcesPath, 'web')
}

/**
 * 私有 bin 目录（`resources/runtime/bin`，M3-2）：里面是**生成物**启动器
 * （`node` / `npx` / `dsh` / `opencode`，Windows 另有 `.cmd`），由 `scripts/gen-runtime-bin.mjs` 产出。
 *
 * 它只做一件事：被壳前置到子进程 `PATH` 的最前面（设计 §6.6），让核心与其子进程（MCP server、
 * 用户脚本、我们的载荷）在"机器上没有 node"时也能拿到 Electron 的 Node 与随包载荷。
 * **不改用户环境**——只影响我们 spawn 出去的那条进程链。
 */
export function runtimeBinDir(): string {
  if (!app.isPackaged) return resolve(app.getAppPath(), 'resources', 'runtime', 'bin')
  return join(process.resourcesPath, 'runtime', 'bin')
}

/**
 * 随包运行时载荷根（`resources/payloads/<name>/<version>/`，M3-1 装配、M3-4 解析）。
 *
 * 解析链（显式配置 → 随包载荷 → PATH）与"命中哪一级"的回报都在 M3-4 落地；这里只给根路径，
 * 与 `webDir()`/`coreExecutable()` 同款：未打包看仓库、打包看 `process.resourcesPath`，
 * **不做多级回落搜索**（路径算错要明确失败，而不是悄悄用别处的资产）。
 */
export function payloadsDir(): string {
  if (!app.isPackaged) return resolve(app.getAppPath(), 'resources', 'payloads')
  return join(process.resourcesPath, 'payloads')
}

/**
 * 桌面核心二进制（`mate-core`：**不带**内嵌资产，前端资产由壳以 `--web-dir` 指过来）。
 *
 * 未打包态按 `MATE_DESKTOP_CORE` → `resources/mate-core`（`make core/desktop` 的产物）找；
 * 找不到就抛错（不回落 `PATH` 上的 `mate`：那是独立形态的二进制，内嵌了资产，
 * 用它会让桌面态出现"两份 dist"，正是设计 §6.1 要避免的）。
 */
export function coreExecutable(): string {
  if (!app.isPackaged) {
    const override = process.env.MATE_DESKTOP_CORE
    if (override) {
      const p = resolve(override)
      // 显式覆盖也要**先看存在性**：否则错误要等到 spawn 的异步 ENOENT 才出现，
      // 中间那段窗口是"什么都没发生"（恢复页因此晚一步）。
      if (!existsSync(p)) throw new Error(`MATE_DESKTOP_CORE 指向的二进制不存在：${p}`)
      return p
    }
    const dev = resolve(app.getAppPath(), 'resources', 'mate-core')
    if (existsSync(dev)) return dev
    throw new Error(
      `找不到桌面核心二进制：${dev}\n` +
        '  先构建：make core/desktop（或设 MATE_DESKTOP_CORE 指向已有二进制）',
    )
  }
  return join(process.resourcesPath, 'mate-core')
}
