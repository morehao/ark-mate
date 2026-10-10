/**
 * 诱饵目录（M3-4 / V10 ③ 的"假底座"）：把 `dsh` / `opencode` / `node` / `npx` 各放一个假货，
 * 被调到就把自己的名字追加到**标记文件**并打印 `FAKE-<NAME>-CALLED`，然后 `exit 0`。
 *
 * 为什么做成"会写标记文件"而不是只看日志：日志里没有 `FAKE-DSH-CALLED` 可能只是因为那段日志没被
 * 我们读到；**标记文件不存在**才是"真的没被调到"的硬证据。
 *
 * 为什么抽成 lib：它有两个消费方——`desktop-ui-acceptance.mjs`（12 个场景的最小 PATH 起点）与
 * `desktop-no-host-acceptance.mjs`（最小 PATH 下真跑一轮 + 重启续聊）。诱饵名单复制两份会漂移，
 * 而**少一个名字就等于少一条判据**，且是静默的。
 *
 * 用法：
 *   const decoy = installDecoyBin()
 *   const env = { PATH: decoy.path, HOME: probeHome, ... }
 *   // ... 跑完之后：
 *   decoy.calledNames()   // [] = 一次都没被调到（`[]` 才是绿）
 *   decoy.cleanup()       // 收尾
 */
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

/**
 * 诱饵名单：两个底座 + Node 本体 + 包管理器转发器。
 *
 * 这四样正是"宿主开发者环境"的判据面：桌面壳若去问本机（`which` / `command -v` / PATH 搜索），
 * 撞上的就是它们。名单变了要同步 `backend/internal/archtest/desktop_host_path_test.go` 的静态面
 * （那条守卫禁的是"写死宿主路径"，与本表配对）。
 */
export const DECOY_NAMES = ['dsh', 'opencode', 'node', 'npx']

/** 系统目录基线（macOS/Linux 都有）：诱饵之外的 PATH 只留它。 */
export const SYSTEM_PATH = '/usr/bin:/bin'

/**
 * 装一份诱饵目录。返回 `{ binDir, marker, path, calledNames(), cleanup() }`。
 *
 * `path` 就是把诱饵目录插在**最前面**的 PATH 值——链上任何"探测本机"的写法都会先撞上它。
 */
export function installDecoyBin({ names = DECOY_NAMES, prefix = 'mate-fake-bin-' } = {}) {
  const binDir = mkdtempSync(join(tmpdir(), prefix))
  const marker = join(binDir, 'called')
  for (const name of names) {
    writeFileSync(
      join(binDir, name),
      `#!/bin/sh\necho "${name}" >> "${marker}"\necho "FAKE-${name.toUpperCase()}-CALLED"\nexit 0\n`,
      { mode: 0o755 },
    )
  }
  return {
    binDir,
    marker,
    path: `${binDir}:${SYSTEM_PATH}`,
    /** 标记文件里的名字（空数组 = 一次都没被调到）。失败信息里直接打印它。 */
    calledNames: () =>
      existsSync(marker)
        ? readFileSync(marker, 'utf8')
            .split('\n')
            .map((s) => s.trim())
            .filter(Boolean)
        : [],
    cleanup: () => rmSync(binDir, { recursive: true, force: true }),
  }
}
