#!/usr/bin/env node
/**
 * 生成随包合规文件（M4-5，设计 §11）：`resources/compliance/{THIRD-PARTY-NOTICES.txt,sbom.cdx.json}`。
 *
 * 用法：
 *   node scripts/gen-compliance.mjs [--check]
 *
 *   `--check` 只生成到内存并报告组件规模，**不写盘**（排障/预览用）。
 *
 * 谁在读这两个文件：
 *   · `electron-builder.yml` 的 extraResources 把它们放进 `<Resources>/compliance/`（随包分发，
 *     许可证义务要求它跟着二进制走）；
 *   · `verify-compliance.mjs` 按**同一套输入**复算一遍并逐字节比对。
 *
 * 纪律（C9）：字段序固定、无时间戳、组件排序固定、原子写（先写临时文件再 rename）。
 * 两次生成（同一份输入）必须逐字节相同——这条是门禁"生成物不手改"的前提。
 */

import { createHash } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { parseArgs } from 'node:util'

import {
  COMPLIANCE_DIRNAME,
  collectComplianceModel,
  ensureDir,
  renderComplianceArtifacts,
  writeTextAtomic,
} from './lib/compliance.mjs'
import { readJson } from './lib/release.mjs'

const SCRIPT_DIR = import.meta.dirname
const DESKTOP = resolve(SCRIPT_DIR, '..')
const RELEASE_JSON = join(DESKTOP, 'release.json')
const PAYLOADS = join(DESKTOP, 'resources', 'payloads')
const RUNTIME = join(DESKTOP, 'runtime')
const OUT_DIR = join(DESKTOP, 'resources', COMPLIANCE_DIRNAME)

function fail(message) {
  console.error(`gen-compliance: ${message}`)
  process.exit(1)
}

function main() {
  const rawArgs = process.argv.slice(2)
  const args = rawArgs[0] === '--' ? rawArgs.slice(1) : rawArgs
  const { values } = parseArgs({
    args,
    options: { check: { type: 'boolean', default: false }, help: { type: 'boolean', default: false } },
  })
  if (values.help) {
    console.log('用法：node scripts/gen-compliance.mjs [--check]')
    return
  }

  if (!existsSync(RELEASE_JSON)) {
    fail(`${RELEASE_JSON} 不存在\n  先跑 \`make desktop/dist\`（或单独跑 build-release-json.mjs）——组件版本取自它`)
  }
  const release = readJson(RELEASE_JSON)

  // 先确认这两个文件真的会进包：随包义务要求它们在 `<Resources>/compliance/` 里，
  // 而"生成了但没进包"正是这类文件最容易出现的假绿（生成期与打包期是两处配置）。
  const builderYml = readFileSync(join(DESKTOP, 'electron-builder.yml'), 'utf8')
  if (!builderYml.includes(`to: ${COMPLIANCE_DIRNAME}`)) {
    fail(`electron-builder.yml 的 extraResources 里没有 \`to: ${COMPLIANCE_DIRNAME}\`：生成物不会进包（V14 判的是包内那份）`)
  }

  let model
  try {
    model = collectComplianceModel({ release, payloadsRoot: PAYLOADS, runtimeRoot: RUNTIME })
  } catch (e) {
    fail(e.message)
  }

  const artifacts = renderComplianceArtifacts(model)
  const rows = Object.entries(artifacts).map(([name, text]) => ({
    name,
    text,
    bytes: Buffer.byteLength(text),
    digest: createHash('sha256').update(text).digest('hex'),
  }))

  console.log(`==> 合规生成物（M4-5）：ark-mate ${model.app.version}`)
  console.log(`    组件：${model.packages.length} 个 npm 包（其中 ${model.bundled.length} 个是 npm 自带发布的 bundles）`)
  console.log(`    + 载荷自身 ${model.payloads.length} 份、运行时 ${model.runtimes.length} 个（Electron / Node）`)
  for (const row of rows) {
    console.log(`    ${row.name}  ${row.bytes} 字节  sha256:${row.digest.slice(0, 16)}…`)
  }

  if (values.check) {
    console.log('    （--check：只生成到内存，未写盘）')
    return
  }

  ensureDir(OUT_DIR)
  for (const row of rows) writeTextAtomic(join(OUT_DIR, row.name), row.text)
  console.log(`    落点 ${OUT_DIR}`)
  console.log(`    随包：extraResources → <Resources>/${COMPLIANCE_DIRNAME}/`)
}

main()
