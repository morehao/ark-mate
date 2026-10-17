import { create } from 'zustand'
import {
  HttpError,
  cancelSession,
  cancelQueuedInput as apiCancelQueuedInput,
  sessionHistory,
  createSession,
  createWorkspace as apiCreateWorkspace,
  deleteWorkspace as apiDeleteWorkspace,
  listSessions,
  listModelCatalog,
  listQueuedInputs,
  listWorkspaces,
  moveWorkspace as apiMoveWorkspace,
  renameWorkspace as apiRenameWorkspace,
  promptSession,
  resolveElicitation,
  resolvePermission,
  setToken as persistToken,
  getToken,
  getMeta,
  getRuntimeDiagnostics,
  toErrorMessage,
  updateSession,
} from '@ark-mate/api'
import type {
  BusEvent,
  CommandInfo,
  ElicitationFormSchema,
  PermissionOption,
  ConfigOption,
  SessionSummary,
  ModelCatalogResp,
  ModelRef,
  QueuedInput,
  RunPhase,
  RuntimeDiagnostics,
  RuntimeInfo,
  StreamEntry,
  Workspace,
  WorkspaceListResp,
  WsServerFrame,
} from '@ark-mate/types'
import { BizCode, Ev } from '@ark-mate/types'
import { resolveStreamConfig, type StreamConfig } from './carrier'
import { WsTransport } from './transport/ws'
import { olderCursor, prependOlder } from './console/transcriptView'
import { setExpanded } from './console/navView'
import {
  cancelNotice,
  cancelTone,
  deliveryLabel,
  queuedFromResp,
  transitionQueue,
  type DeliveryChoice,
} from './console/queueView'

export type WsState = 'idle' | 'connecting' | 'open' | 'closed'

export interface SessionView {
  entries: StreamEntry[]
  streaming: boolean
  lastSeq: number
  /** 冷挂载 hydration：activate 全量 attach（snapshot 到达）前为 true（P3 加载四态）。 */
  hydrating: boolean
  /** §6 busy input 队列：事件的投影（input_queued 建项 / delivered|cancelled 出队）。 */
  queue: QueuedInput[]
  /**
   * 本视图已应用的**队列事件**计数（不受普通消息事件推进）。
   * 冷挂载要用 GET /inputs 查当前态补齐队列，而 HTTP 有在途窗口：若期间来了队列事件，
   * 服务端读到的可能已是旧状态——用这个计数当锚点，动过就以事件为准，避免把已撤下的项复活。
   */
  queueSeq: number
  /**
   * 底座自报的可用斜杠命令（`session.commands` 全量事件的投影；ADR-17 命令发现面）。
   *
   * `undefined` = 本会话**从未**收到过通告（dsh 的 ACP 面不含命令，永远如此）——
   * 面板据此不显示候选，且**不得**说成"你还没配命令"；
   * `[]` = 底座通告了"当前没有命令"（同样是事实，要清掉既有候选）。
   * 会话重开时由事件全量回放重建，故没有 REST 读端点。
   */
  commands?: CommandInfo[]
  /**
   * 当前轮次的**过程状态**（`session.run.status` 事件的投影，ADR-6）。
   *
   * 与 `sessions[]` 行上的 `run`（REST 读面）**同源**：这里是实时的、那里是刷新时的快照。
   * 界面优先读这里（它跟着事件流走），没有再回落到列表行——两处都缺时**什么都不说**，
   * 绝不退回"猜"（`serverBusy` 那套倒推已经删了，见 archtest 的
   * `TestNoClientSideRunInference`）。
   */
  run?: RunPhase
}

const emptyView = (): SessionView => ({
  entries: [],
  streaming: false,
  // 位次是**会话内**的、0 是合法位次（v21）：初值必须是 -1，否则 `applyEvent` 的
  // `e.seq <= lastSeq` 去重会把该会话的**第 0 条**（`session.created`/首条用户消息）丢掉。
  lastSeq: -1,
  hydrating: false,
  queue: [],
  queueSeq: 0,
})

/** 权限终局事件载荷 → 卡片决策摘要（reason/allowed/rejected/always 组合出可读文案）。 */
function permissionResolvedLabel(p: Record<string, unknown> | undefined): string {
  const reason = String(p?.reason ?? '')
  const always = p?.always === true
  if (reason === 'timeout') return '超时自动拒绝'
  if (reason === 'cancel') return '已取消'
  if (p?.allowed === true) return always ? '允许 · 始终' : '允许 · 本次'
  if (p?.rejected === true) return always ? '拒绝 · 始终' : '拒绝 · 本次'
  return '已结束'
}

/** 提问终局事件载荷 → 卡片决策摘要。 */
function elicitationResolvedLabel(p: Record<string, unknown> | undefined): string {
  const reason = String(p?.reason ?? '')
  const action = String(p?.action ?? '')
  // 底座告知 URL 模式提问已在它那侧完成（elicitation/complete）→ 不是「用户提交了内容」。
  if (reason === 'complete') return '链接流程已完成'
  if (action === 'accept') return '已提交'
  if (action === 'decline') return '已拒绝'
  if (reason === 'timeout') return '超时自动取消'
  return '已取消'
}

let uid = 0
const nextId = (): string => `e${++uid}`

/**
 * 事件应用（纯函数）：user/assistant/thought 增量合并、tool 卡更新、终态收流。
 * 语义与旧 useReducer 版一致（M1 DoD 验收过），原样迁移到 zustand。
 */
// 终态事件：到达后服务端会话不再 busy，触发一次会话列表刷新，让「服务端忙水位」状态收敛。
const TERMINAL_TYPES = new Set<string>([Ev.RunEnded, Ev.RunError, Ev.RunCancelled])

export function applyEvent(v: SessionView, e: BusEvent): SessionView {
  // seq 幂等去重：事件只前进不回退（§4.5「按 seq 去重」）。快照/重放为开区间、服务端不重发，
  // 此守卫兜底重复 attach / 竞态下的重复投递，避免重连把同一段文本再拼一遍。
  if (e.seq <= v.lastSeq) return v
  const entries = v.entries
  const lastSeq = e.seq
  const text = String(e.payload?.text ?? '')
  const msgId = e.payload?.msg_id !== undefined && e.payload.msg_id !== null ? String(e.payload.msg_id) : ''

  const push = (kind: StreamEntry['kind'], text_?: string, extra?: Partial<StreamEntry>): StreamEntry[] =>
    [...entries, { kind, id: nextId(), text: text_, seq: e.seq, ...extra }]

  const appendToTail = (kind: StreamEntry['kind'], text_: string): StreamEntry[] => {
    const tail = entries[entries.length - 1]
    if (tail && tail.kind === kind && typeof tail.text === 'string') {
      const next = [...entries]
      next[next.length - 1] = { ...tail, text: tail.text + text_ }
      return next
    }
    return push(kind, text_)
  }

  // 终态/取消收敛：把未决的交互卡（权限/结构化提问）标记为已结束（不再显示待决按钮）。
  // 覆盖「会话被取消/结束但仍挂着卡」的边缘（如超时后端已收敛、底座继续产生末尾终态）。
  const settleCards = (list: StreamEntry[], label: string): StreamEntry[] =>
    list.map((en): StreamEntry => {
      if (en.resolved) return en
      if (en.kind === 'permission' || en.kind === 'elicitation') return { ...en, resolved: true, decision: label }
      return en
    })

  // 文本段归并：带 msg_id（ACP unstable messageId）时并入同 kind、同 msgId 的既有段
  // （允许跨工具穿插，决策 v5①）；无 msg_id 回退「同 kind 尾拼接」（老行为）。
  const mergeText = (kind: StreamEntry['kind'], text_: string): StreamEntry[] => {
    if (msgId) {
      for (let i = entries.length - 1; i >= 0; i--) {
        const en = entries[i]
        if (en.kind === kind && en.msgId === msgId && typeof en.text === 'string') {
          const next = [...entries]
          next[i] = { ...en, text: en.text + text_ }
          return next
        }
      }
      return push(kind, text_, { msgId })
    }
    return appendToTail(kind, text_)
  }

  switch (e.type) {
    case Ev.UserMessage:
      return { ...v, entries: mergeText('user', text), lastSeq }
    case Ev.Message:
      return { ...v, entries: mergeText('assistant', text), lastSeq }
    case Ev.Thought:
      return { ...v, entries: mergeText('thought', text), lastSeq }
    case Ev.ToolCall: {
      const id = String(e.payload?.tool_call_id ?? '')
      const title = String(e.payload?.title ?? id)
      const status = String(e.payload?.status ?? '')
      return { ...v, entries: push('tool', undefined, { toolId: id, title, status }), lastSeq }
    }
    case Ev.ToolUpdate: {
      const id = String(e.payload?.tool_call_id ?? '')
      return {
        ...v,
        entries: entries.map((en) =>
          en.kind === 'tool' && en.toolId === id
            ? {
                ...en,
                title: String(e.payload?.title ?? en.title ?? id),
                status: String(e.payload?.status ?? en.status ?? ''),
              }
            : en,
        ),
        lastSeq,
      }
    }
    case Ev.SessionResumed: {
      // P3 会话可移植：进程重启/跨 runtime 后会话按需恢复（reason=restart 原生会话续接 /
      // handoff 历史摘要重灌）。以系统提示入流，避免用户误以为会话被「重新开始」。
      const reason = String(e.payload?.reason ?? '')
      const runtime = String(e.payload?.runtime ?? '')
      // generation（M3-6）：会话在轮次边界迁到了新的能力代际（定义被改过）。
      // 与"进程重启后原生续接"必须分开说——用户改完定义最关心的正是"到底生效了没有"。
      const label =
        reason === 'handoff'
          ? '会话已恢复（历史摘要重灌）'
          : reason === 'generation'
            ? '会话已用上新的能力（下一轮生效）'
            : '会话已恢复（原生会话续接）'
      return {
        ...v,
        entries: push('system', undefined, {
          tone: 'info',
          text: runtime ? `${label} · ${runtime}` : label,
        }),
        lastSeq,
      }
    }
    case Ev.RunStatus: {
      // 过程状态：整块替换（服务端每次给的都是那一刻的完整状态，不做增量合并）。
      // 不做任何本地推断——包括"看起来该结束了"。
      const status = String(e.payload?.status ?? '')
      if (!status) return { ...v, lastSeq }
      return {
        ...v,
        run: {
          run_id: e.payload?.run_id ? String(e.payload.run_id) : undefined,
          status: status as RunPhase['status'],
          phase: e.payload?.phase ? String(e.payload.phase) : undefined,
          detail: e.payload?.detail ? String(e.payload.detail) : undefined,
          tool_call_id: e.payload?.tool_call_id ? String(e.payload.tool_call_id) : undefined,
          tool_name: e.payload?.tool_name ? String(e.payload.tool_name) : undefined,
          waiting_for: e.payload?.waiting_for ? String(e.payload.waiting_for) : undefined,
          reason: e.payload?.reason ? String(e.payload.reason) : undefined,
          at: e.payload?.at ? String(e.payload.at) : undefined,
          seq: e.seq,
        },
        lastSeq,
      }
    }
    case Ev.RunEnded:
      return {
        ...v,
        entries: settleCards(
          push('system', undefined, {
            tone: 'ok',
            text: `完成 · ${String(e.payload?.stop_reason ?? '')}`.replace(/ · $/, ''),
          }),
          '已结束',
        ),
        streaming: false,
        lastSeq,
      }
    case Ev.RunError: {
      // A3：失败事件可能携带 runtime stderr 诊断尾巴（数组；非数组/空则忽略）。
      const rawTail = e.payload?.stderr_tail
      const stderrTail = Array.isArray(rawTail) ? rawTail.map((l) => String(l)) : undefined
      return {
        ...v,
        entries: settleCards(
          push('system', undefined, {
            tone: 'err',
            text: `失败 [${String(e.payload?.class ?? '')}/${String(e.payload?.code ?? '')}] ${String(
              e.payload?.details ?? '',
            )}`,
            stderrTail: stderrTail && stderrTail.length > 0 ? stderrTail : undefined,
          }),
          '已结束',
        ),
        streaming: false,
        lastSeq,
      }
    }
    case Ev.RunCancelled:
      return {
        ...v,
        entries: settleCards(push('system', undefined, { tone: 'cancel', text: '已取消' }), '已取消'),
        streaming: false,
        lastSeq,
      }
    case Ev.PermissionRequest: {
      // ACP 权限确认：底座发起请求 → 服务端发布 session.permission_request → 前端渲染卡片，
      // 用户点选 allow/reject × once/always 后经 POST /sessions/:id/permission 回传。
      const callId = String(e.payload?.call_id ?? '')
      const title = String(e.payload?.title ?? '权限请求')
      const toolKind = String(e.payload?.kind ?? '')
      const toolCallId = String(e.payload?.tool_call_id ?? '')
      const timeoutAt = e.payload?.timeout_at != null ? String(e.payload.timeout_at) : undefined
      const optionKinds = Array.isArray(e.payload?.options)
        ? (e.payload.options as PermissionOption[])
        : []
      return {
        ...v,
        entries: push('permission', undefined, { callId, title, optionKinds, toolKind, toolCallId, resolved: false, timeoutAt }),
        lastSeq,
      }
    }
    case Ev.PermissionResolved: {
      // 挂起权限终局（用户决策/超时自动拒绝/会话取消）——服务端事件是收敛唯一事实，前端不再自设时钟。
      const callId = String(e.payload?.call_id ?? '')
      return {
        ...v,
        entries: entries.map((en): StreamEntry =>
          en.kind === 'permission' && en.callId === callId && !en.resolved
            ? { ...en, resolved: true, decision: permissionResolvedLabel(e.payload) }
            : en,
        ),
        lastSeq,
      }
    }
    case Ev.ElicitationResolved: {
      // 挂起提问终局（accept/decline/cancel/超时自动取消）——同上，收敛以服务端事件为准。
      const elicitationId = String(e.payload?.elicitation_id ?? '')
      return {
        ...v,
        entries: entries.map((en): StreamEntry =>
          en.kind === 'elicitation' && en.elicitationId === elicitationId && !en.resolved
            ? { ...en, resolved: true, decision: elicitationResolvedLabel(e.payload) }
            : en,
        ),
        lastSeq,
      }
    }
    case Ev.ElicitationRequest: {
      // ACP 结构化提问（elicitation/create）：底座请求 → 服务端发布 session.elicitation_request
      // → 前端渲染表单/URL 提问卡；用户提交/放弃经 POST /sessions/:id/elicitation 回传。
      const elicitationId = String(e.payload?.elicitation_id ?? '')
      const mode = String(e.payload?.mode ?? 'form') === 'url' ? 'url' : 'form'
      const message = String(e.payload?.message ?? '')
      const timeoutAt = e.payload?.timeout_at != null ? String(e.payload.timeout_at) : undefined
      const schema = (e.payload?.schema as ElicitationFormSchema | undefined) ?? undefined
      const url = e.payload?.url != null ? String(e.payload.url) : undefined
      return {
        ...v,
        entries: push('elicitation', undefined, {
          elicitationId,
          mode,
          message,
          schema,
          url,
          resolved: false,
          timeoutAt,
        }),
        lastSeq,
      }
    }
    case Ev.InputQueued: {
      // §6：会话正忙，输入已入队。队列只在事件里生长——POST 的 queued 回执只说明「这次入队了」，
      // 队项的真相永远是 input_queued / input_delivered / input_cancelled 三件套。
      return {
        ...v,
        queue: transitionQueue(v.queue, e).queue,
        queueSeq: v.queueSeq + 1,
        lastSeq,
      }
    }
    case Ev.InputDelivered:
      // 投递出去的文本紧接着会以 session.user_message 出现在正文里，队列行随之消失即可，
      // 不再补系统提示（否则同一件事在流里说两遍）。
      return {
        ...v,
        queue: transitionQueue(v.queue, e).queue,
        queueSeq: v.queueSeq + 1,
        lastSeq,
      }
    case Ev.InputCancelled: {
      // 离队但没投出去（用户撤下 / 投递失败 / 重启作废）：文本从此不会出现在正文里，
      // 所以必须在流里留一条带原因与出路的系统提示——「我排的那句话去哪了」得有答案。
      // 队项文本只存在于队列投影里（事件载荷不带 text），故从被移除的队项取。
      const tr = transitionQueue(v.queue, e)
      const reason = e.payload?.reason == null ? '' : String(e.payload.reason)
      return {
        ...v,
        queue: tr.queue,
        queueSeq: v.queueSeq + 1,
        entries: push('system', undefined, {
          tone: cancelTone(reason),
          text: cancelNotice(reason, tr.removed?.text),
        }),
        lastSeq,
      }
    }
    case Ev.Commands: {
      // 全量语义（协议保证非增量）：直接替换，空数组也替换——否则"底座通告现在没有命令"之后
      // 面板会一直显示陈旧候选。载荷形状不符（非数组）时不猜、保留原值，只推进水位。
      const list = e.payload?.commands
      if (!Array.isArray(list)) return { ...v, lastSeq }
      return { ...v, commands: list as CommandInfo[], lastSeq }
    }
    case Ev.Usage:
      return { ...v, lastSeq }
    default:
      return { ...v, lastSeq }
  }
}

interface Toast {
  id: number
  msg: string
}

export interface MateState {
  /**
   * 构建期固定的底座身份（`GET /meta` 的 `runtime{}`；null = 还没取到）。
   *
   * v19 起界面要说"当前用的是哪个底座"只有这一处事实来源：底座不由用户选，
   * 所以它只能从服务端的装配事实出口读，不能从会话或本地配置推。
   */
  runtime: RuntimeInfo | null
  /**
   * 底座诊断快照（`GET /runtime/diagnostics`；null = 还没取到）。
   *
   * 它是"底座进程此刻在不在、上一次为什么没起来"的唯一读面。v19 前这份快照挂在 agent 上
   * （`/agents/:id/diagnostics`，面板在设置 · Agent 管理里）；那个面板整体撤掉之后，
   * 事实改由**状态条**（常驻）与**底座徽标**（hover）如实标出——不新开一个设置分节，
   * 因为这里没有任何可写的东西：底座由构建期固定，启停也不该是用户的常规动作。
   */
  runtimeDiagnostics: RuntimeDiagnostics | null
  sessions: SessionSummary[]
  /** 工作空间登记（v14；左栏分组与选择器的事实源）。 */
  workspaces: WorkspaceListResp | null
  views: Record<string, SessionView>
  activeId: string | null
  wsState: WsState
  /** 载体接入失败的原因（M1-8：桌面态 boot() 失败等）——有值就显示明确状态，不静默重连。 */
  carrierError: string | null
  toast: Toast | null
  drawerOpen: boolean
  /** 左栏「扩展」这类导航组的展开态（groupId → 是否展开；不落盘，刷新回默认）。 */
  navExpanded: Record<string, boolean>
  /** 设置弹窗是否打开（设置既不是页面、也不再是左栏二级菜单：点一下直接开这一个弹窗）。 */
  settingsOpen: boolean
  /** 弹窗里当前选中的分节 id（不会为 null：关掉弹窗也保留，下次打开回到上次那一节）。 */
  settingsSection: string
  /** 会话侧栏当前视图：active=未归档列表；archived=已归档列表（REST archived=true）。 */
  railMode: 'active' | 'archived'
  /** 搜索命中跳转锚点：目标会话的 seq（ChatPage 消费后清空；null = 无跳转请求）。 */
  anchorSeq: number | null
  /**
   * 新建面板要**预选**的工作空间 id（左栏分组行的「＋」设置；null = 未指定 → 用默认工作空间）。
   *
   * 它是两个面之间唯一的桥：左栏是浏览面（不负责建会话），新建面板才是选择面
   * （设计 §5.1）。面板读取后**立即清空**（一次性意图），否则用户手动改选之后
   * 再打开面板会被旧值顶掉。
   */
  preferredWorkspaceId: string | null
  /** A6 单操作员令牌是否已配置（true = 后端启用鉴权且本机已填令牌）。 */
  token: string
  /** 令牌设置弹窗开关（顶栏锁图标 / 收到 401 时自动打开）。 */
  /** C2：该会话是否已翻到最早（false/缺省 = 可能还有更早，ChatPage 显示「加载更早」）。 */
  historyStart: Record<string, boolean>
  /**
   * 运行时目录读面（当前底座的 ACP 会话配置项投影；null = 未加载）。
   *
   * v12 起它来自独立的 `GET /models/catalog`——与设置 · 模型的平台清单是两个读面。
   * v19 起这条读面**无参**（原来要带 `?agent=<id>`，而底座只有一个）。
   */
  modelCatalog: ModelCatalogResp | null
}

export interface PermissionDecision {
  allowed: boolean
  optionId?: string
  always: boolean
  label: string
}

export interface ElicitationDecision {
  action: 'accept' | 'decline' | 'cancel'
  content?: Record<string, unknown>
  label: string
}

export interface MateActions {
  boot: () => void
  /** 拉一次装配事实（`GET /meta`）与底座诊断快照（`GET /runtime/diagnostics`）。 */
  loadMeta: () => Promise<void>
  refreshSessions: () => Promise<void>
  /** 拉取工作空间登记表（左栏分组 + 选择器共用；失败保持 null = 未装载）。 */
  loadWorkspaces: () => Promise<void>
  /**
   * 登记一个目录为工作空间（**get-or-create**：已登记则返回既有那一行并选中它）。
   *
   * 这是「添加工作空间…」唯一的落点：选目录那一半在 `DirectoryBrowser`（它只负责挑路径）。
   * 成功后刷新登记表——本地插入会让顺序与服务端不一致（新条目在尾部，那是服务端才知道的事）。
   *
   * **失败会抛**（不吞、也不自己弹提示）：调用方需要把原因呈现给用户。v14.3 起目录对话框是**模态**的，
   * 所以调用方用 toast（旧的内嵌弹层那种"留在原地换个目录"的交互已不存在）。
   */
  createWorkspace: (path: string, title?: string) => Promise<Workspace>
  /** 改名（不改身份）。 */
  renameWorkspace: (id: string, title: string) => Promise<void>
  /** 删除登记（**不删目录、不删会话**）；其会话回到默认工作空间，页面随之刷新。 */
  deleteWorkspace: (id: string) => Promise<void>
  /** 重排（beforeId 缺省 = 追加到尾部）。 */
  moveWorkspace: (id: string, beforeId?: string) => Promise<void>
  activate: (id: string | null, sinceSeq?: number) => void
  /** 跳转到某会话的锚点消息（搜索命中点击）；activate 后由 ChatPage 滚动定位并清空。 */
  jumpToAnchor: (id: string, seq: number) => void
  /** 清除跳转锚点（ChatPage 定位完成后调用）。 */
  clearAnchor: () => void
  /**
   * 新建会话并起首轮。
   *
   * `workspaceId` = 选中的工作空间登记 id（v14）；null/省略 = 落在**默认工作空间**。
   * 工作空间创建时定稿、之后不可变（它是底座 ACP session 的 cwd 与 workspace-write 的边界）。
   *
   * v14.3 起这里只收 id：面板的选择面收敛为"登记项 + 添加工作空间"之后，前端不再有"直接指定
   * 绝对路径"的入口（接口层的 `cwd` 仍然有效，那是对外契约，不因面板收敛而改变）。
   */
  createAndRun: (
    prompt: string,
    model?: ModelRef,
    workspaceId?: string | null,
    capabilities?: { skills?: string[]; mcps?: string[] },
  ) => Promise<string | undefined>
  /** 拉取模型目录（来源 session|resume|cache|none，失败 → 空目录 + 提示）。 */
  loadModelCatalog: () => Promise<void>
  /** 会话级模型变更（经运行时 set_config_option 立即生效；运行时拒绝 → 报错且不改元数据）。 */
  updateSessionModel: (id: string, model?: string) => Promise<void>
  /** 发送一轮 prompt；`delivery` 仅在会话正忙时有意义（§6 排队/插话）。 */
  sendPrompt: (text: string, delivery?: DeliveryChoice) => Promise<void>
  /** §6：撤下一条尚未投递的排队输入（收敛以 input_cancelled 事件为准）。 */
  cancelQueuedInput: (inputId: string) => Promise<void>
  /** §6：用服务端当前态补齐队列（冷挂载/HISTORY 未覆盖时用；`anchorQueueSeq` 见 SessionView.queueSeq）。 */
  refreshQueue: (id: string, anchorQueueSeq: number) => Promise<void>
  cancel: () => Promise<void>
  respondPermission: (callId: string, decision: PermissionDecision) => Promise<void>
  respondElicitation: (elicitationId: string, decision: ElicitationDecision) => Promise<void>
  renameSession: (id: string, title: string) => Promise<void>
  togglePin: (id: string) => Promise<void>
  archiveSession: (id: string, archived: boolean) => Promise<void>
  setRailMode: (mode: 'active' | 'archived') => void
  /** 左栏「＋」：记下要预选的工作空间，交给新建面板消费（一次性）。 */
  setPreferredWorkspace: (id: string | null) => void
  showToast: (msg: string) => void
  hideToast: (id: number) => void
  setDrawer: (open: boolean) => void
  /** 导航组展开/收起（左栏「扩展」）。 */
  setNavGroupExpanded: (groupId: string, expanded: boolean) => void
  /** 打开设置弹窗（回到上次那一节；首次是「通用」）。 */
  openSettings: () => void
  /** 打开设置弹窗并直接切到某一节（深链接 / 401 自动弹出用）。id 见 console/settingsView.tsx。 */
  openSettingsSection: (id: string) => void
  closeSettings: () => void
  /** A6：设置/清除单操作员令牌并重连 WS + 重新拉取数据。 */
  saveToken: (token: string) => void
  /** C2：向服务端翻页加载更早的事件（before 游标），返回本页是否还有更早。 */
  loadOlder: (id: string) => Promise<boolean>
  /** P3 加载四态：hydration 超时兜底（仍无 snapshot/事件 → 退出加载态显示空态）。 */
  expireHydration: (id: string) => void
  // transport 接线（内部）
  setWsState: (st: WsState) => void
  applyFrame: (f: WsServerFrame) => void
}

export type MateStore = MateState & MateActions

export const initialState: MateState = {
  runtime: null,
  runtimeDiagnostics: null,
  sessions: [],
  workspaces: null,
  views: {},
  activeId: null,
  anchorSeq: null,
  preferredWorkspaceId: null,
  wsState: 'idle',
  carrierError: null,
  toast: null,
  drawerOpen: false,
  // 空 = 没被用户切过：NavRail 按「当前路由是否命中该组」决定默认展开（见 navView.groupExpanded）
  navExpanded: {},
  settingsOpen: false,
  settingsSection: 'general',
  railMode: 'active',
  token: getToken(),
  historyStart: {},
  modelCatalog: null,
}

/** C2：服务端「加载更早」每页条数。 */
const HISTORY_PAGE = 50

let toastSeq = 0
let transportRef: WsTransport | null = null

/**
 * C2「加载更早」的**冷挂载待裁决**集合：activate 冷挂载（since_seq=-1 全量回放）时登记，
 * 由该会话到达的**首个帧**裁决——回事件 = 服务端从会话第一条起全给了（已到最早）；
 * 回快照 = 起点已被缓冲淘汰、更早的历史还在（未到最早）。见 applyFrame 两个分支。
 */
const coldFullReplay = new Set<string>()

// reportApiError：401（A6 令牌缺失/无效）时自动弹出「设置 → 访问令牌」那一节，其余走 toast。
function reportApiError(err: unknown): void {
  if (err instanceof HttpError && err.code === BizCode.Unauthorized) {
    useMate.getState().openSettingsSection('token')
    useMate.getState().showToast('需要访问令牌：请在左栏「设置 → 访问令牌」中填写 MATE_TOKEN')
    return
  }
  useMate.getState().showToast(toErrorMessage(err))
}

// transport 单例：常驻整个会话生命周期（跨路由保活），重连/重订阅由 transport 自理。
function ensureTransport(): WsTransport {
  if (transportRef) return transportRef
  transportRef = new WsTransport(
    (f) => {
      // 水位推进（供重连续传）
      if (f.type === 'event') transportRef?.ackSeq(f.agg_id, f.event.seq)
      if (f.type === 'snapshot') transportRef?.ackSeq(f.agg_id, f.event_seq)
      // 回放结束 = 这一段历史确实看完了：把续传游标推到那里，重连时不会再把同一段重放一遍。
      if (f.type === 'replay_end') transportRef?.ackSeq(f.agg_id, f.event_seq)
      if (f.type === 'gap') {
        // 慢订阅丢帧（服务端缓冲满）：按当前已应用水位立即重挂该聚合——
        // 服务端 EventsAfter 为开区间（seq > since），重放不会重复已应用事件；若起点已被淘汰则回 snapshot。
        transportRef?.attach(f.agg_id)
        return
      }
      useMate.getState().applyFrame(f)
    },
    (st) => {
      useMate.getState().setWsState(st === 'open' ? 'open' : st === 'connecting' ? 'connecting' : 'closed')
    },
  )
  return transportRef
}

export const useMate = create<MateStore>((set, get) => {
  const showToast = (msg: string): void => {
    const id = ++toastSeq
    set({ toast: { id, msg } })
    window.setTimeout(() => get().hideToast(id), 4200)
  }

  return {
    ...initialState,

    hideToast: (id) => {
      const cur = get().toast
      if (cur && cur.id === id) set({ toast: null })
    },
    showToast,

    // C2：向服务端翻页加载更早的事件（before 游标）。
    // 游标 = 当前视图最小已知位次（空视图回落到"服务端水位 + 1" = 最新那一页，见 olderCursor）；
    // 一个已知位次都没有时省略 before（后端给第一页）。折叠到空视图后整体前插
    //（事件 seq 全小于游标 → 与现有条目天然不重叠），lastSeq 不回退。
    loadOlder: async (id) => {
      const cur = get().views[id] ?? emptyView()
      const before = olderCursor(cur.entries, cur.lastSeq)
      try {
        const page = await sessionHistory(id, {
          ...(before === null ? {} : { before }),
          limit: HISTORY_PAGE,
        })
        let older = emptyView()
        for (const e of page.events) older = applyEvent(older, e)
        set((s) => {
          const view = s.views[id] ?? emptyView()
          const merged = prependOlder(older.entries, view.entries, before)
          return {
            ...s,
            views: { ...s.views, [id]: { ...view, entries: merged } },
            historyStart: { ...s.historyStart, [id]: !page.has_more },
          }
        })
        if (page.events.length === 0) showToast('已到会话最早一条')
        return page.has_more
      } catch (err) {
        reportApiError(err)
        return true
      }
    },

    setWsState: (st) => {
      if (get().wsState !== st) set({ wsState: st })
    },

    applyFrame: (f) => {
      let terminal = false
      let seedQueue = ''
      set((s) => {
        switch (f.type) {
          case 'snapshot': {
            // snapshot = 服务端整包终态视图（冷挂载或「淘汰即快照回退」）：
            // M0/M1 state 为空对象，客户端重置本会话视图并以 event_seq 初始化游标。
            // 重置会连带清掉队列投影，而 snapshot 不回放历史 → 立刻用 GET /inputs 把当前态查回来
            // （否则刷新后「已排队但还没发出去的话」在界面上凭空消失）。
            //
            // 对「加载更早」的裁决：回快照 = 服务端**没有**从起点重放（请求起点已被缓冲淘汰），
            // 更早的历史仍在 → 该会话明确「未到最早」，按钮该显示。
            seedQueue = f.agg_id
            coldFullReplay.delete(f.agg_id)
            return {
              ...s,
              historyStart: { ...s.historyStart, [f.agg_id]: false },
              views: {
                ...s.views,
                [f.agg_id]: {
                  ...emptyView(),
                  // 水位原样落库（`-1` = 该聚合还没有任何事件）：夹到 0 会让水位与
                  // "第 0 条"撞车，后续到达的 seq 0 事件被去重丢掉。
                  lastSeq: f.event_seq,
                },
              },
            }
          }
          case 'replay_begin': {
            // 进入回放（ADR-13）：**服务端说它在回放**，不是我们猜的。
            //
            // 同时裁决「加载更早」：冷挂载（`since_seq=-1`）收到 `replay_begin` 就证明服务端
            // 是**从会话第一条起**给的（淘汰过的话它会回 snapshot）——这就是「已到最早」的事实，
            // 不必等第一个事件到达才敢下结论（新会话可能一条事件都没有）。
            const atStart = coldFullReplay.delete(f.agg_id)
            const view = s.views[f.agg_id] ?? emptyView()
            return {
              ...s,
              historyStart: atStart ? { ...s.historyStart, [f.agg_id]: true } : s.historyStart,
              // 回放期不渲染"实时"的样子——`hydrating` 就是那面旗。
              views: { ...s.views, [f.agg_id]: { ...view, hydrating: true } },
            }
          }
          case 'replay_end': {
            // 回放结束：加载态**精确**结束在这里（12s 的 `expireHydration` 降级成保险丝）。
            const view = s.views[f.agg_id] ?? emptyView()
            return {
              ...s,
              views: { ...s.views, [f.agg_id]: { ...view, hydrating: false } },
            }
          }
          case 'event': {
            terminal = TERMINAL_TYPES.has(f.event.type)
            const view = applyEvent(s.views[f.agg_id] ?? emptyView(), f.event)
            // 首个事件即证明已就绪（配合 snapshot 与 hydration 超时兜底）
            if (view.hydrating) view.hydrating = false
            // 冷挂载（since_seq=-1）收到的是**重放**而不是快照 → 服务端把该会话在缓冲里的历史
            // 全给了（缓冲按聚合淘汰，淘汰过就会回快照）——这就是「已到最早」的事实。
            // 记下来，免得只问了一个问题的会话也一直挂着「加载更早的消息」。
            const atStart = coldFullReplay.delete(f.agg_id)
            // 运行时推送的配置项更新（全量语义）→ 就地刷新模型目录（**同一个底座**的才生效）：
            // 模型下拉与「当前模型」无需轮询即可跟随运行时的真实状态。
            //
            // v19 前这里比的是 `payload.agent_id === catalog.agent`；两个字段都已不存在，
            // 判据换成底座 id（`payload.runtime` ↔ `catalog.runtime`）——语义不变：跨底座的
            // 配置项不能套到当前目录上，否则会显示另一套模型的选项。
            let catalog = s.modelCatalog
            if (f.event.type === Ev.ConfigOptions && catalog) {
              const payload = f.event.payload ?? {}
              const options = payload.options as ConfigOption[] | undefined
              if (options && String(payload.runtime ?? '') === catalog.runtime) {
                catalog = { ...catalog, source: 'session', options }
              }
            }
            return {
              ...s,
              modelCatalog: catalog,
              historyStart: atStart ? { ...s.historyStart, [f.agg_id]: true } : s.historyStart,
              views: { ...s.views, [f.agg_id]: view },
            }
          }
          default:
            return s
        }
      })
      // 冷挂载：用服务端当前态补齐队列（锚点取重置后的 queueSeq=0；期间若有队列事件到达则让位事件）。
      if (seedQueue) void get().refreshQueue(seedQueue, 0)
      // 终态已到：会话列表 status 同步刷新（侧栏/服务端忙水位随之收敛）。
      if (terminal) void get().refreshSessions()
    },

    setDrawer: (open) => set({ drawerOpen: open }),

    setNavGroupExpanded: (groupId, expanded) => set((s) => ({ navExpanded: setExpanded(s.navExpanded, groupId, expanded) })),
    openSettings: () => set({ settingsOpen: true }),
    openSettingsSection: (id) => set({ settingsOpen: true, settingsSection: id }),
    closeSettings: () => set({ settingsOpen: false }),

    loadMeta: async () => {
      // 两个请求各自收敛：装配事实答"是哪个底座"，诊断快照答"它此刻在不在"——
      // 一条失败不该把另一条也吞掉（装配事实取不到时状态条仍要能报"底座未知"）。
      try {
        const meta = await getMeta()
        set({ runtime: meta.runtime })
      } catch (err) {
        // 取不到装配事实不该挡住界面：底座身份是**展示事实**，缺它只是少一行字，
        // 会话/能力那些面各有各的失败出路，不该被这里连坐。
        reportApiError(err)
      }
      try {
        set({ runtimeDiagnostics: await getRuntimeDiagnostics() })
      } catch (err) {
        reportApiError(err)
      }
    },
    refreshSessions: async () => {
      try {
        // 按侧栏当前视图拉取：active=未归档；archived=已归档。
        set({ sessions: await listSessions(get().railMode === 'archived') })
      } catch (err) {
        reportApiError(err)
      }
    },

    loadWorkspaces: async () => {
      try {
        set({ workspaces: await listWorkspaces() })
      } catch (err) {
        // 登记表取不到（服务不可达 / 老版本服务端）→ 保持 null。
        // 界面据此退化为"只有输入路径"一条路，而不是给一堆空分组。
        set({ workspaces: null })
        reportApiError(err)
      }
    },

    createWorkspace: async (path, title) => {
      // 失败**不吞**：登记失败的原因必须回到调用方。工作空间选择器要把它**就地**显示在弹层里
      // （设计 §5.2 状态 E：弹层不关，原因用服务端 msg，不自己编文案）。这里原先把错误收成一句
      // 会自己消失的全局 toast 并返回 undefined，于是调用方只能编一句"服务端未返回工作空间"，
      // 真实原因（目录不存在 / 落在白名单外 / 不是目录）反而丢了。
      const ws = await apiCreateWorkspace(path, title)
      // 刷新而不是本地插入：顺序与服务端的 list_order 只能由服务端决定（新条目在尾部），
      // 本地插在中间或末尾都可能与服务端不一致，而那种不一致下次刷新会"跳一下"。
      await get().loadWorkspaces()
      return ws
    },

    renameWorkspace: async (id, title) => {
      try {
        await apiRenameWorkspace(id, title)
        await get().loadWorkspaces()
        // 会话行上的 workspace_title 也要跟着变（否则左栏分组改名了、会话详情还显示旧名）。
        await get().refreshSessions()
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    deleteWorkspace: async (id) => {
      try {
        const resp = await apiDeleteWorkspace(id)
        await get().loadWorkspaces()
        // 归属被清空（那些会话回到默认工作空间）→ 重拉列表拿到新的分组。
        await get().refreshSessions()
        showToast(
          resp.released_sessions > 0
            ? `已移除登记；${resp.released_sessions} 个会话回到「默认工作空间」，目录与文件未动`
            : '已移除登记；目录与文件未动',
        )
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    moveWorkspace: async (id, beforeId) => {
      try {
        await apiMoveWorkspace(id, beforeId)
        await get().loadWorkspaces()
      } catch (err) {
        // 顺序没改成 → 重新拉一次（本地已经是乐观顺序的话，这次拉取会把它拉回事实）。
        await get().loadWorkspaces()
        showToast(toErrorMessage(err))
      }
    },

    saveToken: (token) => {
      const v = token.trim()
      persistToken(v)
      set({ token: v })
      // 令牌变化必须重连 WS（凭据走带内首帧 attach，重连才会带上新值）并重新拉取数据。
      // 浏览器态的通道凭据就是它，因此要重解析一次载体配置再连。
      const t = ensureTransport()
      void resolveStreamConfig().then((cfg) => {
        t.configure(cfg)
        t.connect()
      })
      void get().loadMeta()
      void get().refreshSessions()
      get().closeSettings()
      get().showToast(v ? '已保存访问令牌，正在重连…' : '已清除访问令牌')
    },

    boot: () => {
      // 载体接入（M1-8）：先问清"事件流打哪儿、通道凭据是什么"，再建常驻连接。
      // 桌面窗口的 origin 是 mate-app://app，基址只可能来自壳的 boot()——所以这一步不能省。
      void (async () => {
        let cfg: StreamConfig
        try {
          cfg = await resolveStreamConfig()
        } catch (e) {
          // 明确状态，不静默重连：拿不到基址时连一条错误的地址只会刷屏重连。
          set({ carrierError: `事件流配置解析失败：${e instanceof Error ? e.message : String(e)}` })
          return
        }
        set({ carrierError: null })
        const t = ensureTransport()
        t.configure(cfg)
        // 常驻连接：console 首帧即反映真实 WS 状态（appbar/状态条），attach 免等待
        t.connect()
        // 装配事实（底座身份）与在册会话：都与事件流并行拉，首帧就有东西可画。
        void get().loadMeta()
        void get().refreshSessions()
        // 工作空间登记表：左栏分组与选择器共用（选择器每次打开也会刷一次，
        // 这里拉是为了"首帧就有分组"，而不是等用户点开选择器才出现）。
        void get().loadWorkspaces()
      })()
    },

    activate: (id, sinceSeq) => {
      const s = get()
      const t = ensureTransport()
      if (s.activeId && s.activeId !== id) t.detach(s.activeId)
      if (id) {
        t.connect()
        if (!t.isAttached(id)) {
          const cur = s.views[id]
          // 冷挂载/切回（无 since_seq）：视图重置为 hydrating（P3 四态），等服务端
          // snapshot（或首个事件）置回就绪；带 since_seq 的增量续传（createAndRun
          // 本地气泡续流）不视为冷挂载。
          const cold = sinceSeq === undefined
          if (cold) {
            // 冷挂载一律 since_seq=-1（全量回放：含该会话第 0 条）→ 登记待裁决，由该会话首个帧给出
            // 「已到最早 / 还有更早」的事实（applyFrame）。
            coldFullReplay.add(id)
          }
          if (cold && (!cur || cur.entries.length === 0)) {
            set((st) => ({
              views: { ...st.views, [id]: { ...emptyView(), hydrating: true } },
            }))
          } else if (cold) {
            set((st) => ({ views: { ...st.views, [id]: emptyView() } }))
          }
          t.attach(id, sinceSeq)
        }
        set({ activeId: id, drawerOpen: false, anchorSeq: null })
      } else {
        set({ activeId: null })
      }
    },

    jumpToAnchor: (id, seq) => {
      // 复用 activate 的挂载/续传语义，再把锚点交给 ChatPage（先清空再设置，保证跨会话重复跳转也触发）。
      set({ anchorSeq: null })
      get().activate(id)
      set({ anchorSeq: seq })
    },

    clearAnchor: () => set({ anchorSeq: null }),

    createAndRun: async (prompt, model, workspaceId, capabilities) => {
      try {
        const resp = await createSession({
          prompt,
          model: model?.modelID,
          // **不外发空串**：缺省才是「落在默认工作空间」语义（后端把空/缺省当作未指定）。
          workspace_id: workspaceId ?? undefined,
          // 能力选定：调用方（输入卡）已在"两个 kind 都没限制"时不传，这里不再判一次
          // ——缺省路径必须与 M3 之前逐字节相同，判据只留一处。传了就钉住代际；
          // 不可达的 id 由服务端拒（40001，msg 里逐条给原因）。
          capabilities,
        })
        // 本地先落用户气泡（echo 事件 seq 与返回水位相等，重放 seq>水位 会跳过它）。
        // attach since_seq=event_seq（exclusive）续传首轮后续事件。
        // 这个会话是**此刻由本端创建**的：返回的 event_seq 已经覆盖「建会话 + 首条提问」那一批
        // 事件的最后一个（`session.created` 与这条 user_message 回显都在其中），之前**没有任何**
        // 属于它的事件 → 直接记为「已到最早」，不必等回放裁决（否则刚建好、只问了一个问题的
        // 会话也会挂着「加载更早的消息」，而点它只取回一条不渲染的 `session.created`）。
        set((s) => ({
          historyStart: { ...s.historyStart, [resp.id]: true },
          views: {
            ...s.views,
            [resp.id]: {
              ...emptyView(),
              entries: [{ kind: 'user', id: nextId(), text: prompt }],
              streaming: true,
              lastSeq: resp.event_seq,
            },
          },
        }))
        get().activate(resp.id, resp.event_seq)
        // 新建后回「会话」视图并刷新列表（避免停在归档视图看不到新会话）。
        if (get().railMode !== 'active') set({ railMode: 'active' })
        void get().refreshSessions()
        return resp.id
      } catch (err) {
        showToast(toErrorMessage(err))
        return undefined
      }
    },

    loadModelCatalog: async () => {
      try {
        // v12：走独立的运行时目录读面（`GET /models/catalog`）——不再复用 `GET /models`，
        // 后者已改成平台清单面。
        const resp = await listModelCatalog()
        set({ modelCatalog: resp })
      } catch (err) {
        // 取不到目录 = 运行时没有可用来源（未启动/未登录）：保持空目录（面板显示空态 + 提示），
        // 不阻塞建会话（不指定模型即跟随运行时默认）。
        set({ modelCatalog: null })
        showToast(toErrorMessage(err))
      }
    },

    updateSessionModel: async (id, model) => {
      try {
        await updateSession(id, { model })
        await get().refreshSessions()
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    sendPrompt: async (text, delivery) => {
      const id = get().activeId
      if (!id) return
      // 忙时是否放行取决于投递模式（§6）：排队/插话是允许的，缺省/reject 仍然是「防重入拒绝」。
      const busy = get().views[id]?.streaming ?? false
      const queueing = delivery === 'after_run' || delivery === 'interrupt_then_prompt'
      if (busy && !queueing) return // 已有一轮在跑，防重入（连点/并发）
      try {
        if (!busy) {
          // 乐观置忙：在 POST 返回前就把 Composer 置为禁用，消除「请求在途仍可再发」的竞态窗口，
          // 避免第二次 send 打到后端 run_in_progress（决策 v5 前修复）。
          // 入队时**不置忙**：这一轮本来就还在跑，忙碌水位由 run 事件收敛。
          set((s) => {
            const view = s.views[id] ?? emptyView()
            return { views: { ...s.views, [id]: { ...view, streaming: true } } }
          })
        }
        ensureTransport().connect()
        const resp = await promptSession(id, delivery ? { prompt: text, delivery } : { prompt: text })
        if (resp.queued) {
          // 队项由 input_queued 事件建项（不靠这里乐观插入）：事件才是队列的唯一事实。
          showToast(`会话正忙，已入队（${deliveryLabel(delivery ?? '')}）——可随时撤下`)
        }
      } catch (err) {
        // 失败恢复忙态（若无终态事件，回退为非忙以便重试）。入队失败时不要动 streaming：
        // 那一轮的忙碌水位归它的 run 事件管，这里改掉会让「停止」按钮凭空消失。
        if (!busy) {
          set((s) => {
            const view = s.views[id]
            if (!view) return {}
            return { views: { ...s.views, [id]: { ...view, streaming: false } } }
          })
        }
        showToast(toErrorMessage(err))
      }
    },

    cancelQueuedInput: async (inputId) => {
      const id = get().activeId
      if (!id) return
      try {
        const res = await apiCancelQueuedInput(id, inputId)
        if (!res.deleted) {
          // 已经不在了（已投递/已作废/被别的标签页撤下）：如实说，不假装成功。
          showToast('该排队输入已不在队列里（可能已投递或已作废）')
        }
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    refreshQueue: async (id, anchorQueueSeq) => {
      try {
        const items = await listQueuedInputs(id)
        set((s) => {
          const view = s.views[id]
          // 期间有队列事件到达（一般意味着入队/投递/撤下刚刚发生）→ 以事件为准：
          // 服务端那份当前态可能读得更早，覆盖回去会把已撤下的项复活成幻影。
          if (!view || view.queueSeq !== anchorQueueSeq) return {}
          return { views: { ...s.views, [id]: { ...view, queue: items.map(queuedFromResp) } } }
        })
      } catch {
        // 补齐失败不打扰用户：实时事件仍会让队列长出来（队列是投影，不是必需状态）。
      }
    },

    cancel: async () => {
      const id = get().activeId
      if (!id) return
      try {
        await cancelSession(id)
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    respondPermission: async (callId, decision) => {
      const id = get().activeId
      if (!id) return
      try {
        await resolvePermission(id, {
          call_id: callId,
          option_id: decision.optionId,
          always: decision.always,
          allowed: decision.allowed,
        })
        // 本地就地标记该权限卡已决策（按钮禁用 + 展示决策），底座随后经 WS 续流。
        set((s) => {
          const view = s.views[id]
          if (!view) return {}
          const entries = view.entries.map((en): StreamEntry =>
            en.kind === 'permission' && en.callId === callId
              ? { ...en, resolved: true, decision: decision.label }
              : en,
          )
          return { views: { ...s.views, [id]: { ...view, entries } } }
        })
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    respondElicitation: async (elicitationId, decision) => {
      const id = get().activeId
      if (!id) return
      try {
        await resolveElicitation(id, {
          elicitation_id: elicitationId,
          action: decision.action,
          content: decision.content,
        })
        // 本地就地标记该提问卡已决策（按钮禁用 + 展示结果），底座随后经 WS 续流。
        set((s) => {
          const view = s.views[id]
          if (!view) return {}
          const entries = view.entries.map((en): StreamEntry =>
            en.kind === 'elicitation' && en.elicitationId === elicitationId
              ? { ...en, resolved: true, decision: decision.label, content: decision.content }
              : en,
          )
          return { views: { ...s.views, [id]: { ...view, entries } } }
        })
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    renameSession: async (id, title) => {
      try {
        await updateSession(id, { title })
        void get().refreshSessions()
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    togglePin: async (id) => {
      const cur = get().sessions.find((x) => x.id === id)
      if (!cur) return
      try {
        await updateSession(id, { pinned: !cur.pinned })
        void get().refreshSessions()
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    archiveSession: async (id, archived) => {
      // 归档/恢复（软归档：archived 为真即从「会话」移入「归档」视图，数据不删）。
      try {
        await updateSession(id, { archived })
        void get().refreshSessions()
      } catch (err) {
        showToast(toErrorMessage(err))
      }
    },

    expireHydration: (id) => {
      set((s) => {
        const view = s.views[id]
        if (!view || !view.hydrating) return {}
        return { views: { ...s.views, [id]: { ...view, hydrating: false } } }
      })
    },

    setRailMode: (mode) => {
      if (get().railMode === mode) return
      set({ railMode: mode })
      void get().refreshSessions()
    },

    setPreferredWorkspace: (id) => set({ preferredWorkspaceId: id }),
  }
})
