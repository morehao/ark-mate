import type { BusEvent } from './events'

// ——— WS 帧（对照 backend ws.go 契约；浏览器原生 WebSocket，JSON 帧） ———

// 客户端 → 服务端
//
// M1-1 起 `attach` 分两级语义：
//   - **通道级**（无 `agg_id`，带 `token`）：连接建立后的首帧，交付 stream 面凭据。
//     服务端 5s 内未收到即关闭（1008）；桌面态 token 必填、独立态可空。
//     形状唯一真源是 `@ark-mate/desktop-contract` 的 `buildAttachFrame`。
//   - **订阅级**（带 `agg_id`）：既有的按会话订阅 / 断线续传，不变。
export type WsClientFrame =
  | { action: 'attach'; token?: string | null; agg_id?: string; since_seq?: number | null }
  | { action: 'detach'; agg_id: string }
  | { action: 'ping' }

// 服务端 → 客户端
export type WsServerFrame =
  | { type: 'snapshot'; agg_id: string; event_seq: number; state: Record<string, unknown> }
  | { type: 'event'; agg_id: string; event_seq: number; event: BusEvent }
  | { type: 'gap'; agg_id: string; event_seq: number; from_seq: number }
  | { type: 'detached'; agg_id: string; reason?: string }
  | { type: 'pong' }
