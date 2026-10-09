/**
 * electron-builder `afterSign` 钩子（M4-2a）：逐对象 ad-hoc 重签的**补齐与复核**。
 *
 * ## 为什么不是"无脑把包里每个 Mach-O 重签一遍"
 *
 * macOS 的签名是**两层**的：外层 `.app` 的签名里有一份 `CodeResources` 封条，把包内所有资源
 * （包括那些我们没有独立签名的文件）的哈希都封进去了。所以**在 afterSign 阶段改动任何一个包内文件，
 * 都会让外层封条失效**——症状是 `codesign --verify --deep --strict` 报
 * `a sealed resource is missing or invalid`，而包"看起来"是有签名的。
 * 计划 §M4-2a 说的"对已签名子树设置不重签的排除项，避免同一对象签两遍"，落到实现上就是这个约束。
 *
 * ## 实际的做法（三态）
 *
 * osx-sign（electron-builder 的签名实现）在签外层 bundle 之前会**先**逐对象签包内的 Mach-O
 * （实测：`payloads/**` 里的 `opencode` 二进制与 dsh 的 `.node` 都是它签的，identifier 形如
 * `opencode-<uuid>`）。因此本钩子的职责不是"重签一遍"，而是**把漏掉的补齐、并把结果说出来**：
 *
 *   1. 扫包里每个 Mach-O，读它的签名状态（是否有签名、是否带 hardened runtime 标记）；
 *   2. **一个都不缺** → 什么都不改，打印"osx-sign 已覆盖 N 个对象"（这是正常路径）；
 *   3. **有缺的**（未签名 / 缺 `runtime` 标记）→ 由深到浅逐个补签，最后**重签外层 `.app` 与 Helpers**
 *      把封条重新封上（这一步是必须的，否则第 2 步的改动会让整包校验红）。
 *
 * 三态都要出声：静默"修好了"会让下一次的 `make verify/signature` 变成唯一发现点，而那时人已经
 * 忘了钩子动过手。
 *
 * ## identity 可注入
 *
 * 从 electron-builder 的 mac 配置读（今天 `identity: "-"` = ad-hoc；将来填 Developer ID 名字即可），
 * 也可用 `MATE_SIGN_IDENTITY` 覆盖（人工验证用）。**取不到 identity 就不签**——绝不"猜一个身份去签"。
 */

const { execFileSync, spawnSync } = require('node:child_process')
const { existsSync, readdirSync, readFileSync, openSync, readSync, closeSync, statSync } = require('node:fs')
const { join } = require('node:path')

/** Mach-O 的四种魔数（含大小端变体）与 universal binary 的两种。前 4 字节判定足够，且不依赖 `file`。 */
const MACHO_MAGICS = new Set([
  0xfeedface, // MH_MAGIC
  0xcefaedfe, // MH_CIGAM
  0xfeedfacf, // MH_MAGIC_64
  0xcffaedfe, // MH_CIGAM_64
  0xcafebabe, // FAT_MAGIC
  0xbebafeca, // FAT_CIGAM
  0xcafebabf, // FAT_MAGIC_64
  0xbfbafeca, // FAT_CIGAM_64
])

function isMachO(abs) {
  let fd
  try {
    fd = openSync(abs, 'r')
    const buf = Buffer.alloc(4)
    if (readSync(fd, buf, 0, 4, 0) !== 4) return false
    return MACHO_MAGICS.has(buf.readUInt32BE(0))
  } catch {
    return false
  } finally {
    if (fd !== undefined) closeSync(fd)
  }
}

/** 收集包里全部 Mach-O（跳过符号链接：它们的签名由目标对象负责）。 */
function collectMachO(root) {
  const out = []
  const visit = (dir) => {
    for (const name of readdirSync(dir)) {
      const abs = join(dir, name)
      let st
      try {
        st = statSync(abs)
      } catch {
        continue
      }
      if (st.isSymbolicLink()) continue
      if (st.isDirectory()) {
        visit(abs)
      } else if (st.isFile() && isMachO(abs)) {
        out.push(abs)
      }
    }
  }
  visit(root)
  // 深的先签、浅的后签（嵌套对象先于宿主），与 codesign 的要求一致。
  return out.sort((a, b) => b.split('/').length - a.split('/').length)
}

/**
 * 读一个对象的签名事实。
 *
 * 必须用 spawnSync 而不是 execFileSync：`codesign -dv` 把结论写在 **stderr**，而 execFileSync 在
 * 成功时只给 stdout —— 于是"已签名"的对象会被判成"没签名"，本钩子就会在每一条上都做一次无谓补签
 * （实测踩到：44/44 全被判缺 runtime，而 `codesign -dv` 明明写着 `flags=0x10002(adhoc,runtime)`）。
 */
function signatureOf(abs) {
  const r = spawnSync('codesign', ['-dv', '--verbose=4', abs], { encoding: 'utf8' })
  const text = `${r.stdout ?? ''}${r.stderr ?? ''}`
  return {
    signed: /^Signature=/m.test(text) && !/code object is not signed at all/.test(text),
    // flags 行形如 `flags=0x10000(runtime)`；ad-hoc 是 `flags=0x2(adhoc)`。
    runtime: /flags=0x[0-9a-f]+\([^)]*\bruntime\b/.test(text),
    adhoc: /\badhoc\b/.test(text),
    text,
  }
}

/** 补签一个对象（`--force` 覆盖既有签名；`--timestamp=none` 不需要联网的时间戳服务）。 */
function signOne(abs, { identity, entitlements }) {
  const args = ['--force', '--options', 'runtime', '--timestamp=none']
  if (entitlements && existsSync(entitlements)) args.push('--entitlements', entitlements)
  args.push('--sign', identity, abs)
  execFileSync('codesign', args, { stdio: ['ignore', 'pipe', 'pipe'] })
}

/** 重签外层 `.app`（重新封装 CodeResources）与 Electron 的 Helpers。 */
function reseal(appPath, { identity, entitlements, entitlementsInherit }) {
  const frameworks = join(appPath, 'Contents', 'Frameworks')
  if (existsSync(frameworks)) {
    for (const name of readdirSync(frameworks)) {
      if (!name.endsWith('.app')) continue
      signOne(join(frameworks, name), { identity, entitlements: entitlementsInherit ?? entitlements })
    }
  }
  signOne(appPath, { identity, entitlements })
}

/** electron-builder 调用入口。 */
module.exports = async function afterSign(context) {
  const appPath = context?.appOutDir && context?.packager?.appInfo?.productFilename
    ? join(context.appOutDir, `${context.packager.appInfo.productFilename}.app`)
    : null
  if (!appPath || !existsSync(appPath)) {
    console.log(`  · afterSign：拿不到 .app 路径（appPath=${appPath}），跳过逐对象复核`)
    return
  }

  const mac = context.packager.platformSpecificBuildOptions ?? {}
  const identity = process.env.MATE_SIGN_IDENTITY || mac.identity || null
  const buildDir = join(context.packager.info.projectDir ?? process.cwd(), 'build')
  const entitlements = mac.entitlements ? join(context.packager.info.projectDir ?? process.cwd(), mac.entitlements) : join(buildDir, 'entitlements.mac.plist')
  const entitlementsInherit = join(buildDir, 'entitlements.mac.inherit.plist')

  if (!identity) {
    console.log('  · afterSign：没有可用 identity（mac.identity 为空且未设 MATE_SIGN_IDENTITY），跳过逐对象复核')
    return
  }

  const objects = collectMachO(appPath)
  const missing = []
  for (const abs of objects) {
    const sig = signatureOf(abs)
    if (!sig.signed || !sig.runtime) missing.push({ abs, signed: sig.signed, runtime: sig.runtime })
  }

  if (missing.length === 0) {
    console.log(`  · afterSign：包内 ${objects.length} 个 Mach-O 全部已签名且带 hardened runtime（osx-sign 已覆盖，未改任何对象）`)
    return
  }

  const reportOnly = ['1', 'true', 'yes'].includes(String(process.env.MATE_SIGN_REPORT_ONLY ?? '').toLowerCase())
  console.log(
    `  · afterSign：发现 ${missing.length}/${objects.length} 个对象缺签名或缺 runtime 标记` +
      (reportOnly ? '（MATE_SIGN_REPORT_ONLY=1：只报告，不补签）' : '，逐个补签后重封外层封条'),
  )
  for (const item of missing) {
    if (!reportOnly) signOne(item.abs, { identity, entitlements: entitlementsInherit })
    console.log(`      补签 ${item.abs.replace(appPath, '.app')}`)
  }
  if (reportOnly) return
  reseal(appPath, { identity, entitlements, entitlementsInherit })
  console.log('  · afterSign：补签完成，外层已重新封装（make verify/signature 是判据）')
}
