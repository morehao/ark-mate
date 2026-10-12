#!/usr/bin/env node
/**
 * 生成私有 bin 启动器（M3-2，设计 §5.3 / §6.6）。
 *
 * 产出 `apps/desktop/resources/runtime/bin/`：
 *
 *   node      → 用 Electron 的 Node 执行（`ELECTRON_RUN_AS_NODE=1` + `MATE_NODE_EXECUTABLE`）
 *   npx       → 同上，再执行载荷里 npm 的 `npx-cli.js`（`MATE_NPX_ENTRY`）
 *   dsh       → 同上，再执行随包载荷里的 dsh 入口（`MATE_DSH_ENTRY`）
 *
 * **新增一个底座 = 在 LAUNCHERS 里加一条**（名字 + 转发目标变量 + 它自己的隔离兜底表）；
 * 下面的生成逻辑与两个 shell 模板都不必改。这是 adapter 层在桌面这侧的落点。
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
    targetVar: 'MATE_NODE_EXECUTABLE',
    targetLabel: 'Electron 可执行文件（壳启动时注入）',
    nodeMode: true,
    passthroughOnly: true,
  },
  {
    name: 'npx',
    targetVar: 'MATE_NPX_ENTRY',
    targetLabel: '随包载荷里 npm 的 npx-cli.js（壳按 payload 解析结果注入）',
    nodeMode: true,
  },
  {
    name: 'dsh',
    targetVar: 'MATE_DSH_ENTRY',
    targetLabel: '随包载荷里 dsh 的 bin.js（壳按 payload 解析结果注入）',
    nodeMode: true,
    // dsh 自己的**数据面**（设计 §6.6.1）：**必须在启动器里兜底**，因为 dsh 连 `--version` 都会写自己的 home
    // （实测：不隔离时它往用户的 `~/.dsh` 里写了日志）。核心拉 ACP runtime 时会显式注入同名变量，
    // 那时这里的 `${VAR:=…}` 不会覆盖显式值——两条路径得到同一份 home，只是来源不同。
    //
    // **刻意不兜底 `DSH_AGENTS_HOME`**：它不是 dsh 的私有数据面，而是**用户全局的 skill/agents 面**
    // ——平台把 skill 投射到 `${HOME}/.agents/skills`（Go 侧 adapter 的投影声明），而 dsh 按
    // `config.agentsHome ?? $DSH_AGENTS_HOME ?? ~/.agents` 解析该根。这里一兜底，桌面态 dsh 的读根
    // 就与平台写根分叉、skill **静默不投放**（2026-10-09 桌面态验收实测：平台物化的 `probe-desktop`
    // 不在 dsh 会话的 `<available_skills>` 里，而子进程环境里正是这个变量）。Go 侧
    // `pkg/runtime/isolation.go` 曾犯同一个错、同批修掉；两侧由 archtest 的
    // `TestDesktopLauncherIsolationMatchesCore` 钉住"启动器隔离表 ⊆ 核心隔离表"，且**核心新增的变量
    // 必须二选一**：在这里兜底，或在那个守卫的 `launcherCoreOnly` 里登记"启动器刻意不兜底 + 理由"。
    isolation: { DSH_HOME: 'runtime/dsh' },
  },
]

/** 启动器运行前必须由壳注入的变量：Node 模式两个（转发目标 + Node），原生二进制一个。 */
function requiredVars(launcher) {
  const vars = [
    ...(launcher.nodeMode ? ['MATE_NODE_EXECUTABLE'] : []),
    ...(launcher.isolation ? ['MATE_HOME'] : []),
    launcher.targetVar,
  ]
  return vars.filter((v, i, all) => all.indexOf(v) === i)
}

/**
 * 兜底导出隔离变量（只在未设置时赋值，绝不覆盖核心/壳显式注入的值）。
 *
 * **空串是有语义的、不是"没写"**：`isolation` 表里取值写成 `''` 的键，语义是"清掉用户 shell 里
 * 可能存在的同名变量"（JS 侧 `''` 是 falsy，等价于"没设"）。所以这里必须按空值走**另一条分支**，
 * 绝不能拿 `$MATE_HOME/` 去兜底：`<KEY>=$MATE_HOME/` 会让底座把**一个目录**当成配置文件去读
 * （实测 `EISDIR`，桌面态的 skill 验收就是这么抓到它的——2026-10-10），而"内联配置内容"类的键
 * 则是把路径当内容，比目录那条更隐蔽。
 *
 * 当前**没有**任何 launcher 用到空值（dsh 的隔离面全是路径）。这条分支与它下面那段判据刻意留着：
 * 它是**机制**，不是为某个底座写的，删掉之后下一个需要清空变量的底座会把空值并进普通分支，
 * 而那个错在静态守卫那侧只比键名、比不出来。
 *
 * 早先版本的 `: "${VAR:=$MATE_HOME/${sub}}"` 对所有键一视同仁，正是把"空的语义"机械地翻译成了
 * "兜底成 $MATE_HOME/"——这类错误的形态**很坏**：启动器照常 exec，底座才在内部报一个看不出
 * 因果的错（`Config.load` 里一句 EISDIR）。
 */
function isolationLines(launcher, style) {
  const entries = Object.entries(launcher.isolation ?? {})
  if (!entries.length) return []
  return entries.map(([key, sub]) => {
    if (sub === '') {
      // 空值 = **清除**（unset 比空串更彻底：连"存在但为空"都不留给下游判断）。
      return style === 'posix' ? `unset ${key}` : `set "${key}="`
    }
    return style === 'posix'
      ? `: "\${${key}:=$MATE_HOME/${sub}}" ; export ${key}`
      : `if not defined ${key} set "${key}=%MATE_HOME%/${sub.replace(/\//g, '\\')}"`
  })
}

function posixScript(launcher) {
  // 三种转发形态：node 自身（只转发参数）/ Node 模式的入口 JS（node + 入口 + 参数）/ 原生二进制。
  const exec = launcher.nodeMode
    ? `ELECTRON_RUN_AS_NODE=1 exec "$MATE_NODE_EXECUTABLE" ${launcher.name === 'node' ? '"$@"' : `"$${launcher.targetVar}" "$@"`}`
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
    ...isolationLines(launcher, 'posix'),
    exec,
    '',
  ].join('\n')
}

function windowsScript(launcher) {
  const exec = launcher.nodeMode
    ? `"%MATE_NODE_EXECUTABLE%" ${launcher.name === 'node' ? '%*' : `"%${launcher.targetVar}%" %*`}`
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
