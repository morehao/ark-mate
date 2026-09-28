import type { WsClientFrame, WsServerFrame } from '@ark/types'
import { getToken } from '@ark/api'

/**
 * WS 事件传输（P3 可靠性细节）：
 * - 浏览器原生 WebSocket；路径 = 同源 /v1/forge/ws/events（dev 走 vite 代理，prod 内嵌同源）
 * - 指数退避断线重连（300ms 起、封顶 8s）；重连后对全部订阅 agg 按 lastSeq 重新 attach
 *   （0 = 全量回放，>0 = 增量续传；服务端 EventsAfter 为开区间 → 权威收尾：重连重放
 *   不会重复本地已应用事件，seq 只前进不回退）
 * - 连接存活期间每 25s ping（服务端 60s 未 pong 会断连；pong 帧计入活跃度）
 * - **stalled 保险丝**：套接字 OPEN 但长时间无任何服务端帧（半开连接/黑洞：网络切换、路由
 *   失效、服务端僵尸连接）→ 主动 close 触发重连，重连后按各 agg lastSeq 重新 attach 裁决增量。
 *   健康连接每 25s ping → pong，正常阈值内不会误熔断。
 */

/** 无任何服务端帧（含 pong）超过该时长视为 stalled（须 > ping 周期 25s 且留裕量）。 */
export const STALL_MS = 70_000
/** 保险丝巡检周期。 */
export const STALL_CHECK_MS = 5_000

export class WsTransport {
  private ws: WebSocket | null = null
  private closedByUser = false
  private reconnectDelay = 300
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private pingTimer: ReturnType<typeof setInterval> | null = null
  private stallTimer: ReturnType<typeof setInterval> | null = null
  /** 最近一次收到服务端帧的时间（OPEN 即置位；stalled 判定依据）。 */
  private lastActivity = 0

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
    this.stopStallWatch()
    if (this.ws) {
      this.ws.onclose = null
      this.ws.close()
    }
    this.ws = null
  }

  private open(): void {
    if (this.closedByUser) return
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    // A6：后端启用单操作员令牌时，浏览器无法为 WS 握手设置请求头 → 用查询参数传递。
    const token = getToken()
    const url = `${proto}://${location.host}/v1/forge/ws/events${token ? `?token=${encodeURIComponent(token)}` : ''}`
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
      this.lastActivity = Date.now()
      this.startPing()
      this.startStallWatch()
      // 重连 resync 裁决：按各 agg 已确认水位重新 attach（服务端 EventsAfter 开区间 →
      // 权威收尾，仅补缺不重复）；水位为 0 的 agg 全量回放（冷挂载/起点已被淘汰）。
      for (const [aggId, since] of this.att) {
        this.rawSend({ action: 'attach', agg_id: aggId, since_seq: since })
      }
    }
    ws.onmessage = (ev) => {
      this.lastActivity = Date.now()
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
      this.stopStallWatch()
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

  /**
   * stalled 保险丝：OPEN 后持续无服务端帧 → 主动断开，交由 onclose 重连（重连即 resync）。
   * 触发时把退避重置为 300ms，尽快恢复；不置 closedByUser（保活语义）。
   */
  private startStallWatch(): void {
    this.stopStallWatch()
    this.stallTimer = setInterval(() => {
      const ws = this.ws
      if (!ws || ws.readyState !== WebSocket.OPEN) return
      if (Date.now() - this.lastActivity > STALL_MS) {
        console.warn('[ws] stalled：%dms 无服务端帧，主动重连（resync）', STALL_MS)
        this.reconnectDelay = 300
        ws.close() // onclose → scheduleReconnect
      }
    }, STALL_CHECK_MS)
  }

  private stopStallWatch(): void {
    if (this.stallTimer) clearInterval(this.stallTimer)
    this.stallTimer = null
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
