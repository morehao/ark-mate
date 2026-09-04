import type { WsClientFrame, WsServerFrame } from '@ark/types'

/**
 * WS 事件传输：
 * - 浏览器原生 WebSocket；路径 = 同源 /v1/forge/ws/events（dev 走 vite 代理，prod 内嵌同源）
 * - 指数退避断线重连（300ms 起、封顶 8s）；重连后对全部订阅 agg 按 lastSeq 重新 attach
 *   （0 = 全量回放，>0 = 增量续传；服务端 EventsAfter 为开区间，见 ws.go）
 * - 连接存活期间每 25s ping（服务端 60s 未 pong 会断连）
 */
export class WsTransport {
  private ws: WebSocket | null = null
  private closedByUser = false
  private reconnectDelay = 300
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private pingTimer: ReturnType<typeof setInterval> | null = null

  /** aggId → 已确认水位（last applied event_seq；未知=0 → 全量回放） */
  private att = new Map<string, number>()

  constructor(
    private onFrame: (f: WsServerFrame) => void,
    private onState: (s: 'connecting' | 'open' | 'closed') => void,
  ) {}

  /** 订阅一个会话流：sinceSeq 为空时按 lastSeq（无记录则 0 = 全量）。 */
  attach(aggId: string, sinceSeq?: number): void {
    const since = sinceSeq ?? this.att.get(aggId) ?? 0
    this.att.set(aggId, since)
    this.send({ action: 'attach', agg_id: aggId, since_seq: since })
  }

  detach(aggId: string): void {
    this.att.delete(aggId)
    this.send({ action: 'detach', agg_id: aggId })
  }

  /** 收到 seq 后更新水位（重连续传游标）。 */
  ackSeq(aggId: string, seq: number): void {
    const cur = this.att.get(aggId)
    if (cur === undefined || seq > cur) this.att.set(aggId, seq)
  }

  isAttached(aggId: string): boolean {
    return this.att.has(aggId)
  }

  connect(): void {
    this.closedByUser = false
    if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) {
      return // 幂等
    }
    this.open()
  }

  close(): void {
    this.closedByUser = true
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    if (this.pingTimer) clearInterval(this.pingTimer)
    if (this.ws) {
      this.ws.onclose = null
      this.ws.close()
    }
    this.ws = null
  }

  private open(): void {
    if (this.closedByUser) return
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const url = `${proto}://${location.host}/v1/forge/ws/events`
    this.onState('connecting')
    let ws: WebSocket
    try {
      ws = new WebSocket(url)
    } catch {
      this.scheduleReconnect()
      return
    }
    this.ws = ws
    ws.onopen = () => {
      this.onState('open')
      this.reconnectDelay = 300
      this.startPing()
      // 全量重新订阅（重连后服务端按各自水位重放）
      for (const [aggId, since] of this.att) {
        this.rawSend({ action: 'attach', agg_id: aggId, since_seq: since })
      }
    }
    ws.onmessage = (ev) => {
      try {
        const f = JSON.parse(String(ev.data)) as WsServerFrame
        this.onFrame(f)
      } catch {
        // 忽略坏帧
      }
    }
    ws.onerror = () => {
      /* onclose 收尾 */
    }
    ws.onclose = () => {
      this.stopPing()
      this.onState('closed')
      if (!this.closedByUser) this.scheduleReconnect()
    }
  }

  private scheduleReconnect(): void {
    if (this.closedByUser || this.reconnectTimer) return
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      this.open()
    }, this.reconnectDelay)
    this.reconnectDelay = Math.min(this.reconnectDelay * 2, 8000)
  }

  private startPing(): void {
    this.stopPing()
    this.pingTimer = setInterval(() => {
      this.rawSend({ action: 'ping' })
    }, 25000)
  }

  private stopPing(): void {
    if (this.pingTimer) clearInterval(this.pingTimer)
    this.pingTimer = null
  }

  private send(f: WsClientFrame): void {
    if (this.ws?.readyState === WebSocket.OPEN) this.rawSend(f)
  }

  private rawSend(f: WsClientFrame): void {
    try {
      this.ws?.send(JSON.stringify(f))
    } catch {
      /* 连接刚断：重连后统一重订阅 */
    }
  }
}
