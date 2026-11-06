import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Button, Dropdown } from 'antd'
import { InfoCircleOutlined, MoreOutlined, PicRightOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { Ev, modelValues } from '@ark-mate/types'
import type { AttachmentMeta } from '@ark-mate/types'
import { listSessionArtifacts, sessionHistory, sessionRuns } from '@ark-mate/api'
import { useMate } from '../store'
import { SessionDetailDrawer, SessionInfoPanel } from '../components/SessionDetail'
import { Composer } from '../components/Composer'
import { ConfirmArea } from '../components/ConfirmArea'
import { QueuedInputs } from '../components/QueuedInputs'
import { StartPanel } from '../components/StartPanel'
import { EntryView, TurnView } from '../components/Transcript'
import { anchorIndex } from '../console/searchView'
import { titleFor } from '../console/util'
import { fmtRunDockLine, fmtRunUsageLine, toRunUsageInfo, type RunUsageInfo } from '../console/usageView'
import {
  TRANSCRIPT_WINDOW,
  buildTurnRows,
  foldThoughts,
  stickToBottom,
  turnStatusOf,
  windowEntries,
} from '../console/transcriptView'
import { runNarration } from '../console/runView'
import { elapsedLabel } from '../console/time'
import type { DeliveryChoice } from '../console/queueView'

/** 会话信息面板默认收起的本地记忆键（`mate.web.*` 前缀与其它前端持久键一致）。 */
const INFO_OPEN_KEY = 'mate.web.infoPanelOpen'

/**
 * 宽屏判定（≥1280）：与 `.fg-info-panel` 的 CSS 断点保持一致。
 * 宽屏时「会话信息」作为可收起右栏常驻渲染；窄屏时切换按钮改为打开抽屉。
 */
function useWideLayout(): boolean {
  const [wide, setWide] = useState(() => typeof window !== 'undefined' && window.matchMedia('(min-width: 1280px)').matches)
  useEffect(() => {
    const mq = window.matchMedia('(min-width: 1280px)')
    const on = () => setWide(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  return wide
}

/** 问答控制台主视图：会话侧栏 + 聊天（head / transcript / composer）+ 状态条。 */
export function ChatPage() {
  const navigate = useNavigate()
  const params = useParams<{ id: string }>()
  const paramId = params.id ?? null

  const runtime = useMate((s) => s.runtime)
  const sessions = useMate((s) => s.sessions)
  const views = useMate((s) => s.views)
  const activeId = useMate((s) => s.activeId)
  const activate = useMate((s) => s.activate)
  const expireHydration = useMate((s) => s.expireHydration)
  const sendPrompt = useMate((s) => s.sendPrompt)
  const cancelQueuedInput = useMate((s) => s.cancelQueuedInput)
  const moveQueuedInput = useMate((s) => s.moveQueuedInput)
  const sendQueuedInputNow = useMate((s) => s.sendQueuedInputNow)
  const popQueuedInput = useMate((s) => s.popQueuedInput)
  const cancel = useMate((s) => s.cancel)
  const archiveSession = useMate((s) => s.archiveSession)
  const showToast = useMate((s) => s.showToast)
  const anchorSeq = useMate((s) => s.anchorSeq)
  const clearAnchor = useMate((s) => s.clearAnchor)
  const loadOlder = useMate((s) => s.loadOlder)
  const historyStart = useMate((s) => s.historyStart)
  // 会话级模型选择（原型 §12 模型 chip）：读运行时目录，落库走既有 PATCH /sessions/:id（updateSessionModel）。
  const modelCatalog = useMate((s) => s.modelCatalog)
  const loadModelCatalog = useMate((s) => s.loadModelCatalog)
  const updateSessionModel = useMate((s) => s.updateSessionModel)
  // 直达会话页（刷新 / 从列表点进 `/sessions/:id`）时目录可能还没拉过——`StartPanel` 会拉，但那条路
  // 不经过这里，于是模型 chip 会在目录为空时退化成「裸 mdl_ id」。进页面补拉一次（幂等，已有缓存则命中）。
  useEffect(() => {
    if (!modelCatalog) void loadModelCatalog()
    // 只在挂载时按当时是否已有目录判断一次：目录被置空（运行时下线）交给复核再拉，不在这里死循环。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const scrollRef = useRef<HTMLDivElement | null>(null)
  const lastParamRef = useRef<string | null | undefined>(undefined)
  // 尾部窗口（P3 余留）：长会话只渲染尾部 TRANSCRIPT_WINDOW 条，更早的按需展开。
  const [revealed, setRevealed] = useState(0)
  // 「取出编辑」塞回输入框的草稿（ADR-10 动作③）：nonce 变化即生效一次。
  const [injectedDraft, setInjectedDraft] = useState<{ text: string; nonce: number } | null>(null)
  // 首次定位到底部：每个会话只在首次拿到条目时执行一次（切回不打扰用户阅读位置）。
  const anchoredRef = useRef<string | null>(null)
  // 「显示更早」前后的高度差补偿（保持视口停在原处）。
  const pendingAnchorRef = useRef<number | null>(null)
  const [detailOpen, setDetailOpen] = useState(false)
  // 会话信息右栏（宽屏可收起，默认收起）。持久化到 localStorage，跨会话记忆开合偏好。
  const [infoOpen, setInfoOpen] = useState(() => {
    try {
      return localStorage.getItem(INFO_OPEN_KEY) === '1'
    } catch {
      return false
    }
  })
  const toggleInfo = () =>
    setInfoOpen((cur) => {
      const next = !cur
      try {
        localStorage.setItem(INFO_OPEN_KEY, next ? '1' : '0')
      } catch {
        /* 隐私模式等禁用存储时忽略，仅本会话内生效 */
      }
      return next
    })
  const wide = useWideLayout()
  // 搜索命中跳转（M3 ②）：已应用的锚点 key（防重复滚动）+ 高亮条目 id。
  // 滚动本身排在**高亮渲染之后**的 effect 里：命中的条目可能在被折叠的过程块里，
  // 要先让 `flashId` 把那块展开（DOM 里才有节点），否则只能滚到轮首。
  const anchorAppliedRef = useRef<string | null>(null)
  const pendingScrollRef = useRef<{ idx: number; id: string } | null>(null)
  const [flashEntryId, setFlashEntryId] = useState<string | null>(null)
  // 命中同一个条目时 `flashEntryId` 不变，单靠它不会触发第二次滚动；用一个 tick 显式驱动。
  const [scrollTick, setScrollTick] = useState(0)
  // 「已处理 Ns」的时钟：只在本来就有开始时刻时每 1s 推进一次（没有开始时刻就一个字都不显示）。
  const [nowTick, setNowTick] = useState(() => Date.now())
  // 用户手动展开过的思考折：流式中有人展开着看历史时，自动贴底要停下（D6 / 7.9）。
  const [foldOpen, setFoldOpen] = useState<Record<string, boolean>>({})
  // err 轮重试文本回退（会话 id → 历史最后 user_message；undefined=未取）
  const [errFallback, setErrFallback] = useState<Record<string, string | undefined>>({})
  // C2：服务端「加载更早」在途标记（同一时刻只允许一次请求）。
  const [loadingOlder, setLoadingOlder] = useState(false)
  // §6 撤下排队输入的在途标记（按钮转圈，避免连点）。
  const [cancellingInput, setCancellingInput] = useState<string | null>(null)
  // 每轮用量（run.last_seq → 展示行）：会话切换或轮次终态后重拉 runs 读模型。
  const [runsBySeq, setRunsBySeq] = useState<Map<number, { usageText: string; info: RunUsageInfo }>>(new Map())

  // URL → 激活（刷新/直达 /sessions/:id 续看；参数不变不重复 attach）
  useEffect(() => {
    if (lastParamRef.current === paramId) return
    lastParamRef.current = paramId
    // 无 id（/sessions）= 未选中会话 → 清空选中，问答区落回新建面板。
    if (paramId) activate(paramId)
    else activate(null)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paramId])

  const view = activeId ? views[activeId] : undefined
  const activeSum = activeId ? sessions.find((x) => x.id === activeId) : undefined
  const streaming = view?.streaming ?? false
  const hydrating = view?.hydrating ?? false

  // 会话产物计数（v35）：只用于头栏入口的角标。轮次结束时重拉一次——工具写出的文件是在 run
  // 过程中登记的；不等 run 收尾就拉会永远慢一拍。
  const [artifactCount, setArtifactCount] = useState(0)
  useEffect(() => {
    if (!activeId) {
      setArtifactCount(0)
      return
    }
    let alive = true
    void listSessionArtifacts(activeId)
      .then((r) => {
        if (alive) setArtifactCount(r.total ?? 0)
      })
      .catch(() => {
        if (alive) setArtifactCount(0)
      })
    return () => {
      alive = false
    }
  }, [activeId, streaming])

  // 每轮用量：activeId 变化或轮次终态（streaming true→false）后重拉 runs；
  // 轮进行中 run 未落库，跳过以省请求。构建 last_seq → 展示行（Δ 相对前一有上报的轮）。
  useEffect(() => {
    if (!activeId) {
      setRunsBySeq(new Map())
      return
    }
    if (streaming) return
    let cancelled = false
    void (async () => {
      try {
        const { runs } = await sessionRuns(activeId)
        if (cancelled) return
        const m = new Map<number, { usageText: string; info: RunUsageInfo }>()
        const withData = runs
          .filter((r) => toRunUsageInfo(r))
          .sort((a, b) => a.first_seq - b.first_seq)
        for (let i = 0; i < withData.length; i++) {
          const r = withData[i]
          const info = toRunUsageInfo(r)!
          const prev = i > 0 ? withData[i - 1].usage : undefined
          let usageText = fmtRunUsageLine(r.usage, prev)
          if (usageText === '—') usageText = fmtRunDockLine(info) // 无上下文快照但有计时/成本 → 显示计时
          if (!usageText) continue
          m.set(r.last_seq, { usageText, info })
        }
        setRunsBySeq(m)
      } catch {
        // 单轮用量为增强信息，加载失败静默，不影响会话渲染。
      }
    })()
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeId, streaming])
  // 挂起交互请求条数（权限确认 + 结构化提问的待决项；聊天头角标）
  const pendingPerms =
    activeId && view ? view.entries.filter((en) => (en.kind === 'permission' || en.kind === 'elicitation') && !en.resolved).length : 0
  // 一轮在干什么 = **服务端铸造的过程状态**（`session.run.status` 事件 → 视图投影，ADR-6）。
  //
  // 这里原来是一段倒推：`serverBusy = 会话 status==='in_progress' && !streaming && 本地尾条 tone
  // 不是终态`，再据此显示「可点「停止」解除」。它的失效形态不是报错，而是**让用户去点「停止」
  // 解除一个其实已经死掉的轮次**（刷新/重启之后本地条目与服务端 status 必然分叉，那段注释
  // 自己就写着"如刷新/重启后该 run 仍在跑或已卡住"）。
  //
  // 现在的两个输入都是事实：`views[id].run`（实时事件流）与 `activeSum.run`（REST 快照）。
  // 两处都缺 = 服务端没说过任何过程状态 → **什么都不说**，不当作忙（宁可少显示，不猜）。
  const run = (activeId ? views[activeId]?.run : undefined) ?? activeSum?.run
  const narration = runNarration(run)
  const busy = streaming || narration.busy
  const canStop = streaming || narration.canStop
  const runStartedAt = activeId ? views[activeId]?.runStartedAt : undefined
  useEffect(() => {
    if (!busy || !runStartedAt) return
    const t = window.setInterval(() => setNowTick(Date.now()), 1000)
    return () => window.clearInterval(t)
  }, [busy, runStartedAt])
  const elapsed = busy ? elapsedLabel(runStartedAt, nowTick) : ''
  // v21：会话头标题走 `plain` 兜底——没命名、也没发过言的会话就叫「新会话」，
  // 不再拼成「新会话 · dsh」（底座信息不进对话框页面），也不摊一串 id 冒充名字。
  // 左栏那行仍走带底座/带短 id 的口径（那是浏览面，本轮不动）。
  const title = activeId ? titleFor(activeSum, view, undefined, 'plain') : ''
  // 会话的**执行事实**与构建期固定的那个不同 = 这条历史会话跑在别的底座上。
  // v19 前这里判的是"会话绑定的 agent 已不存在"（被删除/未注册）；agent 并入 runtime 后
  // 判据换成同一形状的底座事实：id 对不上就是"当前这个构建续不了它"，照样要如实说明并给出路。
  const foreignRuntime = !!activeSum?.runtime_id && !!runtime && activeSum.runtime_id !== runtime.id
  // 失败 run 一键重试：仅当最新一条为 run.error（无流/非 busy）时，取其所属轮的用户输入重发。
  // 视图可能是服务端快照的部分重放（该轮 user 气泡不在本地条目里），此时回退到历史事件取输入。
  const entries = view?.entries ?? []
  const lastEntry = entries[entries.length - 1]
  const hasErrTail = !!(!busy && lastEntry && lastEntry.kind === 'system' && lastEntry.tone === 'err')
  const retryLocalText = (() => {
    if (!hasErrTail) return null
    for (let i = entries.length - 1; i >= 0; i--) {
      const en = entries[i]
      if (en === lastEntry) continue
      if (en.kind === 'system') return null
      if (en.kind === 'user' && typeof en.text === 'string' && en.text.trim()) return en.text
    }
    return null
  })()
  const errFallbackText = activeId ? errFallback[activeId] : undefined
  const retryText = hasErrTail ? retryLocalText ?? (errFallbackText || undefined) : undefined
  const retryLast = retryText ? () => void sendPrompt(retryText) : undefined
  // 「上一轮因进程重启中断，可重发」（Q7 选①）：重发的就是**最近一条用户输入**。
  // 与上面的失败重试分开算——那是 `run.error` 的入口（历史/失败轮列表相关），
  // 这是恢复收口（`run.status(interrupted)`）的入口。两者都不猜：没有用户输入就不给按钮。
  const lastUserText = (() => {
    for (let i = entries.length - 1; i >= 0; i--) {
      const en = entries[i]
      if (en.kind === 'system') break
      if (en.kind === 'user' && typeof en.text === 'string' && en.text.trim()) return en.text
    }
    return null
  })()
  const resendable = narration.canRetry && !!lastUserText

  // 本地无该轮输入（部分重放/冷挂载后直接失败）时，从历史取最后一条 user_message 作为重试文本。
  useEffect(() => {
    const id = activeId
    if (!id || !hasErrTail || retryLocalText !== null || errFallback[id] !== undefined) return
    let dead = false
    void (async () => {
      let txt = ''
      try {
        const page = await sessionHistory(id, { limit: 30 })
        const last = [...page.events].reverse().find((ev) => ev.type === Ev.UserMessage)
        if (last && typeof last.payload?.text === 'string') txt = last.payload.text
      } catch {
        txt = ''
      }
      if (!dead) setErrFallback((p) => (p[id] !== undefined ? p : { ...p, [id]: txt }))
    })()
    return () => {
      dead = true
    }
  }, [activeId, hasErrTail, retryLocalText, errFallback])
  // P3 加载四态：hydration 超时兜底——attach 后长时间无 snapshot/事件（连接被熔断重连中、
  // 服务端无响应）→ 退出加载态回空态（stalled 保险丝随后会触发重连恢复）。
  useEffect(() => {
    const id = activeId
    if (!id || !hydrating) return
    const t = window.setTimeout(() => expireHydration(id), 12000)
    return () => window.clearTimeout(t)
  }, [activeId, hydrating, expireHydration])

  // 首次挂载/切换会话：条目首次出现时定位到底部（此前实现只做「近底跟随」，
  // 冷挂载 scrollTop=0 → 打开会话看到的是最早的消息）。
  useEffect(() => {
    const el = scrollRef.current
    if (!el || !activeId) return
    if ((view?.entries.length ?? 0) === 0) return
    if (anchoredRef.current === activeId) return
    anchoredRef.current = activeId
    el.scrollTop = el.scrollHeight
  }, [activeId, view?.entries])

  // 事件增长时若已近底部则跟随滚动（避免打扰上翻阅读）；「显示更早」用高度差补偿保持视口。
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    const prevHeight = pendingAnchorRef.current
    if (prevHeight !== null) {
      pendingAnchorRef.current = null
      el.scrollTop += el.scrollHeight - prevHeight
      return
    }
    // 流式中有人手动展开了某一段思考 = 他在读历史：别再把他拉回底部。
    if (streaming && Object.values(foldOpen).some(Boolean)) return
    if (stickToBottom({ scrollTop: el.scrollTop, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight })) {
      el.scrollTop = el.scrollHeight
    }
  }, [view?.entries, view?.streaming, view?.lastSeq, revealed, streaming, foldOpen])


  // 搜索命中跳转（M3 ②）：条目就绪后滚动到锚点条目并短暂高亮。
  // 锚点早于折叠窗口 → 先展开到该条（下一轮渲染再滚动）；无法定位（早于窗口起点）→ 就近落到首条。
  useEffect(() => {
    if (anchorSeq === null || !activeId) return
    if (hydrating || entries.length === 0) return
    const key = `${activeId}#${anchorSeq}`
    if (anchorAppliedRef.current === key) return
    const idx = anchorIndex(entries, anchorSeq)
    if (idx < 0) {
      clearAnchor()
      return
    }
    const hidden = Math.max(0, entries.length - (TRANSCRIPT_WINDOW + revealed))
    if (idx < hidden) {
      setRevealed((n) => n + (hidden - idx)) // 展开后由下一轮 effect 完成滚动
      return
    }
    // 先只登记命中的条目并置高亮；滚动交给下一个 effect——命中项若在折叠块里，
    // 那一块会因 `flashId` 在**同一次渲染**展开，节点才是可定位的。
    anchorAppliedRef.current = key
    pendingScrollRef.current = { idx, id: entries[idx].id }
    setFlashEntryId(entries[idx].id)
    setScrollTick((n) => n + 1)
    window.setTimeout(() => setFlashEntryId(null), 2400)
    clearAnchor()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [anchorSeq, activeId, hydrating, entries, revealed])

  // 高亮渲染完成后再滚动：轮内每个条目/折叠块都带 `data-fg-idx`，所以命中的是轮内较深条目时
  // 也能精确定位（而不是只滚到轮首）。窗口/锚点计算仍由 `anchorIndex` + `entryIndex` 决定。
  useEffect(() => {
    if (!flashEntryId) return
    const pending = pendingScrollRef.current
    if (!pending || pending.id !== flashEntryId) return
    pendingScrollRef.current = null
    const el = scrollRef.current
    if (!el) return
    const node = el.querySelector<HTMLElement>(`[data-fg-idx="${pending.idx}"]`)
    if (!node) return
    const box = node.getBoundingClientRect()
    const base = el.getBoundingClientRect()
    el.scrollTo({ top: Math.max(0, el.scrollTop + (box.top - base.top) - 16), behavior: 'smooth' })
  }, [flashEntryId, scrollTick])

  const entryWindow = windowEntries(view?.entries ?? [], TRANSCRIPT_WINDOW, revealed)
  const turnStatus = turnStatusOf(view?.entries ?? [], streaming, narration.label)
  // 分组在**窗口之后**：先按条目窗口取尾部，再把其中相邻的 thought 收成一折。
  const folded = foldThoughts(entryWindow.visible, streaming)
  // 搜索锚点仍按**原条目下标**定位：折叠块的 id 是它首条条目的 id，所以它沿用那个下标。
  const windowBase = Math.max(0, (view?.entries.length ?? 0) - entryWindow.visible.length)
  const entryIndex = new Map(entryWindow.visible.map((e, i) => [e.id, windowBase + i]))
  // 轮级视图：identity 头 / 过程流 / 整轮折叠都从这一层派生；窗口与搜索下标仍按原条目。
  const rows = buildTurnRows(folded, {
    streaming,
    durationForSeq: (seq) => (seq != null ? runsBySeq.get(seq)?.info.durationMS : undefined),
    indexOfId: (id) => entryIndex.get(id) ?? 0,
  })
  const settleScroll = () => {
    const el = scrollRef.current
    if (!el) return
    if (stickToBottom({ scrollTop: el.scrollTop, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight })) {
      el.scrollTop = el.scrollHeight
    }
  }

  // 切换会话：复位尾部窗口（新会话从尾部一页开始）与折叠展开态。
  useEffect(() => {
    setRevealed(0)
    setFoldOpen({})
  }, [activeId])

  const onSend = (text: string, delivery?: DeliveryChoice, attachments?: AttachmentMeta[]) => {
    if (!activeId) return
    void sendPrompt(text, delivery, attachments)
  }

  // 新建会话：清空当前选中（无会话选中时问答区本身就是新建面板）→ 回到 /sessions。
  const openNew = () => {
    setDetailOpen(false)
    activate(null)
    if (paramId) navigate('/sessions')
  }

  // 软清空：归档当前会话（数据保留，可在「归档」视图恢复）→ 清空选中，问答区落回新建面板。
  const softClear = async () => {
    if (!activeId) return
    try {
      await archiveSession(activeId, true)
      showToast('当前会话已归档，可随时从「归档」恢复', 'success')
      openNew()
    } catch {
      /* archiveSession 内部已 toast 错误 */
    }
  }

  const headMenu = {
    items: [{ key: 'soft-clear', label: '归档并新建会话', title: '保留数据到归档，问答区回到新建面板' }],
    onClick: (info: { key: string }) => {
      if (info.key === 'soft-clear') void softClear()
    },
  }

  const renderChatHead = () => {
    if (!activeId) return null
    // 旁白优先：`narration.label` 就是"这一刻在干什么"（服务端铸造的过程状态）。
    // 没有过程状态（历史会话 / 本轮还没开始）才回落到会话的**终态归类**——那是另一件事，
    // 它说得出「已完成/已取消」，说不出「正在执行 Bash」。
    const metaText = streaming
      ? '进行中'
      : narration.label
        ? narration.label
        : activeSum
          ? statusLabel(activeSum.status)
          : '未知状态'
    return (
      <div
        style={{
          flex: 'none',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 12,
          // 长名字**截断**，不把右侧动作按钮挤到第二行（需求 7）。左侧块可伸缩、右侧不参与压缩。
          flexWrap: 'nowrap',
          // nowrap 会让这个 flex 项在**列方向**上的自动最小尺寸变成 min-content（长标题 + 右侧按钮
          // 的宽度），把整个会话区撑出视口。显式 minWidth:0 才允许它随窗口收缩。
          minWidth: 0,
          overflow: 'hidden',
          padding: '10px 20px',
          borderBottom: `1px solid ${tokens.border}`,
          background: tokens.surface,
        }}
      >
        <div style={{ flex: '1 1 auto', minWidth: 0 }}>
          <div style={{ fontSize: 14.5, fontWeight: 650, letterSpacing: '-0.01em', lineHeight: 1.35, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={title}>
            {title}
          </div>
          <div
            style={{
              marginTop: 4,
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              flexWrap: 'wrap',
              minWidth: 0,
              fontFamily: tokens.fontMono,
              fontSize: 10.5,
              color: tokens.textSecondary,
            }}
          >
            {/* v21：这里原来有一枚底座徽标（RuntimeBadge）。底座由构建期固定、用户没有可选项，
                对话框页面不该常驻报它——底座事实仍在「会话详情」抽屉与管理面可见；
                真正需要用户知道的那一种情况（会话跑在别的底座上、无法继续提问）见下方 foreignRuntime。 */}
            <span style={{ maxWidth: 220, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={activeId}>
              {activeId}
            </span>
          </div>
          {/* 会话跑在别的底座上（历史会话 / 换过构建）：如实说明并给出路——
              归属是执行事实、构建期固定不可改，所以平台不会"自动改绑"，只能导出历史另开会话。 */}
          {foreignRuntime && (
            <div
              style={{
                marginTop: 4,
                display: 'inline-flex',
                alignItems: 'center',
                gap: 4,
                fontSize: 11.5,
                color: tokens.warning,
              }}
            >
              <InfoCircleOutlined />
              该会话由另一个版本创建，当前版本无法继续提问（历史仍可阅读）：可导出历史后新建会话继续。
            </div>
          )}
        </div>
        <div style={{ display: 'inline-flex', alignItems: 'center', gap: 10, flex: 'none' }}>
          <Dropdown menu={headMenu} trigger={['click']} placement="bottomRight">
            <Button
              size="small"
              type="text"
              icon={<MoreOutlined />}
              title="会话操作"
              aria-label="会话操作"
              style={{ color: tokens.textSecondary }}
            />
          </Dropdown>
          {/* 会话信息切换（合并原「产物」「会话详情」两颗按钮）：宽屏开合右栏、窄屏开抽屉。
              收起态仍用角标提示「有待确认 / 有产物」，避免合并后丢掉这两个感知入口。 */}
          <Button
            size="small"
            type={infoOpen && wide ? 'default' : 'text'}
            icon={<PicRightOutlined />}
            title={wide ? (infoOpen ? '收起会话信息' : '展开会话信息') : '会话信息'}
            aria-label="会话信息"
            aria-pressed={wide ? infoOpen : undefined}
            onClick={() => {
              if (wide) {
                toggleInfo()
              } else {
                setDetailOpen(true)
              }
            }}
            style={{ color: tokens.textSecondary, fontSize: 12 }}
          >
            会话信息
            {!infoOpen && (artifactCount > 0 || pendingPerms > 0) && (
              <span
                style={{
                  marginLeft: 4,
                  fontFamily: tokens.fontMono,
                  fontSize: 10.5,
                  color: pendingPerms > 0 ? tokens.warning : tokens.textSecondary,
                }}
              >
                {pendingPerms > 0 ? `待确认 ${pendingPerms}` : artifactCount}
              </span>
            )}
          </Button>
          <span
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 7,
              fontFamily: tokens.fontMono,
              fontSize: 11,
              color: tokens.textSecondary,
            }}
          >
            {(streaming || narration.tone === 'run' || narration.tone === 'wait') && (
              <span
                className="fg-anim-pulse"
                style={{
                  width: 7,
                  height: 7,
                  borderRadius: '50%',
                  // 等待态用警示色：那是最需要用户看见的一档（「等待你确认授权」）。
                  background: narration.tone === 'wait' ? tokens.warning : tokens.primary,
                  display: 'inline-block',
                }}
              />
            )}
            <span
              style={{
                color: narration.tone === 'wait' ? tokens.warning : streaming ? tokens.text : tokens.textSecondary,
                fontWeight: streaming || narration.tone === 'wait' ? 600 : 400,
              }}
            >
              {metaText}
            </span>
          </span>
          {resendable && (
            <Button size="small" onClick={() => lastUserText && void sendPrompt(lastUserText)} >
              重发
            </Button>
          )}
        </div>
      </div>
    )
  }

  const renderEmptyTranscript = () => {
    return (
      <div
        style={{
          padding: '12vh 24px 0',
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          textAlign: 'center',
          gap: 10,
        }}
      >
        <div
          style={{
            width: 44,
            height: 44,
            borderRadius: 12,
            border: `1px solid ${tokens.border}`,
            background: tokens.surface,
            display: 'grid',
            placeItems: 'center',
            color: tokens.text,
          }}
        >
          <svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true">
            <path d="M21 15a2 2 0 0 1-2 2H8l-5 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v10z" />
          </svg>
        </div>
        <h1 style={{ margin: '4px 0 0', fontSize: 19, fontWeight: 650, letterSpacing: '-0.01em' }}>问点什么？</h1>
        {/* v21：这里原来报"当前会话跑在底座 X 上"——底座信息不进对话框页面（见 renderChatHead）。
            换成只讲用户能感知的行为：在下方输入，回复怎么出现。 */}
        <p style={{ margin: 0, maxWidth: '46ch', color: tokens.textSecondary, fontSize: 13.5, lineHeight: 1.7 }}>
          在下方输入问题，回复将以流式事件渲染：思考、工具调用与正文实时出现，可随时停止。
        </p>
      </div>
    )
  }

  return (
    <div style={{ flex: 1, minWidth: 0, minHeight: 0, display: 'flex', flexDirection: 'column', background: tokens.bg }}>
      <div style={{ flex: 1, minWidth: 0, minHeight: 0, display: 'flex', position: 'relative' }}>
        <section style={{ flex: 1, display: 'flex', flexDirection: 'column', minWidth: 0, minHeight: 0, background: tokens.bg }}>
          {!activeId ? (
            // 未选中会话 = 新建会话面板（不再有独立的「开始问答」空态页）；
            // 直达 /sessions/:id 的首帧（paramId 已就位、activate 未落地）留空，避免闪一下面板。
            paramId ? null : <StartPanel />
          ) : (
            <>
              {renderChatHead()}
              <div ref={scrollRef} style={{ flex: 1, overflowY: 'auto', minHeight: 0, scrollBehavior: 'smooth' }}>
                <div
                  style={{
                    maxWidth: 840,
                    margin: '0 auto',
                    padding: '8px 24px 12px',
                    display: 'flex',
                    flexDirection: 'column',
                    gap: 0,
                    boxSizing: 'border-box',
                  }}
                >
                  {(view?.entries.length ?? 0) === 0 ? (
                    hydrating ? (
                      <div style={{ paddingTop: 56, textAlign: 'center', color: tokens.textSecondary, fontSize: 13 }}>
                        连接会话…
                        <span className="fg-anim-blink" style={{ display: 'inline-block', marginLeft: 6, width: 2, height: '1em', background: tokens.primary, verticalAlign: '-0.12em' }} />
                      </div>
                    ) : (
                      renderEmptyTranscript()
                    )
                  ) : (
                    <>
                      {/* C2：服务端更早一页（before 游标）——SQLite 模式下可一直翻到会话第一条；
                          翻到最早后隐藏。与下方「折叠 N 条」是两层：这里拉数据，那里展开已加载的条目。 */}
                      {activeId && !historyStart[activeId] && !streaming && (
                        <div style={{ display: 'flex', justifyContent: 'center' }}>
                          <Button
                            size="small"
                            type="link"
                            loading={loadingOlder}
                            onClick={async () => {
                              setLoadingOlder(true)
                              try {
                                await loadOlder(activeId)
                              } finally {
                                setLoadingOlder(false)
                              }
                            }}
                          >
                            加载更早的消息
                          </Button>
                        </div>
                      )}
                      {entryWindow.hidden > 0 && (
                        <div style={{ display: 'flex', justifyContent: 'center' }}>
                          <Button
                            size="small"
                            onClick={() => {
                              const el = scrollRef.current
                              if (el) pendingAnchorRef.current = el.scrollHeight
                              setRevealed((n) => n + TRANSCRIPT_WINDOW)
                            }}
                          >
                            显示更早的 {Math.min(TRANSCRIPT_WINDOW, entryWindow.hidden)} 条（共折叠 {entryWindow.hidden} 条）
                          </Button>
                        </div>
                      )}
                      {rows.map((row) => {
                        if (row.kind === 'turn') {
                          const usage = row.endSeq != null ? runsBySeq.get(row.endSeq) : undefined
                          const flash = !!flashEntryId && row.entryIds.includes(flashEntryId)
                          return (
                            <div
                              key={row.id}
                              style={{
                                display: 'flex',
                                flexDirection: 'column',
                                borderRadius: 12,
                                transition: 'box-shadow .35s ease',
                                boxShadow: flash ? `0 0 0 2px ${tokens.primary}` : 'none',
                              }}
                            >
                              <TurnView
                                turn={row}
                                elapsed={elapsed}
                                stage={turnStatus.stage}
                                showCaret={turnStatus.showCaret}
                                onThoughtToggle={(id, open) => setFoldOpen((prev) => ({ ...prev, [id]: open }))}
                                onSettled={settleScroll}
                                usageText={usage?.usageText}
                                runInfo={usage?.info}
                                stopReason={row.stopReason}
                                flashId={flashEntryId}
                                indexOf={(id) => entryIndex.get(id) ?? 0}
                              />
                            </div>
                          )
                        }
                        const entry = row.entry
                        return (
                          <div
                            key={entry.id}
                            data-fg-idx={entryIndex.get(entry.id) ?? 0}
                            data-fg-entry={entry.id}
                            style={{
                              display: 'flex',
                              flexDirection: 'column',
                              borderRadius: 12,
                              transition: 'box-shadow .35s ease',
                              boxShadow: flashEntryId === entry.id ? `0 0 0 2px ${tokens.primary}` : 'none',
                            }}
                          >
                            <EntryView
                              e={entry}
                              onRetry={entry === lastEntry ? retryLast : undefined}
                              usageText={entry.seq != null ? runsBySeq.get(entry.seq)?.usageText : undefined}
                              runInfo={entry.seq != null ? runsBySeq.get(entry.seq)?.info : undefined}
                            />
                          </div>
                        )
                      })}
                    </>
                  )}
                </div>
              </div>
              {activeId && (
                <>
                  {/* 确认区（S9）：输入区上方常驻；条目流里未结算的卡已不再渲染，所以这里是
                      用户能作答的**唯一**去处。空则整块不占位。 */}
                  <ConfirmArea entries={view?.entries ?? []} />
                  <QueuedInputs
                    queue={view?.queue ?? []}
                    cancelling={cancellingInput}
                    onCancel={(inputId) => {
                      setCancellingInput(inputId)
                      void cancelQueuedInput(inputId).finally(() => setCancellingInput(null))
                    }}
                    onMove={(inputId, beforeId) => void moveQueuedInput(inputId, beforeId)}
                    onSendNow={(inputId) => void sendQueuedInputNow(inputId)}
                    onPop={(inputId) => {
                      // 取出编辑：正文回到输入框（**不**立刻发出去——用户是拿去改的）。
                      void popQueuedInput(inputId).then((text) => {
                        if (text === null) return
                        setInjectedDraft((prev) => ({ text, nonce: (prev?.nonce ?? 0) + 1 }))
                      })
                    }}
                  />
                  <Composer
                    busy={busy}
                    canStop={canStop}
                    onStop={() => void cancel()}
                    // 已有会话不再展示工作空间 chip（工作空间创建即定稿，重复一行是噪声；
                    // 仍可在会话信息浮层「所属空间」看到）。原先这里按 activeSum.cwd 拼 workspace 传下去。
                    // 只传**等待态**文案（等授权/等回答）：它由 Composer 渲染在卡上方一行（.fg-wait-banner）。
                    // 进行中的"在跑"不再由一句「run 进行中」旁白表达——那已由卡片品牌描边（.fg-running）承担，
                    // 且真实阶段（如「正在执行 Bash」）在 ChatHead 的 metaText 里已经在说（见上方 metaText）。
                    busyNote={narration.tone === 'wait' ? narration.label : undefined}
                    placeholder={busy ? '正在回复…' : '向助手提问…（Enter 发送）'}
                    onSend={onSend}
                    // 命令发现面（ADR-17）：底座自报的斜杠命令，undefined = 该底座不提供发现面
                    // （dsh 的 ACP 面不含命令）。输入框里打 `/` 时据此给候选，平台不声明这些命令。
                    commands={view?.commands}
                    injectDraft={injectedDraft ?? undefined}
                    // 会话内改能力（M3-8）：`+` 菜单 + 专家/技能/连接器 chip。能力集是**会话钉住的**
                    // 那一份（`activeSum.capabilities` 是服务端读面快照，改动后由 store 用响应替换）。
                    sessionId={activeId}
                    capabilities={activeSum?.capabilities}
                    // 底座对不上（历史会话跑在别的底座上）时禁掉入口：那条会话连 prompt 都发不出去，
                    // 改能力只会换来一次「能力不可达」的拒绝。
                    capabilitiesDisabled={foreignRuntime}
                    // 会话级模型 chip（原型 §12）：候选 = 运行时广告 ∩ 平台清单（modelValues 收窄）。
                    model={activeSum?.model ?? null}
                    // 反查不到候选时用它兜底展示（`mdl_…` 原始 id 不上界面，同 SessionInfoPopover 口径）。
                    modelLabel={activeSum?.model_name}
                    modelOptions={modelValues(modelCatalog ?? undefined)}
                    onModelChange={(v) => {
                      if (activeId) void updateSessionModel(activeId, v ?? undefined)
                    }}
                  />
                </>
              )}
            </>
          )}
        </section>
        {/* 会话信息右栏（宽屏 ≥1280 且已展开时才渲染；默认收起 → 主列居中）。
            内容 = SessionInfoPanel（与抽屉同源），不再是只读残缺副本。
            新建会话（!activeId）时 infoOpen 无意义，此处也因无 activeId 不渲染。 */}
        {activeId && wide && infoOpen && (
          <aside className="fg-info-panel" aria-label="会话信息">
            <SessionInfoPanel sessionId={activeId} />
          </aside>
        )}
      </div>
      {activeId && !wide && (
        <SessionDetailDrawer
          open={detailOpen}
          sessionId={activeId}
          onClose={() => setDetailOpen(false)}
        />
      )}
    </div>
  )
}

function statusLabel(status: string): string {
  const map: Record<string, string> = {
    in_progress: '进行中',
    completed: '已完成',
    cancelled: '已取消',
    failed: '失败',
  }
  return map[status] ?? status
}
