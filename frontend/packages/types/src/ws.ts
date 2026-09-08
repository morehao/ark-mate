import type { BusEvent } from './events'

// ——— WS 帧（对照 backend ws.go 契约；浏览器原生 WebSocket，JSON 帧） ———

// 客户端 → 服务端
export type WsClientFrame =
  | { action: 'attach'; agg_id: string; since_seq?: number | null }
  | { action: 'detach'; agg_id: string }
  | { action: 'ping' }

// 服务端 → 客户端
export type WsServerFrame =
  | { type: 'snapshot'; agg_id: string; event_seq: number; state: Record<string, unknown> }
  | { type: 'event'; agg_id: string; event_seq: number; event: BusEvent }
  | { type: 'gap'; agg_id: string; event_seq: number; from_seq: number }
  | { type: 'detached'; agg_id: string; reason?: string }
  | { type: 'pong' }
