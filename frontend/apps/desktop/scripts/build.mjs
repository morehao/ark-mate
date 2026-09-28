#!/usr/bin/env node
/**
 * 桌面壳的 TS → CJS 构建（esbuild，零配置）。
 *
 * 为什么 CJS：**preload 必须是 CommonJS**（Electron 的 preload 在 sandbox 下按 CJS 语义执行），
 * 主进程也一并统一成 CJS，省掉"两个产物两种模块制式"的心智。`electron` 是 external
 * （由 Electron 运行时提供，不能打进产物——它根本不是一个可打包的 JS 模块）。
 *
 * 入口：
 *   src/main.ts        → dist/main.cjs        （package.json 的 main 指向它）
 *   src/preload-app.ts → dist/preload-app.cjs
 *
 * 说明：本轮（M0-5）只做打包；**签名、asar、extraResources 都在 M4-1 的 electron-builder.yml**，
 * 不在这里做——构建产物与安装包是两件事。
 */
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const appRoot = fileURLToPath(new URL('..', import.meta.url))

/** 「壳侧」的两个入口：主进程与 preload（renderer/ 是手写静态页，不进这条构建）。 */
const ENTRIES = [
  { in: 'src/main.ts', out: 'dist/main.cjs' },
  { in: 'src/preload-app.ts', out: 'dist/preload-app.cjs' },
]

for (const e of ENTRIES) {
  const result = await build({
    absWorkingDir: appRoot,
    entryPoints: [e.in],
    outfile: e.out,
    bundle: true,
    platform: 'node',
    // Electron 44 自带 Node 24；target 只用于语法降级，取 Electron 的 Node 主版本。
    target: 'node24',
    format: 'cjs',
    sourcemap: true,
    external: ['electron'],
    // 产物要可读（审计安装包时能直接看内容），不做最小化。
    minify: false,
    logLevel: 'warning',
    metafile: true,
  })
  const bytes = Object.values(result.metafile.outputs)[0]?.bytes ?? 0
  console.log(`[build] ${e.in} → ${e.out}（${bytes} bytes）`)
}
