/**
 * WS 首帧（`attach`）形状的唯一真源——**与核心侧 `ctrws` 的解析对齐**（设计 §7.2）。
 *
 * 握手不带凭据：连接建立后必须在 `STREAM_ATTACH_DEADLINE_MS` 内把这一帧发出去；未按时发出
 * 由服务端主动关闭（1008）。
 */
export type StreamAttachFrame = {
  action: 'attach'
}

/** 构造首帧（通道级 attach，无 agg_id）。 */
export function buildAttachFrame(): StreamAttachFrame {
  return { action: 'attach' }
}
