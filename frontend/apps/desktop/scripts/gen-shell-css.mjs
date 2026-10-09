#!/usr/bin/env node
/**
 * 壳静态页的 CSS 变量生成器（C7 的例外场景，唯一允许的"新样式文件"来源）。
 *
 * 背景：`renderer/*.html` 是**壳自己的静态页**（错误页 / 更新对话框 / 欢迎页），不引 antd、
 * 不走 React、不进共享构建——但它们的色值仍必须来自唯一事实源 `packages/ui/src/theme.ts` 的
 * `tokens`（业务代码禁止硬编码色值）。于是：**生成**一份 `renderer/shell-tokens.css`，
 * 生成物入库、禁手改；改了 tokens 就跑本脚本刷新。
 *
 * 为什么直接 import `.ts` 而不是走 `@ark/ui` 的入口：`@ark/ui` 的入口是 React 组件库
 * （引 antd），Node 跑不动；`theme.ts` 自身只有一个 `import type`（可擦除），Node 24 的
 * 原生类型擦除直接吃得下——**零新依赖**。
 *
 * 确定性（C9）：键序 = tokens 声明顺序、不写时间戳、同目录 staging + rename 原子替换。
 * 验收：连跑两次逐字节相同；跑完 `git diff --exit-code` 干净。
 */
import { readFileSync, renameSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { tokens } from '../../../packages/ui/src/theme.ts'

/** camelCase → kebab-case（`radiusSm` → `radius-sm`）。 */
const kebab = (k) => k.replace(/[A-Z]/g, (m) => `-${m.toLowerCase()}`)

/** 数值型 token 目前只有圆角（单位 px）；字符串型原样输出（含 rgba / 渐变 / 字体栈）。 */
const cssValue = (k, v) => {
  if (typeof v === 'number') return `${v}px`
  if (typeof v === 'string') return v
  throw new Error(`tokens.${k} 的类型 ${typeof v} 不是可生成 CSS 的标量（生成器需要扩类型映射）`)
}

const entries = Object.entries(tokens)
if (entries.length === 0) throw new Error('tokens 为空——事实源路径变了？生成器拒绝产出空文件')

const lines = entries.map(([k, v]) => `  --fg-${kebab(k)}: ${cssValue(k, v)};`)
const css = [
  '/* 生成物，禁手改：由 apps/desktop/scripts/gen-shell-css.mjs 从 packages/ui/src/theme.ts 的 tokens 生成。',
  '   壳静态页（renderer/*.html）不引 antd、不走 React，因此只能消费这份 CSS 变量；',
  '   改了 tokens 就重跑脚本（`node apps/desktop/scripts/gen-shell-css.mjs`）。 */',
  ':root {',
  ...lines,
  '}',
  '',
].join('\n')

const out = fileURLToPath(new URL('../renderer/shell-tokens.css', import.meta.url))
const staging = `${out}.staging-${process.pid}`
writeFileSync(staging, css, 'utf8')
renameSync(staging, out)

// 自证：写完后读回逐字节比对（staging 被抢占 / 半写会被这一步抓住）。
if (readFileSync(out, 'utf8') !== css) throw new Error(`生成物读回不一致：${out}`)
console.log(`[gen-shell-css] ${out}（${lines.length} 个 token）`)
