/**
 * `release.json`（M4-1，计划 §0.3「唯一发布契约」）的公共装配件：
 * 字段契约、确定性序列化、原子写、资产摘要、载荷清单。
 *
 * ## 为什么单独成文件
 *
 * 三个脚本要读**同一套**规则：`build-release-json.mjs`（生成）、`verify-release-json.mjs`
 * （逐项复算）、以及 M4-5 的合规门禁（NOTICES / SBOM 从它派生）。三处各写一份的典型后果是
 * "生成时算出来的摘要"与"校验时算出来的摘要"不是同一个东西，于是门禁永远绿或永远红。
 *
 * ## 两条纪律
 *
 * 1. **确定性（C9）**：字段顺序固定（`RELEASE_FIELDS`）、无时间戳、逐文件排序、原子写
 *    （先写同级临时文件再 rename）。两次构建的 release.json 除版本号外**逐字节相同**。
 * 2. **摘要与核心侧同一套算法**：`assetDigest()` 算出的 webDigest 必须与 Go
 *    `router.compositeDigest`（逐文件 sha256，按相对路径排序后 `rel\0hex\n` 拼接再 sha256）
 *    得到同一个值——两侧不一致 = 版本闸门（设计 §6.7）当场失去意义。逐字节对照由
 *    `verify-release-json.mjs --live`（拿核心 `/meta` 的回报值比）钉住，**不靠人工比对**。
 */

import { existsSync, readdirSync, readFileSync, renameSync, statSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

import { DIGEST_PREFIX, compositeDigest, fileDigest } from './payload.mjs'

/**
 * release.json 的字段与**顺序**（唯一发布契约，计划 §0.3 + §12.27 的交付形态四字段）。
 *
 * 顺序即序列化顺序：校验侧按这个数组逐字比对 `Object.keys(release)`，多一个、少一个、
 * 挪个位置都红——契约变更必须先改设计文档（计划 §12「偏离处理」第 2 条）。
 */
export const RELEASE_FIELDS = [
  'appVersion',
  'coreVersion',
  'webBuildId',
  'webDigest',
  'apiContract',
  'electron',
  'node',
  'nodeAbi',
  'payloads',
  // 以下四个是"交付事实"（§12.27）：不付费、不上架 → ad-hoc 签名 + 内网 tar.gz。
  // 将来拿到证书时，**这三个字段是唯一改动点**（M4-2b）。
  'signing',
  'notarized',
  'channel',
  'macUpdate',
]

/** payloads[] 里一条的字段与顺序。 */
export const PAYLOAD_FIELDS = ['name', 'version', 'digest']

/** 打包必须随包的两份载荷（M3-1）：缺任何一份都不是"零依赖首启"（R2/V1）。 */
export const REQUIRED_PAYLOADS = ['dsh', 'opencode']

export function readJson(abs) {
  return JSON.parse(readFileSync(abs, 'utf8'))
}

/**
 * 确定性序列化：`JSON.stringify(…, 2)` 的键序＝对象字面量键序（调用方按 RELEASE_FIELDS 构造），
 * 末尾补一个换行（diff 友好、逐字节可复现）。
 */
export function serializeRelease(release) {
  const ordered = {}
  for (const key of RELEASE_FIELDS) {
    if (!(key in release)) throw new Error(`release.json 缺字段 ${key}（字段集见 RELEASE_FIELDS）`)
    ordered[key] = release[key]
  }
  ordered.payloads = release.payloads.map((p) => {
    const row = {}
    for (const key of PAYLOAD_FIELDS) {
      if (!(key in p)) throw new Error(`payloads[] 的条目缺字段 ${key}：${JSON.stringify(p)}`)
      row[key] = p[key]
    }
    return row
  })
  return `${JSON.stringify(ordered, undefined, 2)}\n`
}

/**
 * 原子写一份文本：先写同级 `.tmp-<pid>` 再 rename。
 *
 * 为什么不能直接 writeFileSync：release.json 是 electron-builder 的 extraResources 输入，
 * 也是 `make verify/release-json` 的判据。半份文件（构建中途被 Ctrl-C）会以"JSON 语法错误"
 * 或更坏的"字段少了但能解析"的形态留在盘上，而打包可能照样进行下去。
 */
export function writeTextAtomic(absPath, text) {
  const tmp = `${absPath}.tmp-${process.pid}`
  writeFileSync(tmp, text)
  renameSync(tmp, absPath)
  return absPath
}

/** 原子写 release.json（顺序由 `RELEASE_FIELDS` 决定，与校验侧的逐字比对同一份文本）。 */
export function writeReleaseAtomic(absPath, release) {
  return writeTextAtomic(absPath, serializeRelease(release))
}

/**
 * 前端资产的逐文件摘要与合成摘要（与 Go `router.withDigests` 同算法）。
 *
 * 与 Go 侧的三处对齐（任何一处不一致，webDigest 就不再是"核心实际服务的那份资产"的指纹）：
 *   - 相对路径一律 POSIX 分隔（Go 的 `fs.WalkDir` 也是斜杠）；
 *   - **不过滤任何文件**（点文件、build-id.txt 都在内）；
 *   - 合成方式 = 排序后 `rel\0hex\n` 拼接再 sha256（`compositeDigest`）。
 *
 * @returns {{digest: string, files: number, bytes: number, buildId: string}}
 */
export function assetDigest(rootDir) {
  if (!existsSync(rootDir)) throw new Error(`资产目录不存在：${rootDir}`)
  const entries = []
  let bytes = 0
  const visit = (dir, prefix) => {
    for (const name of readdirSync(dir).sort()) {
      const abs = join(dir, name)
      const rel = prefix ? `${prefix}/${name}` : name
      const st = statSync(abs) // 跟随符号链接：Go 侧 fs.ReadFile 同样跟随
      if (st.isDirectory()) {
        visit(abs, rel)
        continue
      }
      if (!st.isFile()) throw new Error(`${abs} 既不是目录也不是普通文件；资产目录里不允许出现它`)
      entries.push({ rel, digest: fileDigest(abs) })
      bytes += st.size
    }
  }
  visit(rootDir, '')

  if (entries.length === 0) throw new Error(`资产目录里没有任何文件：${rootDir}`)
  if (!entries.some((e) => e.rel === 'index.html')) {
    throw new Error(`资产目录里没有 index.html（核心会拒绝启动，M2-1 的失败语义）：${rootDir}`)
  }

  const buildIdPath = join(rootDir, 'build-id.txt')
  if (!existsSync(buildIdPath)) {
    // 不是"回退 unknown"：打包是把资产冻进交付物的那一刻，这里读不到就是构建链缺了一环
    // （前端构建的 forge-build-id 插件负责写它，M2-4）。
    throw new Error(`资产目录里没有 build-id.txt（先跑 \`make web\` / \`make core/desktop\`）：${rootDir}`)
  }
  const buildId = readFileSync(buildIdPath, 'utf8').trim()
  if (!buildId) throw new Error(`build-id.txt 是空的：${buildIdPath}`)

  return { digest: compositeDigest(entries), files: entries.length, bytes, buildId }
}

/** 摘要的短打点（人话报告用）：`sha256:abcd…`。 */
export function shortDigest(digest) {
  return digest.length > 16 ? `${digest.slice(0, 16)}…` : digest
}

/**
 * 扫描已装配的载荷清单：`<root>/<name>/<version>/payload.json`。
 *
 * 与 `assert-engines.mjs` 的扫描口径一致（同一个目录、同一份清单），按 name 排序返回
 * `release.json` 需要的三字段。
 */
export function readPayloadManifests(rootDir) {
  if (!existsSync(rootDir)) {
    throw new Error(`载荷目录不存在：${rootDir}\n  先跑 \`make desktop/payloads\`（M3-1 装配 dsh 闭包与 opencode 二进制）`)
  }
  const found = []
  for (const name of readdirSync(rootDir).sort()) {
    const nameDir = join(rootDir, name)
    if (!statSync(nameDir).isDirectory()) continue
    for (const version of readdirSync(nameDir).sort()) {
      const dir = join(nameDir, version)
      if (!statSync(dir).isDirectory()) continue
      const manifestPath = join(dir, 'payload.json')
      if (!existsSync(manifestPath)) continue
      const manifest = readJson(manifestPath)
      for (const key of ['name', 'version', 'digest']) {
        if (typeof manifest[key] !== 'string' || !manifest[key]) {
          throw new Error(`${manifestPath} 的 ${key} 不合法：${JSON.stringify(manifest[key])}`)
        }
      }
      if (manifest.name !== name || manifest.version !== version) {
        throw new Error(
          `${manifestPath} 的清单与目录不一致：目录是 ${name}/${version}，清单写的是 ${manifest.name}/${manifest.version}`,
        )
      }
      found.push({ name: manifest.name, version: manifest.version, digest: manifest.digest, manifestPath })
    }
  }
  found.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
  if (found.length === 0) {
    throw new Error(`${rootDir} 下没扫到任何 <name>/<version>/payload.json\n  先跑 \`make desktop/payloads\``)
  }
  const names = found.map((p) => p.name)
  const missing = REQUIRED_PAYLOADS.filter((name) => !names.includes(name))
  if (missing.length > 0) {
    throw new Error(
      `随包载荷缺 ${missing.join('、')}（现有 ${names.join('、')}）：桌面包必须自带这两份载荷，` +
        '否则 R2/V1 的"零依赖首启"不成立',
    )
  }
  return found
}

/** 摘要算法常量（与核心侧同款）；导出给校验侧做前缀断言，避免"少了 sha256: 前缀"这种静默不一致。 */
export { DIGEST_PREFIX }
