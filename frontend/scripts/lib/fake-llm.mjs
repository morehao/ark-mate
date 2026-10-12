/**
 * 假 LLM（零额度、零凭证）：Anthropic `/v1/messages`（流式）+ `/v1/models` + OpenAI 面兜底。
 *
 * 为什么抽成 lib：它有两个消费方，而且都是**桌面态真起底座**的验收——
 *   1. `desktop-skill-projection-acceptance.mjs`（能力投放：skill 有没有进模型请求体）；
 *   2. `desktop-no-host-acceptance.mjs`（不依赖宿主机环境：最小 PATH + 诱饵下真跑一轮并重启续聊）。
 * 两份复制会漂移，而这类漂移是**静默**的（一边改了回复文本，另一边还在断言旧字符串）。
 *
 * 设计上刻意保持"只回答一句话"：
 *   - 它**不是**模型仿真器。断言要的是"请求体里有什么"（`captured`）与"链路有没有真跑起来"，
 *     不是回复质量。
 *   - 因此它**发不出 `tool_use`**，也就触发不了权限卡/提问卡——那属产品链路完备性，
 *     不在本 lib 的范围内（见 `.dsh/docs/plans/2026-10-10-no-host-dependency-plan.md` §5）。
 *
 * 端口由调用方给（各脚本用固定端口：端口被占时现象明确，比随机端口好排查）。
 *
 * 用法：
 *   const llm = await startFakeLLM({ port: 10078 })
 *   // dsh 侧：DEEPSEEK_BASE_URL=http://127.0.0.1:<port>（核心按 M2 的 provider 注入读它）
 *   // 原生配置目录侧：providers.*.settings.baseURL 指向同一端口
 *   llm.captured   // 收到的每个请求 { path, body, raw }（按到达顺序）
 *   llm.close()    // 收尾（关监听；进程退出前调用）
 */
import http from 'node:http'

/** 假 LLM 的固定回复文本。断言方 import 它，避免两边各写一份字符串。 */
export const FAKE_LLM_REPLY = 'PROBE-REPLY-OK'

/**
 * 起一个假 LLM。返回 `{ server, captured, port, reply, close }`。
 *
 * `server` 原样返回是给老调用方用的（`llm.server.close()`），新代码用 `close()`。
 */
export function startFakeLLM({ port, reply = FAKE_LLM_REPLY } = {}) {
  if (!port) throw new Error('startFakeLLM: 必须给 port（本仓各脚本用固定端口）')
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
          delta: { type: 'text_delta', text: reply },
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
      res.end(JSON.stringify({ id: 'chatcmpl-probe', choices: [{ message: { role: 'assistant', content: reply } }] }))
    })
  })
  return new Promise((resolve) => {
    server.listen(port, '127.0.0.1', () =>
      resolve({
        server,
        captured,
        port,
        reply,
        close: () => new Promise((done) => server.close(() => done())),
      }),
    )
  })
}
