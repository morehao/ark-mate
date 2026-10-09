#!/usr/bin/env node
/**
 * 真会话端到端联调：REST 创建会话（真实 runtime 子进程）→ WS attach 续传 → 收流至终态。
 * 需要：vite dev（默认 :5173，/v1 代理 127.0.0.1:10001）、真实 runtime + 模型额度。
 *
 * 用法：
 *   node scripts/e2e-live-turn.mjs <agent_id> "<prompt>"
 *   E2E_BASE=http://localhost:5173 node scripts/e2e-live-turn.mjs agent-opencode "请只回复两个字：明白"
 *
 * 判定：收到终态帧（turn.ended / turn.error / run.cancelled）即 PASS；
 * 若在超时前未收尾则 POST /cancel 并继续等终态。退出码 0=通路可用。
 */
import { buildAttachFrame } from '@ark/desktop-contract'

const E2E_BASE = process.env.E2E_BASE || 'http://localhost:5173'
const API = E2E_BASE + '/v1/mate'
// 后端启用凭据时用 MATE_SMOKE_TOKEN 传入：REST 走 Authorization 头，
// WS 走带内首帧 attach（M1-1 起令牌不进 URL）。
const TOKEN = process.env.MATE_SMOKE_TOKEN || ''
const AUTH = TOKEN ? { authorization: `Bearer ${TOKEN}` } : {}

const [, , AGENT_ID = '', PROMPT = ''] = process.argv
if (!AGENT_ID || !PROMPT) {
  console.error('用法: node scripts/e2e-live-turn.mjs <agent_id> "<prompt>"')
  process.exit(2)
}
const WAIT_TERMINAL_MS = Number(process.env.E2E_TURN_TIMEOUT_MS || 90_000)
const TAIL_MS = 2000 // 终态帧后多收一小段，观察是否有后继事件

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function main() {
  console.log(`真会话 e2e  agent=${AGENT_ID} prompt=${JSON.stringify(PROMPT)} base=${E2E_BASE}\n`)

  // 1) REST 创建（真实 dial + session/new；带 prompt → 首轮异步开跑）
  console.log('· POST /v1/mate/conversations …')
  const r = await fetch(API + '/conversations', {
    method: 'POST',
    headers: { 'content-type': 'application/json', ...AUTH },
    body: JSON.stringify({ agent_id: AGENT_ID, prompt: PROMPT }),
  })
  const env = await r.json()
  if (env.code !== 0) {
    console.error(`✘ 创建失败 code=${env.code} msg=${env.msg}`)
    process.exit(1)
  }
  const { id, event_seq, status } = env.data
  console.log(`  ✔ 会话 ${id} status=${status} 水位 event_seq=${event_seq}`)

  // 2) WS attach（since_seq = 创建返回水位 → 只收首轮增量）
  const wsUrl = E2E_BASE.replace(/^http/, 'ws') + '/v1/mate/ws/events'
  const ws = new WebSocket(wsUrl)
  await new Promise((resolve, reject) => {
    ws.onopen = resolve
    ws.onerror = () => reject(new Error('WS 升级失败'))
  })
  // 首帧：通道级 attach（凭据）；随后才是订阅帧。
  ws.send(JSON.stringify(buildAttachFrame(TOKEN || null)))
  console.log('· WS 已连接，attach …')
  ws.send(JSON.stringify({ action: 'attach', agg_id: id, since_seq: event_seq }))

  const terminal = new Set(['turn.ended', 'turn.error', 'run.cancelled'])
  const frames = []
  let termFrame = null
  let snapshotSeq = null

  const waitTerminal = new Promise((resolve) => {
    const timer = setTimeout(() => resolve('timeout'), WAIT_TERMINAL_MS)
    const onMsg = (ev) => {
      let f
      try {
        f = JSON.parse(String(ev.data))
      } catch {
        return
      }
      frames.push(f)
      if (f.type === 'snapshot') snapshotSeq = f.event_seq
      if (f.type === 'event') {
        const t = f.event?.type
        const p = f.event?.payload ?? {}
        const brief =
          t === 'session.message' || t === 'session.thought' || t === 'session.user_message'
            ? String(p.text ?? '').slice(0, 80)
            : t === 'session.tool_call' || t === 'session.tool_update'
              ? `${p.title ?? ''} [${p.status ?? p.tool_call_id ?? ''}]`
              : t === 'session.usage'
                ? `in=${p.input_tokens ?? '?'} out=${p.output_tokens ?? '?'}`
                : ''
        console.log(`  [evt #${f.event.seq}] ${t} ${brief}`.trimEnd())
        if (terminal.has(t) && !termFrame) {
          termFrame = { t, payload: p }
          clearTimeout(timer)
          setTimeout(resolve, TAIL_MS, 'terminal')
        }
      }
    }
    ws.addEventListener('message', onMsg)
  })
  const how = await waitTerminal
  ws.send(JSON.stringify({ action: 'detach', agg_id: id }))
  ws.close()

  // 3) 判定
  console.log('')
  if (termFrame) {
    console.log(`✔ 收到终态 ${termFrame.t} payload=${JSON.stringify(termFrame.payload)}`)
    const gotMid = frames.some(
      (f) => f.type === 'event' && ['session.message', 'session.thought', 'session.tool_call'].includes(f.event.type),
    )
    const seqs = frames.filter((f) => f.type === 'event').map((f) => f.event.seq)
    console.log(`✔ 事件 ${seqs.length} 帧 seq=${seqs.join(',')}${snapshotSeq != null ? `（snapshot水位=${snapshotSeq}）` : ''}`)
    if (termFrame.t === 'turn.error') {
      console.log('ℹ turn.error 收尾：通路本身可用，但 runtime 侧未产出正常回答')
    } else if (!gotMid) {
      console.log('ℹ 终态前无流式中段事件（可能瞬时完成/取消）')
    }
    process.exit(0)
  }
  console.error(`✘ ${WAIT_TERMINAL_MS}ms 未收尾（frames=${frames.length}），通路异常`)
  process.exit(1)
}

main().catch((e) => {
  console.error('✘ 未预期异常:', e?.stack || e)
  process.exit(1)
})
