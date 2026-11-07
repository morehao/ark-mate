// 状态页：加载失败。
import { tokens } from '@ark-mate/ui'

export function ErrorState({ message }: { message: string }) {
  return (
    <div style={{ padding: 24, color: tokens.error, fontSize: 13, lineHeight: 1.8 }}>
      <div>预览加载失败：{message}</div>
    </div>
  )
}
