#!/usr/bin/env node
/**
 * 前后端联调「通路」冒烟（无需浏览器、无需新依赖，Node ≥ 22）。
 *
 * 验证对象：运行中的 vite dev（默认 http://localhost:5173，/v1 代理到 forge serve :8099）。
 * 覆盖：
 *   1. REST 信封（health / agents / runtimes / conversations）——@ark/api 的 unwrap 语义前提；
 *   2. REST 业务错误仍为 HTTP 200、信封 code!=0（前端 HttpError 读取 code/msg 的契约）；
 *   3. 载荷键形状与 @ark/types 声明一致（Agent/Runtime 列表字段）；
 *   4. WS 实时通道帧契约：attach(无 since) → snapshot、ping → pong、detach → detached。
 *
 * 用法（先起 make dev，或后端 :8099 + 前端 vite dev :5173）：
 *   E2E_BASE=http://localhost:5173 node scripts/smoke-e2e.mjs
 *   pnpm smoke:e2e
 *
 * 退出码：0 = 全部通路 PASS；1 = 存在 FAIL。
 */
const E2E_BASE = process.env.E2E_BASE || 'http://localhost:5173'
const API = E2E_BASE + '/v1/forge'

let failed = 0
const ok = (name) => console.log(`  ✔ ${name}`)
const bad = (name, why) => {
  failed++
  console.error(`  ✘ ${name}: ${why}`)
}

// ——— 通用工具 ———

async function apiGet(path) {
  const r = await fetch(API + path)
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
  for (const key of ['id', 'name', 'state']) {
    if (!(key in a0)) return bad('GET /agents 载荷', `Agent 缺字段 ${key}`)
  }
  ok(`GET /agents → ${agentList.length} 个（字段 id/name/state 齐）`)

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

  // P2 §5.2 探测状态字段（能力卡「探测」列渲染依据）。
  const mcps = await apiGet('/mcps')
  if (mcps.env.code !== 0 || !Array.isArray(mcps.env.data)) return bad('GET /mcps', JSON.stringify(mcps.env))
  if (mcps.env.data.length > 0 && !('tool_count' in mcps.env.data[0])) {
    return bad('GET /mcps 载荷', 'McpServer 缺探测状态字段 tool_count')
  }
  ok(`GET /mcps → 数组信封（含 tool_count 等探测状态；${mcps.env.data.length} 条）`)
}

// ——— 2. REST 业务错误信封 ———

async function checkBizError() {
  console.log('· REST 业务错误（HTTP 200 + 信封 code≠0，前端 HttpError 依据）')
  const r = await fetch(API + '/conversations', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ agent_id: 'agent-no-such', prompt: 'hi' }),
  })
  const env = await r.json()
  if (r.status === 200 && env.code === 40401 && typeof env.msg === 'string') {
    ok(`POST /conversations 未知 agent → HTTP 200 code=${env.code} msg=${JSON.stringify(env.msg)}`)
  } else {
    bad('POST /conversations 未知 agent', `http=${r.status} ${JSON.stringify(env)}`)
  }

  // P2 §5.2 未知 MCP server 探测 → 40401（前端 HttpError 提示依据；不真的 spawn 任何进程）。
  const pr = await fetch(API + '/mcps/no-such-server/probe', { method: 'POST' })
  const pEnv = await pr.json()
  if (pr.status === 200 && pEnv.code === 40401) {
    ok(`POST /mcps/:id/probe 未知 server → HTTP 200 code=${pEnv.code}`)
  } else {
    bad('POST /mcps/:id/probe 未知 server', `http=${pr.status} ${JSON.stringify(pEnv)}`)
  }
}

// ——— 3. WS 帧契约 ———

async function checkWs() {
  console.log('· WS 实时通道帧契约（经 vite proxy ws:true）')
  const wsUrl = E2E_BASE.replace(/^http/, 'ws') + '/v1/forge/ws/events'
  const ws = new WebSocket(wsUrl)
  const opened = new Promise((resolve, reject) => {
    ws.onopen = () => resolve()
    ws.onerror = () => reject(new Error('WS 连接错误/升级失败'))
  })
  await withTimeout(opened, 8000, 'WS open')
  ok('WS 升级成功（proxy ws:true 生效）')

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

// ——— 主流程 ———

async function main() {
  console.log(`联调通路冒烟  base=${E2E_BASE}\n`)
  try {
    await checkRest()
    await checkBizError()
    await checkWs()
  } catch (err) {
    bad('未预期异常', err?.stack || String(err))
  }
  console.log(failed === 0 ? '\n✅ 通路全部 PASS' : `\n❌ ${failed} 项 FAIL`)
  process.exit(failed === 0 ? 0 : 1)
}

main()
