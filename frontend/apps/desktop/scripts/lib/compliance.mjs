/**
 * 合规生成物（M4-5，设计 §11）的公共装配件：组件盘点、`THIRD-PARTY-NOTICES` 文本、
 * CycloneDX SBOM。
 *
 * ## 为什么要有生成物，而不是手写一份 NOTICES
 *
 * 随包再分发要履行的是**许可证义务**（保留版权与许可声明），而义务的边界由"包里到底有哪些
 * 第三方代码"决定——那是 600 多个 npm 包（dsh 的生产闭包 + npm 自带的 bundles）加上
 * Electron / Node 运行时。手写一份必然漂移，而且漂移的方向总是"漏"（新增依赖没人记得加）。
 * 所以：**生成物**（C9）+ **逐字节复算**（`verify-compliance.mjs`）+ **交叉校验**
 * （版本必须同时出现在 `release.json`、载荷清单与锁文件里）。
 *
 * ## 输入与"事实"的分工
 *
 *	release.json          整套发布契约：appVersion / electron / node / nodeAbi / payloads[]
 *	payload.json          每份载荷的清单（name/version/digest/nodeAbi/fileList）——与契约逐项对照
 *	载荷树里的 package.json  每个第三方包的**许可证事实**（license / licenses 字段，原样保留）
 *	pnpm 锁文件            pin 事实：树里出现的包必须在锁文件里同版本钉死（npm 自带的 bundles 除外）
 *
 * ## 一条刻意的口径：许可证**不纠正**
 *
 * 包自己写什么就记什么（`Apache 2.0` 就写 `Apache 2.0`，不"帮它"改成 SPDX 的 `Apache-2.0`）。
 * 纠正等于我们替上游编造许可事实；而 SBOM 里 `license.id` 与 `license.name` 都是合法形状，
 * 形状由 `licensesField()` 按"能不能原样当 id 用"决定。缺字段的包**必须显式登记**在
 * `LICENSE_FALLBACKS`（见那里的注释），否则生成期直接失败——不允许静默降级成"许可证未知"。
 */

import { existsSync, mkdirSync, readdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

import { REQUIRED_PAYLOADS, readJson } from './release.mjs'

/** 生成物的落点与文件名（随包时经 electron-builder 的 extraResources 进 `<Resources>/compliance/`）。 */
export const COMPLIANCE_DIRNAME = 'compliance'
export const NOTICES_FILENAME = 'THIRD-PARTY-NOTICES.txt'
export const SBOM_FILENAME = 'sbom.cdx.json'
/** CycloneDX 版本：1.5 是当前被广泛支持的版本（含 `platform` 组件类型与 `properties`）。 */
export const CYCLONEDX_SPEC_VERSION = '1.5'

/** 本应用自己的许可与出处（仓库根 LICENSE 是 MIT；显式声明，不从文本里"识别"）。 */
export const APP_LICENSE = { license: 'MIT', source: 'https://github.com/morehao/ark-forge' }

/** 已知上游出处（key = 包名 / 运行时名）。没有条目的包按 npm 注册表页机械推导。 */
export const UPSTREAM = {
  electron: 'https://github.com/electron/electron',
  node: 'https://nodejs.org/',
  '@deepseek-ai/dsh': 'https://www.npmjs.com/package/@deepseek-ai/dsh',
  'opencode-darwin-arm64': 'https://github.com/anomalyco/opencode',
}

/**
 * 载荷名 → 它在 npm 上的**真实包名**。
 *
 * 为什么需要这张表：`release.json` 里的载荷名是**平台侧的叫法**（`dsh` / `opencode`，
 * 也是 `FORGE_RUNTIME_<NAME>` 与 `payloads/<name>/` 的键），而许可证义务挂在**真实包**上
 * （`@deepseek-ai/dsh` / `opencode-darwin-arm64`）。两者的绑定不能靠"看起来像"来猜：
 * 这里是**声明**，生成期硬校验"载荷树里确实有这个名字的包、版本也一致"，对不上就失败
 * （包改名/换发行包时当场红，而不是让 NOTICES 里出现一个不存在的组件名）。
 */
export const PAYLOAD_PACKAGES = {
  dsh: '@deepseek-ai/dsh',
  opencode: 'opencode-darwin-arm64',
}

/**
 * 上游漏写许可证字段时的**显式**补充（key = 包名）。
 *
 * 为什么不用"找不到就当 unknown"：那会让 NOTICES 从此少一个组件而没人发现——而许可证义务
 * 恰恰是"漏一个就违约"的东西。缺字段就报错，逼人来这里登记（登记时去看上游仓库的 LICENSE）。
 */
export const LICENSE_FALLBACKS = {
  'opencode-darwin-arm64': {
    license: 'MIT',
    source: 'https://github.com/anomalyco/opencode/blob/dev/LICENSE',
  },
}

/** 包管理器元数据（不属于载荷内容，也不是"包"）。 */
const IGNORED_DIRS = new Set(['.bin', '.pnpm', '.modules.yaml'])

/** SBOM 组件的类型（CycloneDX component.type 的闭集取值）。 */
const COMPONENT_TYPE = { app: 'application', electron: 'framework', node: 'platform', npm: 'library' }

/** `pkg:npm` purl：作用域包的 `@` 要写成 `%40`，`/` 保持原样（purl 规范里的 npm 例子如此）。 */
export function purlFor(name, version) {
  const encoded = name.startsWith('@') ? `%40${name.slice(1)}` : name
  return `pkg:npm/${encoded}@${version}`
}

/**
 * 把 package.json 里的许可证字段归一成一个字符串（原样保留文本，不做 SPDX 纠正）。
 *
 * 三种上游形态都要认（实测都存在）：
 *   `"license": "MIT"`、`"license": {"type": "MIT"}`、`"licenses": [{"type": "Apache 2.0"}]`
 */
export function normalizeLicenseField(pkg) {
  const one = (v) => {
    if (typeof v === 'string') return v.trim()
    if (v && typeof v.type === 'string') return v.type.trim()
    return ''
  }
  if (pkg.license) {
    const s = one(pkg.license)
    if (s) return s
  }
  if (Array.isArray(pkg.licenses)) {
    const parts = pkg.licenses.map(one).filter(Boolean)
    if (parts.length > 0) return parts.join(' OR ')
  }
  return ''
}

/**
 * CycloneDX 的 `licenses` 数组。
 *
 * 判定只有一条、且是**形状**判定而不是语义纠正：字符串能原样当 SPDX id 用（不含空格等
 * 特殊字符）就写 `id`，否则写 `name`；出现 SPDX 表达式运算符（`OR`/`AND`/`WITH` 两侧带空格）
 * 时写 `expression`。三种都是合法 CycloneDX，且都不改变上游写的字面值。
 */
export function licensesField(raw) {
  const text = String(raw ?? '').trim()
  if (!text) throw new Error('许可证为空：调用方应先补 LICENSE_FALLBACKS')
  if (/\s(?:OR|AND|WITH)\s/.test(text)) return [{ expression: text }]
  if (/^[A-Za-z0-9.+-]+$/.test(text)) return [{ license: { id: text } }]
  return [{ license: { name: text } }]
}

/**
 * 解析 pnpm 锁文件里的 pin 事实：`packages:` 段的 `name@version` 键 → `{name: Set<version>}`。
 *
 * 为什么用行解析而不是引 YAML 库：本仓前端**零运行期依赖**（C12/C14），而这里只需要一个
 * 结构极其固定的段落（pnpm v9 的锁文件把每个包写成一个两空格缩进的顶层键）。解析不出来
 * （锁文件格式变了）时**报错**而不是静默返回空表——空表会让"pin 交叉校验"变成永远通过。
 */
export function lockfilePins(lockPath) {
  if (!existsSync(lockPath)) throw new Error(`锁文件不存在：${lockPath}`)
  const lines = readFileSync(lockPath, 'utf8').split('\n')
  const pins = new Map()
  let inPackages = false
  for (const line of lines) {
    if (/^[A-Za-z]/.test(line)) {
      inPackages = line.startsWith('packages:')
      continue
    }
    if (!inPackages) continue
    const m = line.match(/^ {2}'?(\S.*?)'?:\s*$/)
    if (!m) continue
    // `'@scope/name@1.2.3':` 或 `'name@1.2.3(peer@x)':` —— 版本在最后一个 '@' 之后、'(' 之前。
    const key = m[1]
    const at = key.lastIndexOf('@')
    if (at <= 0) continue
    const name = key.slice(0, at)
    const version = key.slice(at + 1).split('(')[0]
    if (!version) continue
    if (!pins.has(name)) pins.set(name, new Set())
    pins.get(name).add(version)
  }
  if (pins.size === 0) throw new Error(`${lockPath} 里没解析出任何包：pnpm 锁文件格式变了吗？（pin 交叉校验会因此变成永远通过）`)
  return pins
}

/**
 * 走查一份载荷树，收集所有第三方包的 `{name, version, license}`。
 *
 * **什么算"一个组件"**：`node_modules` 目录下的一级条目（作用域包算到 `@scope/name` 那一层），
 * 或者载荷根自身（opencode 形态：包内容直接铺在载荷根）。
 *
 * 为什么**不**按"任何含 package.json 的目录"来算：载荷里有大量**不是依赖**的 package.json——
 * dsh 的 skill 模板就带着 `@local/my-decoration` 这种示例工程的清单（装配期 fail-closed 当场
 * 抓到了它，见 §12.31）。把模板当成组件会把 NOTICES 变成一份"包里出现过的所有 JSON"，
 * 而许可证义务只覆盖**随包分发的依赖**。反过来，`node_modules` 里的**嵌套**依赖（pnpm 不做
 * 提升的那部分、以及 npm 自带的 `npm/node_modules/**`）必须算进来——它们确实随包走。
 */
export function collectPackagesFromTree(payloadRoot, payloadName) {
  const found = new Map()

  const record = (dir, bundledBy) => {
    const manifest = join(dir, 'package.json')
    if (!existsSync(manifest)) return undefined
    const pkg = readJson(manifest)
    if (!pkg.name || !pkg.version) return undefined
    const raw = normalizeLicenseField(pkg)
    const key = `${pkg.name}@${pkg.version}`
    const prev = found.get(key)
    if (prev) {
      if (raw && prev.license && prev.license !== raw) {
        // 同名同版本出现两份、却写着不同许可证：无法判定该报哪个，直接失败（人工看一眼）。
        throw new Error(`${payloadName} 载荷里 ${key} 出现两份且 license 不同（${prev.license} / ${raw}）`)
      }
      if (!prev.license && raw) prev.license = raw
      prev.dirs.push(dir)
      return prev
    }
    const entry = {
      name: pkg.name,
      version: pkg.version,
      license: raw,
      payloads: [payloadName],
      dirs: [dir],
      bundledBy: bundledBy ?? '',
    }
    found.set(key, entry)
    return entry
  }

  /** 该包是否声明了"自带发布"的依赖（npm 是唯一一个这么干的：它把自己的依赖打进 tarball）。 */
  const declaresBundled = (pkg) => {
    const v = pkg.bundleDependencies ?? pkg.bundledDependencies
    return Array.isArray(v) ? v.length > 0 : v === true
  }

  const walkNodeModules = (nodeModulesDir, depth, bundledBy) => {
    if (depth > 12) throw new Error(`载荷树层数超过 12：${nodeModulesDir}（软链环？装配期本应去引用）`)
    for (const e of readdirSync(nodeModulesDir, { withFileTypes: true })) {
      if (!e.isDirectory() || IGNORED_DIRS.has(e.name)) continue
      const pkgDirs = []
      if (e.name.startsWith('@')) {
        const scopeDir = join(nodeModulesDir, e.name)
        for (const s of readdirSync(scopeDir, { withFileTypes: true })) {
          if (s.isDirectory()) pkgDirs.push(join(scopeDir, s.name))
        }
      } else {
        pkgDirs.push(join(nodeModulesDir, e.name))
      }
      for (const pkgDir of pkgDirs) {
        const pkgManifest = join(pkgDir, 'package.json')
        record(pkgDir, bundledBy)
        // 嵌套 node_modules 的两种来源必须分开对待：
        //   · 该包声明了 bundleDependencies（npm）→ 里面是**它自带发布**的依赖，
        //     pnpm 锁文件根本不建模它们（版本也可能与锁文件里的同名包不同，例如 diff@8 vs 9）；
        //   · 其余（pnpm 的不提升依赖）→ 它们仍由锁文件 pin 住，必须参与交叉校验。
        const nested = join(pkgDir, 'node_modules')
        if (!existsSync(nested)) continue
        let childBundledBy = bundledBy
        if (existsSync(pkgManifest)) {
          const pkg = readJson(pkgManifest)
          if (declaresBundled(pkg)) childBundledBy = `${pkg.name}@${pkg.version}`
        }
        walkNodeModules(nested, depth + 1, childBundledBy)
      }
    }
  }

  // 载荷根自身可能就是那个包（opencode：入口 `bin/opencode` 与 package.json 都在根上）。
  record(payloadRoot, '')
  const rootNodeModules = join(payloadRoot, 'node_modules')
  if (existsSync(rootNodeModules)) walkNodeModules(rootNodeModules, 0, '')

  if (found.size === 0) throw new Error(`${payloadRoot} 下没扫到任何第三方包（载荷没装配？）`)
  return { packages: [...found.values()] }
}

/**
 * 盘点整套组件（生成与校验共用**同一个函数**：两处各写一遍的典型后果是"生成时算出来的
 * 组件集"与"校验时算出来的"不是同一个东西，门禁于是永远绿或永远红）。
 *
 * @returns {{
 *   app: {name, version, license, source},
 *   runtimes: Array<{name, kind, version, license, source}>,
 *   payloads: Array<{name, version, digest, license, source}>,
 *   packages: Array<{name, version, license, source, payloads: string[], bundledBy: string}>,
 *   bundled: string[],
 * }}
 */
export function collectComplianceModel({ release, payloadsRoot, runtimeRoot }) {
  if (!release?.appVersion) throw new Error('release.json 缺 appVersion：合规生成物的组件版本必须取自唯一发布契约')
  if (!release.electron || !release.node) {
    throw new Error('release.json 缺 electron / node：随包运行时也必须出现在 NOTICES 与 SBOM 里（设计 §11）')
  }
  if (!Array.isArray(release.payloads) || release.payloads.length === 0) {
    throw new Error('release.json 的 payloads[] 为空：合规生成物必须以实际随包载荷为准')
  }

  // 载荷清单（盘上）与契约逐项对照：契约与包内清单对不上，组件的版本就是错的。
  const manifests = []
  for (const payload of release.payloads) {
    const manifestPath = join(payloadsRoot, payload.name, payload.version, 'payload.json')
    if (!existsSync(manifestPath)) {
      throw new Error(`载荷清单不存在：${manifestPath}\n  先跑 \`make desktop/payloads\`（M3-1 装配）`)
    }
    const manifest = readJson(manifestPath)
    if (manifest.name !== payload.name || manifest.version !== payload.version || manifest.digest !== payload.digest) {
      throw new Error(
        `载荷清单与 release.json 不一致（${payload.name}）：清单 ${manifest.name}@${manifest.version} ${manifest.digest}，` +
          `契约 ${payload.name}@${payload.version} ${payload.digest}`,
      )
    }
    manifests.push({ dir: join(payloadsRoot, payload.name, payload.version), manifest })
  }
  const names = manifests.map((m) => m.manifest.name)
  const missingRequired = REQUIRED_PAYLOADS.filter((n) => !names.includes(n))
  if (missingRequired.length > 0) {
    throw new Error(`随包载荷缺 ${missingRequired.join('、')}（现有 ${names.join('、')}）：零依赖首启不成立（R2/V1）`)
  }

  // pin 事实：一份载荷一个锁文件（`runtime/<name>/pnpm-lock.yaml`）。
  const pins = new Map()
  for (const name of names) {
    const lockPath = join(runtimeRoot, name, 'pnpm-lock.yaml')
    if (!existsSync(lockPath)) throw new Error(`载荷 ${name} 的锁文件不存在：${lockPath}`)
    for (const [pkgName, versions] of lockfilePins(lockPath)) {
      if (!pins.has(pkgName)) pins.set(pkgName, new Set())
      for (const v of versions) pins.get(pkgName).add(v)
    }
  }

  const byKey = new Map()
  const bundled = []
  for (const { manifest, dir } of manifests) {
    const { packages } = collectPackagesFromTree(dir, manifest.name)
    for (const pkg of packages) {
      const key = `${pkg.name}@${pkg.version}`
      const pinnedVersions = pins.get(pkg.name)
      if (pkg.bundledBy) {
        // 随某个包**自带发布**的依赖（npm 是唯一一个：它把自己的 node_modules 打进 tarball）。
        // 它们确实随包分发、必须进 NOTICES，但锁文件不建模它们——所以 pin 交叉校验对它们不适用，
        // 且同名包可能与锁文件版本不同（实测：npm 自带的 diff@8.0.4 vs 锁文件的 diff@9.0.0）。
        bundled.push(`${key}（${pkg.bundledBy}）`)
      } else if (!pinnedVersions) {
        throw new Error(
          `载荷 ${manifest.name} 里的 ${key} 既不在锁文件里、也不是任何包自带发布的 bundles：\n` +
            '  树与锁文件不是同一次安装的（先跑 make desktop/payloads 重装），或者载荷被人动过',
        )
      } else if (!pinnedVersions.has(pkg.version)) {
        throw new Error(
          `载荷 ${manifest.name} 里的 ${key} 与锁文件不符：锁文件 pin 的是 ${[...pinnedVersions].sort().join('、')}\n` +
            '  树与锁文件不是同一次安装的（先跑 make desktop/payloads 重装）',
        )
      }
      const existing = byKey.get(key)
      if (existing) {
        if (!existing.payloads.includes(manifest.name)) existing.payloads.push(manifest.name)
        continue
      }
      const fallback = LICENSE_FALLBACKS[pkg.name]
      const license = pkg.license || fallback?.license || ''
      if (!license) {
        throw new Error(
          `组件 ${key} 没有许可证字段，也没有登记在 LICENSE_FALLBACKS：\n` +
            `  去看 ${pkg.dirs[0]}/package.json 与上游仓库的 LICENSE，然后在这里显式登记（不猜、不静默记 unknown）`,
        )
      }
      byKey.set(key, {
        name: pkg.name,
        version: pkg.version,
        license,
        source: fallback?.source ?? UPSTREAM[pkg.name] ?? `https://www.npmjs.com/package/${pkg.name}`,
        payloads: [...pkg.payloads],
        bundledBy: pkg.bundledBy,
      })
    }
  }

  const packages = [...byKey.values()].sort((a, b) =>
    a.name === b.name ? (a.version < b.version ? -1 : a.version > b.version ? 1 : 0) : a.name < b.name ? -1 : 1,
  )

  // 载荷 ↔ 真实包名的绑定（见 PAYLOAD_PACKAGES）：把 digest 挂到**那个真实组件**上，
  // 而不是另造一个"载荷"组件——同一个软件在 SBOM 里出现两次（一次叫 dsh、一次叫
  // @deepseek-ai/dsh）会让清单看起来更全，实际上只是同名不同物，机器消费方没法用。
  const payloads = manifests
    .map(({ manifest }) => {
      const pkgName = PAYLOAD_PACKAGES[manifest.name]
      if (!pkgName) {
        throw new Error(`载荷 ${manifest.name} 没在 PAYLOAD_PACKAGES 里登记真实包名（NOTICES/SBOM 需要真名，不能只用短名）`)
      }
      const component = byKey.get(`${pkgName}@${manifest.version}`)
      if (!component) {
        throw new Error(
          `载荷 ${manifest.name} 声明为 ${pkgName}@${manifest.version}，但载荷树里没有这个包（声明与树不符：包改名了？）`,
        )
      }
      component.payloadRoot = manifest.name
      component.digest = manifest.digest
      return {
        name: manifest.name,
        package: pkgName,
        version: manifest.version,
        digest: manifest.digest,
        license: component.license,
        source: UPSTREAM[pkgName] ?? `https://www.npmjs.com/package/${pkgName}`,
      }
    })
    .sort((a, b) => (a.name < b.name ? -1 : 1))

  return {
    app: { name: 'ark-forge', version: release.appVersion, ...APP_LICENSE },
    runtimes: [
      { name: 'electron', kind: 'electron', version: release.electron, license: 'MIT', source: UPSTREAM.electron },
      { name: 'node', kind: 'node', version: release.node, license: 'MIT', source: UPSTREAM.node },
    ],
    payloads,
    packages,
    bundled: bundled.sort(),
    nodeAbi: release.nodeAbi,
  }
}

/** `THIRD-PARTY-NOTICES.txt` 的文本（确定性：字段序固定、组件按 name@version 排序、无时间戳）。 */
export function renderNotices(model) {
  const lines = []
  lines.push(`ark-forge ${model.app.version} — 第三方软件声明（THIRD-PARTY NOTICES）`)
  lines.push('='.repeat(72))
  lines.push('')
  lines.push(`本文件由 frontend/apps/desktop/scripts/gen-compliance.mjs 生成，**不要手工修改**：`)
  lines.push(`\`make verify/compliance\` 会按同一套输入复算一遍并**逐字节**比对（生成物不手改，C9）。`)
  lines.push(`机器可读的同源版本见同目录 ${SBOM_FILENAME}（CycloneDX ${CYCLONEDX_SPEC_VERSION}）。`)
  lines.push('')
  lines.push('生成口径')
  lines.push(`  · 组件集合 = 随包载荷树里实际存在的每个 npm 包（${model.packages.length} 个），`)
  lines.push(`    外加本应用、Electron 与 Node 运行时的确切版本——都取自 release.json（唯一发布契约）。`)
  lines.push(`  · 许可证取自各包自己的 package.json（license / licenses 字段），**原样保留**，不做 SPDX 纠正。`)
  lines.push(`  · 组件版本必须同时出现在载荷清单（payload.json）与 pnpm 锁文件里；`)
  lines.push(`    其中 ${model.bundled.length} 个是随 npm 包**自带发布**的依赖（npm 把自己的 node_modules 打进 tarball，`)
  lines.push(`    锁文件不建模它们，版本可能与锁文件里的同名包不同——所以单独标注，不参与 pin 校验）。`)
  lines.push('')
  lines.push('一、本应用与运行时')
  lines.push(`  ${model.app.name} ${model.app.version} — ${model.app.license}`)
  lines.push(`    来源 ${model.app.source}`)
  for (const rt of model.runtimes) {
    lines.push(`  ${rt.name} ${rt.version} — ${rt.license}`)
    lines.push(`    来源 ${rt.source}`)
  }
  lines.push('')
  lines.push('二、随包运行时载荷')
  for (const p of model.payloads) {
    lines.push(`  ${p.name} ${p.version} — ${p.license}（npm 包 ${p.package}，digest ${p.digest}，Node ABI ${model.nodeAbi}）`)
    lines.push(`    来源 ${p.source}`)
  }
  lines.push('')
  lines.push(`三、传递依赖（${model.packages.length} 个 npm 包，按名称排序）`)
  model.packages.forEach((pkg, i) => {
    const scope = pkg.payloads.join('+')
    const bundled = pkg.bundledBy ? `（${pkg.bundledBy} 自带发布）` : ''
    lines.push(`  ${i + 1}. ${pkg.name} ${pkg.version} — ${pkg.license}${bundled}〔载荷 ${scope}〕`)
    lines.push(`     来源 ${pkg.source}`)
  })
  lines.push('')
  return `${lines.join('\n')}`
}

/** `sbom.cdx.json` 的文本（CycloneDX，确定性：无时间戳、无序列号、组件排序固定）。 */
export function renderSbom(model) {
  const component = (type, name, version, license, purl, properties) => {
    // `bom-ref` 用 purl 本身：它天然唯一（同名同版本只有一条），且机器消费方可以直接拿它当坐标。
    // 每个组件都带 `purl`（含 Electron / Node 的 `pkg:generic/...@<版本>`）：只有 npm 组件才带
    // `pkg:npm/...`，但"能定位到具体版本"这件事对每个组件都成立。
    const out = { type, 'bom-ref': purl, name, version, purl, licenses: licensesField(license) }
    if (properties && properties.length > 0) out.properties = properties
    return out
  }
  const appRef = `pkg:generic/ark-forge@${model.app.version}`
  const runtimePurl = (name, version) => `pkg:generic/${name}@${version}`

  const components = [
    component(COMPONENT_TYPE.electron, 'electron', model.runtimes.find((r) => r.kind === 'electron').version, 'MIT',
      runtimePurl('electron', model.runtimes.find((r) => r.kind === 'electron').version), [
        { name: 'forge:source', value: UPSTREAM.electron },
      ]),
    component(COMPONENT_TYPE.node, 'node', model.runtimes.find((r) => r.kind === 'node').version, 'MIT',
      runtimePurl('node', model.runtimes.find((r) => r.kind === 'node').version), [
        { name: 'forge:source', value: UPSTREAM.node },
        { name: 'forge:nodeAbi', value: String(model.nodeAbi) },
      ]),
    ...model.packages.map((pkg) => {
      // 载荷根那份包（`@deepseek-ai/dsh` / `opencode-darwin-arm64`）就在这里，带上
      // `forge:payloadRoot` 与载荷 digest——**不另造一个"载荷"组件**（同一份软件出现两次
      // 只会让清单看起来更全，实际是同名不同物，机器消费方没法用）。
      const props = [{ name: 'forge:payload', value: pkg.payloads.join('+') }]
      if (pkg.payloadRoot) props.push({ name: 'forge:payloadRoot', value: pkg.payloadRoot })
      if (pkg.digest) props.push({ name: 'forge:digest', value: pkg.digest })
      props.push({ name: 'forge:bundledBy', value: pkg.bundledBy })
      return component(COMPONENT_TYPE.npm, pkg.name, pkg.version, pkg.license, purlFor(pkg.name, pkg.version), props)
    }),
  ]

  // 键序固定（确定性）：字面量顺序即序列化顺序；组件数组的排序由 collectComplianceModel 负责。
  const bom = {
    bomFormat: 'CycloneDX',
    specVersion: CYCLONEDX_SPEC_VERSION,
    version: 1,
    metadata: {
      component: {
        type: COMPONENT_TYPE.app,
        'bom-ref': appRef,
        name: model.app.name,
        version: model.app.version,
        licenses: licensesField(model.app.license),
        externalReferences: [{ type: 'website', url: model.app.source }],
      },
    },
    components,
  }
  return `${JSON.stringify(bom, undefined, 2)}\n`
}

/** 生成两个文本（纯函数部分）：`{[NOTICES_FILENAME]: text, [SBOM_FILENAME]: text}`。 */
export function renderComplianceArtifacts(model) {
  return {
    [NOTICES_FILENAME]: renderNotices(model),
    [SBOM_FILENAME]: renderSbom(model),
  }
}

/**
 * 原子写一份文本（先写同级临时文件再 rename）。
 *
 * 为什么不能直接 writeFileSync：这两个文件是**门禁的判据**，也是随包内容。半份文件
 * （生成中途被 Ctrl-C）会以"少了一半组件但语法正确"的形态留在盘上——那正是最坏的一种坏法。
 */
export function writeTextAtomic(absPath, text) {
  const tmp = `${absPath}.tmp-${process.pid}`
  writeFileSync(tmp, text)
  renameSync(tmp, absPath)
  return absPath
}

/** 递归建目录（生成侧用；`recursive` 对已存在的目录是幂等的）。 */
export function ensureDir(dir) {
  if (!existsSync(dir)) mkdirSync(dir, { recursive: true })
  return dir
}
