#!/usr/bin/env node
/**
 * 生成 `release.json`（M4-1，计划 §0.3「唯一发布契约」）。
 *
 * ## 它回答的问题
 *
 * "这个交付物到底是哪一套东西？"——应用版本、核心版本、前端构建 id 与内容摘要、API 契约代数、
 * Electron / Node / ABI、随包载荷的名字与版本与摘要、以及**交付事实**（签名形态、是否公证、
 * 分发渠道、更新方式）。上一个项目的教训是这套信息散在十几个地方，最后没有人能回答
 * "用户装的那一份里 dsh 是哪个版本"。
 *
 * ## 三条纪律
 *
 * 1. **确定性（C9）**：字段顺序固定、无时间戳、载荷按 name 排序、原子写。两次构建的
 *    release.json 除版本号外逐字节相同；`make verify/release-json` 拿这条当判据。
 * 2. **值一律取自"实际产物"，不取自声明**：coreVersion 来自 `mate-core --version --json`
 *    （不是 package.json），webBuildId/webDigest 来自 `resources/web` 的内容，electron/node/ABI
 *    来自 `assert-engines.mjs`（它自己跑 Electron 二进制去问）。**任何一项拿不到就失败**，
 *    不写空值、不写 `null`、不猜。
 * 3. **发布号标识整套**：coreVersion 必须等于 appVersion。核心不带版本注入（= `dev`）却要出包，
 *    是"装上去看着正常、事后没人能对上号"的典型来源，因此这里是硬失败而不是警告。
 *
 * 用法：node scripts/build-release-json.mjs [--out <path>] [--quiet]
 */

import { execFileSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { parseArgs } from 'node:util'

import {
  RELEASE_FIELDS,
  assetDigest,
  readJson,
  readPayloadManifests,
  serializeRelease,
  shortDigest,
  writeReleaseAtomic,
} from './lib/release.mjs'
import { formatBytes } from './lib/payload.mjs'

const SCRIPT_DIR = import.meta.dirname
const DESKTOP = resolve(SCRIPT_DIR, '..')
const FRONTEND = resolve(DESKTOP, '..', '..')

const RESOURCES = join(DESKTOP, 'resources')
const CORE_BINARY = join(RESOURCES, 'mate-core')
const WEB_DIR = join(RESOURCES, 'web')
const PAYLOADS_DIR = join(RESOURCES, 'payloads')
const DEFAULT_OUT = join(DESKTOP, 'release.json')

function fail(...lines) {
  for (const line of lines) if (line !== '') console.error(`build-release-json: ${line}`)
  process.exit(1)
}

/** 跑一个脚本并拿它的 stdout（stderr 原样透出，失败时把人话错误留给用户）。 */
function runJson(script, args) {
  let out
  try {
    out = execFileSync(process.execPath, [join(SCRIPT_DIR, script), ...args], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'inherit'],
    })
  } catch (e) {
    fail(
      `${script} 失败（退出码 ${e.status ?? '?'}）——release.json 的值只取自实际产物，它判不了就不出包`,
      '先把上面那条断言弄绿再重跑打包流水线',
    )
  }
  try {
    return JSON.parse(out)
  } catch {
    fail(`${script} 的 stdout 不是 JSON：${JSON.stringify(String(out).slice(0, 200))}`)
  }
}

/**
 * 交付事实（§12.27 的决策，不是"环境探测"）：无证书 → ad-hoc 签名 + 内网 tar.gz + 手动更新。
 */
/**
 * 交付事实：**ad-hoc 签名 + 内网 tar.gz + 手动换包**，这是本项目的**长期口径**，不是"还没做"。
 *
 * 决策链：计划 §12.27（不上架、不购买 Apple Developer / Windows 代码签名证书 → 不公证、不签
 * Developer ID）→ 2026-09-28 项目方复核确认"没有证书、也不需要上架应用市场"→ **M4-2b 不执行**。
 * 因此这里不是"等证书到位就改"的占位，而是**把口径钉死**：任何一个公证/签名凭据出现在环境里，
 * 都说明有人正走在另一条路上，而那条路没有被实现——此时代码选择**直接失败**，而不是产出一个
 * release.json 与包里实际签名状态不符的包（那是最坏的一种撒谎：包是 Developer ID 签的，契约里写 adhoc，
 * 之后任何基于这四个字段的判断都会错）。
 *
 * 真要重新开这条路（换主意了）：改本函数 + `release.json` 的四个字段 + `mac.identity`/`mac.notarize`，
 * 并按计划 §13 的"契约变更先改设计文档"走一遍。绝不静默地半开。
 */
function deliveryFacts() {
  const certish = [
    'CSC_LINK',
    'CSC_NAME',
    'CSC_IDENTITY',
    'CSC_KEY_PASSWORD',
    'APPLE_ID',
    'APPLE_APP_SPECIFIC_PASSWORD',
    'APPLE_TEAM_ID',
    'APPLE_API_KEY',
    'APPLE_API_KEY_ID',
    'APPLE_API_ISSUER',
    'APPLE_KEYCHAIN_PROFILE',
  ]
  const present = certish.filter((key) => (process.env[key] ?? '') !== '')
  if (present.length > 0) {
    fail(
      `检测到签名/公证类凭据：${present.join('、')}——本项目**不做证书**（计划 §12.27；M4-2b 不执行）`,
      '打包形态固化在"ad-hoc 签名 + 内网 tar.gz + 手动换包"，release.json 的 signing/notarized/channel/macUpdate',
      '四个字段是这句话的机器可读形式。要用证书 = 改本脚本 + electron-builder.yml 的 mac 段 + 那四个字段，',
      '并按"契约变更先改设计文档"走一遍；在那之前请先把这些变量 unset（它们对本次打包没有任何作用）。',
    )
  }
  if (['1', 'true', 'yes'].includes(String(process.env.MAC_NOTARIZE ?? '').toLowerCase())) {
    fail(
      'MAC_NOTARIZE 已设置，但本项目不公证（计划 §12.27；M4-2b 不执行）——不假装已公证，直接失败',
      'electron-builder.yml 里 `mac.notarize: false` 是长期口径，不是待办',
    )
  }
  return { signing: 'adhoc', notarized: false, channel: 'tarball', macUpdate: 'manual' }
}

function main() {
  const { values } = parseArgs({
    options: {
      out: { type: 'string' },
      quiet: { type: 'boolean', default: false },
      help: { type: 'boolean', default: false },
    },
  })
  if (values.help) {
    console.log('用法：node scripts/build-release-json.mjs [--out <path>] [--quiet]')
    return
  }
  const out = values.out ? resolve(values.out) : DEFAULT_OUT

  const appVersion = readJson(join(DESKTOP, 'package.json')).version
  if (typeof appVersion !== 'string' || !appVersion) fail('apps/desktop/package.json 里没有 version')

  // 核心自己报的版本与契约代数：只信二进制说的（ldflags 注入是构建期生效的）。
  const coreInfo = (() => {
    if (!existsSync(CORE_BINARY)) {
      fail(`核心二进制不存在：${CORE_BINARY}`, '先跑 `make core/desktop`（或直接用 `make desktop/dist`，它会按顺序做完）')
    }
    let out
    try {
      out = execFileSync(CORE_BINARY, ['--version', '--json'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
    } catch (e) {
      const stderr = String(e.stderr ?? '').trim()
      fail(
        `跑 ${CORE_BINARY} --version --json 失败（退出码 ${e.status ?? '?'}）`,
        ...(stderr ? stderr.split('\n').slice(0, 4).map((l) => `  核心说：${l}`) : []),
        '核心的版本出口是 release.json 的 coreVersion/apiContract 唯一取法（M4-1）；',
        '若报"未知子命令 --version"，这份核心是加 --version 之前的旧构建——重跑 `make core/desktop`',
      )
    }
    try {
      return JSON.parse(out)
    } catch {
      fail(`核心的 --version --json 输出不是 JSON：${JSON.stringify(String(out).slice(0, 200))}`)
    }
  })()

  if (coreInfo.version === 'dev' || coreInfo.version !== appVersion) {
    fail(
      `核心报的版本是 ${JSON.stringify(coreInfo.version)}，与 apps/desktop/package.json 的 ${JSON.stringify(appVersion)} 不一致`,
      '发布号标识整套（设计 §2 第 75 行的那条）：出包时核心必须带同样的版本，',
      '用 `make desktop/dist`（它会给 mate-core 注入 -X …/version.Version=<appVersion>）；',
      '如果你只跑了 `make core/desktop`，那份核心是 dev 构建，不能拿来出包',
    )
  }
  if (coreInfo.apiContract !== 'v1' && typeof coreInfo.apiContract !== 'string') {
    fail(`核心没有回报 apiContract：${JSON.stringify(coreInfo)}`)
  }

  // 前端资产：构建 id 与内容摘要都取自**这一份**目录（壳服务它、核心用 --web-dir 读同一份）。
  const web = assetDigest(WEB_DIR)

  // Electron / Node / ABI：由 assert-engines.mjs 实测（它跑 Electron 二进制去问）。
  const engines = runJson('assert-engines.mjs', ['--json'])

  // 随包载荷（M3-1 的 payload.json 是唯一清单）。
  const payloads = readPayloadManifests(PAYLOADS_DIR).map((p) => ({
    name: p.name,
    version: p.version,
    digest: p.digest,
  }))

  const release = {
    appVersion,
    coreVersion: coreInfo.version,
    webBuildId: web.buildId,
    webDigest: web.digest,
    apiContract: coreInfo.apiContract,
    electron: engines.electron,
    node: engines.node,
    nodeAbi: engines.nodeAbi,
    payloads,
    ...deliveryFacts(),
  }

  // 序列化会顺带校验字段集（多/少/乱序都抛错），所以这里先序列化再落盘。
  const text = serializeRelease(release)
  writeReleaseAtomic(out, release)

  if (!values.quiet) {
    console.log('==> release.json（唯一发布契约，M4-1）')
    console.log(`  appVersion   ${release.appVersion}（apps/desktop/package.json）`)
    console.log(`  coreVersion  ${release.coreVersion}（mate-core --version）  apiContract ${release.apiContract}`)
    console.log(`  webBuildId   ${release.webBuildId}（resources/web/build-id.txt）`)
    console.log(`  webDigest    ${shortDigest(release.webDigest)}  ${web.files} 个文件 ${formatBytes(web.bytes)}`)
    console.log(`  electron     ${release.electron} / Node ${release.node} / ABI ${release.nodeAbi}（assert-engines）`)
    for (const p of release.payloads) {
      console.log(`  载荷         ${p.name}@${p.version}  ${shortDigest(p.digest)}`)
    }
    console.log(`  交付事实     signing=${release.signing} notarized=${release.notarized} channel=${release.channel} macUpdate=${release.macUpdate}`)
    console.log(`  字段         ${RELEASE_FIELDS.join(' → ')}`)
    console.log(`  落点         ${out}（${text.length} 字节，确定性输出 + 原子写）`)
  }
}

main()
