/**
 * 轮次过程的**旁白**（S2 / ADR-6 的前端一半）：把服务端给的 `run.status` 翻译成一句人话 + 几个
 * 可判定的属性（忙不忙、能不能停、要不要给「重发」入口）。
 *
 * 为什么必须是纯函数且单独一个文件：
 *   - 它是**唯一**的旁白出处——头栏、侧栏会话行都读它，两处各写一份 `switch` 就是"同一个状态
 *     两种说法"的老问题；
 *   - 纯函数可以在 Node 侧测（本仓不引前端测试框架，但这不妨碍把它写成可测的样子）。
 *
 * 一条纪律：**不猜**。服务端没发过的阶段不在这里发明（例如 `retrying` / `compacting` 今天没有
 * 生产者，映射表里照样留着它们，因为契约里有；但界面不会因为"看起来该重试了"而自己切过去）。
 */
import type { RunPhase } from '@ark-mate/types'

/** 旁白的气质：'run' 在跑（可停）/ 'wait' 在等人（**不可**停）/ 'idle' 收口 / 'interrupted' 可重发。 */
export type RunTone = 'run' | 'wait' | 'idle' | 'interrupted'

export interface RunNarration {
  tone: RunTone
  /** 头栏/侧栏显示的那句话（空串 = 这一档不需要旁白）。 */
  label: string
  /** 服务端说这一轮还在进行（含等待）——`busy` 的唯一来源。 */
  busy: boolean
  /** 能不能点「停止」：**等待态不给**（那时点停止等于把用户正要答的那一轮扔掉）。 */
  canStop: boolean
  /** 要不要给「重试/重发」入口（只有恢复收口这一档给）。 */
  canRetry: boolean
}

const IDLE: RunNarration = { tone: 'idle', label: '', busy: false, canStop: false, canRetry: false }

/**
 * 把 `run` 映射成旁白。`undefined`（没有过过程事件）= 什么都不说，也不当作忙。
 *
 * 文案口径（spec §5.2 + 原型）：
 * - `tool_executing` 带工具名时说「正在执行 Bash」（Q6 选①：只说"正在执行工具"等于把最有用的
 *   那半藏起来）；
 * - `waiting_for_permission` → 「等待你确认授权」且**不给停止**——原型里那一格明确没有按钮；
 * - `interrupted` → 「上一轮因进程重启中断，可重发」，**不说"失败"**（那是底座的结论，
 *   平台只知道自己被杀了）。
 */
export function runNarration(run: RunPhase | undefined): RunNarration {
  if (!run) return IDLE
  switch (run.status) {
    case 'preparing':
      return { tone: 'run', label: '正在准备', busy: true, canStop: true, canRetry: false }
    case 'model_requesting':
      return { tone: 'run', label: '正在请求模型', busy: true, canStop: true, canRetry: false }
    case 'model_streaming':
      return { tone: 'run', label: '正在输出', busy: true, canStop: true, canRetry: false }
    case 'tool_executing':
      return {
        tone: 'run',
        label: run.tool_name ? `正在执行 ${run.tool_name}` : '正在执行工具',
        busy: true,
        canStop: true,
        canRetry: false,
      }
    case 'waiting_for_permission':
      return { tone: 'wait', label: '等待你确认授权', busy: true, canStop: false, canRetry: false }
    case 'waiting_for_user':
      return { tone: 'wait', label: '等待你回答', busy: true, canStop: false, canRetry: false }
    case 'retrying':
      return { tone: 'run', label: '正在重试', busy: true, canStop: true, canRetry: false }
    case 'compacting':
      return { tone: 'run', label: '正在压缩上下文', busy: true, canStop: true, canRetry: false }
    case 'cancelling':
      return { tone: 'run', label: '正在停止', busy: true, canStop: false, canRetry: false }
    case 'interrupted':
      return {
        tone: 'interrupted',
        label: '上一轮因进程重启中断，可重发',
        busy: false,
        canStop: false,
        canRetry: true,
      }
    case 'idle':
      return IDLE
    default:
      // 契约外的取值（后端加了新档、前端还没跟）：**不猜**语义，只把原值透出来。
      return { tone: 'run', label: String(run.status), busy: true, canStop: true, canRetry: false }
  }
}
