/** 输入发送键档位（持久化 localStorage；聊天/新建两处共用）。 */
export type SendKeyMode = 'enter-send' | 'enter-newline'

const LS_KEY = 'mate.sendKey'

export function getSendKey(): SendKeyMode {
  try {
    return localStorage.getItem(LS_KEY) === 'enter-newline' ? 'enter-newline' : 'enter-send'
  } catch {
    return 'enter-send'
  }
}

/**
 * 写入发送键档位。
 *
 * 2026-11-05：输入卡右下角那条「Enter 发送 · Shift+Enter 换行 ⇄」撤掉了（用户反馈是噪声），
 * 档位开关搬到「设置 · 通用 · 键盘操作」。所以这里从 `toggleSendKey` 改成**显式写入**——
 * 开关知道自己要写哪一档，不再依赖"读当前值再取反"。
 * 输入区每次按键都现读 `getSendKey()`（不再缓存进 state），设置里改完立刻生效。
 */
export function setSendKey(mode: SendKeyMode): void {
  try {
    localStorage.setItem(LS_KEY, mode)
  } catch {
    /* 忽略持久化失败（内存态照样生效） */
  }
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
