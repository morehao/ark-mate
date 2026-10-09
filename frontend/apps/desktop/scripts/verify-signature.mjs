#!/usr/bin/env node
/**
 * 签名事实的逐对象复核（M4-2a，计划 §M4-2a「验收」）。
 *
 * ## 断言什么
 *
 * 1. **外层封条完整**：`codesign --verify --deep --strict` 通过。这一条最容易假绿——只要能
 *    "打出一个包"，`codesign -dv` 就会打印一堆看起来正常的签名信息；而 afterSign 阶段动过任何
 *    包内文件、或某个嵌套对象后来被替换，都会让 `CodeResources` 封条失效，症状只在这条命令里出现。
 * 2. **每个 Mach-O 都有签名**：未签名的对象在 arm64 上会被内核直接 `SIGKILL`（设计 §8.1），
 *    而"某个 payloads 里的 `.node` 没签上"这类漏网只会在真跑到那条路径时炸——所以这里逐个扫。
 * 3. **每个 Mach-O 都带 hardened runtime 标记**（`--options runtime`）：这才是 M4-2a 的形态结论，
 *    也是设计 §6.3 回退条件 ② 的判据之一（"平台签名/hardened runtime 是否允许包内执行"）。
 * 4. **identity 与声明一致**：`release.json` 写 `signing: adhoc` 就必须全是 ad-hoc（无 Team ID）；
 *    将来填 Developer ID 时，这里改为断言 Team ID 等于期望值。
 * 5. **entitlements 真的在**（主二进制上有 `allow-jit` 与 `disable-library-validation`）：
 *    plist 文件写了不等于签进去。
 *
 * `spctl -a -vvv -t exec` 的结论**如实打印、不判失败**：ad-hoc 签名会被 Gatekeeper 拒，这是
 * §12.27 的预期（非浏览器渠道分发，quarantine 由下载器写入、我们不经过下载器）。
 *
 * ## 用法
 *
 *   node scripts/verify-signature.mjs                       # 默认 release/mac-arm64/ark-mate.app
 *   node scripts/verify-signature.mjs --app <path.app>      # 显式指定（解包后的 .app 也行）
 *   node scripts/verify-signature.mjs --from-tarball        # 先把 release/*.tar.gz 解到临时目录再判
 *   --json  只打一份确定性 JSON（给后续门禁消费）
 */

import { execFileSync, spawnSync } from 'node:child_process'
import { closeSync, existsSync, mkdtempSync, openSync, readFileSync, readSync, readdirSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { parseArgs } from 'node:util'

import { readJson } from './lib/release.mjs'

const SCRIPT_DIR = import.meta.dirname
const DESKTOP = resolve(SCRIPT_DIR, '..')
const RELEASE_DIR = join(DESKTOP, 'release')
const RELEASE_JSON = join(DESKTOP, 'release.json')
const DEFAULT_APP = join(RELEASE_DIR, 'mac-arm64', 'ark-mate.app')

const MACHO_MAGICS = new Set([
  0xfeedface, 0xcefaedfe, 0xfeedfacf, 0xcffaedfe, 0xcafebabe, 0xbebafeca, 0xcafebabf, 0xbfbafeca,
])

let failures = 0
function check(name, ok, detail = '') {
  if (!ok) failures++
  console.log(`  ${ok ? '✅' : '❌'} ${name}${detail ? ` — ${detail}` : ''}`)
  return !!ok
}
const section = (t) => console.log(`\n=== ${t} ===`)
const note = (t) => console.log(`  · ${t}`)

function run(cmd, args) {
  const r = spawnSync(cmd, args, { encoding: 'utf8' })
  if (r.error?.code === 'ENOENT') {
    // `spctl` 在 /usr/sbin（不在 /usr/bin）：只按 PATH 找会在"最小 PATH"下退化成一个看不懂的 ENOENT。
    const fallback = join('/usr/sbin', cmd)
    if (existsSync(fallback)) {
      const r2 = spawnSync(fallback, args, { encoding: 'utf8' })
      return { status: r2.status ?? (r2.error ? -1 : 0), stdout: r2.stdout ?? '', stderr: r2.stderr ?? (r2.error ? String(r2.error.message) : '') }
    }
  }
  return { status: r.status ?? (r.error ? -1 : 0), stdout: r.stdout ?? '', stderr: r.stderr ?? (r.error ? String(r.error.message) : '') }
}

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

/** 包里（或任意目录下）全部 Mach-O，按"深→浅"排序；跳过符号链接（签名由目标对象负责）。 */
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
      if (st.isDirectory()) visit(abs)
      else if (st.isFile() && isMachO(abs)) out.push(abs)
    }
  }
  visit(root)
  return out.sort((a, b) => b.split('/').length - a.split('/').length)
}

/** `codesign -dv --verbose=4` 的事实（结论在 stderr）。 */
function signatureFacts(abs) {
  const r = run('codesign', ['-dv', '--verbose=4', abs])
  const text = `${r.stdout}\n${r.stderr}`
  return {
    signed: /^Signature=/m.test(text) && !/code object is not signed at all/.test(text),
    adhoc: /\badhoc\b/.test(text),
    runtime: /flags=0x[0-9a-f]+\([^)]*\bruntime\b/.test(text),
    developerId: /Authority=Developer ID/.test(text),
    team: /^TeamIdentifier=(.+)$/m.exec(text)?.[1]?.trim() ?? '',
    identifier: /^Identifier=(.+)$/m.exec(text)?.[1]?.trim() ?? '',
    text,
  }
}

/** 一个对象上的 entitlements 键集合（plist XML；`codesign -d --entitlements :-` 输出到 stdout）。 */
function entitlementKeys(abs) {
  const r = run('codesign', ['-d', '--entitlements', ':-', abs])
  const text = `${r.stdout}${r.stderr}`
  return [...text.matchAll(/<key>([^<]+)<\/key>/g)].map((m) => m[1])
}

/** 分类打点：把人读得懂的分布打出来（"包里 92 个 Mach-O"比一句"全过"信息量大）。 */
function categorize(machos, appPath) {
  const buckets = new Map()
  for (const abs of machos) {
    const rel = abs.slice(appPath.length + 1)
    const top = rel.startsWith('Contents/Resources/payloads/')
      ? `payloads/${rel.split('/')[3] ?? '?'}`
      : rel.startsWith('Contents/Resources/')
        ? `Resources/${rel.split('/')[2] ?? '?'}`
        : rel.startsWith('Contents/Frameworks/')
          ? 'Frameworks'
          : rel.startsWith('Contents/MacOS/')
            ? 'MacOS'
            : '其它'
    buckets.set(top, (buckets.get(top) ?? 0) + 1)
  }
  return [...buckets.entries()].sort((a, b) => b[1] - a[1]).map(([k, v]) => `${k} ${v}`).join('、')
}

/** `--from-tarball`：把 release/*.tar.gz 解到临时目录，返回其中的 .app 路径。 */
function extractAppFromTarball() {
  const tarball = existsSync(RELEASE_DIR)
    ? readdirSync(RELEASE_DIR).find((f) => f.endsWith('.tar.gz'))
    : undefined
  if (!tarball) throw new Error(`release/ 下没有 .tar.gz（先跑 make desktop/dist）：${RELEASE_DIR}`)
  const dir = mkdtempSync(join(tmpdir(), 'mate-verify-signature-'))
  const untar = run('tar', ['-xzf', join(RELEASE_DIR, tarball), '-C', dir])
  if (untar.status !== 0) throw new Error(`解 ${tarball} 失败：${untar.stderr.split('\n')[0]}`)
  const app = readdirSync(dir).find((n) => n.endsWith('.app'))
  if (!app) throw new Error(`${tarball} 里没有 .app`)
  return { appPath: join(dir, app), cleanup: () => rmSync(dir, { recursive: true, force: true }) }
}

function main() {
  const rawArgs = process.argv.slice(2)
  const args = rawArgs[0] === '--' ? rawArgs.slice(1) : rawArgs
  const { values } = parseArgs({
    args,
    options: {
      app: { type: 'string' },
      'from-tarball': { type: 'boolean', default: false },
      json: { type: 'boolean', default: false },
      help: { type: 'boolean', default: false },
    },
  })
  if (values.help) {
    console.log('用法：node scripts/verify-signature.mjs [--app <path.app>] [--from-tarball] [--json]')
    return
  }

  let appPath
  let cleanup = () => {}
  try {
    if (values.app) {
      appPath = resolve(values.app)
      if (!existsSync(appPath)) throw new Error(`--app 指向的路径不存在：${appPath}`)
    } else if (values['from-tarball']) {
      const extracted = extractAppFromTarball()
      appPath = extracted.appPath
      cleanup = extracted.cleanup
    } else {
      appPath = DEFAULT_APP
      if (!existsSync(appPath)) throw new Error(`默认路径不存在：${appPath}\n  先跑 make desktop/dist（或用 --from-tarball / --app）`)
    }

    console.log(`==> 签名事实逐对象复核（M4-2a）\n  包 ${appPath}`)

    // release.json 里的签名声明是判据的一部分（"声明"与"事实"必须对上）。
    const release = existsSync(RELEASE_JSON) ? readJson(RELEASE_JSON) : null
    const declared = release?.signing ?? null
    note(`release.json 声明 signing=${declared ?? '(没有 release.json，跳过一致性判定)'}`)

    // ① 外层封条：deep + strict。afterSign 动过包内文件就会在这里红。
    section('① 外层封条（codesign --verify --deep --strict）')
    const verify = run('codesign', ['--verify', '--deep', '--strict', '--verbose=2', appPath])
    check('整包签名校验通过（封条完整）', verify.status === 0, verify.status === 0 ? '' : `${verify.stdout}${verify.stderr}`.split('\n').filter(Boolean).slice(0, 2).join(' / '))

    // ② 逐对象：签名 + hardened runtime + identity。
    section('② 逐对象：每个 Mach-O 都有签名、都带 hardened runtime')
    const machos = collectMachO(appPath)
    check('扫到 Mach-O 对象', machos.length > 0, `${machos.length} 个：${categorize(machos, appPath)}`)
    const facts = machos.map((abs) => ({ abs, ...signatureFacts(abs) }))
    const unsigned = facts.filter((f) => !f.signed)
    const noRuntime = facts.filter((f) => f.signed && !f.runtime)
    const devId = facts.filter((f) => f.developerId || (f.team && f.team !== 'not set'))
    check('没有完全未签名的对象（arm64 上会被 SIGKILL）', unsigned.length === 0, unsigned.slice(0, 3).map((f) => f.abs.slice(appPath.length + 1)).join('、'))
    check('每个已签名对象都带 hardened runtime 标记（--options runtime）', noRuntime.length === 0, noRuntime.slice(0, 3).map((f) => f.abs.slice(appPath.length + 1)).join('、'))
    if (declared === 'adhoc') {
      check('没有 Developer ID 签名（声明 adhoc）', devId.length === 0, devId.slice(0, 2).map((f) => `${f.abs.slice(appPath.length + 1)} team=${f.team}`).join('、'))
      check('全部对象都是 ad-hoc（无 Team ID）', facts.every((f) => f.adhoc), facts.filter((f) => !f.adhoc).slice(0, 2).map((f) => f.abs.slice(appPath.length + 1)).join('、'))
    } else {
      // **fail-closed**：本项目不做证书（计划 §12.27，2026-09-28 项目方确认无证书、不上架 → M4-2b 不执行），
      // 所以 `signing` 只可能是 adhoc。出现别的声明 = 有人改了契约（那要先改设计文档），这里当场红，
      // 而不是"打印一句 note 然后放行"——放行等于把判据悄悄降级成"只要包有签名就算过"。
      check(
        'release.json 的 signing 必须是 adhoc（本项目不接证书，§12.27）',
        declared === 'adhoc',
        declared ? `声明是 ${declared}` : 'release.json 里没有 signing 字段',
      )
    }

    // ③ entitlements：写在 plist 里 ≠ 签进去了。
    section('③ entitlements 真的签进了对象（不是只写了 plist）')
    const mainBinary = readdirSync(join(appPath, 'Contents', 'MacOS'))
      .map((n) => join(appPath, 'Contents', 'MacOS', n))
      .find((abs) => statSync(abs).isFile())
    const keys = mainBinary ? entitlementKeys(mainBinary) : []
    check('主二进制带 allow-jit（V8 JIT 必需）', keys.includes('com.apple.security.cs.allow-jit'), keys.join('、') || '(读不到)')
    check(
      '主二进制带 disable-library-validation（ad-hoc + 多来源原生件必需）',
      keys.includes('com.apple.security.cs.disable-library-validation'),
      keys.join('、') || '(读不到)',
    )
    check(
      '刻意没有 allow-unsigned-executable-memory（能不加就不加）',
      !keys.includes('com.apple.security.cs.allow-unsigned-executable-memory'),
      keys.filter((k) => k.includes('executable-memory')).join('、'),
    )

    // ④ Gatekeeper：如实记录，不判失败。**ad-hoc 必然被拒，而且这是长期状态**（§12.27：不上架、
    // 不买证书 → 不走 Gatekeeper 那条路，只走"非浏览器渠道的内网 tar.gz"），所以这条永远会是
    // rejected —— 它是"我们没依赖 Gatekeeper"的证据，不是待修的问题。
    section('④ Gatekeeper 结论（如实记录，不作为失败判据；ad-hoc 下预期恒为 rejected）')
    const spctl = run('spctl', ['-a', '-vvv', '-t', 'exec', appPath])
    const verdict = `${spctl.stdout}${spctl.stderr}`.trim().split('\n')[0] || `(退出码 ${spctl.status})`
    note(`spctl -a -vvv -t exec → ${verdict}`)
    note('预期且**长期**如此：ad-hoc 签名会被 Gatekeeper 拒（assess 需要 Developer ID + 公证）。')
    note('本项目不上架、不买证书（§12.27；M4-2b 不执行），分发走"非浏览器渠道的内网 tar.gz"（不带 quarantine），')
    note('所以 Gatekeeper 这条路本来就不在链路上——这条 rejected 是预期事实，不是待修的问题。')

    if (values.json) {
      console.log(
        JSON.stringify(
          {
            app: appPath,
            objects: machos.length,
            unsigned: unsigned.length,
            withoutRuntime: noRuntime.length,
            developerId: devId.length,
            declaredSigning: declared,
            sealOk: verify.status === 0,
            spctl: verdict,
          },
          undefined,
          2,
        ),
      )
    }
  } catch (e) {
    console.error(`verify-signature: ${e.message}`)
    cleanup()
    process.exit(1)
  }

  cleanup()
  console.log(failures === 0 ? '\n==> 通过：签名事实与 hardened runtime 形态成立' : `\n==> 失败：${failures} 条不符`)
  if (failures > 0) process.exit(1)
}

main()
