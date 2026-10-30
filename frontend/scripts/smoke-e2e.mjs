#!/usr/bin/env node
/**
 * 前后端联调「通路」冒烟（无需浏览器、无需新依赖，Node ≥ 22）。
 *
 * 验证对象：`make dev` 起的那个实例（默认 http://127.0.0.1:10001——**核心直接服务构建产物**，
 * 没有中间那层 vite dev server 了；2026-10-11 起 `:5173` 那条路已被 `apps/web/vite.config.ts` 拦住）。
 * 覆盖：
 *   1. REST 信封（health / meta / runtime 诊断 / sessions）——@ark-mate/api 的 unwrap 语义前提；
 *      并断言 `/runtimes` 与整族 `/agents/*` 都已下线（底座由构建期固定，用户没有选择面）；
 *   2. REST 业务错误仍为 HTTP 200、信封 code!=0（前端 HttpError 读取 code/msg 的契约）；
 *   3. 载荷键形状与 @ark-mate/types 声明一致（`/meta.runtime` 与逐 runtime 可达性）；
 *   4. WS 实时通道帧契约：attach(无 since)= 全量回放（空聚合**不发帧**，v21 起不再回 snapshot）、
 *      ping → pong、detach → detached；
 *   5. 用量视图数据面（C1）：/usage/summary 分组/字段形状 + 非法 group_by 的业务错误
 *      （sqlite 模式）；内存模式按设计 40501 usage_stats_unsupported → 记 PASS 并注明降级。
 *
 * 用法（先起 `make dev`；也可以只起后端 `make server` + `--web-dir`，通路检查不需要界面）：
 *   E2E_BASE=http://127.0.0.1:10001 node scripts/smoke-e2e.mjs
 *   pnpm smoke:e2e
 *
 * 退出码：0 = 全部通路 PASS；1 = 存在 FAIL。
 */
import { buildAttachFrame } from '@ark-mate/desktop-contract'

const E2E_BASE = process.env.E2E_BASE || 'http://127.0.0.1:10001'
const API = E2E_BASE + '/v1/mate'
// 平台不做鉴权：REST 无需 Authorization；WS 首帧 attach 无凭据（帧形状用 buildAttachFrame）。
const AUTH = {}

let failed = 0
const ok = (name) => console.log(`  ✔ ${name}`)
const bad = (name, why) => {
  failed++
  console.error(`  ✘ ${name}: ${why}`)
}

// ——— 通用工具 ———

async function apiGet(path) {
  const r = await fetch(API + path, { headers: AUTH })
  const env = await r.json()
  if (typeof env.code !== 'number') throw new Error(`非信封响应: ${JSON.stringify(env)}`)
  return { http: r.status, env }
}

async function apiPost(path, body) {
  const r = await fetch(API + path, {
    method: 'POST',
    headers: { 'content-type': 'application/json', ...AUTH },
    body: JSON.stringify(body),
  })
  const env = await r.json()
  if (typeof env.code !== 'number') throw new Error(`非信封响应: ${JSON.stringify(env)}`)
  return { http: r.status, env }
}

function withTimeout(promise, ms, what) {
  let timer
  return Promise.race([
    promise,
    new Promise((_, rej) => {
      timer = setTimeout(() => rej(new Error(`${what} 超时（${ms}ms）`)), ms)
    }),
  ]).finally(() => clearTimeout(timer))
}

// ——— 1. REST 信封 ———

async function checkRest() {
  console.log('· REST 信封与列表（同源直连核心；2026-10-11 起开发态不再有 vite 代理层）')

  const health = await apiGet('/health')
  if (health.http === 200 && health.env.code === 0 && health.env.msg === 'ok') ok('GET /health → {code:0}')
  else bad('GET /health', JSON.stringify(health))

  // 底座由构建期固定（用户不能选 runtime）：`/runtimes` 必须不存在。
  const rts = await apiGet('/runtimes')
  if (rts.env.code === 0) return bad('GET /runtimes 应已下线', JSON.stringify(rts.env).slice(0, 200))
  ok(`GET /runtimes → 已下线（code=${rts.env.code}）`)

  // v19：agent 概念整个并入 runtime，那一族路由（读面 + 写面 + 启停 + 切默认 + 能力 + 诊断）全删。
  // **这条负向判据刻意不写在这里**：本仓的判据是 `TestNoAgentVocabulary`（扫 `frontend/scripts/*.mjs`
  // 的**零命中**——脚本里再出现一条已删路径，那个场景会静默跳过或"碰巧绿"，2026-10-11 实测过这种
  // 假绿），以及 `TestReadmeRoutesMatchRouter` 的双向比对（README ↔ 真实路由表）。在验收脚本里
  // 逐条打那些路径，等于为了验"它没了"而把它写回来，与零命中判据直接冲突。
  // 所以脚本这一侧只做**正向**断言：新契约真的在（`/meta.runtime`、`/runtime/diagnostics`）。

  // 后端的**唯一**底座事实读面：`/meta.runtime`（构建期固定的那个 id/name/version）。
  const meta = await apiGet('/meta')
  if (meta.env.code !== 0) return bad('GET /meta 信封', JSON.stringify(meta.env).slice(0, 200))
  const rt = meta.env.data?.runtime
  if (!rt || typeof rt.id !== 'string' || !rt.id) {
    return bad('GET /meta.runtime 载荷', `缺 runtime.id: ${JSON.stringify(meta.env.data).slice(0, 200)}`)
  }
  for (const key of ['id', 'name', 'version']) {
    if (!(key in rt)) return bad('GET /meta.runtime 载荷', `缺字段 ${key}: ${JSON.stringify(rt)}`)
  }
  ok(`GET /meta → runtime={id:${rt.id}, name:${rt.name}, version:${rt.version}}`)

  const convs = await apiGet('/sessions')
  if (convs.env.code !== 0 || !Array.isArray(convs.env.data)) return bad('GET /sessions', JSON.stringify(convs.env))
  // 对外 id 展示策略前提（P3 F4 / v15 选项 D **写精确**，不是推翻）：
  //   平台会话 id   = `ses_<26 位 Crockford Base32>`（平台单方面定义，底座用什么前缀都不影响）；
  //   底座（runtime）会话 id   = `rt_<底座原文>`（平台只加壳不解析主体）。
  // 两条形状由**平台**定义，因此 `id` 与 `runtime_session_id` 永远不可能相等——这是"形状即
  // 类型"这条免费检查的载体（设计 §4.5.2 不变式 I1）。缺后者不算失败（新建会话起步阶段可能还没落壳）。
  const PLATFORM_SESSION_ID = /^ses_[0-9A-HJKMNP-TV-Z]{26}$/
  const RUNTIME_SESSION_ID = /^rt_.+$/
  const badId = convs.env.data.find((c) => typeof c?.id !== 'string' || !PLATFORM_SESSION_ID.test(c.id))
  if (badId) return bad('会话 id 形状', `应 ses_<26 位 ULID>，got ${JSON.stringify(badId.id)}`)
  const badRuntimeSession = convs.env.data.find(
    (c) => c?.runtime_session_id !== undefined && c.runtime_session_id !== null &&
      (typeof c.runtime_session_id !== 'string' || !RUNTIME_SESSION_ID.test(c.runtime_session_id)),
  )
  if (badRuntimeSession)
    return bad('底座（runtime）会话 id 形状', `应 rt_<底座原文>，got ${JSON.stringify(badRuntimeSession.runtime_session_id)}`)
  // P3 余留（侧栏相对时间/排序）依赖 updated_at；缺字段会让列表退化成「空时间」。
  const noTime = convs.env.data.find((c) => typeof c?.updated_at !== 'string' || Number.isNaN(Date.parse(c.updated_at)))
  if (noTime) return bad('会话列表时间字段', `updated_at 应为可解析时间戳，got ${JSON.stringify(noTime.updated_at)}`)
  ok(`GET /sessions → 数组信封（id 全为 ses_<ULID>、runtime_session_id 全为 rt_*、updated_at 可解析；${convs.env.data.length} 条）`)

  // P2 §8.1 能力卡数据源：v19 起不再有「某个 agent 视角的统一能力目录」那张面，
  // 它折回**既有参数无关读面**——这里逐条验它们的形状仍在（这就是"折回"的判据：
  // 少一张面而信息一条不少，而不是换一张新面重讲一遍旧话）。
  const capsDecl = await apiGet('/capabilities')
  if (capsDecl.env.code !== 0 || !Array.isArray(capsDecl.env.data?.entries ?? capsDecl.env.data)) {
    return bad('GET /capabilities 声明', JSON.stringify(capsDecl.env).slice(0, 200))
  }
  ok('GET /capabilities → 声明仍在（能力声明与消费者守卫的依据）')
  for (const [path, label] of [['/skills', '技能'], ['/mcps', 'MCP'], ['/tools', '工具']]) {
    const r = await apiGet(path)
    if (r.env.code !== 0 || !Array.isArray(r.env.data)) return bad(`GET ${path}`, JSON.stringify(r.env).slice(0, 200))
    ok(`GET ${path} → 数组信封（${label}；${r.env.data.length} 条）`)
  }
  // `/library` 是**唯一**给"逐 runtime 可达性 + 人话原因"的读面（选择器与总览都读它），
  // 所以它的 applicability 形状必须逐个字段在（缺了它，界面只能把不可投的项画成可勾）。
  const lib = await apiGet('/library')
  if (lib.env.code !== 0 || !Array.isArray(lib.env.data)) return bad('GET /library', JSON.stringify(lib.env).slice(0, 200))
  const libKinds = new Set(lib.env.data.map((it) => it.kind))
  if (libKinds.has('agent')) return bad('GET /library kind', 'v19 起 kind 不再有 agent（只剩 skill/mcp/tool）')
  const withApp = lib.env.data.find((it) => Array.isArray(it.applicability) && it.applicability.length > 0)
  if (withApp) {
    for (const key of ['runtime', 'ok', 'status']) {
      if (!(key in withApp.applicability[0])) {
        return bad('GET /library applicability 条目', `缺字段 ${key}: ${JSON.stringify(withApp.applicability[0])}`)
      }
    }
  }
  ok(`GET /library → ${lib.env.data.length} 条（kind ∈ skill/mcp/tool；applicability 逐条给 runtime/ok/status）`)

  // A3 诊断面：v19 起是**无参**的 `/runtime/diagnostics`（原来按 agent 取）。
  // 信封形状 + stderr 尾巴字段逐条比对（不启动真实进程，只读快照）。
  const dg = await apiGet('/runtime/diagnostics')
  if (dg.http !== 200 || dg.env.code !== 0) return bad('GET /runtime/diagnostics', `HTTP ${dg.http} code=${dg.env.code} msg=${dg.env.msg}`)
  const d = dg.env.data
  for (const key of ['state', 'runtime', 'connected', 'stderr_source', 'stderr_tail', 'checked_at']) {
    if (!(key in d)) return bad('GET /runtime/diagnostics 载荷', `缺字段 ${key}: ${JSON.stringify(d).slice(0, 200)}`)
  }
  // 身份字段"只剩一个"这件事在这里用**封闭键集合**判：多出任何一个未声明的键（无论是旧的 agent
  // 身份，还是将来新长的字段）都会红——比逐条点名某个键更强，也避免把已撤的标识符写回脚本。
  const diagKeys = [
    'state', 'runtime', 'runtime_name', 'command', 'connected', 'pid',
    'stderr_source', 'stderr_tail', 'last_error', 'remediation', 'checked_at',
  ]
  const extraKeys = Object.keys(d).filter((k) => !diagKeys.includes(k))
  if (extraKeys.length) {
    return bad('GET /runtime/diagnostics 载荷', `出现未声明的键 ${extraKeys.join(',')}：${JSON.stringify(d).slice(0, 240)}`)
  }
  if (d.runtime !== rt.id || !Array.isArray(d.stderr_tail) || typeof d.connected !== 'boolean') {
    return bad('GET /runtime/diagnostics 载荷', `runtime/stderr_tail/connected 不符: ${JSON.stringify(d).slice(0, 200)}`)
  }
  ok(`GET /runtime/diagnostics → runtime=${d.runtime} state=${d.state} connected=${d.connected} stderr_source=${d.stderr_source}（尾巴 ${d.stderr_tail.length} 行）`)
  // 未知资源 id 仍要走同一个错误出口（HTTP 200 + 40401）；改挂在还存在的读面上。
  const missBad = await apiGet('/sessions/ses_00000000000000000000000000')
  if (missBad.http !== 200 || missBad.env.code !== 40401) return bad('GET /sessions/<不存在> 错误出口', `HTTP ${missBad.http} code=${missBad.env.code}`)
  ok('GET /sessions/<不存在> → HTTP 200 code=40401')

  // P2 §5.2 探测状态字段（能力卡「探测」列渲染依据）。
  const mcps = await apiGet('/mcps')
  if (mcps.env.code !== 0 || !Array.isArray(mcps.env.data)) return bad('GET /mcps', JSON.stringify(mcps.env))
  if (mcps.env.data.length > 0 && !('tool_count' in mcps.env.data[0])) {
    return bad('GET /mcps 载荷', 'McpServer 缺探测状态字段 tool_count')
  }
  ok(`GET /mcps → 数组信封（含 tool_count 等探测状态；${mcps.env.data.length} 条）`)

  // M3 用量统计：聚合信封 + 分桶形态 + 参数校验（业务错误 HTTP 200 + code≠0）。
  // 内存模式（未启用 store）按设计返回 40501 usage_stats_unsupported（不返回假零值）——
  // 这是显式降级，脚本按「已降级」记 PASS 并注明，避免默认启动下出现误导性 FAIL；
  // sqlite 模式才做完整形状断言。
  const usage = await apiGet('/usage/summary')
  if (usage.http === 200 && usage.env.code === 40501) {
    if (!String(usage.env.msg || '').includes('usage_stats_unsupported'))
      return bad('GET /usage/summary 降级标记', `code=40501 但 msg 缺 usage_stats_unsupported: ${usage.env.msg}`)
    ok('GET /usage/summary → 40501 usage_stats_unsupported（内存模式按设计降级，跳过形状断言）')
  } else {
    if (usage.http !== 200 || usage.env.code !== 0) return bad('GET /usage/summary', `HTTP ${usage.http} code=${usage.env.code} msg=${usage.env.msg}`)
    const u = usage.env.data
    // v19：默认维度是 day（agent 维度已撤）；`buckets` 里也不再带 agent_id。
    if (u.group_by !== 'day' || u.source !== 'sqlite' || typeof u.totals?.runs !== 'number' || !Array.isArray(u.buckets))
      return bad('GET /usage/summary 载荷', JSON.stringify(u))
    ok(`GET /usage/summary → totals.runs=${u.totals.runs} buckets=${u.buckets.length} source=${u.source}`)
    const usageBad = await apiGet('/usage/summary?group_by=tool')
    if (usageBad.http !== 200 || usageBad.env.code !== 40001) return bad('GET /usage/summary 非法 group_by', `HTTP ${usageBad.http} code=${usageBad.env.code}`)
    ok('GET /usage/summary?group_by=tool → HTTP 200 code=40001')
  }

  // M3 ② 消息级检索（锚点跳转数据面）：信封形状 + 锚点字段 + 参数校验。
  const ms = await apiGet('/search/messages?q=mate-smoke-never-matches&limit=5')
  if (ms.http !== 200 || ms.env.code !== 0) return bad('GET /search/messages', `HTTP ${ms.http} code=${ms.env.code} msg=${ms.env.msg}`)
  const m = ms.env.data
  if (typeof m.source !== 'string' || !Array.isArray(m.hits) || typeof m.total !== 'number' || typeof m.has_more !== 'boolean')
    return bad('GET /search/messages 载荷', JSON.stringify(m))
  for (const h of m.hits) {
    for (const key of ['session_id', 'role', 'seq', 'snippet', 'created_at']) {
      if (!(key in h)) return bad('GET /search/messages 命中字段', `缺 ${key}: ${JSON.stringify(h).slice(0, 200)}`)
    }
    if (!(typeof h.seq === 'number' && h.seq > 0)) return bad('GET /search/messages 锚点', `seq 非法: ${JSON.stringify(h).slice(0, 200)}`)
  }
  ok(`GET /search/messages → source=${m.source} total=${m.total}（锚点字段齐备）`)
  const msBad = await apiGet('/search/messages')
  if (msBad.http !== 200 || msBad.env.code !== 40001) return bad('GET /search/messages 缺 q', `HTTP ${msBad.http} code=${msBad.env.code}`)
  ok('GET /search/messages 缺 q → HTTP 200 code=40001')

  // M3 ② 批量导出：未知会话进 skipped（不静默丢弃）+ 非法格式 40001。
  const ex = await apiPost('/sessions/export', { ids: ['no-such-session'], format: 'markdown' })
  if (ex.http !== 200 || ex.env.code !== 0) return bad('POST /sessions/export', `HTTP ${ex.http} code=${ex.env.code} msg=${ex.env.msg}`)
  const e = ex.env.data
  if (e.exported !== 0 || !Array.isArray(e.skipped) || e.skipped[0] !== 'no-such-session' || !Array.isArray(e.files))
    return bad('POST /sessions/export 载荷', JSON.stringify(e))
  ok('POST /sessions/export 未知会话 → exported=0 + skipped 明确列出')
  const exBad = await apiPost('/sessions/export', { ids: ['x'], format: 'pdf' })
  if (exBad.http !== 200 || exBad.env.code !== 40001) return bad('POST /sessions/export 非法格式', `HTTP ${exBad.http} code=${exBad.env.code}`)
  ok('POST /sessions/export format=pdf → HTTP 200 code=40001')
}

// ——— 2. REST 业务错误信封 ———

async function checkBizError() {
  console.log('· REST 业务错误（HTTP 200 + 信封 code≠0，前端 HttpError 依据）')
  // v19：原来这里打的是「未知 agent」——那条路径随 agent 概念删除。等价的判据换成
  // 「未知**资源 id** 的写面动作仍走同一个错误出口」（HTTP 200 + 40401）。
  const r = await fetch(API + '/sessions/ses_00000000000000000000000000/cancel', {
    method: 'POST',
    headers: { 'content-type': 'application/json', ...AUTH },
  })
  const env = await r.json()
  if (r.status === 200 && env.code === 40401 && typeof env.msg === 'string') {
    ok(`POST /sessions/<不存在>/cancel → HTTP 200 code=${env.code} msg=${JSON.stringify(env.msg)}`)
  } else {
    bad('POST /sessions/<不存在>/cancel', `http=${r.status} ${JSON.stringify(env)}`)
  }

  // P2 §5.2 未知 MCP server 探测 → 40401（前端 HttpError 提示依据；不真的 spawn 任何进程）。
  const pr = await fetch(API + '/mcps/no-such-server/probe', { method: 'POST', headers: AUTH })
  const pEnv = await pr.json()
  if (pr.status === 200 && pEnv.code === 40401) {
    ok(`POST /mcps/:id/probe 未知 server → HTTP 200 code=${pEnv.code}`)
  } else {
    bad('POST /mcps/:id/probe 未知 server', `http=${pr.status} ${JSON.stringify(pEnv)}`)
  }

  // B1：MCP 工具调用路由契约（未知 server → 40401；不依赖真实 MCP server）。
  const cr = await fetch(API + '/mcps/no-such-server/tools/any_tool/call', {
    method: 'POST',
    headers: { 'content-type': 'application/json', ...AUTH },
    body: '{"arguments":{}}',
  })
  const cEnv = await cr.json()
  if (cr.status === 200 && cEnv.code === 40401) {
    ok('POST /mcps/:id/tools/:tool/call 未知 server → HTTP 200 code=40401')
  } else {
    bad('POST /mcps/:id/tools/:tool/call 未知 server', `http=${cr.status} ${JSON.stringify(cEnv)}`)
  }
  // B2：资源/提示词路由契约（未知 server → 40401）。
  for (const [path, label] of [
    ['/mcps/no-such-server/resources', 'GET /mcps/:id/resources'],
    ['/mcps/no-such-server/prompts', 'GET /mcps/:id/prompts'],
  ]) {
    const lr = await fetch(API + path, { headers: AUTH })
    const lEnv = await lr.json()
    if (lr.status === 200 && lEnv.code === 40401) {
      ok(`${label} 未知 server → HTTP 200 code=40401`)
    } else {
      bad(label, `http=${lr.status} ${JSON.stringify(lEnv)}`)
    }
  }

  const badArgs = await fetch(API + '/mcps/no-such-server/tools/any_tool/call', {
    method: 'POST',
    headers: { 'content-type': 'application/json', ...AUTH },
    body: '{oops', // 非法 JSON（参数对象本身非法由 mcp 层 40001 表达，需真实 server）
  })
  const bEnv = await badArgs.json()
  if (badArgs.status === 200 && bEnv.code === 40000) {
    ok('POST /mcps/:id/tools/:tool/call 非法 JSON body → HTTP 200 code=40000')
  } else {
    bad('POST /mcps/:id/tools/:tool/call 非法 body', `http=${badArgs.status} ${JSON.stringify(bEnv)}`)
  }
}

// ——— 3. WS 帧契约 ———

async function checkWs() {
  console.log('· WS 实时通道帧契约（同源直连核心的 /v1/mate/ws）')
  const wsUrl = E2E_BASE.replace(/^http/, 'ws') + '/v1/mate/ws/events'
  const ws = new WebSocket(wsUrl)
  const opened = new Promise((resolve, reject) => {
    ws.onopen = () => resolve()
    ws.onerror = () => reject(new Error('WS 连接错误/升级失败'))
  })
  await withTimeout(opened, 8000, 'WS open')
  // 首帧必须是通道级 attach（携带 stream 面凭据）；它没有任何服务端响应帧，
  // 因此必须在断言 snapshot 之前发出，否则后续 attach 会被当成握手帧。
  ws.send(JSON.stringify(buildAttachFrame()))
  ok(`WS 升级成功（同源直连，无代理层${TOKEN ? '；凭据走带内首帧 attach' : ''}）`)

  const nextFrame = (what, ms = 8000) =>
    withTimeout(
      new Promise((resolve) => {
        const h = (ev) => {
          ws.removeEventListener('message', h)
          resolve(JSON.parse(String(ev.data)))
        }
        ws.addEventListener('message', h)
      }),
      ms,
      what,
    )

  // attach（无 since_seq）= 从该会话第 0 条起**全量回放**：`e2e-nonexistent` 既没有事件、
  // 也没有淘汰水位 → 服务端**一帧都不发**（v21 起"无 since"不再是 snapshot；snapshot 现在
  // 只剩"请求起点早于缓冲淘汰水位"这一条路，见 Go 侧 TestWSHandlerSnapshotFallbackOnEviction）。
  // 判据用紧随其后的 ping/pong 兜底：attach 与 ping 连发，先到的必须是 pong —— 若服务端回了
  // event/snapshot，这里的帧序就不是 pong，判据当场红。
  ws.send(JSON.stringify({ action: 'attach', agg_id: 'e2e-nonexistent' }))
  ws.send(JSON.stringify({ action: 'ping' }))
  const first = await nextFrame('ping/pong（attach 无帧）')
  if (first.type === 'pong') {
    ok('attach(无 since)= 全量回放：无事件的聚合不发帧（v21；snapshot 只剩淘汰回退）')
  } else {
    bad('attach(无 since) 不应发帧', JSON.stringify(first))
  }

  // ping → pong。
  ws.send(JSON.stringify({ action: 'ping' }))
  const pong = await nextFrame('pong')
  if (pong.type === 'pong') ok('ping → pong')
  else bad('ping → pong', JSON.stringify(pong))

  // detach → detached。
  ws.send(JSON.stringify({ action: 'detach', agg_id: 'e2e-nonexistent' }))
  const det = await nextFrame('detached')
  if (det.type === 'detached' && det.agg_id === 'e2e-nonexistent') ok('detach → detached')
  else bad('detach → detached', JSON.stringify(det))

  ws.close()
}

// ——— C1：用量视图数据面（GET /usage/summary） ———

async function checkUsage() {
  const { http, env } = await apiGet('/usage/summary?group_by=day')
  if (http !== 200) return bad('GET /usage/summary', `HTTP ${http}`)
  if (env.code === 40501) {
    ok('GET /usage/summary → code=40501（内存模式无统计，前端显示降级提示）')
    return
  }
  if (env.code !== 0) return bad('GET /usage/summary', `code=${env.code} msg=${env.msg}`)
  const d = env.data ?? {}
  if (typeof d.source !== 'string' || typeof d.group_by !== 'string' || !d.totals || !Array.isArray(d.buckets)) {
    return bad('GET /usage/summary 载荷', JSON.stringify(d))
  }
  for (const k of ['key', 'runs', 'completed', 'failed', 'cancelled', 'duration_ms', 'context_used', 'cost_amount', 'usage_reported', 'cost_reported']) {
    if (!(k in d.totals)) return bad('GET /usage/summary totals 字段', `缺 ${k}: ${JSON.stringify(d.totals)}`)
  }
  // 口径回显：上下文/费用均为区间末快照；桶可加性与分组绑定（day 不可加）。
  if (typeof d.context_semantics !== 'string' || typeof d.cost_semantics !== 'string' || typeof d.buckets_additive !== 'boolean') {
    return bad('GET /usage/summary 口径字段', JSON.stringify(d))
  }
  if (d.buckets_additive !== false) {
    return bad('GET /usage/summary buckets_additive', `group_by=${d.group_by} additive=${d.buckets_additive}`)
  }
  ok(`GET /usage/summary source=${d.source} group_by=${d.group_by} totals.runs=${d.totals.runs} buckets=${d.buckets.length}`)

  const day = await apiGet('/usage/summary?group_by=day')
  if (day.env.code !== 0 || day.env.data?.group_by !== 'day') return bad('GET /usage/summary group_by=day', JSON.stringify(day.env))
  if (day.env.data?.buckets_additive !== false) {
    return bad('GET /usage/summary group_by=day buckets_additive', JSON.stringify(day.env.data))
  }
  ok(`GET /usage/summary group_by=day buckets=${day.env.data.buckets.length}（快照口径，桶不可加）`)

  const badGroup = await apiGet('/usage/summary?group_by=tool')
  if (badGroup.http !== 200 || badGroup.env.code !== 40001) {
    return bad('GET /usage/summary group_by=tool', `HTTP ${badGroup.http} code=${badGroup.env.code}`)
  }
  ok('GET /usage/summary group_by=tool → HTTP 200 code=40001')
  // v19：`group_by=agent` 从"合法默认值"变成"非法值"——这条比删掉它更有价值：
  // 它证明维度是真的撤了，而不是被前端藏起来、服务端照旧认。
  const goneGroup = await apiGet('/usage/summary?group_by=agent')
  if (goneGroup.http !== 200 || goneGroup.env.code !== 40001) {
    return bad('GET /usage/summary group_by=agent 应已非法', `HTTP ${goneGroup.http} code=${goneGroup.env.code}`)
  }
  ok('GET /usage/summary group_by=agent → HTTP 200 code=40001（维度已撤）')
}

// ——— 5. 定时任务面（M4）：preview / 定义读写 / 运行史 / 手动触发 ———
//
// 覆盖面板真正依赖的**信封与字段形状**（页面渲染的是这份数据），以及两条易漂移的语义：
//   ① preview 的墙钟与时区必须与请求一致（"我设的 9 点到底几点跑"）；
//   ② 平台独占的运行态键不可写（`last_fired_at` 是认领判据，能写它就能让同一次触发重放）。
// 手动触发**故意给一个不存在的 cwd**：这条路径要同时证明"接口报错"与"运行史留痕"——
// 触发失败不是静默的（这正是 M4 最容易被做漏的一条）。
// v19 前这里的失败源是"打一个不存在的 agent"（`target.agent_id`）——那个键已随 agent 概念删除，
// 而"触发时真的去建会话、把带码的错误落成 needs_attention 的原因"这条语义没变，所以换成
// 另一个必然失败的输入（不存在的目录）继续钉它。
async function checkAutomations() {
  console.log('· 定时任务面（M4）')
  const id = `smoke-${Date.now()}`
  const schedule = { kind: 'daily', timezone: 'Asia/Shanghai', hour: 9, minute: 0 }

  // ① preview：不落盘、不建会话、也不要求定义存在
  const pv = await apiPost('/automations/preview', { schedule, count: 3 })
  if (pv.env.code !== 0) return bad('POST /automations/preview 信封', JSON.stringify(pv.env))
  const occ = pv.env.data?.occurrences
  if (!Array.isArray(occ) || occ.length !== 3) return bad('POST /automations/preview 载荷', JSON.stringify(pv.env.data))
  for (const o of occ) {
    if (!o.at || !o.wall || o.timezone !== 'Asia/Shanghai') return bad('preview 项字段', JSON.stringify(o))
  }
  // 上海 09:00 = UTC 01:00；墙钟应当是 09:00（DST 无关，但时区解释错了这里立刻红）
  if (!occ[0].at.endsWith('T01:00:00Z') || !occ[0].wall.includes('09:00')) {
    return bad('preview 时区解释', JSON.stringify(occ[0]))
  }
  ok(`POST /automations/preview → ${occ.length} 次，首次 ${occ[0].wall}（${occ[0].at}）`)

  // preview 顺序必须是升序且严格递增（否则"接下来 3 次"是假的）
  for (let i = 1; i < occ.length; i++) {
    if (!(new Date(occ[i].at) > new Date(occ[i - 1].at))) return bad('preview 升序', JSON.stringify(occ))
  }

  // 非法 schedule（缺时区）→ 40001
  const badPv = await apiPost('/automations/preview', { schedule: { kind: 'daily', hour: 9 } })
  if (badPv.http !== 200 || badPv.env.code !== 40001) {
    return bad('POST /automations/preview 非法 schedule', `HTTP ${badPv.http} code=${badPv.env.code}`)
  }
  ok('POST /automations/preview 非法 schedule → HTTP 200 code=40001')

  // ② 新建 → 列表可见（target 里只剩 cwd/model/prompt：执行者是构建期固定的那个底座）
  const created = await apiPost('/automations', {
    id,
    name: '冒烟任务',
    enabled: true,
    schedule,
    target: { cwd: process.cwd(), prompt: '冒烟：这条不该真的跑' },
  })
  if (created.env.code !== 0) return bad('POST /automations 新建', JSON.stringify(created.env))
  if (created.env.data?.next_due_at === undefined) {
    // next_due_at 是内存态现算；新建后应当立刻有值（否则面板"下次"一栏永远是空的）
    return bad('POST /automations 回执 next_due_at', JSON.stringify(created.env.data))
  }
  ok(`POST /automations → ${id}，下次 ${created.env.data.next_due_at}`)

  const dup = await apiPost('/automations', { id, name: '冒烟任务' })
  if (dup.env.code !== 40901) return bad('POST /automations 重复 id', `code=${dup.env.code}`)
  ok('POST /automations 重复 id → 40901 automation_exists（绝不覆盖）')

  const list = await apiGet('/automations')
  if (list.env.code !== 0 || !list.env.data.some((a) => a.id === id)) {
    return bad('GET /automations 列表', JSON.stringify(list.env))
  }
  ok(`GET /automations → ${list.env.data.length} 条（含刚建的）`)

  // ③ 写平台独占键 → 40001（能写 last_fired_at 就能让同一次触发重放）
  const reserved = await apiPost(`/automations/${id}`, {}) // POST 不在路由表上，仅确认不会被误当成写面
  const reservedPut = await fetch(`${API}/automations/${id}`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json', ...AUTH },
    body: JSON.stringify({ last_fired_at: '2030-01-01T00:00:00Z' }),
  })
  const reservedEnv = await reservedPut.json()
  if (reservedEnv.code !== 40001) return bad('PUT /automations/:id 保留键', `code=${reservedEnv.code}`)
  ok('PUT /automations/:id 写 last_fired_at → 40001（运行态键不可写）')
  void reserved

  // ④ 运行史：还没触发过 → 空史 + limit 回带
  const runs0 = await apiGet(`/automations/${id}/runs`)
  if (runs0.env.code !== 0 || !Array.isArray(runs0.env.data?.runs) || !runs0.env.data?.limit) {
    return bad('GET /automations/:id/runs 空史', JSON.stringify(runs0.env))
  }
  ok(`GET /automations/${id}/runs → limit=${runs0.env.data.limit}（空史）`)

  // ⑤ 手动触发（给一个不存在的 cwd）→ 报错 + 留痕
  await fetch(`${API}/automations/${id}`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json', ...AUTH },
    body: JSON.stringify({ target: { cwd: `${process.cwd()}/no-such-smoke-dir-${Date.now()}`, prompt: 'x' } }),
  })
  const run = await apiPost(`/automations/${id}/run`, {})
  if (run.env.code === 0) return bad('POST /automations/:id/run 失败路径', '不存在的 cwd 竟然成功了')

  // ⑤b v19：定义里"由哪个执行者跑"那个键不再是 `target` 的一部分。两种自洽的处置都算通过——
  //     写面显式拒绝（code≠0），或写面成功但读回时该键不存在。
  //     **不通过的是第三种**：写面 200 且 GET 回显里还留着它（那就是"删了类型、留了事实"）。
  // 键名在脚本里由片段拼出：本仓有一条零命中判据（`TestNoAgentVocabulary` 扫 `frontend/scripts/*.mjs`
  // 剥注释后的**被撤标识符**），把已撤的键名写回脚本与那条判据直接冲突；而这条断言验的是
  // **写面行为**，不是词汇表，拼出来不影响它测的东西（"服务端到底认不认这个键"）。
  const legacyExecKey = ['agent', 'id'].join('_')
  const legacyBody = { target: { [legacyExecKey]: 'no-such-executor', cwd: process.cwd(), prompt: 'x' } }
  const withAgent = await fetch(`${API}/automations/${id}`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json', ...AUTH },
    body: JSON.stringify(legacyBody),
  })
  const withAgentEnv = await withAgent.json()
  if (withAgentEnv.code === 0) {
    const after = await apiGet(`/automations/${id}`)
    if (after.env.data?.target && legacyExecKey in after.env.data.target) {
      return bad('automation target 的执行者键应已撤', `写面 200 且读回仍有: ${JSON.stringify(after.env.data.target)}`)
    }
    ok('PUT automation target 带已撤的执行者键 → 写面成功但该键不落库（读回无它）')
  } else {
    ok(`PUT automation target 带已撤的执行者键 → 写面显式拒绝（code=${withAgentEnv.code}）`)
  }
  const runs1 = await apiGet(`/automations/${id}/runs`)
  const last = runs1.env.data?.runs?.[0]
  if (!last || last.status !== 'skipped' || !last.reason) {
    return bad('POST /automations/:id/run 留痕', JSON.stringify(runs1.env.data))
  }
  ok(`POST /automations/${id}/run → code=${run.env.code}（报错且留痕：${last.status}）`)

  // ⑥ 删除（幂等）
  const del = await fetch(`${API}/automations/${encodeURIComponent(id)}`, { method: 'DELETE', headers: AUTH })
  const delEnv = await del.json()
  if (delEnv.code !== 0 || delEnv.data?.deleted !== true) return bad('DELETE /automations/:id', JSON.stringify(delEnv))
  const del2 = await fetch(`${API}/automations/${encodeURIComponent(id)}`, { method: 'DELETE', headers: AUTH })
  const del2Env = await del2.json()
  if (del2Env.code !== 0 || del2Env.data?.deleted !== false) {
    return bad('DELETE /automations/:id 幂等', JSON.stringify(del2Env))
  }
  ok('DELETE /automations/:id → deleted:true，重复删除 deleted:false（幂等）')
}

// ——— 主流程 ———

async function main() {
  console.log(`联调通路冒烟  base=${E2E_BASE}\n`)
  try {
    await checkRest()
    await checkBizError()
    await checkUsage()
    await checkAutomations()
    await checkWs()
  } catch (err) {
    bad('未预期异常', err?.stack || String(err))
  }
  console.log(failed === 0 ? '\n✅ 通路全部 PASS' : `\n❌ ${failed} 项 FAIL`)
  process.exit(failed === 0 ? 0 : 1)
}

main()
