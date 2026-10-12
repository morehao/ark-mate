import type { CommandInfo } from '@ark-mate/types'

/**
 * 斜杠命令补全的**纯逻辑**（无 React、无 IO），便于单点读懂与复用。
 *
 * 补全只在「正在输入命令名」这一种上下文里出现：`/` 开头且**尚未出现空白**。
 * 一旦用户敲了空格，说明已进入参数区（`/review src/a.go`），此时补全必须让位——
 * 否则每敲一个参数都会弹候选，把输入框变成障碍。
 *
 * **命令名里可以有 `/`**：有的底座把命名空间写进名字（实测通告里有 `git/branch`、`git/commit-push`）。
 * 所以判定边界只能是**空白**，不能是 `/`——第一版按 `/` 截断，结果用户手打 `/git/` 时弹层当场消失
 * （真机验收抓到的，见 `.dsh/docs/notes/2026-09-24-panel-ui-acceptance.md`）。
 */

/**
 * 判断当前草稿是否处于补全上下文。
 * 返回待匹配前缀（可能是空串，表示只敲了 `/`）；`null` = 不补全。
 */
export function commandHintQuery(draft: string): string | null {
  if (!draft.startsWith('/')) return null
  const rest = draft.slice(1)
  // 任何空白（含换行）都表示已进入参数区或已另起一行。
  if (/\s/.test(rest)) return null
  return rest.toLowerCase()
}

/**
 * 按前缀筛选命令（大小写不敏感）。**保留底座给出的顺序**——那是它的展示意图，平台不重排；
 * 前缀为空时全部候选。无候选时返回空数组（调用方据此不渲染弹层）。
 */
export function matchCommands(commands: CommandInfo[] | undefined, query: string): CommandInfo[] {
  if (!commands || commands.length === 0) return []
  return commands.filter((c) => c.name.toLowerCase().startsWith(query))
}

/**
 * 选中某条命令后的草稿文本：`/name `（带尾随空格）。
 *
 * 尾随空格是刻意的：需要参数的命令（`hint` 非空）接着就能打字；不需要参数的命令多一个
 * 空格也无害。**不自动发送**——补全只帮用户把命令名写对，发不发由用户决定（Enter 在
 * 弹层打开时是"选中"，不是"发送"）。
 */
export function applyCommandHint(cmd: CommandInfo): string {
  return `/${cmd.name} `
}

/**
 * 弹层打开时的键盘导航：返回新的高亮下标（循环）。
 * `delta` 为 ±1；`total` 为候选数（0 → 恒为 0）。
 */
export function moveHintIndex(index: number, delta: number, total: number): number {
  if (total <= 0) return 0
  return (index + delta + total) % total
}
