#!/usr/bin/env node
/**
 * 装配 ark-mate 自带的 `primary-runtime` 载荷（bundled Python/Node + Office 库）。
 *
 * 目的：让 ark-mate **不依赖 DSH 桌面端**也能给 dsh 的 office/依赖能力提供运行时——
 * 该载荷落在 `resources/payloads/primary-runtime/`，运行期由 dsh.ResolveBundledDirs
 * 从 `MATE_DSH_ENTRY` 推到它并注入 DSH_PRIMARY_RUNTIME。
 *
 * 实现：复用 DeepSeek Harness 的 `prepare.ts`（已 vendor 到 `scripts/primary-runtime/`，
 * 含 lock.json / prune-python-tests.ts / smoke.py / npm-platform.ts）。按 lock.json 的固定版本
 * 下载 python-build-standalone + Node + wheels，解包、装库、生成 runtime.json——可复现、
 * 构建期不碰宿主机环境。
 *
 * 为什么用 import 而不是把 prepare.ts 当 CLI 直接 `node prepare.ts`：prepare.ts 的 CLI 分支依赖
 * `import.meta.main`（Bun/Deno 专有，Node 无此属性），直接跑会静默 exit 13。这里改为
 * import 其导出的 `preparePrimaryRuntime` 显式调用。
 *
 * 依赖解析：
 *  - `extract-zip` / `tar` / `papaparse`：apps/desktop 的 devDependencies；
 *  - `@deepseek-ai/dsh-tool-workspace-dependencies`：只随 dsh 载荷分发，运行前软链到
 *    `apps/desktop/node_modules/@deepseek-ai/` 供 prepare.ts 解析。
 *
 * 用法：
 *   node scripts/prepare-primary-runtime.mjs [--target mac-arm64] [--output <dir>] [--cache <dir>] [--skip-smoke]
 */
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, rmSync, symlinkSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url))
const DESKTOP = resolve(SCRIPT_DIR, '..')
const BUILD_DIR = join(SCRIPT_DIR, 'primary-runtime')

function fail(msg) {
  console.error(`prepare-primary-runtime: ${msg}`)
  process.exit(1)
}

function defaultTarget() {
  const platform = process.platform === 'win32' ? 'win' : process.platform === 'darwin' ? 'mac' : 'linux'
  const arch = process.arch === 'arm64' ? 'arm64' : 'x64'
  return `${platform}-${arch}`
}

// 把 @deepseek-ai/dsh-tool-workspace-dependencies（只在 dsh 载荷里）软链到 apps/desktop/node_modules，
// 好让 vendored prepare.ts 的 import 解析到它。
function ensureWorkspaceDependenciesResolvable() {
  const src = join(DESKTOP, 'runtime', 'dsh', 'node_modules', '@deepseek-ai', 'dsh-tool-workspace-dependencies')
  if (!existsSync(src)) fail(`找不到 dsh 载荷里的 dsh-tool-workspace-dependencies：${src}（先跑 make desktop/payloads）`)
  const linkDir = join(DESKTOP, 'node_modules', '@deepseek-ai')
  mkdirSync(linkDir, { recursive: true })
  const link = join(linkDir, 'dsh-tool-workspace-dependencies')
  if (!existsSync(link)) {
    try {
      symlinkSync(src, link, 'dir')
    } catch (e) {
      if (e.code !== 'EEXIST') throw e
    }
  }
}

async function main() {
  const { values } = parseArgs({
    options: {
      target: { type: 'string', default: defaultTarget() },
      output: { type: 'string' },
      cache: { type: 'string' },
      'skip-smoke': { type: 'boolean', default: false },
    },
  })
  ensureWorkspaceDependenciesResolvable()
  // 动态 import vendored prepare.ts（strip-types 直跑 .ts）。
  const mod = await import(join(BUILD_DIR, 'prepare.ts'))
  const lock = (await import(join(BUILD_DIR, 'lock.json'), { with: { type: 'json' } })).default
  if (!Object.hasOwn(lock.targets, values.target)) {
    fail(`target ${values.target} 不在 lock.json（可选：${Object.keys(lock.targets).join(' | ')}）`)
  }
  const output = resolve(values.output ?? join(DESKTOP, 'resources', 'payloads'))
  const cache = resolve(values.cache ?? join(DESKTOP, '.cache', 'primary-runtime-downloads'))
  mkdirSync(output, { recursive: true })
  mkdirSync(cache, { recursive: true })
  rmSync(join(output, 'primary-runtime'), { recursive: true, force: true })

  // office skill assets：优先用 dsh 载荷里的（ark-mate 自带），避免依赖 workspace 里装没装该包。
  const skillSource = join(DESKTOP, 'runtime', 'dsh', 'node_modules', '@deepseek-ai', 'dsh-skill-office', 'assets')

  const version = process.env.MATE_VERSION ?? '0.0.0'
  console.log(`prepare-primary-runtime: 装配 ${values.target} → ${output}（office assets: ${skillSource}）`)
  await mod.preparePrimaryRuntime({
    target: values.target,
    output,
    cache,
    version,
    skillSource: existsSync(skillSource) ? skillSource : undefined,
  })
  const rt = join(output, 'primary-runtime', 'runtime.json')
  if (!existsSync(rt)) fail(`装配后缺少 runtime.json：${rt}`)

  if (!values['skip-smoke']) {
    // 目标架构与本机一致时实跑解释器/库；否则跳过（smokePrimaryRuntime 内部已判断 platform/arch）。
    try {
      mod.smokePrimaryRuntime(join(output, 'primary-runtime'))
    } catch (e) {
      fail(`冒烟失败：${e?.message ?? e}`)
    }
  }
  console.log(`prepare-primary-runtime: 完成，runtime.json = ${rt}`)
}

main().catch((e) => fail(e?.stack ?? String(e)))
