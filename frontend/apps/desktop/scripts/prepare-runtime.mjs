#!/usr/bin/env node
/**
 * 装配桌面随包运行时载荷（M3-1）。
 *
 * 产出：`apps/desktop/resources/payloads/<name>/<version>/`
 *   - `dsh/<version>/`      dsh 的**生产依赖闭包**（含 npm/npx 等 JS 部分，见设计 §6.3）
 *
 * **新增一个随包底座 = 在下面 `PAYLOADS` 里加一条**（外加 `runtime/<dir>/` 的 package.json
 * 与锁文件）。装配、确定性、清单写入这些逻辑都不认识任何具体底座。
 * 每份载荷旁 `payload.json`：`{name, version, platform, arch, nodeAbi, entry, digest, files, bytes, fileList, fileListBase}`。
 *   `fileListBase` 说明 fileList 的相对路径相对于载荷根下的哪一层（M4-5 起写入；`""` = 载荷根本身）。
 *
 * ## 三条硬纪律
 *
 * 1. **版本只认锁文件里的精确 pin**：载荷依赖面写在 `runtime/<name>/package.json`（精确版本、不带 ^/~），
 *    解析结果由同目录 `pnpm-lock.yaml` 钉死（含 integrity）。脚本装完后复核"装出来的版本 === pin"，
 *    对不上直接失败；出现非精确 specifier 也失败。**任何地方都不查 `latest`。**
 * 2. **确定性**：两次装配产出的 `payload.json` 逐字节相同——字段顺序固定、无时间戳、逐文件排序、
 *    原子写（先写临时文件再 rename），装配结果整体原子换入（不会留下半份载荷）。
 * 3. **构建期不依赖用户环境**：node 用 `process.execPath`、pnpm 用 `require.resolve` 从仓内解析
 *    （**都不走 PATH**），因此 `env -i PATH=/usr/bin:/bin <绝对路径 node> prepare-runtime.mjs` 也能跑通。
 *    这条同时是"桌面态不探测本机安装"（M3-4）在构建侧的对照面。
 *
 * 用法：
 *   node scripts/prepare-runtime.mjs [--platform darwin|win32|linux] [--arch arm64|x64]
 *                                    [--payload <name>] [--skip-install] [--offline] [--abi <n>]
 *
 *   `--skip-install --abi <n>` 是**离线重放**：不碰 pnpm store，只从 `runtime/<name>/node_modules`
 *   已装好的树重新产出清单（用于复算/核对，或改了清单字段后重建）。重放**必须**显式给 --abi，
 *   否则会写进本机 Node 的 ABI（与 Electron 的不一样，会被交叉校验抓出来）。
 *
 * 环境变量（都可选）：`MATE_PAYLOAD_REGISTRY`（CI 镜像源）、`MATE_PAYLOAD_STORE_DIR`（pnpm store）。
 */

import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, dirname, join, resolve } from 'node:path'
import { parseArgs } from 'node:util'

import { copyTreeFiltered, formatBytes, swapIntoPlace, writeManifestAtomic } from './lib/payload.mjs'

const require = createRequire(import.meta.url)
const SCRIPT_DIR = import.meta.dirname
const DESKTOP = resolve(SCRIPT_DIR, '..')
const RUNTIME_ROOT = join(DESKTOP, 'runtime')
const PAYLOAD_OUT = join(DESKTOP, 'resources', 'payloads')

/**
 * 载荷表：每份载荷对应 `runtime/<dir>` 下一个独立的工作区（自带 package.json + lockfile）。
 *
 * `entry` 是"交给 Electron 的 Node 去执行的那个文件"（M3-2 的私有 bin 转发目标）——
 * 写进清单是因为**载荷形态不该让运行期去猜**（猜错的症状是"装是装上了，起不来"）。
 */
const PAYLOADS = [
  {
    name: 'dsh',
    dir: 'dsh',
    // dsh 是纯 JS 闭包：入口是包内的 bin.js，由 Electron 的 Node 执行。
    dependency: () => '@deepseek-ai/dsh',
    entry: (dep) => `node_modules/${dep}/lib/bin.js`,
    source: 'node_modules',
    // 载荷根下必须留一层 `node_modules/`：Node 的模块解析是"向上找 node_modules 目录"，
    // 把闭包平铺在载荷根上（没有这一层）会让包内的 `require('commander')` 全部找不到。
    destSubdir: 'node_modules',
  },
]

function fail(message) {
  console.error(`prepare-runtime: ${message}`)
  process.exit(1)
}

function readJson(path) {
  return JSON.parse(readFileSync(path, 'utf8'))
}

/** Electron 的 Node ABI（`process.versions.modules`）——M3-7 的 engines 断言与 release.json 都要用。 */
function electronNodeAbi() {
  let executable
  try {
    // 该包在纯 Node 上下文里导出 Electron 可执行文件的绝对路径。
    executable = require('electron')
  } catch (e) {
    fail(`解析 electron 可执行文件失败（apps/desktop 的 devDependencies 装了吗？）：${e.message}`)
  }
  if (typeof executable !== 'string') fail('electron 包没有导出可执行文件路径')
  const out = execFileSync(executable, ['-p', 'process.versions.modules'], {
    encoding: 'utf8',
    // Electron 的 Node 模式：不加载 Chromium，只当一个 node 用（M3-2 的同一条通道）。
    env: { ...process.env, ELECTRON_RUN_AS_NODE: '1' },
  })
  const abi = out.trim()
  if (!/^\d+$/.test(abi)) fail(`Electron 报告的 Node ABI 不像数字：${JSON.stringify(abi)}`)
  return abi
}

/** 用仓内解析出来的 pnpm（不走 PATH）装一份载荷的生产闭包。 */
function installPayload(payloadDir, target, { offline }) {
  // pnpm 的 exports 只开了 "." → 直接 require.resolve('pnpm/bin/pnpm.cjs') 会 ERR_PACKAGE_PATH_NOT_EXPORTED；
  // 先解析包本身，再从包目录取 bin（不依赖那个映射怎么开）。
  let pnpmEntry
  try {
    pnpmEntry = join(dirname(require.resolve('pnpm')), 'bin', 'pnpm.cjs')
  } catch (e) {
    fail(`解析仓内 pnpm 失败（apps/desktop 的 devDependencies 里应有 pnpm）：${e.message}`)
  }
  if (!existsSync(pnpmEntry)) fail(`仓内 pnpm 入口不存在：${pnpmEntry}`)
  const args = [
    pnpmEntry,
    'install',
    '--prod',
    '--frozen-lockfile',
    '--reporter=append-only',
    // 目标平台从命令行下发：镜像里同时存在多平台原生包，不指定就会把全平台都装进来。
    `--config.supportedArchitectures.os=${target.platform}`,
    `--config.supportedArchitectures.cpu=${target.arch}`,
    // 模块目录与当前配置不符时（`.modules.yaml` 里记着上次用的 storeDir / nodeLinker，这次解析出来的
    // 不一样）pnpm 会先把 `runtime/<name>/node_modules` 整个删掉重装，并且**默认要人在 TTY 上确认**——
    // 构建步骤没有 TTY，于是直接 `ERR_PNPM_ABORTED_REMOVE_MODULES_DIR_NO_TTY` 中止（2026-10-09 实测：
    // 上一次带 `MATE_PAYLOAD_STORE_DIR` 装的载荷，换回默认 store 就撞上这条）。
    // 这里是**非交互构建**，而这个 node_modules 是构建中间产物（锁文件驱动、不入库、随时可重建），
    // 自动重建才是对的行为：显式关掉那个确认，别让构建依赖"终端恰好是 TTY"。
    '--config.confirmModulesPurge=false',
  ]
  if (process.env.MATE_PAYLOAD_STORE_DIR) args.push(`--store-dir=${process.env.MATE_PAYLOAD_STORE_DIR}`)
  if (process.env.MATE_PAYLOAD_REGISTRY) args.push(`--config.registry=${process.env.MATE_PAYLOAD_REGISTRY}`)
  args.push(offline ? '--offline' : '--prefer-offline')
  // 依赖的生命周期脚本（koffi/node-pty 的 install、protobufjs 的 postinstall）由 pnpm 交给
  // `sh -c` 执行，脚本内部又会调 `node`——`env -i PATH=/usr/bin:/bin` 下这一步会直接
  // "sh: node: command not found"（实测踩到，pnpm 报 ELIFECYCLE）。因此把**当前 node 所在目录**
  // 前置给子进程：这不是"依赖用户环境"，而是构建工具把自己的解释器交给自己的子进程
  // （npm/pnpm 正常运行脚本时本来就这么做）。
  const childEnv = {
    ...process.env,
    PATH: [dirname(process.execPath), process.env.PATH].filter(Boolean).join(delimiter),
  }
  try {
    execFileSync(process.execPath, args, { cwd: payloadDir, env: childEnv, stdio: ['ignore', 'inherit', 'inherit'] })
  } catch (e) {
    fail(`${payloadDir} 的 pnpm install 失败（退出码 ${e.status ?? '?'}）：${e.message}`)
  }
}

/** 复核"精确 pin"纪律，返回钉死的版本。 */
function pinnedVersion(payloadDir, payload, target) {
  const manifest = readJson(join(payloadDir, 'package.json'))
  const dep = payload.dependency(target)
  const spec = manifest.dependencies?.[dep]
  if (typeof spec !== 'string') fail(`${payloadDir}/package.json 里没有依赖 ${dep}`)
  if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(spec)) {
    fail(`${dep} 的 specifier 不是精确版本（${spec}）：载荷版本必须钉死，禁止 ^ / ~ / latest`)
  }
  if (!existsSync(join(payloadDir, 'pnpm-lock.yaml'))) fail(`${payloadDir} 缺 pnpm-lock.yaml（锁文件必须入库）`)
  return { dep, version: spec }
}

function assemble(payload, target, { skipInstall, offline, abi }) {
  const payloadDir = join(RUNTIME_ROOT, payload.dir)
  if (!existsSync(payloadDir)) fail(`载荷工程目录不存在：${payloadDir}`)
  const { dep, version } = pinnedVersion(payloadDir, payload, target)
  if (!skipInstall) installPayload(payloadDir, target, { offline })

  const installedManifest = join(payloadDir, 'node_modules', dep, 'package.json')
  if (!existsSync(installedManifest)) fail(`${dep} 没装上（${installedManifest} 不存在）`)
  const installedVersion = readJson(installedManifest).version
  if (installedVersion !== version) {
    fail(`${dep} 装出来的版本是 ${installedVersion}，与精确 pin ${version} 不一致`)
  }

  const srcRel = typeof payload.source === 'function' ? payload.source(dep) : payload.source
  const srcRoot = join(payloadDir, srcRel)
  if (!existsSync(srcRoot)) fail(`载荷源不存在：${srcRoot}`)

  // 装配到临时目录再整体换入：中断只会留下临时目录，不会留下"半份载荷 + 完整清单"。
  const staging = mkdtempSync(join(tmpdir(), `mate-payload-${payload.name}-`))
  let result
  try {
    result = copyTreeFiltered(srcRoot, join(staging, payload.destSubdir), target)
    const manifest = {
      // 字段顺序固定（确定性要求之一）：新增字段请追加在末尾。
      name: payload.name,
      version,
      platform: target.platform,
      arch: target.arch,
      nodeAbi: abi,
      entry: payload.entry(dep, target),
      digest: result.digest,
      files: result.files,
      bytes: result.bytes,
      fileList: result.fileList,
      // fileList 里的相对路径**相对于哪一层**（= 复制源根在载荷里的落点）。
      //
      // 为什么必须记下来（M4-5 补）：fileList 是 `copyTreeFiltered` 按**复制源根**记的，而各载荷的
      // 落点形态可以不同——闭包型落在 `node_modules/` 下（源根 = 那份 node_modules），平铺型
      // （原生二进制的包）直接铺在载荷根（源根 = 包目录）。于是同一个 fileList 在两种形态里
      // 指向的层级不一样，而此前清单里**没有任何字段**说明这件事：消费方（`mate doctor
      // --verify-payload` 是第一个）只能靠猜。路径算错必须明确失败，不能靠回落搜索
      // （src/paths.ts 的同一纪律）——所以把这个事实写进清单。
      fileListBase: payload.destSubdir,
    }
    writeManifestAtomic(staging, manifest)
    const finalDir = join(PAYLOAD_OUT, payload.name, version)
    rmSync(join(PAYLOAD_OUT, payload.name), { recursive: true, force: true })
    mkdirSync(dirname(finalDir), { recursive: true })
    swapIntoPlace(staging, finalDir)
    return { ...manifest, payloadDir, pruned: result.pruned, dest: finalDir }
  } finally {
    rmSync(staging, { recursive: true, force: true })
  }
}

function main() {
  const { values } = parseArgs({
    options: {
      platform: { type: 'string', default: process.platform },
      arch: { type: 'string', default: process.arch },
      payload: { type: 'string' },
      'skip-install': { type: 'boolean', default: false },
      offline: { type: 'boolean', default: false },
      abi: { type: 'string' },
    },
  })
  const target = { platform: values.platform, arch: values.arch }
  const selected = values.payload ? PAYLOADS.filter((p) => p.name === values.payload) : PAYLOADS
  if (selected.length === 0) fail(`--payload 只能是 ${PAYLOADS.map((p) => p.name).join(' / ')}`)

  // ABI 的来源：真装配问 Electron（唯一事实源，见 electronNodeAbi）；`--skip-install` 的重放
  // （用来从**已装好的树**重新产出清单，不联网）默认退回本进程的 ABI——但那是**错的**：
  // Electron 44 的 Node ABI 与本机 Node 的 ABI 不同，清单会被 doctor / release.json 的交叉
  // 校验当场抓出来。所以重放时应当显式给 `--abi <Electron 的 process.versions.modules>`；
  // 给了 --abi 就不再问 Electron（受限环境/CI 里那一步可以省掉）。
  if (values.abi && !/^\d+$/.test(values.abi)) fail(`--abi 必须是数字（Electron 的 process.versions.modules），得到 ${values.abi}`)
  const abi = values.abi ?? (values['skip-install'] ? (process.versions.modules ?? 'unknown') : electronNodeAbi())

  const assembled = []
  for (const payload of selected) {
    assembled.push(assemble(payload, target, { skipInstall: values['skip-install'], offline: values.offline, abi }))
  }

  // 体积打点（计划 §7 M3-1 要求对照设计 §8.3 的 200–370 MB 估算）。
  console.log(`\n载荷装配完成（目标 ${target.platform}-${target.arch}，Node ABI ${abi}）`)
  let total = 0
  for (const p of assembled) {
    total += p.bytes
    const pruned = Object.entries(p.pruned).map(([k, v]) => `${k} ${v}`).join('、') || '无'
    console.log(`  ${p.name}@${p.version}`)
    console.log(`    体积 ${formatBytes(p.bytes)} / ${p.files} 个文件  digest ${p.digest.slice(0, 23)}…`)
    console.log(`    裁剪：${pruned}`)
    console.log(`    落点 ${p.dest}`)
  }
  console.log(`  合计 ${formatBytes(total)}（设计 §8.3 对"随包运行时"的估算是 200–370 MB，含 Electron 本体）`)
}

main()
