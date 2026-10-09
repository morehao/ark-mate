/**
 * 运行时载荷（runtime payload）的公共装配件：裁剪策略、逐文件摘要、清单写入。
 *
 * 为什么单独成文件：`prepare-runtime.mjs`（M3-1 装配）与后续的
 * `assert-engines.mjs`（M3-7 ABI 断言）、`materialize-payload.mjs`（M3-6 若退回物化）
 * 都要用**同一套**"怎么算摘要、怎么裁剪"的规则——三处各写一份的典型后果是
 * 清单算出来对不上，且没人知道以谁为准。
 *
 * 确定性要求（计划 §7 M3-1 / C9）：字段顺序固定、无时间戳、原子写、逐文件排序。
 */

import { createHash } from 'node:crypto'
import { chmodSync, copyFileSync, mkdirSync, readdirSync, readFileSync, realpathSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { dirname, join, relative, sep } from 'node:path'

/** 摘要算法：与核心侧资产摘要同款（`sha256:` + 十六进制），便于两侧对照。 */
export const DIGEST_PREFIX = 'sha256:'

/**
 * 从**已安装**的生产依赖树里裁掉的条目（不在随包载荷里的东西）。
 *
 * 原则：只裁"删了不影响运行"的东西——包管理器元数据、source map、类型声明、TS 构建缓存。
 * **不裁** LICENSE / README（许可证义务）、不改写任何源码、不动目标平台以外的原生件
 * （那一层由 pnpm 的 `supportedArchitectures` 在安装期就挡掉了，见 runtime/*\/pnpm-workspace.yaml）。
 *
 * @param {string} rel 相对载荷根的路径（POSIX 分隔符）
 * @param {{platform: string, arch: string}} target 目标平台
 * @returns {string|undefined} 裁掉的原因；保留则返回 undefined
 */
export function pruneReason(rel, target) {
  const parts = rel.split('/')
  if (parts.some((p) => ['.bin', '.pnpm', '.modules.yaml', '.pnpm-workspace-state-v1.json'].includes(p))) {
    return '包管理器元数据'
  }
  const file = parts.at(-1) ?? ''
  if (/\.(?:[cm]?[jt]s|css)\.map$/.test(file)) return 'source map'
  if (/\.d\.[cm]?ts$/.test(file)) return '类型声明'
  if (/\.tsbuildinfo$/.test(file)) return 'TS 构建缓存'
  // 跨平台原生件兜底：有些包（如 node-pty 的 prebuilds、koffi 的平台目录）会把所有平台
  // 都塞进同一个包里，`supportedArchitectures` 管不到包**内部**的目录。
  // 不锚定 `node_modules/` 前缀：载荷根的形态有两种（整棵 node_modules / 单个包目录），
  // 锚死了会导致"包目录形态下规则静默失效"。
  const ptyPrebuild = rel.match(/(?:^|\/)node-pty\/prebuilds\/([^/]+)\//)
  if (ptyPrebuild && ptyPrebuild[1] !== `${target.platform}-${target.arch}`) return 'node-pty 其它平台'
  if (file.endsWith('.pdb')) return '调试符号'
  return undefined
}

/** 逐文件 sha256（十六进制）。 */
export function fileDigest(abs) {
  return createHash('sha256').update(readFileSync(abs)).digest('hex')
}

/**
 * 合成摘要：对**排序后**的 `相对路径\0逐文件摘要\n` 行取 sha256。
 *
 * 为什么不用"把文件内容拼起来再哈希"：那要求先把整棵树读进内存（载荷有几百 MB），
 * 且对文件顺序敏感；按行拼装只需排序后的路径列表，两次装配必然一致。
 */
export function compositeDigest(entries) {
  const lines = entries.map((e) => `${e.rel}\0${e.digest}\n`).sort()
  return DIGEST_PREFIX + createHash('sha256').update(lines.join('')).digest('hex')
}

/**
 * 按裁剪策略把一棵已安装的依赖树复制到目标目录，返回逐文件摘要清单。
 *
 * **必须去引用（dereference）**：pnpm 的 node_modules 里有两类"不是真文件"的条目——
 * 硬链接（顶层提升出来的包，内容与 `.pnpm` 共享 inode）与符号链接（直接依赖、以及
 * `.pnpm` 内部互相引用的那些）。把符号链接原样拷进载荷＝载荷里留着一串指向
 * `~/.pnpm-store` 的死链，用户机器上必然起不来；而硬链接拷贝后自然变成独立文件，无需特殊处理。
 *
 * 去引用的同时**裁掉 `.pnpm/`**：它是包管理器的虚拟仓，内容已被顶层（硬链接/去引用后的）副本覆盖，
 * 留着就等于把整套闭包再存一份。
 *
 * @returns {{files: number, bytes: number, digest: string, fileList: string[], pruned: Record<string, number>}}
 */
export function copyTreeFiltered(srcRoot, destRoot, target) {
  mkdirSync(destRoot, { recursive: true })
  const entries = []
  const pruned = {}
  const fileList = []
  // 只防"环"（祖先链上的真实路径），不做全局去重：同一个真实目录可能被两条不同路径引用
  // （如 `node_modules/foo` 与 `node_modules/bar/node_modules/foo` 同时指向同一份 `.pnpm`），
  // 全局去重会让后者在载荷里凭空消失。
  const ancestors = new Set()
  let bytes = 0

  const visit = (dirAbs, relPrefix) => {
    const real = realpathSync(dirAbs)
    if (ancestors.has(real)) return
    ancestors.add(real)
    try {
      const names = readdirSync(dirAbs, { withFileTypes: true }).map((e) => e.name).sort()
      for (const name of names) {
        const abs = join(dirAbs, name)
        const rel = relPrefix ? `${relPrefix}/${name}` : name
        const reason = pruneReason(rel, target)
        if (reason) {
          pruned[reason] = (pruned[reason] ?? 0) + 1
          continue
        }
        const st = statSync(abs) // 跟随符号链接
        if (st.isDirectory()) {
          visit(abs, rel)
        } else if (st.isFile()) {
          if (st.isSymbolicLink?.() !== true) {
            // statSync 已跟随链接，这里用 lstat 判断"这一项本来是链接吗"（统计用）。
          }
          const dest = join(destRoot, rel)
          mkdirSync(dirname(dest), { recursive: true })
          copyFileSync(abs, dest)
          chmodSync(dest, st.mode & 0o777)
          const digest = fileDigest(abs)
          entries.push({ rel, digest })
          fileList.push(`${digest}  ${rel}`)
          bytes += st.size
        }
      }
    } finally {
      ancestors.delete(real)
    }
  }

  visit(srcRoot, '')
  return { files: entries.length, bytes, digest: compositeDigest(entries), fileList, pruned }
}

/**
 * 原子写清单：先写同级临时文件再 rename——中断（或两次装配并发）都不会留下半份清单，
 * 而"半份清单 + 完整载荷"正是最坏组合（校验通过但内容不对）。
 */
export function writeManifestAtomic(destRoot, manifest) {
  const path = join(destRoot, 'payload.json')
  const tmp = `${path}.tmp-${process.pid}`
  // JSON.stringify 的键序＝对象字面量的键序（固定），末尾补一个换行（diff 友好、逐字节可复现）。
  writeFileSync(tmp, `${JSON.stringify(manifest, undefined, 2)}\n`)
  renameSync(tmp, path)
  return path
}

/** 把已装配好的临时目录原子换到最终位置（旧载荷先移走再删，避免"删完才发现新的没装配好"）。 */
export function swapIntoPlace(staging, finalDir) {
  const previous = `${finalDir}.old-${process.pid}`
  rmSync(previous, { recursive: true, force: true })
  let hadPrevious = false
  try {
    renameSync(finalDir, previous)
    hadPrevious = true
  } catch {
    // 首次装配：没有旧目录
  }
  mkdirSync(dirname(finalDir), { recursive: true })
  renameSync(staging, finalDir)
  if (hadPrevious) rmSync(previous, { recursive: true, force: true })
}

/** 供体积打点用的人类可读体积。 */
export function formatBytes(n) {
  if (n < 1024) return `${n} B`
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 ** 2).toFixed(1)} MB`
}
