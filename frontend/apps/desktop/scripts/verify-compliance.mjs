#!/usr/bin/env node
/**
 * 合规门禁（M4-5，设计 §11 / V14）：判"随包 `THIRD-PARTY-NOTICES` + SBOM 与 `release.json` 一致"。
 *
 * ## 为什么不能只是"文件存在、能解析"
 *
 * 这两个文件是**义务的载体**，而义务只会以"漏"的形式失效：新增一个依赖、换过一次载荷、
 * 改过一个版本号——文件照样存在、照样能解析，但内容已经不覆盖包里实际分发的东西了。
 * 所以这里的每条判据都是**复算**：
 *
 *	① 生成物逐字可复算 —— 用同一套输入（release.json + 载荷清单 + 载荷树 + 锁文件）重算一遍，
 *	   与盘上那份逐字节比对（抓"手改"与"改了输入忘了重生成"）；
 *	② 组件与契约一致 —— 组件集由 `collectComplianceModel` 交叉校验（版本必须在 release.json、
 *	   载荷清单与 pnpm 锁文件里同时成立；缺许可证字段的包必须显式登记）；
 *	③ SBOM 形状 —— 解析回对象后逐项断言（CycloneDX 字段、每个组件的 purl/licenses 形状）；
 *	④ `--require-artifacts` —— 打出来的包里 `<Resources>/compliance/` 那两份与源文件**逐字节相同**
 *	   （V14 判的是**包内**那份，不是工作区里那份）。
 *
 * 用法：
 *   node scripts/verify-compliance.mjs                    # ① ② ③
 *   node scripts/verify-compliance.mjs --require-artifacts # 再加 ④（需要先 make desktop/dist）
 *   node scripts/verify-compliance.mjs --from-tarball      # ④ 判 tar.gz 解包后的那一份（分发形态）
 *
 * 退出码 1 = 有任何一条不符（人话报告在 stdout）。
 */

import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { parseArgs } from 'node:util'

import {
  COMPLIANCE_DIRNAME,
  CYCLONEDX_SPEC_VERSION,
  NOTICES_FILENAME,
  PAYLOAD_PACKAGES,
  SBOM_FILENAME,
  collectComplianceModel,
  licensesField,
  purlFor,
  renderComplianceArtifacts,
} from './lib/compliance.mjs'
import { RELEASE_FIELDS, readJson } from './lib/release.mjs'

const SCRIPT_DIR = import.meta.dirname
const DESKTOP = resolve(SCRIPT_DIR, '..')
const RELEASE_JSON = join(DESKTOP, 'release.json')
const PAYLOADS = join(DESKTOP, 'resources', 'payloads')
const RUNTIME = join(DESKTOP, 'runtime')
const COMPLIANCE = join(DESKTOP, 'resources', COMPLIANCE_DIRNAME)
const RELEASE_DIR = join(DESKTOP, 'release')

let failures = 0
function check(name, ok, detail = '') {
  if (!ok) failures++
  console.log(`  ${ok ? '✅' : '❌'} ${name}${detail ? ` — ${detail}` : ''}`)
  return !!ok
}
const section = (t) => console.log(`\n=== ${t} ===`)

function fail(message) {
  console.error(`verify-compliance: ${message}`)
  process.exit(1)
}

/** 跑一条命令（不抛：调用方决定怎么判）。 */
function run(cmd, args) {
  const r = spawnSync(cmd, args, { encoding: 'utf8' })
  return { status: r.status ?? (r.error ? -1 : 0), stdout: r.stdout ?? '', stderr: r.stderr ?? (r.error ? String(r.error.message) : '') }
}

/** 在解包目录里找 `*.app` 与其 Resources 根。 */
function findAppIn(root) {
  const entries = readdirSync(root, { withFileTypes: true })
  const app = entries.find((e) => e.isDirectory() && e.name.endsWith('.app'))
  if (!app) throw new Error(`${root} 下没有 .app（解包目录不对？）`)
  const appPath = join(root, app.name)
  return { appPath, resourcesPath: join(appPath, 'Contents', 'Resources') }
}

/** 组件是否出现在 NOTICES 文本里（`name version` 相邻，避免只匹配到名字）。 */
function noticesMentions(text, name, version) {
  return text.includes(`${name} ${version}`)
}

function main() {
  const rawArgs = process.argv.slice(2)
  const args = rawArgs[0] === '--' ? rawArgs.slice(1) : rawArgs
  const { values } = parseArgs({
    args,
    options: {
      'require-artifacts': { type: 'boolean', default: false },
      'from-tarball': { type: 'boolean', default: false },
      help: { type: 'boolean', default: false },
    },
  })
  if (values.help) {
    console.log('用法：node scripts/verify-compliance.mjs [--require-artifacts] [--from-tarball]')
    return
  }
  const requireArtifacts = values['require-artifacts'] || values['from-tarball']

  console.log('==> 合规门禁（M4-5 / V14）')

  // ① 生成物在场 + 逐字可复算。
  section('① 生成物逐字可复算（生成物不手改，C9）')
  if (!existsSync(RELEASE_JSON)) fail(`${RELEASE_JSON} 不存在\n  先跑 \`make desktop/dist\``)
  const release = readJson(RELEASE_JSON)
  const releaseKeys = Object.keys(release)
  check('release.json 字段集与顺序符合唯一发布契约', JSON.stringify(releaseKeys) === JSON.stringify(RELEASE_FIELDS), releaseKeys.join(', '))

  for (const file of [NOTICES_FILENAME, SBOM_FILENAME]) {
    if (!existsSync(join(COMPLIANCE, file))) {
      fail(`缺 ${join(COMPLIANCE, file)}\n  先跑 \`node scripts/gen-compliance.mjs\`（desktop/dist 会自动跑）`)
    }
  }

  let model
  let artifacts
  try {
    model = collectComplianceModel({ release, payloadsRoot: PAYLOADS, runtimeRoot: RUNTIME })
    artifacts = renderComplianceArtifacts(model)
  } catch (e) {
    check('组件盘点（release.json ↔ 载荷清单 ↔ 锁文件）', false, e.message.split('\n')[0])
    console.log(`\n==> 失败：${failures} 条不符`)
    process.exit(1)
  }
  check('组件盘点（release.json ↔ 载荷清单 ↔ 锁文件）', true,
    `${model.packages.length} 个 npm 包（${model.bundled.length} 个 npm 自带发布）+ 载荷 ${model.payloads.length} 份 + 运行时 ${model.runtimes.length} 个`)

  const sourceTexts = {}
  for (const [file, expected] of Object.entries(artifacts)) {
    const onDisk = readFileSync(join(COMPLIANCE, file), 'utf8')
    sourceTexts[file] = onDisk
    check(`${file} 与复算结果逐字节相同`, onDisk === expected,
      onDisk === expected ? `${Buffer.byteLength(onDisk)} 字节` : '盘上那份与复算结果不同（手改过，或改了输入没重生成）')
  }

  // ② 组件与契约一致（collectComplianceModel 已经硬校验；这里把"判据要点"显式打点，
  //    让报告自己说清它比过什么，而不是只给一句 ok）。
  section('② 组件与 release.json / 载荷清单一致')
  const sbom = JSON.parse(sourceTexts[SBOM_FILENAME])
  const noticesText = sourceTexts[NOTICES_FILENAME]
  const components = Array.isArray(sbom.components) ? sbom.components : []
  check('SBOM metadata.component.version = release.appVersion', sbom.metadata?.component?.version === release.appVersion, String(sbom.metadata?.component?.version))
  check('组件总数 = 运行时 2 + npm 包 n（**不**给载荷另造组件：载荷根那份包本身就在组件里）',
    components.length === model.runtimes.length + model.packages.length,
    `SBOM ${components.length} 条 / 模型 ${model.runtimes.length + model.packages.length} 条`)

  // 载荷 ↔ 真实包名的绑定由 PAYLOAD_PACKAGES 声明，并按**真实包名**核对（V14 的
  // "组件版本与清单一致"：`dsh` 这个短名不是 npm 包名，@deepseek-ai/dsh 才是）。
  const must = [
    ['electron', release.electron],
    ['node', release.node],
    ...release.payloads.map((p) => [PAYLOAD_PACKAGES[p.name], p.version]),
  ]
  for (const [name, version] of must) {
    const inSbom = components.some((c) => c.name === name && c.version === version)
    check(`SBOM 含 ${name} ${version}（取自 release.json）`, inSbom)
    check(`NOTICES 含 ${name} ${version}`, noticesMentions(noticesText, name, version))
  }
  for (const p of release.payloads) {
    const pkgName = PAYLOAD_PACKAGES[p.name]
    const root = components.find((c) => c.name === pkgName && c.version === p.version)
    check(`NOTICES 逐字带上 ${p.name} 的载荷 digest`, noticesText.includes(p.digest))
    check(`SBOM 的 ${pkgName} 组件带 mate:payloadRoot=${p.name} 与载荷 digest`,
      Boolean(root) && (root.properties ?? []).some((x) => x.name === 'mate:payloadRoot' && x.value === p.name) &&
        (root.properties ?? []).some((x) => x.name === 'mate:digest' && x.value === p.digest))
  }

  // ③ SBOM 形状（解析回对象后逐项断言；只查形状，不改动任何值）。
  section('③ SBOM 形状（CycloneDX）')
  check('bomFormat = CycloneDX', sbom.bomFormat === 'CycloneDX', String(sbom.bomFormat))
  check(`specVersion = ${CYCLONEDX_SPEC_VERSION}`, sbom.specVersion === CYCLONEDX_SPEC_VERSION, String(sbom.specVersion))
  check('无时间戳 / 无序列号（确定性：同一份输入必得同一份字节）', !('serialNumber' in sbom) && !('timestamp' in (sbom.metadata ?? {})))
  const badShape = components.filter((c) => {
    const refsOk = typeof c['bom-ref'] === 'string' && c['bom-ref'].length > 0
    const licensesOk = Array.isArray(c.licenses) && c.licenses.length > 0 && c.licenses.every((l) => l.id || l.license?.id || l.license?.name || l.expression)
    const purlOk = c.type !== 'library' || (typeof c.purl === 'string' && c.purl.startsWith('pkg:npm/'))
    return !(refsOk && licensesOk && purlOk && typeof c.name === 'string' && typeof c.version === 'string' && c.version)
  })
  check('每个组件都有 bom-ref / name / version / licenses，npm 组件都有 purl', badShape.length === 0, badShape.slice(0, 3).map((c) => `${c.name}@${c.version}`).join('、'))
  const dupRefs = components.map((c) => c['bom-ref']).filter((r, i, all) => all.indexOf(r) !== i)
  check('bom-ref 无重复（同名同版本只能有一条）', dupRefs.length === 0, dupRefs.slice(0, 3).join('、'))
  // licenses 的形状函数必须与生成侧同款：拿几个组件回代一遍，证明"渲染规则"没有第二份实现。
  const sample = components.find((c) => c.name === 'electron')
  check('licenses 形状由 lib/compliance.mjs 的同一函数产出', JSON.stringify(sample?.licenses) === JSON.stringify(licensesField('MIT')))
  const purlSample = components.find((c) => c.name.startsWith('@'))
  if (purlSample) {
    check('作用域包的 purl 形如 pkg:npm/%40scope/name@version', purlSample.purl === purlFor(purlSample.name, purlSample.version), purlSample.purl)
  }

  // ④ 包内那份（V14 的"安装包内"）：与源文件逐字节相同。
  let extracted = null
  if (requireArtifacts) {
    section('④ 包内 compliance（与源文件逐字节相同）')
    let resourcesPath = null
    let label = ''
    if (values['from-tarball']) {
      const tarballs = existsSync(RELEASE_DIR) ? readdirSync(RELEASE_DIR).filter((f) => f.endsWith('.tar.gz')) : []
      if (tarballs.length !== 1) fail(`需要一个 tar.gz 产物（现有 ${tarballs.length} 个）\n  先跑 \`make desktop/dist\``)
      const dir = mkdtempSync(join(tmpdir(), 'mate-verify-compliance-'))
      const untar = run('tar', ['-xzf', join(RELEASE_DIR, tarballs[0]), '-C', dir])
      check('tar.gz 可解', untar.status === 0, untar.stderr.split('\n')[0] ?? '')
      if (untar.status === 0) {
        try {
          const found = findAppIn(dir)
          resourcesPath = found.resourcesPath
          extracted = dir
          label = tarballs[0]
        } catch (e) {
          check('tar.gz 内是预期的 .app 结构', false, e.message)
        }
      }
    } else {
      const app = join(RELEASE_DIR, 'mac-arm64', 'ark-mate.app')
      if (!existsSync(app)) fail(`没有 ${app}\n  先跑 \`make desktop/dist\`（或用 --from-tarball 判分发形态）`)
      resourcesPath = join(app, 'Contents', 'Resources')
      label = 'release/mac-arm64/ark-mate.app'
    }
    if (resourcesPath) {
      for (const file of [NOTICES_FILENAME, SBOM_FILENAME]) {
        const inPackage = join(resourcesPath, COMPLIANCE_DIRNAME, file)
        const ok = existsSync(inPackage) && readFileSync(inPackage, 'utf8') === sourceTexts[file]
        check(`包内 ${COMPLIANCE_DIRNAME}/${file} 与源文件逐字节相同`, ok, existsSync(inPackage) ? '' : '（文件不在包里）')
      }
      const inPackageRelease = join(resourcesPath, 'release.json')
      check('包内 release.json 与源文件逐字节相同', existsSync(inPackageRelease) && readFileSync(inPackageRelease, 'utf8') === readFileSync(RELEASE_JSON, 'utf8'))
      console.log(`    （判的是 ${label}）`)
    }
  }

  if (extracted) rmSync(extracted, { recursive: true, force: true })

  console.log(failures === 0
    ? `\n==> 通过：合规生成物与 ${values['from-tarball'] ? 'tar.gz 内' : requireArtifacts ? '包内' : '契约'}逐项一致`
    : `\n==> 失败：${failures} 条不符`)
  if (failures > 0) process.exit(1)
}

main()
