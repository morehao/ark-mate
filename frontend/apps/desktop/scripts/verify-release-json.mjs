#!/usr/bin/env node
/**
 * `release.json` 的逐项复核（M4-1，计划 §M4-1「验收」）。
 *
 * ## 为什么不能只是"读一遍文件"
 *
 * release.json 是唯一发布契约，但**它自己不是证据**：手改一个版本号、换过资产、装错载荷，
 * 文件都照样能解析。所以这里的每条判据都是"拿**实际产物**复算，再与文件里的值比"——
 * 而"实际产物"包含三样：
 *   1. 盘上的 `resources/`（核心二进制、前端资产树、载荷清单）；
 *   2. **核心自己的回报**：`--version --json` 与运行期 `/meta`。后者是 Go 侧实现的
 *      webDigest / build id 出口，与 JS 侧算出来的值必须**逐字**相同——这是版本闸门
 *      （设计 §6.7）成立的前提，也是本脚本存在的主要理由；
 *   3. 打出来的包：`--require-artifacts` 解 tar.gz 逐项对包内那份；`--launch` 再真起一次壳，
 *      断言"解包后首启无阻断"（无证书形态下替代"首次打开无提示"的那条，§12.27）。
 *
 * ## 用法
 *
 *   node scripts/verify-release-json.mjs                     # ① 形状 ② 值复算 ③ 核心 /meta 对照
 *   node scripts/verify-release-json.mjs --require-artifacts # 再加：包内逐项 + 解包布局 + 签名事实
 *   node scripts/verify-release-json.mjs --launch            # 再加：真起一次壳（隐含 --require-artifacts）
 *   --no-live 跳过"起核心对 /meta"（只用于排障；默认一定要跑，它是两侧算法的唯一交叉验证）
 *
 * 退出码 1 = 有任何一条不符（人话报告在 stdout）。
 */

import { spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { parseArgs } from 'node:util'

import {
  DIGEST_PREFIX,
  PAYLOAD_FIELDS,
  RELEASE_FIELDS,
  REQUIRED_PAYLOADS,
  assetDigest,
  readPayloadManifests,
  serializeRelease,
  shortDigest,
} from './lib/release.mjs'

const SCRIPT_DIR = import.meta.dirname
const DESKTOP = resolve(SCRIPT_DIR, '..')
const RESOURCES = join(DESKTOP, 'resources')
const RELEASE_JSON = join(DESKTOP, 'release.json')
const RELEASE_DIR = join(DESKTOP, 'release')
const CORE = join(RESOURCES, 'forge-core')
const WEB = join(RESOURCES, 'web')
const PAYLOADS = join(RESOURCES, 'payloads')

/** §12.27 的交付形态决策：不是"探测"出来的，是本项目拍板的事实。 */
const EXPECTED_DELIVERY = { signing: 'adhoc', notarized: false, channel: 'tarball', macUpdate: 'manual' }

const DIGEST_RE = new RegExp(`^${DIGEST_PREFIX}[0-9a-f]{64}$`)
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

let failures = 0
function check(name, ok, detail = '') {
  if (!ok) failures++
  console.log(`  ${ok ? '✅' : '❌'} ${name}${detail ? ` — ${detail}` : ''}`)
  return !!ok
}
const section = (t) => console.log(`\n=== ${t} ===`)

function fail(message) {
  console.error(`verify-release-json: ${message}`)
  process.exit(1)
}

/** 跑一条命令，返回 `{status, stdout, stderr}`（不抛：调用方决定怎么判）。 */
function run(cmd, args, opts = {}) {
  // 用 spawnSync 而不是 execFileSync：macOS 的 `codesign -dv` 把结论写在 **stderr**，
  // 而 execFileSync 成功时拿不到 stderr——症状是"签名明明是 adhoc，却判成读不到"。
  const r = spawnSync(cmd, args, { encoding: 'utf8', ...opts })
  return {
    status: r.status ?? (r.error ? -1 : 0),
    stdout: r.stdout ?? '',
    stderr: r.stderr ?? (r.error ? String(r.error.message) : ''),
  }
}

/** 递归收集"空值"的字段路径：release.json 里不允许 null / 空串 / 空数组（验收明写"没有 null/空值"）。 */
function emptyPaths(value, prefix = '') {
  if (value === null || value === undefined) return [prefix || '(根)']
  if (typeof value === 'string') return value === '' ? [prefix || '(根)'] : []
  if (Array.isArray(value)) {
    if (value.length === 0) return [prefix || '(根)']
    return value.flatMap((v, i) => emptyPaths(v, `${prefix}[${i}]`))
  }
  if (typeof value === 'object') {
    const keys = Object.keys(value)
    if (keys.length === 0) return [prefix || '(根)']
    return keys.flatMap((k) => emptyPaths(value[k], prefix ? `${prefix}.${k}` : k))
  }
  return []
}

/** macOS 代码签名的关键事实（`codesign -dv` 的结论走 stderr）。 */
function signatureFacts(path) {
  const r = run('codesign', ['-dv', path])
  const text = `${r.stdout}\n${r.stderr}`
  return {
    ok: r.status === 0,
    text,
    adhoc: /flags=0x[0-9a-f]+\(adhoc/.test(text) || /^Signature=adhoc$/m.test(text),
    developerId: /Authority=Developer ID/.test(text),
  }
}

/** 读就绪行（`FORGE_READY {"addr":"127.0.0.1:1234","port":1234}`）。 */
function waitForReady(child) {
  let buffer = ''
  const logs = []
  return new Promise((resolvePromise, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`等核心就绪行超时（30s）；核心输出尾部：\n${logs.join('')}`)),
      30000,
    )
    child.stdout.on('data', (d) => {
      buffer += String(d)
      let idx
      while ((idx = buffer.indexOf('\n')) >= 0) {
        const line = buffer.slice(0, idx).trim()
        buffer = buffer.slice(idx + 1)
        if (!line) continue
        if (line.startsWith('FORGE_READY ')) {
          clearTimeout(timer)
          try {
            resolvePromise(JSON.parse(line.slice('FORGE_READY '.length)))
          } catch (e) {
            reject(new Error(`就绪行不是 JSON（${e.message}）：${line}`))
          }
          return
        }
        logs.push(`${line}\n`)
      }
    })
    child.stderr.on('data', (d) => logs.push(String(d)))
    child.on('exit', (code) => {
      clearTimeout(timer)
      reject(new Error(`核心在就绪前退出（码 ${code}）；输出尾部：\n${logs.join('')}`))
    })
  })
}

/** 收掉一条（detached 起的）进程：先按进程组 SIGTERM，宽限后再 SIGKILL。 */
async function killTree(child) {
  if (!child || child.exitCode !== null) return
  const signal = (sig) => {
    try {
      process.kill(-child.pid, sig)
    } catch {
      try {
        child.kill(sig)
      } catch {}
    }
  }
  signal('SIGTERM')
  for (let i = 0; i < 40; i++) {
    if (child.exitCode !== null) return
    await sleep(100)
  }
  signal('SIGKILL')
  await sleep(300)
}

/**
 * 起一次核心，从 `/meta` 读它**自己算出来**的资产与版本事实。
 *
 * 为什么值得起一次真进程：webDigest 两侧各有一套实现（Go `router.compositeDigest` 与 JS
 * `assetDigest`），"两侧给出同一个值"是版本闸门的前提。纯静态比对做不到这件事。
 */
async function metaFromCore() {
  if (!existsSync(CORE)) throw new Error(`核心二进制不存在：${CORE}（先跑 make core/desktop）`)
  const home = mkdtempSync(join(tmpdir(), 'forge-verify-home-'))
  const env = { ...process.env, FORGE_HOME: home, FORGE_TRANSCRIPTS: join(home, 'transcripts'), FORGE_STORE: 'memory' }
  // 抹掉会影响这次测量的一切：令牌（/meta 会要求凭据）、自动化 tick、以及"用哪份资产"的旁路。
  for (const key of ['FORGE_TOKEN', 'FORGE_WEB_DIR', 'FORGE_ADDR', 'FORGE_AUTOMATION_TICK', 'FORGE_CONFIG_DIR']) {
    delete env[key]
  }

  const child = spawn(CORE, ['serve', '--addr', '127.0.0.1:0', '--web-dir', WEB, '--ready-stdout'], {
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
  })
  try {
    const ready = await waitForReady(child)
    const res = await fetch(`http://127.0.0.1:${ready.port}/v1/forge/meta`)
    if (!res.ok) throw new Error(`/v1/forge/meta 返回 ${res.status}`)
    const body = await res.json()
    if (body?.data === undefined) throw new Error(`/v1/forge/meta 的信封里没有 data：${JSON.stringify(body).slice(0, 200)}`)
    return body.data
  } finally {
    await killTree(child)
    rmSync(home, { recursive: true, force: true })
  }
}

/** 极简 CDP 求值（零依赖：Node 24 自带全局 WebSocket，与 ui:accept 同一套办法）。 */
async function cdpEval(port, urlPrefix, expression, timeoutMs = 60000) {
  const deadline = Date.now() + timeoutMs
  let page
  while (Date.now() < deadline) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()
      page = list.find((t) => t.type === 'page' && t.url.startsWith(urlPrefix))
      if (page) break
    } catch {
      // 壳还没起来 / CDP 还没监听：继续等
    }
    await sleep(400)
  }
  if (!page) throw new Error(`等 ${timeoutMs}ms 也没出现 ${urlPrefix} 的页面（CDP :${port}）`)

  const ws = new WebSocket(page.webSocketDebuggerUrl)
  await new Promise((res, rej) => {
    ws.onopen = res
    ws.onerror = () => rej(new Error('CDP WebSocket 连接失败'))
  })
  try {
    const result = await new Promise((res, rej) => {
      const timer = setTimeout(() => rej(new Error('CDP 求值超时（10s）')), 10000)
      ws.onmessage = (ev) => {
        const m = JSON.parse(ev.data)
        if (m.id !== 1) return
        clearTimeout(timer)
        if (m.error) rej(new Error(JSON.stringify(m.error)))
        else res(m.result)
      }
      ws.send(
        JSON.stringify({
          id: 1,
          method: 'Runtime.evaluate',
          params: { expression, awaitPromise: true, returnByValue: true },
        }),
      )
    })
    if (result?.exceptionDetails) throw new Error(`页面里抛错：${JSON.stringify(result.exceptionDetails).slice(0, 300)}`)
    return result?.result?.value
  } finally {
    ws.close()
  }
}

/** 解包目录里找 `*.app`（electron-builder 的 mac 产物）与它的 Resources 根。 */
function findAppIn(dir) {
  const app = readdirSync(dir).find((name) => name.endsWith('.app'))
  if (!app) throw new Error(`${dir} 里没有 .app（tar.gz 内容不符合预期：${readdirSync(dir).join('、')}）`)
  const appPath = join(dir, app)
  return { appPath, resourcesPath: join(appPath, 'Contents', 'Resources') }
}

/** 包内可执行文件（`Contents/MacOS/<productName>`；不猜名字，按"可执行"挑）。 */
function appExecutable(appPath) {
  const dir = join(appPath, 'Contents', 'MacOS')
  const found = readdirSync(dir)
    .map((name) => join(dir, name))
    .find((abs) => {
      try {
        return statSync(abs).isFile() && (statSync(abs).mode & 0o111) !== 0
      } catch {
        return false
      }
    })
  if (!found) throw new Error(`${dir} 里没有可执行文件`)
  return found
}

/** 包内那份发布事实与源文件逐项对照（`--require-artifacts`）。 */
function verifyInsidePackage(appPath, resourcesPath, release, sourceText) {
  const inPackageJson = join(resourcesPath, 'release.json')
  check('包内有 release.json（extraResources）', existsSync(inPackageJson))
  if (existsSync(inPackageJson)) {
    check(
      '包内 release.json 与源文件逐字节相同（契约的唯一性）',
      readFileSync(inPackageJson, 'utf8') === sourceText,
      '改包内文件 = 改契约，必须改脚本',
    )
  }

  for (const [rel, label] of [
    ['forge-core', '桌面核'],
    ['web/index.html', '前端资产'],
    ['runtime/bin/node', '私有 bin（POSIX）'],
    ['runtime/bin/node.cmd', '私有 bin（Windows）'],
    ['payloads', '随包载荷根'],
    ['app.asar', '壳产物（asar）'],
  ]) {
    check(`包内有 ${rel}（${label}）`, existsSync(join(resourcesPath, rel)))
  }
  for (const name of REQUIRED_PAYLOADS) {
    const dir = join(resourcesPath, 'payloads', name)
    const versions = existsSync(dir) ? readdirSync(dir) : []
    check(`包内有 ${name} 载荷`, versions.length === 1, versions.join('、'))
  }

  // 壳的产物必须在 asar 里（`files` 只带 dist/renderer），运行时树必须在 asar 外。
  check('壳的 dist/ 在 asar 内（不是散在 Resources 下）', !existsSync(join(resourcesPath, 'dist')) && !existsSync(join(resourcesPath, 'app', 'dist')))

  const packagedCore = join(resourcesPath, 'forge-core')
  if (existsSync(packagedCore)) {
    const r = run(packagedCore, ['--version', '--json'])
    let info = null
    try {
      info = JSON.parse(r.stdout)
    } catch {}
    check(
      '包内核心 --version --json 与契约一致',
      info?.version === release.coreVersion && info?.apiContract === release.apiContract,
      `包内报 ${info?.version ?? `（读不到，退出码 ${r.status}）`}`,
    )
  }

  if (existsSync(join(resourcesPath, 'web'))) {
    const web = assetDigest(join(resourcesPath, 'web'))
    check('包内 webBuildId 与契约一致', web.buildId === release.webBuildId, web.buildId)
    check('包内 webDigest 与契约一致', web.digest === release.webDigest, shortDigest(web.digest))
  }

  if (existsSync(join(resourcesPath, 'payloads'))) {
    const rows = readPayloadManifests(join(resourcesPath, 'payloads')).map((p) => ({
      name: p.name,
      version: p.version,
      digest: p.digest,
    }))
    check('包内载荷清单与契约一致', JSON.stringify(rows) === JSON.stringify(release.payloads))
  }

  // "桌面包里只有一份资产"（设计 §6.1 红线）：核心二进制里不该再内嵌一份 index.html 副本。
  if (existsSync(packagedCore) && existsSync(join(resourcesPath, 'web', 'index.html'))) {
    const embedded = readFileSync(packagedCore).includes(readFileSync(join(resourcesPath, 'web', 'index.html')))
    check('核心二进制没有再内嵌一份前端资产（只有一份 dist）', !embedded, embedded ? '核心里找到了同样的 index.html' : '')
  }

  // 签名事实（§12.27）：声明 adhoc 就必须真是 adhoc；出现 Developer ID 就是契约在撒谎。
  if (release.signing === 'adhoc') {
    const appSig = signatureFacts(appPath)
    const coreSig = existsSync(packagedCore) ? signatureFacts(packagedCore) : { ok: false, adhoc: false, developerId: false, text: '' }
    check('包内核心是 ad-hoc 签名（未签名的对象在 arm64 上会被 SIGKILL）', coreSig.adhoc, coreSig.adhoc ? '' : coreSig.text.split('\n')[0])
    check(
      '包内没有 Developer ID 签名（契约声明 signing=adhoc）',
      !appSig.developerId && !coreSig.developerId,
      appSig.developerId ? '整包是 Developer ID 签名' : '',
    )
    check('整包签名可校验（codesign -dv 认得它）', appSig.ok, appSig.ok ? '' : appSig.text.split('\n')[0])
  }

  // 非浏览器渠道分发（§12.27）：本地构建当然不带 quarantine；这条是"分发说明"的机器对照面。
  const xattr = run('xattr', ['-l', appPath])
  check('包上没有 com.apple.quarantine（非浏览器渠道分发）', !xattr.stdout.includes('com.apple.quarantine'))
}

/** 真起一次解包后的壳：首启无阻断（壳 → 核心 → 资产 → 版本闸门）。 */
async function launchPackagedApp(appPath, release) {
  const executable = appExecutable(appPath)
  const port = 9500 + Math.floor(Math.random() * 300)
  const home = mkdtempSync(join(tmpdir(), 'forge-verify-launch-'))

  const env = { ...process.env, FORGE_DESKTOP_HOME: join(home, 'forge-home') }
  delete env.ELECTRON_RUN_AS_NODE
  const child = spawn(executable, [`--remote-debugging-port=${port}`, `--user-data-dir=${join(home, 'user-data')}`], {
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
  })
  const logs = []
  child.stdout.on('data', (d) => logs.push(String(d)))
  child.stderr.on('data', (d) => logs.push(String(d)))

  try {
    // 页面经壳的反代问核心：这一条同时证明"核心起来了""资产是这份""版本闸门没拦"。
    //
    // 为什么要轮询而不是求值一次：CDP 的 page target 在**文档创建时**就出现，此时 React 还没挂载
    // （`#root` 是空的）。一次求值会把"界面正在渲染"误判成"界面没出来"——实测就是这样。
    const expression = `(async () => {
        const r = await fetch('/v1/forge/meta', { cache: 'no-store' })
        const body = await r.json()
        return {
          status: r.status,
          href: location.href,
          coreVersion: body?.data?.coreVersion,
          webBuildId: body?.data?.webBuildId,
          webDigest: body?.data?.webDigest,
          webSource: body?.data?.webSource,
          rootChildren: document.querySelector('#root')?.childElementCount ?? -1,
          readyState: document.readyState,
          text: (document.body?.innerText ?? '').slice(0, 120),
        }
      })()`
    // 求值**允许瞬时失败**：CDP 的 page target 在文档刚创建时就出现，此时 `location.href` 可能还没落成
    // `forge-app://app/`，相对路径 fetch 会抛 "Failed to parse URL from /v1/forge/meta"（实测在加固
    // 运行时下更容易撞到：包起来比源码态慢一拍）。一次求值失败不等于首启失败——重试到超时，
    // 并把最后一次错误带进详情，免得"重试掩盖了真实错误"。
    let facts = null
    let lastError = null
    for (let i = 0; i < 40; i++) {
      try {
        facts = await cdpEval(port, 'forge-app://app', expression)
        lastError = null
        if ((facts?.rootChildren ?? 0) > 0) break
      } catch (e) {
        lastError = e
      }
      await sleep(500)
    }
    check(
      '解包首启：壳进到应用页（forge-app://app）且界面已渲染',
      (facts?.rootChildren ?? 0) > 0,
      `#root 子节点 ${facts?.rootChildren}（href=${facts?.href} readyState=${facts?.readyState}${facts?.text ? `，页面文字「${facts.text.replace(/\s+/g, ' ').trim()}」` : ''}）` +
        (lastError ? `；最后一次求值失败：${lastError.message.split('\n')[0]}` : ''),
    )
    check('解包首启：核心经壳可达且版本与契约一致', facts?.coreVersion === release.coreVersion, `coreVersion=${facts?.coreVersion}`)
    check(
      '解包首启：核心服务的资产就是契约里那份',
      facts?.webBuildId === release.webBuildId && facts?.webDigest === release.webDigest,
      `webBuildId=${facts?.webBuildId} webSource=${facts?.webSource}`,
    )
  } catch (e) {
    check('解包首启：壳能起来并交出页面', false, e.message)
    const tail = logs.join('').split('\n').slice(-12).join('\n')
    if (tail.trim()) console.log(`    壳输出尾部：\n${tail}`)
  } finally {
    await killTree(child)
    // 孤儿清理：壳被收掉后，包内的核心不该还活着（`--exit-with-parent`，M1-6）。
    await sleep(500)
    const leftovers = run('pgrep', ['-fl', appPath])
    check(
      '解包首启：壳退出后没有留下孤儿核心',
      leftovers.stdout.trim() === '',
      leftovers.stdout.trim().split('\n').slice(0, 3).join(' / '),
    )
    rmSync(home, { recursive: true, force: true })
  }
}

// —— 入口 ——————————————————————————————————————————————————————————

async function main() {
  // 容忍 `pnpm run xxx -- --flag` 这种写法：pnpm 会把 `--` 原样传给脚本，而 node 的 parseArgs
  // 把开头的 `--` 当"选项结束"，后面的旗标就变成位置参数并直接抛错（实测踩到）。这里剥掉它。
  const rawArgs = process.argv.slice(2)
  const args = rawArgs[0] === '--' ? rawArgs.slice(1) : rawArgs
  const { values } = parseArgs({
    args,
    options: {
      'require-artifacts': { type: 'boolean', default: false },
      launch: { type: 'boolean', default: false },
      live: { type: 'boolean', default: true },
      // node 的 parseArgs 没有自动取反：想跳过 ③ 得显式声明 `--no-live`（别写出"文档里有、
      // 跑起来 Unknown option"的旗标——这正是本轮自证时踩到的）。
      'no-live': { type: 'boolean', default: false },
      help: { type: 'boolean', default: false },
    },
  })
  if (values.help) {
    console.log('用法：node scripts/verify-release-json.mjs [--require-artifacts] [--launch] [--no-live]')
    return
  }
  const requireArtifacts = values['require-artifacts'] || values.launch
  const live = values.live && !values['no-live']

  console.log('==> release.json 逐项复核（M4-1）')

  // ① 形状：字段集、顺序、序列化确定性、非空性。
  section('① 契约形状')
  if (!existsSync(RELEASE_JSON)) {
    fail(`没有 ${RELEASE_JSON}\n  先跑 \`make desktop/dist\`（这条流水线先出 release.json，再打包）`)
  }
  const sourceText = readFileSync(RELEASE_JSON, 'utf8')
  let release
  try {
    release = JSON.parse(sourceText)
  } catch (e) {
    fail(`${RELEASE_JSON} 不是合法 JSON：${e.message}`)
  }
  const keysOk = JSON.stringify(Object.keys(release)) === JSON.stringify(RELEASE_FIELDS)
  check('字段集与顺序 = RELEASE_FIELDS', keysOk, keysOk ? '' : Object.keys(release).join(' → '))
  // 字段不全时 serializeRelease 会抛错：那也要变成一条 ❌，而不是把栈打到用户脸上。
  let serialized = null
  try {
    serialized = serializeRelease(release)
  } catch (e) {
    check('字段齐全、可确定性序列化', false, e.message)
  }
  if (serialized !== null) {
    check('逐字节 = 确定性序列化（无手改、无多余字段）', sourceText === serialized)
  }
  const empty = emptyPaths(release)
  check('没有 null / 空值 / 空数组', empty.length === 0, empty.join('、'))
  const payloadRows = Array.isArray(release.payloads) ? release.payloads : []
  check(
    '载荷条目的字段集 = name/version/digest',
    payloadRows.length > 0 && payloadRows.every((p) => JSON.stringify(Object.keys(p)) === JSON.stringify(PAYLOAD_FIELDS)),
  )
  const payloadNames = payloadRows.map((p) => p.name)
  check(
    `载荷按 name 升序且包含 ${REQUIRED_PAYLOADS.join(' + ')}`,
    JSON.stringify(payloadNames) === JSON.stringify([...payloadNames].sort()) &&
      REQUIRED_PAYLOADS.every((n) => payloadNames.includes(n)),
    payloadNames.join('、'),
  )
  check('载荷摘要都是 sha256:<64 hex>', payloadRows.every((p) => DIGEST_RE.test(String(p.digest))))
  check('webDigest 是 sha256:<64 hex>', DIGEST_RE.test(release.webDigest), release.webDigest)
  check(
    '交付事实与 §12.27 的决策一致（adhoc / 未公证 / tar.gz / 手动更新）',
    release.signing === EXPECTED_DELIVERY.signing &&
      release.notarized === EXPECTED_DELIVERY.notarized &&
      release.channel === EXPECTED_DELIVERY.channel &&
      release.macUpdate === EXPECTED_DELIVERY.macUpdate,
    `signing=${release.signing} notarized=${release.notarized} channel=${release.channel} macUpdate=${release.macUpdate}`,
  )

  // ② 值复算：每一项都从实际产物重取。
  section('② 值一律来自实际产物')
  const coreInfo = (() => {
    if (!existsSync(CORE)) return null
    const r = run(CORE, ['--version', '--json'])
    try {
      return JSON.parse(r.stdout)
    } catch {
      return { raw: r.stdout, status: r.status }
    }
  })()
  check('coreVersion 与核心二进制自报一致', coreInfo?.version === release.coreVersion, `二进制报 ${coreInfo?.version ?? '（读不到）'}`)
  check('apiContract 与核心二进制自报一致', coreInfo?.apiContract === release.apiContract, `二进制报 ${coreInfo?.apiContract ?? '（读不到）'}`)
  check('coreVersion = appVersion（发布号标识整套）', release.coreVersion === release.appVersion, `${release.coreVersion} vs ${release.appVersion}`)

  let web = null
  if (existsSync(WEB)) {
    try {
      web = assetDigest(WEB)
    } catch (e) {
      check('resources/web 可复算摘要', false, e.message)
    }
  }
  check('webBuildId 与 resources/web 的实际内容一致', web?.buildId === release.webBuildId, `实测 ${web?.buildId ?? '（读不到）'}`)
  check('webDigest 与 resources/web 的实际内容一致', web?.digest === release.webDigest, `实测 ${shortDigest(web?.digest ?? '')}`)

  const enginesRun = run(process.execPath, [join(SCRIPT_DIR, 'assert-engines.mjs'), '--json'])
  let engines = null
  try {
    engines = JSON.parse(enginesRun.stdout)
  } catch {}
  check('electron 与实测一致（M3-7 断言）', engines?.electron === release.electron, `实测 ${engines?.electron ?? '（读不到）'}`)
  check('node 与实测一致', engines?.node === release.node, `实测 ${engines?.node ?? '（读不到）'}`)
  check('nodeAbi 与实测一致', engines?.nodeAbi === release.nodeAbi, `实测 ${engines?.nodeAbi ?? '（读不到）'}`)
  check(
    'assert-engines 本身是绿的（ABI / engines 判定通过）',
    enginesRun.status === 0,
    enginesRun.status === 0 ? '' : enginesRun.stderr.split('\n').slice(0, 2).join(' ') || '见 stderr',
  )

  let manifests = null
  if (existsSync(PAYLOADS)) {
    try {
      manifests = readPayloadManifests(PAYLOADS).map((p) => ({ name: p.name, version: p.version, digest: p.digest }))
    } catch (e) {
      check('载荷清单可读', false, e.message)
    }
  }
  check('载荷 name/version/digest 与 payload.json 逐项一致', JSON.stringify(manifests) === JSON.stringify(release.payloads))

  if (release.signing === 'adhoc' && existsSync(CORE)) {
    const coreSig = signatureFacts(CORE)
    check(
      '核心二进制的签名事实与 signing=adhoc 相符',
      coreSig.adhoc && !coreSig.developerId,
      coreSig.adhoc ? '' : coreSig.text.split('\n').slice(0, 2).join(' / '),
    )
  }

  // ③ 两侧算法交叉验证：核心经 /meta 交出的值必须与契约逐字相同。
  if (live) {
    section('③ 核心 /meta 交叉验证（Go 与 JS 两套摘要实现必须给出同一个值）')
    try {
      const meta = await metaFromCore()
      check('核心报告的 webSource = dir（核心读的确实是这一份目录）', meta.webSource === 'dir', String(meta.webSource))
      check('核心报告的 coreVersion 与契约一致', meta.coreVersion === release.coreVersion, String(meta.coreVersion))
      check('核心报告的 apiContract 与契约一致', meta.apiContract === release.apiContract, String(meta.apiContract))
      check('核心报告的 webBuildId 与契约一致', meta.webBuildId === release.webBuildId, String(meta.webBuildId))
      check(
        '核心报告的 webDigest 与契约逐字一致（两侧算法同源）',
        meta.webDigest === release.webDigest,
        `核心 ${shortDigest(String(meta.webDigest))} / 契约 ${shortDigest(release.webDigest)}`,
      )
    } catch (e) {
      check('起核心对 /meta', false, e.message.split('\n')[0])
    }
  }

  // ④ 打出来的包：解 tar.gz 逐项对包内那份。
  let extracted = null
  if (requireArtifacts) {
    section('④ 包内逐项对照（解 tar.gz）')
    if (!existsSync(RELEASE_DIR)) fail(`没有 ${RELEASE_DIR}\n  先跑 \`make desktop/dist\``)
    const files = readdirSync(RELEASE_DIR)
    const tarballs = files.filter((f) => f.endsWith('.tar.gz'))
    const dmgs = files.filter((f) => f.endsWith('.dmg'))
    check('有 tar.gz（分发形态）', tarballs.length === 1, tarballs.join('、'))
    check('有 dmg（本地自测形态）', dmgs.length === 1, dmgs.join('、'))
    const expectName = `ark-forge-${release.appVersion}-macos-${process.arch}`
    check(
      '产物命名与契约版本/架构一致',
      tarballs[0]?.startsWith(expectName) ?? false,
      `${tarballs[0] ?? '（缺）'}（期望前缀 ${expectName}）`,
    )

    if (tarballs.length === 1) {
      const dir = mkdtempSync(join(tmpdir(), 'forge-verify-untar-'))
      const untar = run('tar', ['-xzf', join(RELEASE_DIR, tarballs[0]), '-C', dir])
      check('tar.gz 可解（解到干净目录不报错）', untar.status === 0, untar.stderr.split('\n')[0] ?? '')
      if (untar.status === 0) {
        try {
          const { appPath, resourcesPath } = findAppIn(dir)
          verifyInsidePackage(appPath, resourcesPath, release, sourceText)
          extracted = { appPath, dir }
        } catch (e) {
          check('tar.gz 内是预期的 .app 结构', false, e.message)
        }
      }
      if (!extracted) rmSync(dir, { recursive: true, force: true })
    }
  }

  // ⑤ 解包后真起一次（--launch）。
  if (values.launch) {
    section('⑤ 解包后首启（无证书形态下替代"首次打开无提示"的那条）')
    if (!extracted) {
      check('解包后首启', false, '上一步没能解出 .app')
    } else {
      await launchPackagedApp(extracted.appPath, release)
    }
  }

  if (extracted) rmSync(extracted.dir, { recursive: true, force: true })

  console.log(
    failures === 0
      ? `\n==> 通过：release.json 与实际产物逐项相符${values.launch ? '（含解包后首启）' : ''}`
      : `\n==> 失败：${failures} 条不符`,
  )
  if (failures > 0) process.exit(1)
}

await main()
