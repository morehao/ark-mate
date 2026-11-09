import { useSyncExternalStore } from 'react'

/**
 * 对话偏好（设置 · 对话一节）：本机 localStorage 偏好，不经后端、不改动会话数据。
 *
 * 三条偏好在本地持久化，通过 `useSyncExternalStore` 让消费方（Transcript / Composer）响应式读取：
 *
 *  - `toolDisplay`：对话过程展示方式。`compact`（简洁）= 只展开正在执行的那条，历史过程默认收起；
 *    `detailed`（详细）= 阶段内全部展开。
 *  - `queueSend`：队列发送方式。`queue`（排队）= 等这一轮 Agent 回答完再发；
 *    `immediate`（立即发送）= 直接插入当前对话引导 Agent（落到 Composer 的 `interrupt_then_prompt`）。
 *  - `showFileChanges`：显示文件修改过程。开启时在过程流里展示每次文件改动的 diff 详情；
 *    关闭时收起 diff（只留一行工具摘要）。
 *
 * 为什么不走后端：这三项是**本机呈现/发送偏好**，与账号、会话、底座都无关；写到后端只会让
 * 一个纯前端观感项变成跨端同步负担。localStorage 是它唯一需要的事实源。
 *
 * 词汇纪律（`TestNoConversationVocabulary`）：本文件与消费方**不得出现 `conversation`**——
 * 统一用 `flow` / `toolDisplay` / `queueSend`。
 */

type ToolDisplay = 'compact' | 'detailed'
type QueueSend = 'queue' | 'immediate'

interface FlowPrefs {
  toolDisplay: ToolDisplay
  queueSend: QueueSend
  showFileChanges: boolean
}

const STORAGE_KEY = 'mate.flowPrefs'

const DEFAULTS: FlowPrefs = {
  toolDisplay: 'compact',
  queueSend: 'queue',
  showFileChanges: true,
}

/** 选项表（设置面板渲染 + 文案）。每个选项带一句「会发生什么」的副说明。 */
export const TOOL_DISPLAY_OPTIONS: { value: ToolDisplay; label: string; hint: string }[] = [
  { value: 'compact', label: '简洁', hint: '只展示正在执行的工具，其余过程收起，可手动展开查看。' },
  { value: 'detailed', label: '详细', hint: '展示某一阶段内的全部工具调用记录。' },
]

export const QUEUE_SEND_OPTIONS: { value: QueueSend; label: string; hint: string }[] = [
  { value: 'queue', label: '排队', hint: '队列消息发出后，排队等待这一轮 Agent 回答完成之后再发出。' },
  { value: 'immediate', label: '立即发送', hint: '队列消息直接插入当前正在进行的对话，引导 Agent 回答；输入时可按「插入队列」临时改回排队。' },
]

/** 从 localStorage 读一份（解析失败/缺字段回落默认值——绝不因为一处坏值丢了整份偏好）。 */
function read(): FlowPrefs {
  if (typeof localStorage === 'undefined') return DEFAULTS
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return DEFAULTS
    const parsed = JSON.parse(raw) as Partial<FlowPrefs>
    return {
      toolDisplay: parsed.toolDisplay === 'detailed' ? 'detailed' : 'compact',
      queueSend: parsed.queueSend === 'immediate' ? 'immediate' : 'queue',
      showFileChanges: parsed.showFileChanges !== false,
    }
  } catch {
    return DEFAULTS
  }
}

// 单一内存快照：`useSyncExternalStore` 要求同一份数据在未变更时返回**同一个引用**，
// 否则每次渲染都判定"变了"会无限重渲。所有写入口都替换这个引用并通知订阅者。
let snapshot: FlowPrefs = read()
const listeners = new Set<() => void>()

function emit(): void {
  for (const l of listeners) l()
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

/** 写一份新偏好（合并 + 落库 + 通知）。 */
function write(next: FlowPrefs): void {
  snapshot = next
  if (typeof localStorage !== 'undefined') {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(next))
    } catch {
      // 忽略写失败（隐私模式等）：内存快照已更新，本会话内仍生效。
    }
  }
  emit()
}

/** 读取当前偏好（非响应式；供事件回调/渲染函数内一次性读取）。 */
export function getFlowPrefs(): FlowPrefs {
  return snapshot
}

export function setToolDisplay(toolDisplay: ToolDisplay): void {
  write({ ...snapshot, toolDisplay })
}

export function setQueueSend(queueSend: QueueSend): void {
  write({ ...snapshot, queueSend })
}

export function setShowFileChanges(showFileChanges: boolean): void {
  write({ ...snapshot, showFileChanges })
}

/** 响应式读取整份偏好（消费方在渲染里用）。 */
export function useFlowPrefs(): FlowPrefs {
  return useSyncExternalStore(subscribe, getFlowPrefs, () => DEFAULTS)
}
