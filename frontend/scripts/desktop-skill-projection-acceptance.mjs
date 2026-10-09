#!/usr/bin/env node
/**
 * 桌面态 **skill 投放**验收（真 Electron 壳 + 真 forge-core + 真 dsh 子进程）。
 *
 * 为什么单独一条：`desktop-ui-acceptance.mjs` 打的是壳/契约/加固/凭据/流/退出六个场景，
 * **不含能力投放**——skill 到底有没有落到底座真正读的那个根，它一概看不见。而这条链恰恰
 * 有个"平台自报一切正常、底座什么都没收到"的失效形态（2026-10-09 实测踩到两次），
 * 只能用"真底座 + 真请求体"验，不能靠单测：两次缺陷都在单测全绿的情况下存在。
 *
 * 验两条修复（都来自 2026-10-09 的 E5 探针）：
 *   A `fix(runtime)` 配置隔离**不注入** `DSH_AGENTS_HOME` ⇒ dsh 的读根 == 平台写根
 *     （`${HOME}/.agents/skills`）；若注入，底座会静默去读 `<FORGE_HOME>/runtime/dsh/agents/skills`。
 *   B `fix(capability)` 物化**自建投影根** ⇒ 全新 home 上"第一条 skill"也能落盘；
 *     此前 staging 目录建在根里面而根不存在，直接 `no such file or directory`、只留一条 WARN。
 *
 * 隔离与代价（都是刻意的）：
 *   - 用**自己的 `HOME` 与 `FORGE_HOME`**（<repo>/.dsh/tmp/desktop-skill-accept）：既不碰用户的
 *     `~/.forge/forge.db`，也让 `${HOME}/.agents/skills` 从**不存在**开始（那正是 B 的前提）。
 *     脚本收尾会核对用户真实的 `~/.agents/skills` 与 `~/.forge` 指纹**零变化**。
 *   - 模型端点是**脚本内置的假 LLM**（Anthropic `/v1/messages` 流式）：零额度、零凭证，
 *     只回答一句话，并把 dsh 真实请求体留在内存里给断言用。
 *   - 只重建 `forge-core`（Go 侧改动）。壳产物与载荷是既有生成物，脚本只断言它们在位。
 *
 * 用法：`node scripts/desktop-skill-projection-acceptance.mjs`（`KEEP_PROBE=1` 保留探针目录）。
 * **不进 CI**：要真 Electron + 真底座。
 */
import { execFileSync, spawn } from 'node:child_process'
import http from 'node:http'
import { existsSync, mkdirSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import {
  CORE_RE,
  DESKTOP,
  REPO,
  SHELL_RE,
  connectCDP,
  installCleanupHooks,
  killShellTree,
  launchShell,
  pidsMatching,
  parseProcessEnv,
  sleep,
  stopShellTree,
  waitFor,
} from './lib/desktop-harness.mjs'

const PROBE = join(REPO, '.dsh', 'tmp', 'desktop-skill-accept')
const PROBE_HOME = join(PROBE, 'home')
const PROBE_FORGE_HOME = join(PROBE_HOME, '.forge')
const SKILL_ID = 'probe-desktop'
const BODY_MARK = 'DESKTOP-ONLY-MARK'
const PROJECTION_ROOT = join(PROBE_HOME, '.agents', 'skills')
const LLM_PORT = 10078

let pass = 0
let fail = 0
const section = (t) => console.log(`\n—— ${t} ——`)
const check = (label, ok, detail = '') => {
  if (ok) pass++
  else fail++
  console.log(`${ok ? '✓' : '✗'} ${label}${detail ? ` — ${detail}` : ''}`)
}

/** 读进程环境（`ps -E`；权限不足返回空对象，调用方按"读不到"处理）。 */
function envOf(pid) {
  if (!pid) return {}
  try {
    return parseProcessEnv(execFileSync('ps', ['-E', '-p', String(pid)], { encoding: 'utf8' }))
  } catch {
    return {}
  }
}

/** 用户既有配置的指纹（M3-3 的"整轮验收前后零变化"）：只取顶层名 + mtime + 大小。 */
function fingerprint(dir) {
  try {
    return readdirSync(dir)
      .sort()
      .map((n) => {
        const st = statSync(join(dir, n))
        return `${n}:${st.size}:${st.mtimeMs}`
      })
      .join('|')
  } catch {
    return '<absent>'
  }
}

/** 桌面态核心：直接重建（Go 侧改动；不拖 make core/desktop 的 pnpm 与 runtime-bin）。 */
function rebuildCore() {
  const out = join(DESKTOP, 'resources', 'forge-core')
  try {
    execFileSync('go', ['build', '-o', out, './cmd/forge'], {
      cwd: join(REPO, 'backend'),
      env: { ...process.env, CGO_ENABLED: '0' },
      stdio: 'ignore',
    })
    return { ok: existsSync(out) }
  } catch (e) {
    return { ok: false, detail: String(e).slice(0, 200) }
  }
}

/** 重建私有运行时启动器（`resources/runtime/bin/*`，生成物；隔离表就在它里面）。 */
function regenerateRuntimeBin() {
  try {
    execFileSync(process.execPath, ['scripts/gen-runtime-bin.mjs'], { cwd: DESKTOP, stdio: 'ignore' })
    return { ok: existsSync(join(DESKTOP, 'resources', 'runtime', 'bin', 'dsh')) }
  } catch (e) {
    return { ok: false, detail: String(e).slice(0, 200) }
  }
}

/** 脚本内置的假 LLM：Anthropic `/v1/messages`（流式）+ OpenAI 面（顺手，便于日后扩到 opencode）。 */
function startFakeLLM() {
  const captured = []
  const server = http.createServer((req, res) => {
    let raw = ''
    req.on('data', (d) => (raw += d))
    req.on('end', () => {
      let body = {}
      try {
        body = JSON.parse(raw || '{}')
      } catch {}
      captured.push({ path: req.url, body, raw })
      if (req.url.startsWith('/v1/models')) {
        res.writeHead(200, { 'content-type': 'application/json' })
        return res.end(JSON.stringify({ data: [{ id: 'probe-model', object: 'model' }] }))
      }
      if (req.url.includes('/messages')) {
        const ev = (kind, obj) => res.write(`event: ${kind}\ndata: ${JSON.stringify(obj)}\n\n`)
        res.writeHead(200, {
          'content-type': 'text/event-stream',
          'cache-control': 'no-cache',
          connection: 'close',
        })
        ev('message_start', {
          type: 'message_start',
          message: {
            id: 'msg_1',
            type: 'message',
            role: 'assistant',
            model: body.model ?? 'probe-model',
            content: [],
            stop_reason: null,
            stop_sequence: null,
            usage: { input_tokens: 1, output_tokens: 1 },
          },
        })
        ev('content_block_start', {
          type: 'content_block_start',
          index: 0,
          content_block: { type: 'text', text: '' },
        })
        ev('content_block_delta', {
          type: 'content_block_delta',
          index: 0,
          delta: { type: 'text_delta', text: 'PROBE-REPLY-OK' },
        })
        ev('content_block_stop', { type: 'content_block_stop', index: 0 })
        ev('message_delta', {
          type: 'message_delta',
          delta: { stop_reason: 'end_turn', stop_sequence: null },
          usage: { output_tokens: 1 },
        })
        ev('message_stop', { type: 'message_stop' })
        return res.end()
      }
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ id: 'chatcmpl-probe', choices: [{ message: { role: 'assistant', content: 'PROBE-REPLY-OK' } }] }))
    })
  })
  return new Promise((resolve) => server.listen(LLM_PORT, '127.0.0.1', () => resolve({ server, captured })))
}

/** 从请求体里取 dsh 看到的 skill 目录块（它在**用户消息**的 <system-reminder> 里，不在 system）。 */
function availableSkillsOf(body) {
  for (const m of body?.messages ?? []) {
    const text =
      typeof m.content === 'string'
        ? m.content
        : (m.content ?? []).map((c) => c?.text ?? '').join('\n')
    const hit = /<available_skills>([\s\S]*?)<\/available_skills>/.exec(text)
    if (hit) return hit[1].trim()
  }
  return ''
}

const realAgentsRoot = join(homedir(), '.agents', 'skills')
const realForgeRoot = join(homedir(), '.forge')

async function main() {
  section('前置：清场与产物')
  if (pidsMatching(SHELL_RE).length || pidsMatching(CORE_RE).length) killShellTree()
  await sleep(500)
  check(
    '没有别的桌面壳/核心在跑（两个核心共用数据目录会让断言读到别人的内存态）',
    pidsMatching(SHELL_RE).length === 0 && pidsMatching(CORE_RE).length === 0,
  )
  const built = rebuildCore()
  check('forge-core 已按当前代码重建', built.ok, built.detail ?? '')
  check(
    '壳与载荷产物在位（壳 dist / resources/web / 私有 runtime bin）',
    existsSync(join(DESKTOP, 'dist', 'main.cjs')) &&
      existsSync(join(DESKTOP, 'resources', 'web', 'index.html')) &&
      existsSync(join(DESKTOP, 'resources', 'runtime', 'bin')),
  )

  rmSync(PROBE, { recursive: true, force: true })
  mkdirSync(PROBE_HOME, { recursive: true })
  const binGen = regenerateRuntimeBin()
  check('私有运行时启动器已按当前代码重建（隔离表就在生成物里）', binGen.ok, binGen.detail ?? '')
  const dshLauncher = binGen.ok ? readFileSync(join(DESKTOP, 'resources', 'runtime', 'bin', 'dsh'), 'utf8') : ''
  check(
    '私有启动器不再兜底 DSH_AGENTS_HOME（它会把 dsh 的读根从平台写根上撬走）',
    dshLauncher.length > 0 && !dshLauncher.includes('DSH_AGENTS_HOME'),
  )
  check('投影根从"不存在"开始（修复 B 的前提）', !existsSync(PROJECTION_ROOT))

  const beforeAgents = fingerprint(realAgentsRoot)
  const beforeForge = fingerprint(realForgeRoot)

  const llm = await startFakeLLM()
  section('真壳 + 真核心')
  const shell = await launchShell({
    env: {
      ...process.env,
      HOME: PROBE_HOME,
      FORGE_HOME: PROBE_FORGE_HOME,
      DEEPSEEK_API_KEY: 'probe-key',
      DEEPSEEK_BASE_URL: `http://127.0.0.1:${LLM_PORT}`,
      ELECTRON_RUN_AS_NODE: undefined,
    },
  })
  check('壳起来了，核心打印了就绪行', shell.logText().includes('core ready'))
  const coreEnv = envOf(shell.corePid)
  const envReadable = Object.keys(coreEnv).length > 0
  check(
    '核心进程环境确实是探针 HOME/FORGE_HOME（不是用户真实目录）',
    !envReadable || (coreEnv.HOME === PROBE_HOME && coreEnv.FORGE_HOME === PROBE_FORGE_HOME),
    envReadable ? `HOME=${coreEnv.HOME} FORGE_HOME=${coreEnv.FORGE_HOME}` : 'ps -E 读不到（按"读不到"放过）',
  )

  const cdp = await connectCDP(shell.port)
  await waitFor(cdp, "return typeof window.__FORGE_DESKTOP__ === 'object'", 20000)

  section('修复 B：全新 home 上第一条 skill 要落盘')
  const created = await cdp.evalJs(`
    const r = await fetch('/v1/forge/skills', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: ${JSON.stringify(
        JSON.stringify({
          id: SKILL_ID,
          name: SKILL_ID,
          description: '桌面态投放探针',
          runtimes: ['deepseekharness'],
          body: `# ${SKILL_ID}\n\n正文标记：${BODY_MARK}\n`,
        }),
      )},
    })
    return await r.json()
  `)
  check('经壳反代建 skill 成功（渲染进程不带凭据）', created?.code === 0, JSON.stringify(created).slice(0, 160))
  const skillPath = join(PROJECTION_ROOT, SKILL_ID, 'SKILL.md')
  const skillText = existsSync(skillPath) ? readFileSync(skillPath, 'utf8') : ''
  check(`物化落盘到 ${skillPath}`, skillText.length > 0)
  check(
    'SKILL.md 内容合规（frontmatter 的 name/description + 正文标记）',
    skillText.includes(`name: "${SKILL_ID}"`) && skillText.includes(BODY_MARK),
    skillText.replace(/\n/g, ' ⏎ ').slice(0, 120),
  )
  check('核心日志没有物化失败的 WARN', !shell.logText().includes('skill 物化跳过'))

  section('修复 A：平台拉起的 dsh 要能读到这条 skill')
  const conv = await cdp.evalJs(`
    const r = await fetch('/v1/forge/conversations', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: ${JSON.stringify(JSON.stringify({ agent_id: 'dsh', prompt: '报告你可见的 skill 名称。' }))},
    })
    return await r.json()
  `)
  check('经壳反代起 dsh 会话成功', conv?.code === 0, JSON.stringify(conv).slice(0, 160))

  // 注意：dsh 会先发一次**会话标题生成**调用（`dsh-session-title-first-prompt-llm`），那一轮**没有**
  // skill 目录块——要等的是**轮次请求**（带工具表与 `<available_skills>` 的那一个）。
  let modelReq = null
  let catalogReq = null
  for (let i = 0; i < 160; i++) {
    modelReq ??= llm.captured.find((c) => c.path.includes('/messages'))
    catalogReq = llm.captured.find((c) => c.path.includes('/messages') && availableSkillsOf(c.body))
    if (catalogReq) break
    await sleep(250)
  }
  llm.captured.forEach((c, i) => writeFileSync(join(PROBE, `llm-req-${String(i + 1).padStart(2, '0')}.json`), c.raw))
  check(
    '假 LLM 收到了 dsh 的真实模型请求（零额度）',
    Boolean(modelReq),
    `收到 ${llm.captured.length} 个请求：${llm.captured.map((c) => c.path).join(', ')}`,
  )
  const skills = availableSkillsOf(catalogReq?.body ?? {})
  check(
    `dsh 轮次请求的 <available_skills> 里出现 ${SKILL_ID}`,
    skills.includes(SKILL_ID),
    skills ? skills.replace(/\n/g, ' / ') : '（没有任何请求带 skill 目录块）',
  )
  // 次证（第二来源）：底层子进程环境里**不该**有 DSH_AGENTS_HOME，且 DSH_HOME 指向探针 home。
  const dshPids = pidsMatching(/--profile\s+acp/)
  const dshEnv = envOf(dshPids[0])
  const dshEnvReadable = Object.keys(dshEnv).length > 0
  check(
    'dsh 子进程环境里没有 DSH_AGENTS_HOME（读根不被撬走）',
    !dshEnvReadable || dshEnv.DSH_AGENTS_HOME === undefined,
    dshEnvReadable
      ? `DSH_HOME=${dshEnv.DSH_HOME} DSH_AGENTS_HOME=${dshEnv.DSH_AGENTS_HOME ?? '（未注入）'}`
      : 'ps -E 读不到（以请求体那条为准）',
  )

  section('收尾')
  const stopped = await stopShellTree()
  await sleep(500)
  check('壳/核心全部退出，无残留', pidsMatching(SHELL_RE).length === 0 && pidsMatching(CORE_RE).length === 0, JSON.stringify(stopped))
  check('dsh 子进程也随核心回收', pidsMatching(/--profile\s+acp/).length === 0)
  check('用户真实 ~/.agents/skills 零变化', fingerprint(realAgentsRoot) === beforeAgents)
  check('用户真实 ~/.forge 零变化', fingerprint(realForgeRoot) === beforeForge)

  cdp.close()
  llm.server.close()
}

installCleanupHooks()
let keepProbe = false
process.on('exit', () => {
  if (!keepProbe && !process.env.KEEP_PROBE) rmSync(PROBE, { recursive: true, force: true })
})

try {
  await main()
} catch (e) {
  console.error(`\n未捕获异常：${e?.stack ?? e}`)
  fail++
} finally {
  killShellTree()
  await sleep(300)
  console.log(`\n结果：${pass} 通过 / ${fail} 失败`)
  keepProbe = fail > 0
  if (fail) console.log(`失败，探针目录保留在 ${PROBE} 便于取证`)
  process.exit(fail ? 1 : 0)
}
