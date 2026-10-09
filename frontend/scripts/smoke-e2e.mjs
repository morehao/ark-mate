#!/usr/bin/env node
/**
 * 前后端联调「通路」冒烟（无需浏览器、无需新依赖，Node ≥ 22）。
 *
 * 验证对象：运行中的 vite dev（默认 http://localhost:5173，/v1 代理到 mate serve 127.0.0.1:10001）。
 * 覆盖：
 *   1. REST 信封（health / agents / runtimes / conversations）——@ark/api 的 unwrap 语义前提；
 *   2. REST 业务错误仍为 HTTP 200、信封 code!=0（前端 HttpError 读取 code/msg 的契约）；
 *   3. 载荷键形状与 @ark/types 声明一致（Agent/Runtime 列表字段）；
 *   4. WS 实时通道帧契约：attach(无 since) → snapshot、ping → pong、detach → detached；
 *   5. 用量视图数据面（C1）：/usage/summary 分组/字段形状 + 非法 group_by 的业务错误
 *      （sqlite 模式）；内存模式按设计 40501 usage_stats_unsupported → 记 PASS 并注明降级。
 *
 * 用法（先起 make dev，或后端 127.0.0.1:10001 + 前端 vite dev :5173）：
 *   E2E_BASE=http://localhost:5173 node scripts/smoke-e2e.mjs
 *   pnpm smoke:e2e
 *
 * 退出码：0 = 全部通路 PASS；1 = 存在 FAIL。
 */
import { buildAttachFrame } from '@ark/desktop-contract'

const E2E_BASE = process.env.E2E_BASE || 'http://localhost:5173'
const API = E2E_BASE + '/v1/mate'
// 后端启用凭据时，同一套脚本带上令牌：REST 走 Authorization 头；
// WS 走**带内首帧 attach**（M1-1 起令牌不进 URL —— 与 apps/web 的 transport 同一条规则，
// 帧形状也用同一个 buildAttachFrame，避免脚本与产品各写一份）。
const TOKEN = process.env.MATE_SMOKE_TOKEN || ''
const AUTH = TOKEN ? { authorization: `Bearer ${TOKEN}` } : {}

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
  console.log('· REST 信封与列表（经 vite proxy）')

  const health = await apiGet('/health')
  if (health.http === 200 && health.env.code === 0 && health.env.msg === 'ok') ok('GET /health → {code:0}')
  else bad('GET /health', JSON.stringify(health))

  const agents = await apiGet('/agents')
  if (agents.env.code !== 0) return bad('GET /agents 信封', JSON.stringify(agents.env))
  const agentList = agents.env.data
  if (!Array.isArray(agentList) || agentList.length === 0) return bad('GET /agents 载荷', '应为非空数组')
  const a0 = agentList[0]
  for (const key of ['id', 'name', 'state', 'seed']) {
    if (!(key in a0)) return bad('GET /agents 载荷', `Agent 缺字段 ${key}`)
  }
  // 种子只读标记（服务端权威字段；前端「种子」徽标与删除按钮据此判定）。
  const seeds = agentList.filter((a) => a.seed === true)
  if (seeds.length === 0) return bad('GET /agents 种子标记', '内置播种 agent 应带 seed:true')
  ok(`GET /agents → ${agentList.length} 个（字段 id/name/state/seed 齐；种子 ${seeds.length} 个）`)

  const rts = await apiGet('/runtimes')
  if (rts.env.code !== 0) return bad('GET /runtimes 信封', JSON.stringify(rts.env))
  const runtimeList = rts.env.data
  if (!Array.isArray(runtimeList) || runtimeList.length === 0) return bad('GET /runtimes 载荷', '应为非空数组')
  const r0 = runtimeList[0]
  for (const key of ['name', 'version']) {
    if (!(key in r0)) return bad('GET /runtimes 载荷', `Runtime 缺字段 ${key}`)
  }
  ok(`GET /runtimes → ${runtimeList.length} 个（字段 name/version 齐）`)

  const convs = await apiGet('/conversations')
  if (convs.env.code !== 0 || !Array.isArray(convs.env.data)) return bad('GET /conversations', JSON.stringify(convs.env))
  // 对外 id 展示策略前提（P3 F4）：会话 id 为平台 uuid（36 位），不再泄露底座 id 形状（ses_* 等）。
  const badId = convs.env.data.find(
    (c) => typeof c?.id !== 'string' || !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(c.id),
  )
  if (badId) return bad('会话 id 形状', `应平台 uuid，got ${JSON.stringify(badId.id)}`)
  // P3 余留（侧栏相对时间/排序）依赖 updated_at；缺字段会让列表退化成「空时间」。
  const noTime = convs.env.data.find((c) => typeof c?.updated_at !== 'string' || Number.isNaN(Date.parse(c.updated_at)))
  if (noTime) return bad('会话列表时间字段', `updated_at 应为可解析时间戳，got ${JSON.stringify(noTime.updated_at)}`)
  ok(`GET /conversations → 数组信封（id 全为平台 uuid、updated_at 可解析；${convs.env.data.length} 条）`)

  // P2 §8.1 能力卡数据源：Agent 统一能力目录（形状 + agent_id 一致）。
  const caps = await apiGet(`/agents/${encodeURIComponent(a0.id)}/capabilities`)
  if (caps.env.code !== 0) return bad('GET /agents/:id/capabilities 信封', JSON.stringify(caps.env))
  const cap = caps.env.data
  for (const key of ['agent_id', 'runtimes', 'skills', 'mcp_servers', 'tools']) {
    if (!(key in cap)) return bad('能力目录载荷', `缺字段 ${key}`)
  }
  if (cap.agent_id !== a0.id || !Array.isArray(cap.runtimes) || !Array.isArray(cap.mcp_servers)) {
    return bad('能力目录载荷', `agent_id/runtimes/mcp_servers 不符: ${JSON.stringify(cap).slice(0, 200)}`)
  }
  const m0 = cap.mcp_servers[0]
  if (m0) {
    for (const key of ['id', 'transport', 'enabled', 'runtimes']) {
      if (!(key in m0)) return bad('能力目录 MCP 条目', `缺字段 ${key}`)
    }
  }
  ok(`GET /agents/:id/capabilities → agent_id 一致（skills=${cap.skills.length} mcps=${cap.mcp_servers.length} tools=${cap.tools.length}）`)

  // A3 诊断面：信封形状 + stderr 尾巴字段（不启动真实进程，只读快照）。
  const dg = await apiGet(`/agents/${a0.id}/diagnostics`)
  if (dg.http !== 200 || dg.env.code !== 0) return bad('GET /agents/:id/diagnostics', `HTTP ${dg.http} code=${dg.env.code} msg=${dg.env.msg}`)
  const d = dg.env.data
  for (const key of ['agent_id', 'state', 'runtime', 'connected', 'stderr_source', 'stderr_tail', 'checked_at']) {
    if (!(key in d)) return bad('GET /agents/:id/diagnostics 载荷', `缺字段 ${key}: ${JSON.stringify(d).slice(0, 200)}`)
  }
  if (d.agent_id !== a0.id || !Array.isArray(d.stderr_tail) || typeof d.connected !== 'boolean') {
    return bad('GET /agents/:id/diagnostics 载荷', `agent_id/stderr_tail/connected 不符: ${JSON.stringify(d).slice(0, 200)}`)
  }
  ok(`GET /agents/:id/diagnostics → state=${d.state} connected=${d.connected} stderr_source=${d.stderr_source}（尾巴 ${d.stderr_tail.length} 行）`)
  const dgBad = await apiGet('/agents/no-such-agent/diagnostics')
  if (dgBad.http !== 200 || dgBad.env.code !== 40401) return bad('GET /agents/:id/diagnostics 未知 agent', `HTTP ${dgBad.http} code=${dgBad.env.code}`)
  ok('GET /agents/no-such-agent/diagnostics → HTTP 200 code=40401')

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
    if (u.group_by !== 'agent' || u.source !== 'sqlite' || typeof u.totals?.runs !== 'number' || !Array.isArray(u.buckets))
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
    for (const key of ['conversation_id', 'role', 'seq', 'snippet', 'created_at']) {
      if (!(key in h)) return bad('GET /search/messages 命中字段', `缺 ${key}: ${JSON.stringify(h).slice(0, 200)}`)
    }
    if (!(typeof h.seq === 'number' && h.seq > 0)) return bad('GET /search/messages 锚点', `seq 非法: ${JSON.stringify(h).slice(0, 200)}`)
  }
  ok(`GET /search/messages → source=${m.source} total=${m.total}（锚点字段齐备）`)
  const msBad = await apiGet('/search/messages')
  if (msBad.http !== 200 || msBad.env.code !== 40001) return bad('GET /search/messages 缺 q', `HTTP ${msBad.http} code=${msBad.env.code}`)
  ok('GET /search/messages 缺 q → HTTP 200 code=40001')

  // M3 ② 批量导出：未知会话进 skipped（不静默丢弃）+ 非法格式 40001。
  const ex = await apiPost('/conversations/export', { ids: ['no-such-conversation'], format: 'markdown' })
  if (ex.http !== 200 || ex.env.code !== 0) return bad('POST /conversations/export', `HTTP ${ex.http} code=${ex.env.code} msg=${ex.env.msg}`)
  const e = ex.env.data
  if (e.exported !== 0 || !Array.isArray(e.skipped) || e.skipped[0] !== 'no-such-conversation' || !Array.isArray(e.files))
    return bad('POST /conversations/export 载荷', JSON.stringify(e))
  ok('POST /conversations/export 未知会话 → exported=0 + skipped 明确列出')
  const exBad = await apiPost('/conversations/export', { ids: ['x'], format: 'pdf' })
  if (exBad.http !== 200 || exBad.env.code !== 40001) return bad('POST /conversations/export 非法格式', `HTTP ${exBad.http} code=${exBad.env.code}`)
  ok('POST /conversations/export format=pdf → HTTP 200 code=40001')
}

// ——— 2. REST 业务错误信封 ———

async function checkBizError() {
  console.log('· REST 业务错误（HTTP 200 + 信封 code≠0，前端 HttpError 依据）')
  const r = await fetch(API + '/conversations', {
    method: 'POST',
    headers: { 'content-type': 'application/json', ...AUTH },
    body: JSON.stringify({ agent_id: 'agent-no-such', prompt: 'hi' }),
  })
  const env = await r.json()
  if (r.status === 200 && env.code === 40401 && typeof env.msg === 'string') {
    ok(`POST /conversations 未知 agent → HTTP 200 code=${env.code} msg=${JSON.stringify(env.msg)}`)
  } else {
    bad('POST /conversations 未知 agent', `http=${r.status} ${JSON.stringify(env)}`)
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
  console.log('· WS 实时通道帧契约（经 vite proxy ws:true）')
  const wsUrl = E2E_BASE.replace(/^http/, 'ws') + '/v1/mate/ws/events'
  const ws = new WebSocket(wsUrl)
  const opened = new Promise((resolve, reject) => {
    ws.onopen = () => resolve()
    ws.onerror = () => reject(new Error('WS 连接错误/升级失败'))
  })
  await withTimeout(opened, 8000, 'WS open')
  // 首帧必须是通道级 attach（携带 stream 面凭据）；它没有任何服务端响应帧，
  // 因此必须在断言 snapshot 之前发出，否则后续 attach 会被当成握手帧。
  ws.send(JSON.stringify(buildAttachFrame(TOKEN || null)))
  ok(`WS 升级成功（proxy ws:true 生效${TOKEN ? '，凭据走带内首帧 attach' : ''}）`)

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

  // attach（无 since_seq）→ snapshot 帧。
  ws.send(JSON.stringify({ action: 'attach', agg_id: 'e2e-nonexistent' }))
  const snap = await nextFrame('snapshot')
  // 契约：snapshot 必带 agg_id + state（终态视图，M0 空对象）；event_seq 为水位，omitempty
  // （bus 尚未有事件时省略 → 视为水位 0）。
  const seqOk = snap.event_seq === undefined || typeof snap.event_seq === 'number'
  if (snap.type === 'snapshot' && snap.agg_id === 'e2e-nonexistent' && seqOk && typeof snap.state === 'object') {
    ok(`attach → snapshot（agg=${snap.agg_id} 水位=${snap.event_seq ?? 0} state=${JSON.stringify(snap.state)}）`)
  } else {
    bad('attach → snapshot', JSON.stringify(snap))
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
  const { http, env } = await apiGet('/usage/summary?group_by=agent')
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
  if (d.buckets_additive !== (d.group_by === 'agent')) {
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
}

// ——— 5. 定时任务面（M4）：preview / 定义读写 / 运行史 / 手动触发 ———
//
// 覆盖面板真正依赖的**信封与字段形状**（页面渲染的是这份数据），以及两条易漂移的语义：
//   ① preview 的墙钟与时区必须与请求一致（"我设的 9 点到底几点跑"）；
//   ② 平台独占的运行态键不可写（`last_fired_at` 是认领判据，能写它就能让同一次触发重放）。
// 手动触发**故意打一个不存在的 agent**：这条路径要同时证明"接口报错"与"运行史留痕"——
// 触发失败不是静默的（这正是 M4 最容易被做漏的一条）。
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

  // ② 新建 → 列表可见
  const agents = await apiGet('/agents')
  const agentID = agents.env.data?.[0]?.id
  const created = await apiPost('/automations', {
    id,
    name: '冒烟任务',
    enabled: true,
    schedule,
    target: { agent_id: agentID, cwd: process.cwd(), prompt: '冒烟：这条不该真的跑' },
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

  // ⑤ 手动触发（打一个不存在的 agent）→ 报错 + 留痕
  await fetch(`${API}/automations/${id}`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json', ...AUTH },
    body: JSON.stringify({ target: { agent_id: 'no-such-agent', cwd: process.cwd(), prompt: 'x' } }),
  })
  const run = await apiPost(`/automations/${id}/run`, {})
  if (run.env.code === 0) return bad('POST /automations/:id/run 失败路径', '打不存在的 agent 竟然成功了')
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
