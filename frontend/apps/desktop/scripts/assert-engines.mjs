#!/usr/bin/env node
/**
 * engines / ABI 打包期断言（M3-7；设计 §5.1 / §5.3，计划 §M3-7）。
 *
 * ## 这条断言为什么存在
 *
 * 桌面态**没有随包的独立 Node 运行时**：壳把 Electron 二进制本身当 Node 用
 * （`ELECTRON_RUN_AS_NODE=1` + `MATE_NODE_EXECUTABLE`，见 M3-2 的私有 bin 生成器），
 * 所以随包载荷（dsh 的 JS 闭包）里的代码跑在 **Electron 自带的那个 Node** 上——
 * 既不是机器上恰好装着的 node，也不是跑本脚本的解释器。
 * 实例：开发机上跑本脚本的是 Node v24.16.0，而 Electron 44.4.5 自带 Node 24.21.0，
 * 两者不同；`engines.node` 是给**载荷运行时**看的，因此判定必须以后者为准。
 *
 * 于是有两种只有在打包期才能便宜发现的失败：
 *   1. `engines.node` 不满足 → 载荷在用户机器上一启动就报"Node 版本不支持"；
 *   2. ABI（`process.versions.modules`）与实际不符 → 载荷闭包里的原生 `.node`
 *      加载失败（`NODE_MODULE_VERSION` 报错），而这类失败通常发生在**运行期某条路径**上。
 * 打包期红一次，比运行期炸一次便宜得多。
 *
 * ## 升级动作
 *
 * **升级 Electron 主版本必须重跑 `make verify/engines`。** Electron 换代同时改它自带的
 * Node 版本与 ABI，而载荷的 `payload.json` 记录的是**装配当时**的 ABI；升完不跑这条，
 * 症状是"装得上、跑不起来"。必要时按设计 §5.4 启用独立 Node 载荷。
 *
 * ## 判定对象（读"实际装配出来的"载荷，不猜路径）
 *
 * 扫描 `<payloads>/<name>/<version>/payload.json`（一个都没有 → 失败并提示先跑
 * `make desktop/payloads`）。每份载荷的 `engines.node` 按下列优先级解析：
 *   1. `payload.json` 的 `enginesNode`（M3-1 之后若补了这个字段，它最权威）；
 *   2. 载荷根的 `package.json`（opencode 这类把包内容铺在载荷根的形态）；
 *   3. 从 `entry` 向上找到的第一个 `package.json`（dsh 这类闭包载荷：entry 是
 *      `node_modules/@deepseek-ai/dsh/lib/bin.js`，声明包在闭包内部）。
 * 声明包里**没有** `engines.node` 时**不静默放行**：人话报告会逐份写明"看过哪些文件、
 * 为什么没判定"，并在最后汇总一条醒目警告（可用 `--require-engines` 把这种情况升级为失败）。
 *
 * ## 输出契约（供 M4-1 的 `release.json` 消费）
 *
 * `--json` 在 stdout 只打一份**确定性** JSON：键序固定、无时间戳、载荷按 name/version 排序：
 *   { "electron": "44.4.5", "node": "24.21.0", "nodeAbi": "149",
 *     "payloads": [ { "name", "version", "enginesNode", "satisfied" } ] }
 * `enginesNode` 为 null = 没解析到声明；`satisfied` 为 null = 未判定（跳过），
 * true/false = 判定结论。失败时 stdout 仍是这份 JSON，可操作的错误说明走 **stderr**、退出码 1。
 * （要拿纯 JSON 就直接 `node apps/desktop/scripts/assert-engines.mjs --json`；
 *  经 `pnpm` 跑会混入 pnpm 自己的输出。）
 *
 * ## semver 判定
 *
 * **零新依赖**，自己实现一份够用的判定：`>=` `>` `<=` `<` `=`（裸版本即 `=`）、
 * `^`、`~`、空格分隔的合取、`||` 析取、`x`/`X`/`*` 通配。实现不了或语义有坑的形式
 * **明确报错**，绝不静默放行——这条断言的全部价值在于"不满足就红"。与 npm 的有意差异：
 *   1. 连字符区间（`1.2.3 - 2.0.0`）：不支持 → 明确报错；
 *   2. 空的 `||` 分支（`>=1.2 ||`）：npm 当 `*`（任何版本都满足），本脚本报错——
 *      "多打一个 || 就让整条断言失效"正是要避免的那种静默放行；
 *   3. `>*` / `>=*` / `<*` / `<=*`（比较运算符与主版本通配混用）：报错；
 *   4. `^`/`~` 的上界不带 `-0` 预发布哨兵（Electron 的 Node 版本不会是预发布版，实际影响为零）。
 * 自检表见 `--self-test`（真值逐条与载荷里那份 npm `semver` 对过，含 `>1.x` → `>=2.0.0`
 * 这类容易写错的通配组合）。
 *
 * 用法：node scripts/assert-engines.mjs [--json] [--payloads-dir <dir>] [--self-test] [--require-engines]
 * 环境变量：MATE_ELECTRON_BINARY 覆盖 Electron 二进制路径（人工验证用；默认从
 *          frontend/node_modules 解析真 Electron）。
 */

import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, relative, resolve, sep } from 'node:path'
import { parseArgs } from 'node:util'

const SCRIPT_DIR = import.meta.dirname
const DESKTOP = resolve(SCRIPT_DIR, '..')
const DEFAULT_PAYLOADS_DIR = join(DESKTOP, 'resources', 'payloads')
const DESKTOP_MANIFEST = join(DESKTOP, 'package.json')

const USAGE = `用法：node scripts/assert-engines.mjs [--json] [--payloads-dir <dir>] [--self-test] [--require-engines]

  无参数            打人话报告；任一断言不满足 → 退出码 1
  --json            只在 stdout 打确定性 JSON（给 M4-1 的 release.json 消费）
  --payloads-dir    载荷根目录（默认 apps/desktop/resources/payloads）
  --self-test       只跑 semver 判定的自检表（不读载荷、不跑 Electron）
  --require-engines 没有任何载荷能判定 engines.node 时视为失败（fail-closed；默认关）

环境变量 MATE_ELECTRON_BINARY 覆盖 Electron 二进制路径。`

/** 统一失败出口：可操作的说明写 stderr，退出码 1。 */
function fail(...lines) {
  for (const line of lines) if (line !== '') console.error(`assert-engines: ${line}`)
  process.exit(1)
}

function readJson(abs) {
  try {
    return JSON.parse(readFileSync(abs, 'utf8'))
  } catch (e) {
    fail(`读不动 ${abs}：${e.message}`)
  }
}

// —— semver（零依赖，够用即可）————————————————————————————————————————
//
// 只覆盖 `engines.node` 实际会写的形式；判不了的形式一律抛错，由调用方变成"红"。

/** 解析一个版本 token；缺失段与 x/X/* 都记成 wildcardAt（第一个通配位），没有通配则为 null。 */
function parseVersion(input) {
  const text = String(input).trim().replace(/^[=v\s]+/, '').split('+')[0]
  if (!text) throw new Error(`空版本号：${JSON.stringify(input)}`)
  const dash = text.indexOf('-')
  const core = dash === -1 ? text : text.slice(0, dash)
  const pre = dash === -1 ? null : text.slice(dash + 1)
  if (pre !== null && !/^[0-9A-Za-z.-]+$/.test(pre)) throw new Error(`预发布段非法：${input}`)
  const parts = core.split('.')
  if (parts.length > 3) throw new Error(`版本段数超过 3：${input}`)
  const keys = ['major', 'minor', 'patch']
  const out = { major: null, minor: null, patch: null, pre, wildcardAt: null }
  for (let i = 0; i < 3; i++) {
    const part = parts[i]
    if (part === undefined || /^[xX*]$/.test(part)) {
      if (out.wildcardAt === null) out.wildcardAt = i
      continue
    }
    if (out.wildcardAt !== null) throw new Error(`通配符之后不能再有具体版本段：${input}`)
    if (!/^\d+$/.test(part)) throw new Error(`无法解析的版本段 ${JSON.stringify(part)}：${input}`)
    out[keys[i]] = Number(part)
  }
  return out
}

/** 通配版本 → 具体端点（通配位补 0）。 */
function concretize(v) {
  return { major: v.major ?? 0, minor: v.minor ?? 0, patch: v.patch ?? 0, pre: v.pre ?? null, wildcardAt: null }
}

const at = (major, minor, patch) => ({ major, minor, patch, pre: null, wildcardAt: null })

function compareVersions(a, b) {
  for (const key of ['major', 'minor', 'patch']) {
    if (a[key] !== b[key]) return a[key] < b[key] ? -1 : 1
  }
  if (a.pre === b.pre) return 0
  if (a.pre === null) return 1 // 有预发布 < 同版本号的正式版
  if (b.pre === null) return -1
  const as = a.pre.split('.')
  const bs = b.pre.split('.')
  for (let i = 0; i < Math.max(as.length, bs.length); i++) {
    if (as[i] === undefined) return -1
    if (bs[i] === undefined) return 1
    const an = /^\d+$/.test(as[i])
    const bn = /^\d+$/.test(bs[i])
    if (an && bn) {
      if (Number(as[i]) !== Number(bs[i])) return Number(as[i]) < Number(bs[i]) ? -1 : 1
    } else if (an) return -1
    else if (bn) return 1
    else if (as[i] !== bs[i]) return as[i] < bs[i] ? -1 : 1
  }
  return 0
}

/** 通配版本的排他上界（`1.2.x` → 1.3.0；`1.x` → 2.0.0；`*` → null）。 */
function wildcardUpper(v) {
  if (v.wildcardAt === 0) return null
  const c = concretize(v)
  return v.wildcardAt === 1 ? at(c.major + 1, 0, 0) : at(c.major, c.minor + 1, 0)
}

/** `^` 的上界（对齐 node-semver：`^1.2.x` → <2.0.0，`^0.2.x` → <0.3.0，`^0.0.x` → <0.1.0）。 */
function caretUpper(v) {
  if (v.wildcardAt === 0) return null
  const c = concretize(v)
  if (v.wildcardAt === 1) return at(c.major + 1, 0, 0)
  if (c.major > 0) return at(c.major + 1, 0, 0)
  if (v.wildcardAt === null && c.minor === 0) return at(0, 0, c.patch + 1)
  return at(0, c.minor + 1, 0)
}

/** `~` 的上界（`~1` → <2.0.0，`~1.2`/`~1.2.3` → <1.3.0）。 */
function tildeUpper(v) {
  if (v.wildcardAt === 0) return null
  const c = concretize(v)
  return v.wildcardAt === 1 ? at(c.major + 1, 0, 0) : at(c.major, c.minor + 1, 0)
}

/** 一个比较子（运算符 + 版本）→ 谓词；不支持的形式在这里抛错。 */
function makeComparator(op, token) {
  const v = parseVersion(token)
  const lo = concretize(v)
  const w = v.wildcardAt
  const ge = (x, t) => compareVersions(x, t) >= 0
  const lt = (x, t) => compareVersions(x, t) < 0
  switch (op) {
    case '=':
      if (w === 0) return { version: v, test: () => true }
      if (w === null) return { version: v, test: (x) => compareVersions(x, lo) === 0 }
      return { version: v, test: (x) => ge(x, lo) && lt(x, wildcardUpper(v)) }
    case '>=':
      if (w === 0) throw new Error(`比较运算符与主版本通配混用没有意义：>=${token}`)
      return { version: v, test: (x) => ge(x, lo) }
    case '>':
      if (w === 0) throw new Error(`比较运算符与主版本通配混用没有意义：>${token}`)
      return w === null
        ? { version: v, test: (x) => compareVersions(x, lo) > 0 }
        : { version: v, test: (x) => ge(x, wildcardUpper(v)) }
    case '<':
      if (w === 0) throw new Error(`比较运算符与主版本通配混用没有意义：<${token}`)
      return { version: v, test: (x) => lt(x, lo) }
    case '<=':
      if (w === 0) throw new Error(`比较运算符与主版本通配混用没有意义：<=${token}`)
      return w === null
        ? { version: v, test: (x) => compareVersions(x, lo) <= 0 }
        : { version: v, test: (x) => lt(x, wildcardUpper(v)) }
    case '^': {
      const up = caretUpper(v)
      return { version: v, test: up === null ? () => true : (x) => ge(x, lo) && lt(x, up) }
    }
    case '~': {
      const up = tildeUpper(v)
      return { version: v, test: up === null ? () => true : (x) => ge(x, lo) && lt(x, up) }
    }
    default:
      throw new Error(`不认识的比较运算符 ${JSON.stringify(op)}：${token}`)
  }
}

/** 解析一条 range → 若干析取分支，每个分支是合取的比较子表。 */
function parseRange(range) {
  const text = String(range).trim()
  if (!text) throw new Error('engines.node 是空字符串')
  const alternatives = text.split('||').map((s) => s.trim())
  return alternatives.map((alt) => {
    if (!alt) throw new Error(`范围 ${JSON.stringify(range)} 里有空的 || 分支`)
    const tokens = alt.split(/\s+/)
    if (tokens.includes('-')) {
      throw new Error(`不支持连字符区间（hyphen range）：${JSON.stringify(alt)}`)
    }
    return tokens.map((token) => {
      const m = /^(>=|<=|>|<|=|\^|~)?(.*)$/.exec(token)
      const op = m[1] ?? '='
      if (!m[2]) throw new Error(`比较子缺版本号：${JSON.stringify(token)}`)
      return makeComparator(op, m[2])
    })
  })
}

/**
 * 判定 `candidate` 是否满足 `range`。candidate 必须是具体版本（载荷运行时的 Version 不会是通配）。
 * 预发布语义按 npm 默认：带预发布的候选只有在同一分支里出现"同 major.minor.patch 的预发布比较子"时才算满足。
 */
export function satisfiesRange(candidate, range) {
  const cand = parseVersion(candidate)
  if (cand.wildcardAt !== null) throw new Error(`判定对象是通配版本，无法判定：${candidate}`)
  for (const alt of parseRange(range)) {
    if (!alt.every((c) => c.test(cand))) continue
    if (cand.pre === null) return true
    if (
      alt.some(
        (c) =>
          c.version.pre !== null &&
          c.version.major === cand.major &&
          c.version.minor === cand.minor &&
          c.version.patch === cand.patch,
      )
    ) {
      return true
    }
  }
  return false
}

// —— Electron：解析二进制 + 实测它自带的 Node ——————————————————————————————

function resolveElectronBinary() {
  const override = process.env.MATE_ELECTRON_BINARY
  if (override) {
    const abs = resolve(override)
    if (!existsSync(abs)) fail(`MATE_ELECTRON_BINARY 指向的文件不存在：${abs}`)
    return { path: abs, source: 'MATE_ELECTRON_BINARY（环境变量覆盖）' }
  }
  const require = createRequire(import.meta.url)
  let resolved
  try {
    resolved = require('electron') // 包在非 Electron 进程里返回二进制绝对路径（字符串）
  } catch (e) {
    fail(
      `解析 Electron 失败：${e.message}`,
      '先跑 `cd frontend && pnpm install`（Electron 是 apps/desktop 的 devDependency）',
    )
  }
  if (typeof resolved !== 'string') {
    fail(
      'require("electron") 没返回二进制路径——本脚本得用仓库的 node 跑（不能跑在 Electron 里）',
      '也可以显式给 MATE_ELECTRON_BINARY=<Electron 二进制路径>',
    )
  }
  if (!existsSync(resolved)) {
    fail(`解析出的 Electron 二进制不存在：${resolved}`, '重跑 `cd frontend && pnpm install`（可能只装了包没下二进制）')
  }
  return { path: resolved, source: 'require("electron")' }
}

function readElectronVersions(binary) {
  let out
  try {
    out = execFileSync(binary, ['-e', 'process.stdout.write(JSON.stringify(process.versions))'], {
      env: { ...process.env, ELECTRON_RUN_AS_NODE: '1' },
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    })
  } catch (e) {
    fail(
      `把 Electron 当 Node 跑失败（${binary}）：${e.message}`,
      '本脚本的做法就是 ELECTRON_RUN_AS_NODE=1 直接跑 Electron 二进制——失败多半是二进制没下全或被安全策略拦了',
    )
  }
  let versions
  try {
    versions = JSON.parse(out)
  } catch {
    fail(`Electron 的 process.versions 不是 JSON：${String(out).slice(0, 200)}`)
  }
  for (const key of ['electron', 'node', 'modules']) {
    if (typeof versions[key] !== 'string') fail(`Electron 的 process.versions 缺 ${key}（拿到 ${JSON.stringify(versions)}）`)
  }
  return { electron: versions.electron, node: versions.node, modules: versions.modules }
}

/** 声明包：Electron 版本必须与 apps/desktop/package.json 里声明的那个一致。问题收集起来，不 fail-fast（一次看全）。 */
function checkDeclaredElectron(actual) {
  const problems = []
  const hints = []
  const manifest = readJson(DESKTOP_MANIFEST)
  const spec = manifest.devDependencies?.electron
  if (typeof spec !== 'string' || !spec) {
    problems.push('apps/desktop/package.json 的 devDependencies 里没有 electron——无从比对"manifest vs 实际装的 Electron"')
    hints.push('把 electron 写进 devDependencies 并钉成精确版本，再跑 `cd frontend && pnpm install`')
    return { spec: null, pinned: false, warning: null, problems, hints }
  }
  const pinned = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(spec)
  if (pinned) {
    if (spec !== actual) {
      problems.push(`Electron 版本不一致：apps/desktop/package.json 钉的是 ${spec}，实际二进制报的是 ${actual}`)
      hints.push(
        '把 package.json 的 electron 改成实际安装的版本，或重跑 `cd frontend && pnpm install` 装回钉死的那版',
      )
    }
    return { spec, pinned: true, warning: null, problems, hints }
  }
  let ok
  try {
    ok = satisfiesRange(actual, spec)
  } catch (e) {
    problems.push(`devDependencies.electron 的声明 ${JSON.stringify(spec)} 判不了：${e.message}`)
    hints.push('把 devDependencies.electron 钉成精确版本（本仓的确定性纪律要求精确 pin）')
    return { spec, pinned: false, warning: null, problems, hints }
  }
  if (!ok) {
    problems.push(`Electron 版本不一致：apps/desktop/package.json 声明 ${spec}，实际二进制报的是 ${actual}`)
    hints.push('把 package.json 的 electron 钉成精确版本（本仓的确定性纪律要求精确 pin）')
  }
  return {
    spec,
    pinned: false,
    warning: `devDependencies.electron 声明为范围 ${spec}（未精确 pin），实际 ${actual} 落在其中`,
    problems,
    hints,
  }
}

// —— 载荷：扫描 + 声明解析 ——————————————————————————————————————————————

function findPayloads(root) {
  if (!existsSync(root)) {
    fail(`载荷目录不存在：${root}`, '先跑 `make desktop/payloads`（M3-1：装配 dsh 闭包与 opencode 二进制）')
  }
  const found = []
  for (const name of readdirSync(root).sort()) {
    const nameDir = join(root, name)
    if (!statSync(nameDir).isDirectory()) continue
    for (const version of readdirSync(nameDir).sort()) {
      const dir = join(nameDir, version)
      if (!statSync(dir).isDirectory()) continue
      const manifestPath = join(dir, 'payload.json')
      if (!existsSync(manifestPath)) continue
      found.push({ name, version, dir, manifest: readJson(manifestPath) })
    }
  }
  if (found.length === 0) {
    fail(`在 ${root} 下没扫到任何 payloads/<name>/<version>/payload.json`, '先跑 `make desktop/payloads`')
  }
  return found
}

const posix = (p) => p.split(sep).join('/')

/**
 * 解析一份载荷的 engines.node 声明（见文件头"判定对象"的优先级）。
 * 返回 { engine, sources, candidates }：engine 为 null 表示没解析到（不是静默放行——
 * 调用方必须把 candidates 写进报告）。
 */
function resolveEnginesDeclaration(payload) {
  if (typeof payload.manifest.enginesNode === 'string') {
    return { engine: payload.manifest.enginesNode, source: 'payload.json 的 enginesNode', candidates: ['payload.json'] }
  }
  const candidates = []
  if (existsSync(join(payload.dir, 'package.json'))) candidates.push('package.json')
  const entry = typeof payload.manifest.entry === 'string' ? payload.manifest.entry : null
  if (entry) {
    let dir = dirname(join(payload.dir, entry))
    while (dir.startsWith(payload.dir) && dir !== payload.dir) {
      const abs = join(dir, 'package.json')
      if (existsSync(abs)) {
        const rel = posix(relative(payload.dir, abs))
        if (!candidates.includes(rel)) candidates.push(rel)
        break
      }
      const parent = dirname(dir)
      if (parent === dir) break
      dir = parent
    }
  }
  for (const rel of candidates) {
    const pkg = readJson(join(payload.dir, rel))
    if (typeof pkg.engines?.node === 'string') return { engine: pkg.engines.node, source: rel, candidates }
  }
  return { engine: null, source: null, candidates }
}

const isJsEntry = (entry) => typeof entry === 'string' && /\.(?:[cm]?js)$/.test(entry)

// —— 组装报告 ——————————————————————————————————————————————————————

function buildReport(payloadsDir) {
  const binary = resolveElectronBinary()
  const versions = readElectronVersions(binary.path)
  const declared = checkDeclaredElectron(versions.electron)

  const payloads = findPayloads(payloadsDir)
  const problems = [...declared.problems]
  const hints = [...declared.hints]
  const rows = []
  const abis = new Set()
  const undeclaredJs = []

  for (const payload of payloads) {
    const label = `${payload.name}@${payload.version}`
    const abi = payload.manifest.nodeAbi
    if (typeof abi !== 'string' || !abi) {
      problems.push(`${label} 的 payload.json 缺 nodeAbi`)
      hints.push('重跑 `make desktop/payloads` 让 M3-1 重新写清单')
    } else {
      abis.add(abi)
      if (abi !== versions.modules) {
        problems.push(
          `ABI 不一致：${label} 的 payload.json nodeAbi=${abi}，而 Electron ${versions.electron} 自带 Node ${versions.node} 的 ABI 是 ${versions.modules}`,
        )
        hints.push(
          `重跑 \`make desktop/payloads\` 按当前 Electron 的 ABI 重装载荷；或把 Electron 钉回装配时的版本（ABI 不一致 = 原生 .node 会在运行期才炸）`,
        )
      }
    }

    const decl = resolveEnginesDeclaration(payload)
    let enginesNode = null
    let satisfied = null
    let note
    if (decl.engine !== null) {
      enginesNode = decl.engine
      try {
        satisfied = satisfiesRange(versions.node, decl.engine)
      } catch (e) {
        problems.push(`${label} 的 engines.node=${JSON.stringify(decl.engine)} 判不了：${e.message}`)
        hints.push(
          '本断言的 semver 判定支持 >= > <= < = ^ ~、空格合取、|| 析取、x/* 通配；不支持的形式请改写，或扩展 scripts/assert-engines.mjs',
        )
        note = `判定失败：${e.message}`
      }
      if (satisfied === false) {
        note = `不满足（声明来源 ${decl.source}）`
        problems.push(
          `engines.node 不满足：${label} 要求 ${decl.engine}，而 Electron ${versions.electron} 自带的 Node 是 ${versions.node}（声明来源 ${decl.source}）`,
        )
        hints.push(
          '对齐两侧：升级/降级 Electron（frontend/apps/desktop/package.json 的 devDependencies.electron），或按设计 §5.4 启用独立 Node 载荷',
        )
      } else if (satisfied === true) {
        note = `满足（来源 ${decl.source}）`
      }
    } else {
      const where = decl.candidates.length ? `看过 ${decl.candidates.join('、')}` : '载荷里没有 package.json，且 entry 之上也没有'
      note = isJsEntry(payload.manifest.entry)
        ? `未声明（${where}，上游 npm 包没带 engines）`
        : `未声明（entry ${payload.manifest.entry} 是原生二进制，不经 Electron 的 Node 执行；${where}）`
      if (isJsEntry(payload.manifest.entry)) undeclaredJs.push(label)
    }

    rows.push({ name: payload.name, version: payload.version, enginesNode, satisfied, abi, note })
  }

  if (abis.size > 1) {
    problems.push(`载荷之间的 nodeAbi 互相不一致：${[...abis].sort().join(' / ')}（同一台机器上只有一种 ABI）`)
    hints.push('重跑 `make desktop/payloads` 一次性重装全部载荷（别混用不同时期装配的载荷）')
  }

  if (undeclaredJs.length > 0) {
    hints.push(
      `engines 断言对 ${undeclaredJs.join('、')} 没有判定对象：随包的上游 npm 包没有 engines 字段。` +
        '要真正拿到这条安全网，得让 M3-1 装配时把上游的 engines.node 写进 payload.json 的 enginesNode（或换一个仍带声明的声明来源）',
    )
  }

  return {
    binary,
    versions,
    declared,
    rows,
    problems,
    hints: [...new Set(hints)],
    undeclaredJs,
    // 给 M4-1 的确定性输出（键序固定、无时间戳）
    json: {
      electron: versions.electron,
      node: versions.node,
      nodeAbi: versions.modules,
      payloads: rows.map((r) => ({
        name: r.name,
        version: r.version,
        enginesNode: r.enginesNode,
        satisfied: r.satisfied,
      })),
    },
  }
}

function humanReport(report, payloadsDir) {
  const lines = []
  lines.push('==> engines / ABI 打包期断言（M3-7）')
  lines.push(
    `Electron ${report.versions.electron}（apps/desktop/package.json 声明 ${report.declared.spec}${report.declared.pinned ? '，已精确 pin' : ''}）`,
  )
  lines.push(`  自带 Node ${report.versions.node} / ABI ${report.versions.modules}`)
  lines.push(`  二进制 ${report.binary.path}（来源：${report.binary.source}）`)
  lines.push(`  载荷根 ${payloadsDir}`)
  for (const row of report.rows) {
    const engines = row.enginesNode === null ? '（无声明）' : row.enginesNode
    const verdict =
      row.satisfied === true
        ? '满足'
        : row.satisfied === false
          ? '不满足'
          : row.enginesNode === null
            ? '跳过判定'
            : '判定失败'
    const abiMark = row.abi === report.versions.modules ? '✓' : '✗'
    lines.push(
      `载荷 ${row.name}@${row.version}  engines.node=${engines}  判定=${verdict}  ABI ${row.abi ?? '缺失'} ${abiMark}（Electron ${report.versions.modules}）`,
    )
    lines.push(`    ${row.note}`)
  }
  if (report.declared.warning) lines.push(`警告：${report.declared.warning}`)
  if (report.undeclaredJs.length > 0) {
    lines.push(
      `警告：${report.undeclaredJs.join('、')} 没有可判定的 engines.node —— 这条断言对它们目前是"退化"的（不是放行，是没有判定对象）`,
    )
  }
  if (report.problems.length === 0) {
    lines.push('==> 通过（Electron 的 Node/ABI 与随包载荷一致；请在有 engines 声明的载荷上保持这条为绿）')
  } else {
    lines.push('==> 失败')
    for (const problem of report.problems) lines.push(`  ✗ ${problem}`)
    for (const hint of report.hints) lines.push(`  下一步：${hint}`)
  }
  return lines.join('\n')
}

// —— 自检：semver 判定的证据（无载荷也能跑）———————————————————————————————

const SELF_TEST_CASES = [
  ['24.21.0', '^22.19.0 || >=24.0.0', true],
  ['22.19.0', '^22.19.0 || >=24.0.0', true],
  ['22.18.9', '^22.19.0 || >=24.0.0', false],
  ['24.0.0', '^22.19.0 || >=24.0.0', true],
  ['23.9.0', '^22.19.0 || >=24.0.0', false],
  ['24.21.0', '>=99', false],
  ['24.21.0', '>=24.21.0', true],
  ['24.21.0', '>24.21.0', false],
  ['24.21.0', '<=24.21.0', true],
  ['24.21.0', '<24.21.0', false],
  ['24.21.0', '24.21.0', true],
  ['24.21.0', '24.x', true],
  ['24.21.0', '24.20.x', false],
  ['24.21.0', '>1.x', true], // npm：>1.x → >=2.0.0
  ['1.2.3', '>1.2.x', false], // npm：>1.2.x → >=1.3.0
  ['1.3.0', '>1.2.x', true],
  ['1.2.3', '>=1.2.x', true], // npm：>=1.2.x → >=1.2.0
  ['1.2.3', '<=1.x', true], // npm：<=1.x → <2.0.0
  ['24.21.0', '*', true],
  ['24.21.0', '~24.21.0', true],
  ['24.22.0', '~24.21.0', false],
  ['24.21.0', '~24', true],
  ['25.0.0', '~24', false],
  ['24.21.0', '^24.1.0', true],
  ['25.0.0', '^24.1.0', false],
  ['0.2.5', '^0.2.3', true],
  ['0.3.0', '^0.2.3', false],
  ['0.0.3', '^0.0.3', true],
  ['0.0.4', '^0.0.3', false],
  ['24.21.0', '>=22.12.0 <25', true], // commander 式的空格合取
  ['26.0.0', '>=22.12.0 <25', false],
  ['20.17.0', '^20.17.0 || >=22.9.0', true], // npm 11.13.0 的声明
  ['22.8.0', '^20.17.0 || >=22.9.0', false],
  ['22.19.0', '>=22.19.0', true], // undici 的声明
  ['24.21.0-rc.1', '>=24.0.0', false], // npm 默认：预发布不满足无预发布的区间
  ['24.21.0-rc.1', '>=24.21.0-rc.0', true],
]

const SELF_TEST_ERRORS = [
  ['1.2.3', '1.2.3 - 2.0.0'], // 连字符区间：不支持
  ['1.2.3', 'latest'],
  ['1.2.3', ''],
  ['1.2.3', '>=1.2 ||'], // 空分支：npm 当 *，我们报错
  ['1.2.3', '>*'], // 比较运算符与主版本通配混用：不支持
  ['1.2.3', '>=*'],
]

function selfTest() {
  let failed = 0
  console.log('==> semver 判定自检（--self-test）')
  for (const [candidate, range, expected] of SELF_TEST_CASES) {
    let actual
    try {
      actual = satisfiesRange(candidate, range)
    } catch (e) {
      actual = `抛错(${e.message})`
    }
    const ok = actual === expected
    if (!ok) failed++
    console.log(`  ${ok ? '✓' : '✗'} ${candidate} ∈ ${JSON.stringify(range)} → ${actual}（期望 ${expected}）`)
  }
  for (const [candidate, range] of SELF_TEST_ERRORS) {
    let actual
    try {
      actual = satisfiesRange(candidate, range)
    } catch (e) {
      actual = `抛错`
    }
    const ok = actual === '抛错'
    if (!ok) failed++
    console.log(`  ${ok ? '✓' : '✗'} ${candidate} ∈ ${JSON.stringify(range)} → ${ok ? '明确报错' : `静默给了 ${actual}`}（期望明确报错）`)
  }
  console.log(failed === 0 ? '==> 自检通过' : `==> 自检失败：${failed} 条不符`)
  return failed === 0 ? 0 : 1
}

// —— 入口 ——————————————————————————————————————————————————————————

function main() {
  let values
  try {
    ;({ values } = parseArgs({
      options: {
        json: { type: 'boolean', default: false },
        'payloads-dir': { type: 'string' },
        'self-test': { type: 'boolean', default: false },
        'require-engines': { type: 'boolean', default: false },
        help: { type: 'boolean', default: false },
      },
    }))
  } catch (e) {
    fail(`参数无法解析：${e.message}`, '', USAGE)
  }
  if (values.help) {
    console.log(USAGE)
    return
  }
  if (values['self-test']) process.exit(selfTest())

  const payloadsDir = values['payloads-dir'] ? resolve(values['payloads-dir']) : DEFAULT_PAYLOADS_DIR
  const report = buildReport(payloadsDir)

  if (values['require-engines'] && report.undeclaredJs.length > 0) {
    report.problems.push(
      `--require-engines：${report.undeclaredJs.join('、')} 没有任何可判定的 engines.node（fail-closed）`,
    )
    report.hints.push('先让 M3-1 在 payload.json 里记下上游的 engines.node，或在载荷里保留带 engines 的声明包')
  }

  if (values.json) {
    console.log(JSON.stringify(report.json, undefined, 2))
    // --json 的 stdout 只留纯 JSON：人话报告与下一步走 stderr（消费方按退出码判断成败）
    if (report.problems.length > 0) {
      for (const line of humanReport(report, payloadsDir).split('\n')) console.error(line)
    }
  } else {
    console.log(humanReport(report, payloadsDir))
  }

  if (report.problems.length > 0) process.exit(1)
}

const isMain = Boolean(process.argv[1]) && resolve(process.argv[1]) === import.meta.filename
if (isMain) main()
