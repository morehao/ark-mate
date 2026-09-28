/**
 * 桌面运行时树里各类路径的解析（M1-3；打包期布局在 M4-1 落地）。
 *
 * 两种形态的差别只有"根在哪"，**不改变任何名字**（设计 §6.1 最后一条：显式覆盖只出现在
 * 「未打包」与「打包层」，核心自己既不推测也不搜索）：
 *
 *   - 未打包（`electron .` 跑源码）：一切都在仓库里——壳的 renderer 在 `renderer/`，
 *     前端产物在 `../web/dist`，核心二进制看 `FORGE_DESKTOP_CORE` 或 `resources/forge-core`；
 *   - 已打包：`process.resourcesPath` 下是 `web/`（= apps/web/dist 的那一份）、`forge-core`、
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
 * 可用 `FORGE_DESKTOP_WEB_DIR` 覆盖（只给开发用；打包态不读任何环境变量）。
 */
export function webDir(): string {
  if (!app.isPackaged) {
    const override = process.env.FORGE_DESKTOP_WEB_DIR
    if (override) return resolve(override)
    return resolve(app.getAppPath(), 'resources', 'web')
  }
  return join(process.resourcesPath, 'web')
}

/**
 * 桌面核心二进制（`forge-core`：**不带**内嵌资产，前端资产由壳以 `--web-dir` 指过来）。
 *
 * 未打包态按 `FORGE_DESKTOP_CORE` → `resources/forge-core`（`make core/desktop` 的产物）找；
 * 找不到就抛错（不回落 `PATH` 上的 `forge`：那是独立形态的二进制，内嵌了资产，
 * 用它会让桌面态出现"两份 dist"，正是设计 §6.1 要避免的）。
 */
export function coreExecutable(): string {
  if (!app.isPackaged) {
    const override = process.env.FORGE_DESKTOP_CORE
    if (override) {
      const p = resolve(override)
      // 显式覆盖也要**先看存在性**：否则错误要等到 spawn 的异步 ENOENT 才出现，
      // 中间那段窗口是"什么都没发生"（恢复页因此晚一步）。
      if (!existsSync(p)) throw new Error(`FORGE_DESKTOP_CORE 指向的二进制不存在：${p}`)
      return p
    }
    const dev = resolve(app.getAppPath(), 'resources', 'forge-core')
    if (existsSync(dev)) return dev
    throw new Error(
      `找不到桌面核心二进制：${dev}\n` +
        '  先构建：make core/desktop（或设 FORGE_DESKTOP_CORE 指向已有二进制）',
    )
  }
  return join(process.resourcesPath, 'forge-core')
}
