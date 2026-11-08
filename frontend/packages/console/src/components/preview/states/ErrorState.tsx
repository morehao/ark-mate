// 状态页：加载失败。
// 展示层不做任何网络请求：仅对 message 做字符串识别，能抽到 http(s):// 时
// 额外单起一行「请求地址：<url>」，方便肉眼判断请求打到了哪个 origin/端口。
import { tokens } from '@ark-mate/ui'

/** 从失败信息里抽出首个 http(s):// URL（纯字符串识别，不做任何请求）。 */
function extractUrl(message: string): string | null {
  // 只取 URL 本身，遇到空白/全角标点/右括号/引号即止 —— 否则会把 URL 后面整句中文
  // （如「）：请求未拿到响应…」）一起吞进「请求地址」行（2026-11-08 实际踩到）。
  const m = message.match(/https?:\/\/[^\s：），。；、「」『』"'）\]]+/)
  return m ? m[0] : null
}

export function ErrorState({ message }: { message: string }) {
  const url = extractUrl(message)
  return (
    <div style={{ padding: 24, color: tokens.error, fontSize: 13, lineHeight: 1.8 }}>
      <div>预览加载失败：{message}</div>
      {url && (
        <div style={{ color: tokens.textSecondary, fontSize: 12, wordBreak: 'break-all' }}>
          请求地址：{url}
        </div>
      )}
    </div>
  )
}
