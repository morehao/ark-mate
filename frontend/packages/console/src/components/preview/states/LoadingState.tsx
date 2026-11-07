// 状态页：加载中。
import { tokens } from '@ark-mate/ui'

export function LoadingState({ text = '正在加载…' }: { text?: string }) {
  return (
    <div style={{ padding: 24, color: tokens.textSecondary, fontSize: 13, textAlign: 'center' }}>{text}</div>
  )
}
