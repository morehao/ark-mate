/** 输入发送键的固定规则（聊天 / 新建两处共用）。 */

/**
 * 2026-11-05：发送键**档位切换**撤掉——「Shift+Enter 发送」这一档没有真实需求，
 * 开关与设置里那句说明都被用户判定为噪声。现在只有一套规则，也不再读写 localStorage；
 * 旧版留下的 `mate.sendKey` 值不再被读取，老用户自然回到 Enter 发送。
 */
export interface SendKeyEvt {
  key: string
  shiftKey: boolean
  metaKey: boolean
  ctrlKey: boolean
}

/** 计算某次按键是否「应发送并 preventDefault」：
 * - ⌘/Ctrl + Enter 恒发送（无论焦点在哪种输入框，直接送出的兜底）；
 * - Enter 发送；
 * - Shift + Enter 换行。
 */
export function shouldSendKey(e: SendKeyEvt): boolean {
  if (e.key !== 'Enter') return false
  if (e.metaKey || e.ctrlKey) return true
  return !e.shiftKey
}
