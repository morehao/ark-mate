import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import { execFileSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

/**
 * 唯一资产路径的兜底（2026-10-11）：**`vite dev server` 起不来，这是刻意的**。
 *
 * 为什么把一条开发便利判死（理由同 `scripts/dev-loop.mjs` 的文件头，也是参照实现 DSH
 * `apps/web/vite.config.ts` 的 `rejectStandaloneServe` 做的事）：dev server 从源码**现编译**，
 * 与交付的 `apps/web/dist` 不是同一次构建。于是"人判断有没有问题时依据的资产"与"用户加载的
 * 资产"分叉——CSP 三条限制不会被下发、`build-id.txt` 不存在（版本闸门只能判"不可判定"）、
 * antd 会多打 `css-dev-only-*` 类名、React 的开发期警告还在。留一条这样的通路，就是留一条漂移通路；
 * 而"改 UI 走它最快"的那点便利，换不回"我验的不是交付那份"。
 *
 * 改 UI 走 `make dev`：它跑 `scripts/dev-loop.mjs`（构建一次 → `vite build --watch` 重建 →
 * 核心服务同一份产物），浏览器开 `http://127.0.0.1:10001`。
 */
const STANDALONE_SERVE_ERROR =
  'apps/web 不是可独立启动的应用：vite dev server 会从源码现编译，与交付的 apps/web/dist 不是同一次构建，' +
  '于是"你验的"与"交付的"分叉（无 CSP / 无 build-id.txt / antd 多打 css-dev-only-* 类名）。' +
  '改 UI 请走 `make dev`（= scripts/dev-loop.mjs：构建一次 → vite build --watch → 核心服务同一份产物），' +
  '浏览器打开 http://127.0.0.1:10001。'

function rejectStandaloneServe(): Plugin {
  return {
    name: 'mate-reject-standalone-serve',
    config(_config, env) {
      if (env.command === 'serve') throw new Error(STANDALONE_SERVE_ERROR)
    },
  }
}

/**
 * 前端构建 id（M2-4，设计 §6.7）。
 *
 * 值 = `git rev-parse --short=12 HEAD`（脏工作区加 `-dirty` 后缀）。它有两个去处，**必须是同一个值**：
 *   1. 编译期 define 进 bundle → 页面里的 `__MATE_WEB_BUILD_ID__`；
 *   2. 写进产物的 `dist/build-id.txt` → 核心启动时从**实际服务的那份资产**里读出来，
 *      经 `/meta` 的 `webBuildId` 回报。
 *
 * 于是页面对比"编译进 bundle 的常量"与"核心实际读到的文件"，就能抓出 index 来自 A、
 * assets 来自 B 这类**混搭**——这类问题在"看界面没报错"的层面完全隐形，只会表现为零星怪现象。
 *
 * 允许 `MATE_WEB_BUILD_ID` 覆盖（打包流水线在没有 .git 的构建容器里用得上）；
 * 拿不到 git 就回 `unknown`——**不阻断构建**，与核心侧"读不到就 unknown"同一口径。
 */
function resolveBuildId(): string {
  const fromEnv = process.env.MATE_WEB_BUILD_ID?.trim()
  if (fromEnv) return fromEnv
  // 从 apps/web 往上数两级 = frontend/：只把前端树的改动算作 dirty，
  // 否则仓库里任何无关改动（文档、后端）都会让每次构建都带 -dirty，这个标记就没信息量了。
  const frontendRoot = resolve(process.cwd(), '..', '..')
  try {
    const head = execFileSync('git', ['rev-parse', '--short=12', 'HEAD'], { encoding: 'utf8' }).trim()
    const dirty =
      execFileSync('git', ['status', '--porcelain', '--', frontendRoot], { encoding: 'utf8' }).trim() !== ''
    return dirty ? `${head}-dirty` : head
  } catch {
    return 'unknown'
  }
}

const BUILD_ID = resolveBuildId()

/** 把 build id 写进产物（核心要读它）。它只在 `build` 生效——而**开发态也走 build**（见上方唯一资产路径）。 */
function mateBuildIdPlugin() {
  return {
    name: 'mate-build-id',
    apply: 'build' as const,
    writeBundle(options: { dir?: string }) {
      const outDir = options.dir ?? resolve(process.cwd(), 'dist')
      writeFileSync(join(outDir, 'build-id.txt'), `${BUILD_ID}\n`, 'utf8')
    },
  }
}

export default defineConfig({
  plugins: [rejectStandaloneServe(), react(), mateBuildIdPlugin()],
  define: {
    // 唯一注入点：业务代码只读 `__MATE_WEB_BUILD_ID__`（声明见 src/env.d.ts）。
    __MATE_WEB_BUILD_ID__: JSON.stringify(BUILD_ID),
  },
  build: {
    outDir: 'dist',
  },
  // 这里原先有一段 `server.proxy`（把 /v1 代理到 127.0.0.1:10001 联调 dev server）。
  // `serve` 已被上方判死，它成了会误导人的死配置：**核心自己服务这份产物**，页面与接口同源，
  // 既不需要代理，也不需要给核心登记 `MATE_ALLOWED_ORIGINS`。
})
