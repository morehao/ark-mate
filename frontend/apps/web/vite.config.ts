import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { execFileSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

/**
 * 前端构建 id（M2-4，设计 §6.7）。
 *
 * 值 = `git rev-parse --short=12 HEAD`（脏工作区加 `-dirty` 后缀）。它有两个去处，**必须是同一个值**：
 *   1. 编译期 define 进 bundle → 页面里的 `__FORGE_WEB_BUILD_ID__`；
 *   2. 写进产物的 `dist/build-id.txt` → 核心启动时从**实际服务的那份资产**里读出来，
 *      经 `/meta` 的 `webBuildId` 回报。
 *
 * 于是页面对比"编译进 bundle 的常量"与"核心实际读到的文件"，就能抓出 index 来自 A、
 * assets 来自 B 这类**混搭**——这类问题在"看界面没报错"的层面完全隐形，只会表现为零星怪现象。
 *
 * 允许 `FORGE_WEB_BUILD_ID` 覆盖（打包流水线在没有 .git 的构建容器里用得上）；
 * 拿不到 git 就回 `unknown`——**不阻断构建**，与核心侧"读不到就 unknown"同一口径。
 */
function resolveBuildId(): string {
  const fromEnv = process.env.FORGE_WEB_BUILD_ID?.trim()
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

/** 把 build id 写进产物（核心要读它；dev 不写——dev 由 vite 内存服务，没有"产物"可读）。 */
function forgeBuildIdPlugin() {
  return {
    name: 'forge-build-id',
    apply: 'build' as const,
    writeBundle(options: { dir?: string }) {
      const outDir = options.dir ?? resolve(process.cwd(), 'dist')
      writeFileSync(join(outDir, 'build-id.txt'), `${BUILD_ID}\n`, 'utf8')
    },
  }
}

export default defineConfig({
  plugins: [react(), forgeBuildIdPlugin()],
  define: {
    // 唯一注入点：业务代码只读 `__FORGE_WEB_BUILD_ID__`（声明见 src/env.d.ts）。
    __FORGE_WEB_BUILD_ID__: JSON.stringify(BUILD_ID),
  },
  build: {
    outDir: 'dist',
  },
  server: {
    // dev：面板与后端 forge serve（默认 127.0.0.1:10001）同源代理联调
    proxy: {
      '/v1': {
        target: 'http://127.0.0.1:10001',
        changeOrigin: true,
        ws: true, // /v1/forge/ws/events 升级为 WS
      },
    },
  },
})
