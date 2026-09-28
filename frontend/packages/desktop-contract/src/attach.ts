/**
 * WS 首帧（`attach`）形状的唯一真源——**与核心侧 `ctrws` 的解析对齐**（设计 §7.2）。
 *
 * 握手不带凭据：连接建立后必须在 `STREAM_ATTACH_DEADLINE_MS` 内把这一帧发出去，核心据此校验
 * `streamToken`（桌面态）或 `FORGE_TOKEN`（独立态带令牌时）；未按时发出或凭据不符 → 服务端
 * 主动关闭（1008）。这样令牌**不进 URL、不进浏览器历史、不进 argv/env/文件**。
 *
 * 浏览器态传 `null`：核心在"未启用令牌"时接受，在"启用了令牌"时拒绝——是否要求凭据由核心决定，
 * 客户端只如实上报"我有什么"。
 */
export type StreamAttachFrame = {
  action: 'attach'
  token: string | null
}

/**
 * 构造首帧。空串**不是**"没有凭据"的表达（那是 `null`），静默当无凭据会把配置错误变成
 * "事件流莫名其妙不通"，因此这里直接抛错。
 */
export function buildAttachFrame(token: string | null): StreamAttachFrame {
  if (token !== null && token.length === 0) {
    throw new Error('buildAttachFrame：空串令牌非法——没有凭据请传 null')
  }
  return { action: 'attach', token }
}
