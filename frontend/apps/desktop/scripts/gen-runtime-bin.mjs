#!/usr/bin/env node
/**
 * 生成私有 bin 启动器（M3-2，设计 §5.3 / §6.6）。
 *
 * 产出 `apps/desktop/resources/runtime/bin/`：
 *
 *   node      → 用 Electron 的 Node 执行（`ELECTRON_RUN_AS_NODE=1` + `FORGE_NODE_EXECUTABLE`）
 *   npx       → 同上，再执行载荷里 npm 的 `npx-cli.js`（`FORGE_NPX_ENTRY`）
 *   dsh       → 同上，再执行随包载荷里的 dsh 入口（`FORGE_DSH_ENTRY`）
 *   opencode  → 直接执行随包载荷里的平台化二进制（`FORGE_OPENCODE_BIN`）
 *
 * 为什么是**生成物**而不是手写文件：每个启动器都要同时有 POSIX 与 Windows 两个版本，
 * 且都要做同一件事——"目标没注入就大声失败"。手写的典型结局是两版行为悄悄分叉，
 * 而分叉的表现是"某个平台上运行时起不来"，最难查。生成器是唯一真源，内容逐字节确定。
 *
 * 为什么启动器**自己不搜索**（不 `which`、不猜路径）：设计 §6.5 —— 我们自己的载荷解析
 * 不做 PATH 探测；名字→载荷的翻译由壳在启动期完成并**经环境注入**，启动器只负责转发。
 * 目标缺失时打印谁该注入它并返回 127（"命令找不到"的约定退出码）。
 *
 * 用法：node scripts/gen-runtime-bin.mjs [--out <dir>]
 */

import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { parseArgs } from 'node:util'

const SCRIPT_DIR = import.meta.dirname
const DESKTOP = resolve(SCRIPT_DIR, '..')
const DEFAULT_OUT = join(DESKTOP, 'resources', 'runtime', 'bin')

/** 每个启动器：目标变量 + 怎么把参数交给它。 */
const LAUNCHERS = [
  {
    name: 'node',
    // Node 本身没有入口文件：`node <script.js> …` 直接转发。
    targetVar: 'FORGE_NODE_EXECUTABLE',
    targetLabel: 'Electron 可执行文件（壳启动时注入）',
    nodeMode: true,
    passthroughOnly: true,
  },
  {
    name: 'npx',
    targetVar: 'FORGE_NPX_ENTRY',
    targetLabel: '随包载荷里 npm 的 npx-cli.js（壳按 payload 解析结果注入）',
    nodeMode: true,
  },
  {
    name: 'dsh',
    targetVar: 'FORGE_DSH_ENTRY',
    targetLabel: '随包载荷里 dsh 的 bin.js（壳按 payload 解析结果注入）',
    nodeMode: true,
  },
  {
    name: 'opencode',
    targetVar: 'FORGE_OPENCODE_BIN',
    targetLabel: '随包载荷里 opencode 的平台化二进制（壳按 payload 解析结果注入）',
    // 原生二进制，不需要 Electron 的 Node 模式。
    nodeMode: false,
    passthroughOnly: true,
  },
]

/** 启动器运行前必须由壳注入的变量：Node 模式两个（转发目标 + Node），原生二进制一个。 */
function requiredVars(launcher) {
  const vars = [...(launcher.nodeMode ? ['FORGE_NODE_EXECUTABLE'] : []), launcher.targetVar]
  return vars.filter((v, i, all) => all.indexOf(v) === i)
}

function posixScript(launcher) {
  // 三种转发形态：node 自身（只转发参数）/ Node 模式的入口 JS（node + 入口 + 参数）/ 原生二进制。
  const exec = launcher.nodeMode
    ? `ELECTRON_RUN_AS_NODE=1 exec "$FORGE_NODE_EXECUTABLE" ${launcher.name === 'node' ? '"$@"' : `"$${launcher.targetVar}" "$@"`}`
    : `exec "$${launcher.targetVar}" "$@"`
  return [
    '#!/bin/sh',
    '# 生成物（apps/desktop/scripts/gen-runtime-bin.mjs）——不要手改；改生成器再重新生成。',
    `# 转发到${launcher.targetLabel}：设计 §6.6 / §6.5（启动器自己不做任何探测）。`,
    // Node 模式的启动器要**两个**变量都在场（转发目标 + 用哪个 Node）；缺哪个就报哪个，
    // 否则症状会退化成"exec 空命令"这种看不出原因的失败（实测踩到过）。
    ...requiredVars(launcher).flatMap((v) => [
      `if [ -z "\${${v}:-}" ]; then`,
      `  echo "${launcher.name}: ${v} 未设置——本启动器只能由桌面壳注入环境后使用" >&2`,
      '  exit 127',
      'fi',
    ]),
    exec,
    '',
  ].join('\n')
}

function windowsScript(launcher) {
  const exec = launcher.nodeMode
    ? `"%FORGE_NODE_EXECUTABLE%" ${launcher.name === 'node' ? '%*' : `"%${launcher.targetVar}%" %*`}`
    : `"%${launcher.targetVar}%" %*`
  return [
    '@echo off',
    'rem 生成物（apps/desktop/scripts/gen-runtime-bin.mjs）——不要手改；改生成器再重新生成。',
    `rem 转发到${launcher.targetLabel}：设计 §6.6 / §6.5（启动器自己不做任何探测）。`,
    ...requiredVars(launcher).flatMap((v) => [
        `if "%${v}%"=="" (`,
        `  echo ${launcher.name}: ${v} 未设置——本启动器只能由桌面壳注入环境后使用 1>&2`,
        '  exit /b 127',
        ')',
      ]),
    ...(launcher.nodeMode ? ['set ELECTRON_RUN_AS_NODE=1'] : []),
    exec,
    '',
  ].join('\r\n')
}

function writeIfChanged(path, content, mode) {
  const existing = existsSync(path) ? readFileSync(path, 'utf8') : undefined
  if (existing !== content) writeFileSync(path, content)
  if (mode !== undefined) chmodSync(path, mode)
  return existing !== content
}

function main() {
  const { values } = parseArgs({ options: { out: { type: 'string' } } })
  const out = resolve(values.out ?? DEFAULT_OUT)
  mkdirSync(out, { recursive: true })
  let written = 0
  const names = []
  for (const launcher of LAUNCHERS) {
    if (writeIfChanged(join(out, launcher.name), posixScript(launcher), 0o755)) written++
    if (writeIfChanged(join(out, `${launcher.name}.cmd`), windowsScript(launcher))) written++
    names.push(launcher.name)
  }
  console.log(`私有 bin 已生成：${out}（${names.join(' / ')}，各含 POSIX 与 .cmd 两版；本次写入 ${written} 个文件）`)
}

main()
