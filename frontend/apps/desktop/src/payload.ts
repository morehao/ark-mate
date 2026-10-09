/**
 * 随包运行时载荷的解析（M3-4，设计 §6.5）。
 *
 * ## 解析链与"我们自己的载荷"的规矩
 *
 * 设计 §6.5 的链是三级：**显式配置 → 随包载荷 → PATH**。对本模块负责的三份载荷（dsh / opencode /
 * npm 的 npx 入口）只有前两级是合法的：
 *
 *   - 命中**显式配置**（`MATE_RUNTIME_<NAME>`）→ `runtime-env`：用户明确指名用哪一份，我们就用它；
 *   - 命中**随包载荷** → `bundled`：这是桌面态的常态；
 *   - 都没命中 → **启动期错误**，不回落 PATH。
 *
 * 为什么不回落：机器上装的 dsh/opencode 是**另一个版本、另一套配置、另一份能力声明**。静默回落的症状是
 * "在我机器上好好的"——那种 bug 最难查，而且违反设计 §6.5 的红线（"两件事不能混为一谈"：**随包运行时**
 * 与**用户自己装的 runtime** 是两件不同的事）。所以这里宁可让壳起不来（走既有的 `boot-failed` 恢复页），
 * 也不猜。`path` 这个来源取值因此在本模块**只作为词表存在**：它描述的是"核心在非桌面形态下从 PATH 找
 * 到用户自己的 runtime"那种情况（由 Go 侧 `exec.Command` 的语义承担），桌面壳不会产生它。
 *
 * ## 为什么不做 PATH 探测（连"顺便找一下"都不做）
 *
 * 本文件与 `node-runtime.ts` 由 archtest 守卫静态扫描，禁止出现 `which` / `command -v` / `LookPath`
 * 这类探测（M3-4 的第 6 条新守卫）。探测一旦存在，迟早会有人在"载荷没装好"时被它救一次，
 * 于是那条路径就成了默认路径——正是上面那段要避免的。
 */
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'

/** 命中级别（与 Go 侧 `/meta` 的 `payloads[].source` 逐字一致）。 */
export type PayloadSource = 'bundled' | 'runtime-env' | 'path'

/** 一份已解析的载荷。 */
export type ResolvedPayload = {
  name: string
  version: string
  /** `payload.json` 里的合成摘要（`sha256:<hex>`）；显式覆盖时是 `unknown`（不是我们发的，无清单可核）。 */
  digest: string
  /** 交给私有 bin 启动器的目标：JS 入口用 Electron 的 Node 跑，原生二进制直接执行。 */
  entry: string
  source: PayloadSource
  /** 命中级别的人话说明（错误信息与诊断用；`/meta` 不下发它）。 */
  detail: string
  /**
   * 随包载荷的根目录（`resources/payloads/<name>/<version>/`）；`runtime-env` 命中时**没有**它。
   * 载荷内派生的入口（npm 的 npx 就在 dsh 载荷的 `node_modules/npm/` 里）从这里拼，不从入口路径
   * 反推层级——反推多一层少一层都会静默指错文件。
   */
  payloadRoot?: string
}

/**
 * 解析要用到的外部依赖：**注入**而不是直接读全局。
 *
 * 两个理由，都不是洁癖：① 验收能在不改机器状态的前提下把三级链走全（假载荷根、假环境变量），
 * "缺载荷时到底报什么错"因此变成可断言的行为；② 本模块不 import `paths.ts`，于是不依赖 Electron，
 * 可以被普通 Node 直接 import —— 否则验收里那份"逐级钉住"的检查只能靠再起一次壳来做。
 */
export type PayloadResolutionDeps = {
  payloadsRoot: string
  env: Record<string, string | undefined>
}

/** 本模块认识的载荷：名字 → 显式覆盖的环境变量。 */
const PAYLOAD_ENV_VARS: Record<string, string> = {
  dsh: 'MATE_RUNTIME_DSH',
  opencode: 'MATE_RUNTIME_OPENCODE',
}

/** 随包载荷清单里读出来的元数据（`payload.json` 的子集）。 */
type PayloadManifest = {
  name?: string
  version?: string
  digest?: string
  entry?: string
}

/**
 * 取某个载荷的**随包**版本目录：`resources/payloads/<name>/<version>/`。
 *
 * 多版本并存时取**排序最大的那个**（同一份载荷在一个包里只会装一个平台的一个版本；出现多个
 * 通常是构建时残留，按确定规则取一个比"随机踩到哪个"可复现）。返回 `undefined` = 没有随包载荷。
 */
function bundledPayloadDir(name: string, payloadsRoot: string): { dir: string; version: string } | undefined {
  const root = join(payloadsRoot, name)
  if (!existsSync(root)) return undefined
  const versions = readdirSync(root)
    .filter((v) => !v.startsWith('.') && statSync(join(root, v)).isDirectory())
    .sort()
  const version = versions[versions.length - 1]
  if (!version) return undefined
  return { dir: join(root, version), version }
}

function readManifest(dir: string): PayloadManifest {
  const file = join(dir, 'payload.json')
  if (!existsSync(file)) {
    throw new Error(`随包载荷缺清单：${file}\n  先构建：make desktop/payloads`)
  }
  try {
    return JSON.parse(readFileSync(file, 'utf8')) as PayloadManifest
  } catch (e) {
    throw new Error(`随包载荷清单不是合法 JSON：${file}（${String(e)}）`)
  }
}

/**
 * 解析一份随包载荷。
 *
 * `entryRel` 是载荷内的入口相对路径（dsh = `node_modules/@deepseek-ai/dsh/lib/bin.js`，
 * opencode = `bin/opencode`）。入口**不由运行期猜**：清单里写死了 `entry`，这里只按它拼绝对路径，
 * 并在拼完后核一次存在性——"装上了但入口不在"必须在启动期报出来，而不是等 ACP 握手超时。
 */
export function resolvePayload(name: string, entryRel: string | undefined, deps: PayloadResolutionDeps): ResolvedPayload {
  const envVar = PAYLOAD_ENV_VARS[name]
  const explicit = envVar ? deps.env[envVar]?.trim() : undefined
  if (explicit) {
    if (!existsSync(explicit)) {
      throw new Error(`${envVar} 指向的路径不存在：${explicit}`)
    }
    return {
      name,
      version: 'external',
      digest: 'unknown', // 不是我们发的，没有可核对的清单（`/meta` 里因此显示 unknown）
      entry: explicit,
      source: 'runtime-env',
      detail: `${envVar}`,
    }
  }

  const bundled = bundledPayloadDir(name, deps.payloadsRoot)
  if (!bundled) {
    throw new Error(
      `找不到随包运行时载荷 ${name}：${join(deps.payloadsRoot, name)}\n` +
        `  桌面态**不回落**到机器上装的同名命令（设计 §6.5：随包运行时与用户自己装的 runtime 是两件事）。\n` +
        `  先构建：make desktop/payloads；或显式指定：${envVar ?? `MATE_RUNTIME_${name.toUpperCase()}`}=<可执行文件绝对路径>`,
    )
  }

  const manifest = readManifest(bundled.dir)
  const rel = entryRel ?? manifest.entry
  if (!rel) {
    throw new Error(`随包载荷 ${name} 的清单里没有 entry（${join(bundled.dir, 'payload.json')}）`)
  }
  const entry = join(bundled.dir, rel)
  if (!existsSync(entry)) {
    throw new Error(`随包载荷 ${name} 的入口不存在：${entry}（清单说的是 ${rel}）\n  重新装配：make desktop/payloads`)
  }
  return {
    name,
    version: manifest.version ?? bundled.version,
    digest: manifest.digest ?? '',
    entry,
    source: 'bundled',
    detail: bundled.dir,
    payloadRoot: bundled.dir,
  }
}

/**
 * 桌面态需要的全部载荷（一次解析完，缺口在启动期一次报清）。
 *
 * npm 的 `npx` 入口不算独立载荷（设计 §6.3：它随 dsh 闭包走，不占载荷位），因此从 **dsh 载荷内**
 * 派生：`<dsh 载荷>/node_modules/npm/bin/npx-cli.js`。它缺失不算致命（私有 `npx` 会以 127 报出来），
 * 但会在 `detail` 里说清"没有 npx 入口"。
 */
export function resolveRuntimePayloads(deps: PayloadResolutionDeps): {
  dsh: ResolvedPayload
  opencode: ResolvedPayload
  npxEntry?: string
  all: ResolvedPayload[]
} {
  const dsh = resolvePayload('dsh', 'node_modules/@deepseek-ai/dsh/lib/bin.js', deps)
  const opencode = resolvePayload('opencode', 'bin/opencode', deps)
  // npm 的入口在 **dsh 载荷内部**（`<载荷>/node_modules/npm/bin/npx-cli.js`，M3-1 的分装结果）。
  const npxCandidate = dsh.payloadRoot ? join(dsh.payloadRoot, 'node_modules', 'npm', 'bin', 'npx-cli.js') : undefined
  const npxEntry = npxCandidate && existsSync(npxCandidate) ? npxCandidate : undefined
  return { dsh, opencode, npxEntry, all: [dsh, opencode] }
}

/**
 * 注入给核心（再经它继承给 runtime / MCP 子进程）的载荷路径变量。
 *
 * 三个变量对应私有 bin 里三个启动器的目标（design §6.6 / M3-2）：启动器只做转发，
 * "名字 → 具体哪一份载荷"的翻译全部发生在启动期、写进环境——这是"不探测本机"能成立的关键。
 */
export function payloadEnvironmentExtras(payloads: ReturnType<typeof resolveRuntimePayloads>): Record<string, string> {
  const extras: Record<string, string> = {
    MATE_DSH_ENTRY: payloads.dsh.entry,
    MATE_OPENCODE_BIN: payloads.opencode.entry,
  }
  if (payloads.npxEntry) extras.MATE_NPX_ENTRY = payloads.npxEntry
  return extras
}

/** `/meta` 的载荷清单（`--payloads` 的 JSON）：只有 Go 侧契约里的四个字段。 */
export function payloadInfoList(payloads: ReturnType<typeof resolveRuntimePayloads>): Array<{
  name: string
  version: string
  digest: string
  source: PayloadSource
}> {
  return payloads.all.map((p) => ({ name: p.name, version: p.version, digest: p.digest, source: p.source }))
}
