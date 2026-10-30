import type { BusEvent } from './events'

// ——— WS 帧（对照 backend ws.go 契约；浏览器原生 WebSocket，JSON 帧） ———

// 客户端 → 服务端
//
// `attach` 分两级语义：
//   - **通道级**（无 `agg_id`）：连接建立后的首帧（协议形状），服务端 5s 内未收到即关闭（1008）。
//     形状唯一真源是 `@ark-mate/desktop-contract` 的 `buildAttachFrame`。
//   - **订阅级**（带 `agg_id`）：既有的按会话订阅 / 断线续传，不变。
export type WsClientFrame =
  | { action: 'attach'; agg_id?: string; since_seq?: number | null }
  | { action: 'detach'; agg_id: string }
  | { action: 'ping' }

// 服务端 → 客户端
export type WsServerFrame =
  | { type: 'snapshot'; agg_id: string; event_seq: number; state: Record<string, unknown> }
  | { type: 'event'; agg_id: string; event_seq: number; event: BusEvent }
  | { type: 'gap'; agg_id: string; event_seq: number; from_seq: number }
  | { type: 'detached'; agg_id: string; reason?: string }
  | { type: 'pong' }
  /**
   * 历史回放**开始**（ADR-13）：`event_seq` = 本次回放从哪个位次起（`-1` = 从该会话最早一条）。
   *
   * 这两个是**帧 type，不是域事件**：它们不进 `events` 表、不进 transcript、不进 `Ev` 联合类型
   * （`TestReplayFramesBracketHistory` 反过来钉这一条）。它们描述的是**这次连接正在做什么**，
   * 不是会话身上发生过什么——同一次回放换成两个客户端就是两段不同的区间。
   *
   * 有了它，加载态就不再靠 12 秒超时猜：`replay_begin` 进、`replay_end` 出。
   */
  | { type: 'replay_begin'; agg_id: string; event_seq: number }
  /** 历史回放**结束**：`event_seq` = 回放到的位次（也是客户端的续传游标）。 */
  | { type: 'replay_end'; agg_id: string; event_seq: number }
