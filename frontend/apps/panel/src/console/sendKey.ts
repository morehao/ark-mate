/** 输入发送键档位（持久化 localStorage；聊天/新建两处共用）。 */
export type SendKeyMode = 'enter-send' | 'enter-newline'

const LS_KEY = 'forge.sendKey'

export function getSendKey(): SendKeyMode {
  try {
    return localStorage.getItem(LS_KEY) === 'enter-newline' ? 'enter-newline' : 'enter-send'
  } catch {
    return 'enter-send'
  }
}

export function toggleSendKey(cur: SendKeyMode): SendKeyMode {
  const next: SendKeyMode = cur === 'enter-send' ? 'enter-newline' : 'enter-send'
  try {
    localStorage.setItem(LS_KEY, next)
  } catch {
    /* 忽略持久化失败（内存态照样生效） */
  }
  return next
}

/** 提示行文案（随档位）。 */
export function sendKeyHint(mode: SendKeyMode): string {
  return mode === 'enter-send' ? 'Enter 发送 · Shift+Enter 换行' : 'Enter 换行 · ⌘/Ctrl+Enter 发送'
}

/** 换行键行为（防止默认行为需要时返回 true）。 */
export interface SendKeyEvt {
  key: string
  shiftKey: boolean
  metaKey: boolean
  ctrlKey: boolean
}

/** 计算某次按键是否「应发送并 preventDefault」：
 * - ⌘/Ctrl+Enter 恒发送（双档都支持，避免档位记忆负担）；
 * - enter-send：Enter（非 Shift）发送，Shift+Enter 换行；
 * - enter-newline：Enter 换行，Shift+Enter 发送（⌘/Ctrl 同上）。
 */
export function shouldSendKey(e: SendKeyEvt, mode: SendKeyMode): boolean {
  if (e.key !== 'Enter') return false
  const meta = e.metaKey || e.ctrlKey
  if (meta) return true
  return mode === 'enter-send' ? !e.shiftKey : e.shiftKey
}
