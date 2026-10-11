import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button } from 'antd'
import { ArrowUpOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { modelValues } from '@ark-mate/types'
import { useMate } from '../store'
import { AgentBadge } from './AgentBadge'
import { ModelSelect } from './ModelSelect'
import { WorkspacePicker, type WorkspaceChoice } from './WorkspacePicker'
import { mostRecentlyUsedWorkspaceId } from '../console/workspaceView'
import { CapabilityPicker } from './CapabilityPicker'
import { FOLLOW_DEFAULT, selectionPayload, type CapabilityChoice } from '../console/capabilityView'
import { getSendKey, sendKeyHint, shouldSendKey, toggleSendKey, type SendKeyMode } from '../console/sendKey'

// 新建会话时记住的「上次所选模型」。
// 键名前缀随 M0-1 的前端产品包改名统一为 `mate.web.*`（与其它前端持久键同前缀）；
// 旧键名不回填——代价是既有本地选择在改名后一次性回到默认模型。
const LAST_MODEL_KEY = 'mate.web.lastModel'

// 示例指令（参照 AionUi /guid「试试这些指令」：`pages/guid/GuidPage.tsx`）
const EXAMPLES = [
  '了解一下这个项目，用大白话告诉我它是做什么的、从哪里入手',
  '帮我找出这个文件夹里重复和没用的文件，列出来让我确认',
  '帮我做一个精美的个人主页网页，做好后直接打开给我看',
]

/**
 * 新建会话面板（hero + 内嵌输入卡 + 示例指令）。
 * 未选中会话时的问答区就是这个面板——不再有独立的「开始问答」空态页，
 * 也不再单独占一条路由（`/conversations` 无 id 即新建）。新建固定用「当前 Agent」
 * （全局默认单例，可在「设置 · Agent 管理」切换——这里只如实标出身份 + 给出切换入口，不在此处做选择器）。
 */
export function StartPanel() {
  const navigate = useNavigate()
  const agents = useMate((s) => s.agents)
  const createAndRun = useMate((s) => s.createAndRun)
  const modelCatalog = useMate((s) => s.modelCatalog)
  const loadModelCatalog = useMate((s) => s.loadModelCatalog)

  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [sendMode, setSendMode] = useState<SendKeyMode>(getSendKey)
  const [model, setModel] = useState<string | null>(() => {
    const v = localStorage.getItem(LAST_MODEL_KEY)
    return v && v.trim() ? v : null
  })
  // 工作空间选择（v14）：`null` = 未指定（会话落在**默认工作空间**）；否则是选中的登记 id
  // （v14.3 起这里只剩 id：选择面删掉「输入路径」后，前端不再产生裸绝对路径这种值）。
  //
  // v14.1 改口径：新建会话**默认预选「最近使用的工作空间」**（用户明确要求，取代 v14 的
  // "每次都从未指定开始"）。理由不冲突——工作空间确实是会话的沙箱边界，所以这里只做**预选**：
  // 选择器在面板上如实显示这个目录，用户看得见、发送前随时能改；不预选反而会出现
  // "我上一轮就在 ark-mate 里干活，这一轮却又落回默认目录"这种需要用户自己重新翻一遍的默认值。
  // 判定与优先级只有一份：`console/workspaceView.ts`；拿不到登记表就退回"未指定"。
  const [workspace, setWorkspace] = useState<WorkspaceChoice>({ id: null })
  // 用户显式选过之后（含左栏「＋」预选），默认值不再回写——否则一次列表刷新
  // 就会把用户刚选好的目录顶掉，而那看起来像"我的选择没生效"。
  const [workspacePicked, setWorkspacePicked] = useState(false)
  const pickWorkspace = (next: WorkspaceChoice) => {
    setWorkspacePicked(true)
    setWorkspace(next)
  }
  const workspaces = useMate((s) => s.workspaces)
  const preferredWorkspaceId = useMate((s) => s.preferredWorkspaceId)
  const setPreferredWorkspace = useMate((s) => s.setPreferredWorkspace)

  // 左栏分组行的「＋」把工作空间预选进来（设计 §5.1 两个面的唯一桥）。
  //
  // 消费后**立即清空**：它是一次性意图——不清的话，用户手动改成别的目录、再打开一次新建面板，
  // 会被旧值顶回去，而这看起来像"我的选择没生效"。
  useEffect(() => {
    if (!preferredWorkspaceId) return
    setWorkspacePicked(true)
    setWorkspace({ id: preferredWorkspaceId })
    setPreferredWorkspace(null)
  }, [preferredWorkspaceId, setPreferredWorkspace])

  // 默认值：登记表到达后（首帧它可能还在路上）预选最近使用的那一条。
  useEffect(() => {
    if (workspacePicked) return
    const id = mostRecentlyUsedWorkspaceId(workspaces)
    if (!id) return
    setWorkspace({ id })
  }, [workspaces, workspacePicked])

  // 能力选定（M3）：缺省 = 两个 kind 都不限制（请求体里不加 capabilities 键，逐字节等同旧行为）。
  //
  // 与工作空间相反，这个选择**保留上一次的**：它不是沙箱边界，而更像"这次任务的工具箱"——
  // 连着做同一类任务时每次重选一遍是纯负担；且它不影响新任务能读到/改到哪些文件，
  // 复用不会造成"静默继承上一个任务的边界"那类问题。不想要了就点回「跟随默认」。
  const [capabilities, setCapabilities] = useState<CapabilityChoice>(FOLLOW_DEFAULT)

  // 新建固定用「当前 Agent」（后端 agent_id 缺省时取它；缺省回落内置种子 dsh），前端无需再传 agent。
  const defaultAgent = agents.find((a) => a.is_default) ?? agents[0]
  const hasAgent = agents.length > 0
  const empty = draft.trim().length === 0
  const canSend = hasAgent && !empty && !sending

  // 候选全部来自运行时自报的配置项（平台无内置清单）：category=model 的候选值；
  // 为空 = 运行时没有可切换的模型（空态提示 + note/登录引导）。
  const modelOptions = modelValues(modelCatalog ?? undefined)
  const catalogNote = modelCatalog?.note ?? ''
  const authHint = (modelCatalog?.auth_guidance ?? [])
    .map((g) => (g.terminal && g.command?.length ? `${g.name}：${g.command.join(' ')}` : g.name))
    .join(' ｜ ')

  // 拉取「当前 Agent」的模型目录（后端按 agent 从运行时会话/缓存/resume 取样）。
  useEffect(() => {
    if (!defaultAgent) return
    void loadModelCatalog(defaultAgent.id)
    // defaultAgent 引用随 agents 每次刷新变化，仅以 id 判定触发。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [defaultAgent?.id, loadModelCatalog])

  // 本地记住的模型若已不在运行时候选里（换了底座/换了已配置的 provider），就地清掉：
  // 否则会把运行时不再广告的值发出去 → 建会话被拒（宁可回到「跟随默认」，不留死状态）。
  useEffect(() => {
    if (!model || modelOptions.length === 0) return
    if (!modelOptions.some((v) => v.value === model)) {
      setModel(null)
      localStorage.removeItem(LAST_MODEL_KEY)
    }
  }, [model, modelOptions])

  // 选中的登记项**不在这里校验**目录是否存在：它的目录状态由 `GET /workspaces` 的 `dir_status`
  // 下发，选择器已经据此禁用了不可用的那一行（拿 id 去 `browse` 是问错了接口）。
  // v14.3 删掉了这里原先对"裸路径"的校验——选择面收敛后前端不再产生裸路径（`WorkspaceChoice`
  // 只剩 id），留着那段等于维护一条走不到的路径。

  const send = async () => {
    if (!canSend) return
    setSending(true)
    try {
      const id = await createAndRun(
        draft.trim(),
        model ? { modelID: model } : undefined,
        workspace.id,
        // UI 三态（null = 未限制）→ wire 形态（键缺失 = 未限制）：两个 kind 都没限制时返回
        // undefined，请求体里连 capabilities 键都不出现。转换只此一处，别的层不再判。
        selectionPayload(capabilities),
      )
      if (model) localStorage.setItem(LAST_MODEL_KEY, model)
      if (id) navigate(`/conversations/${id}`)
    } finally {
      setSending(false)
    }
  }

  return (
    <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', background: tokens.bg }}>
      <div
        style={{
          maxWidth: 780,
          width: '100%',
          margin: '0 auto',
          padding: '11vh 28px 0',
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          gap: 26,
          boxSizing: 'border-box',
        }}
      >
        {/* hero */}
        <h1 style={{ margin: 0, fontSize: 27, fontWeight: 680, letterSpacing: '-0.02em', color: tokens.text }}>
          Hi，今天有什么安排？
        </h1>

        {/* Agent 列表已隐去：新建固定用「当前 Agent」，不在此展示可选 agent 选项卡。
            但「现在用的是哪个 agent」必须如实标出（否则面板上完全看不出身份），
            并给出切换入口——切换本身在设置区「Agent 管理」（新建流程不做 agent 选择器）。 */}
        {defaultAgent ? (
          <div
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 10,
              flexWrap: 'wrap',
              justifyContent: 'center',
              marginTop: -8,
            }}
          >
            <AgentBadge agent={defaultAgent} prefix="当前 Agent" />
            <span style={{ fontSize: 11.5, color: tokens.textSecondary }}>
              新建会话使用当前 Agent（会话与 Agent 一对一绑定）·
            </span>
            <Link to="/settings/agents" style={{ fontSize: 11.5, color: tokens.primary, textDecoration: 'none' }}>
              切换 Agent
            </Link>
          </div>
        ) : (
          <div style={{ color: tokens.textPlaceholder, fontSize: 12.5 }}>暂无可用 Agent（服务不可达或未播种）</div>
        )}

        {/* 内嵌输入卡 */}
        <div
          className="fg-composer"
          style={{
            width: '100%',
            border: `1px solid ${tokens.border}`,
            borderRadius: 16,
            background: tokens.surface,
            padding: '14px 16px 10px',
            display: 'flex',
            flexDirection: 'column',
            gap: 10,
            boxSizing: 'border-box',
          }}
        >
          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (shouldSendKey(e, sendMode)) {
                e.preventDefault()
                void send()
              }
            }}
            onPaste={(e) => {
              if (!e.clipboardData || !e.clipboardData.types.includes('text/html')) return
              const plain = e.clipboardData.getData('text/plain')
              e.preventDefault()
              const ta = e.currentTarget
              const s = ta.selectionStart
              const en = ta.selectionEnd
              setDraft(draft.slice(0, s) + plain + draft.slice(en))
              requestAnimationFrame(() => {
                const p = s + plain.length
                ta.setSelectionRange(p, p)
              })
            }}
            rows={2}
            placeholder={'输入你的问题，或点下方示例…'}
            style={{
              width: '100%',
              boxSizing: 'border-box',
              border: 'none',
              outline: 'none',
              resize: 'none',
              background: 'transparent',
              color: tokens.text,
              fontSize: 15,
              lineHeight: 1.7,
              fontFamily: 'inherit',
              minHeight: 56,
            }}
          />

          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
            {/* 左栏必须可裁剪：窄容器下压到很窄时，nowrap 文案要省略号收起，
                而不是溢出到右栏下面被选择器白底盖住（那会渲染成「文字重影」）。 */}
            <span
              style={{
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'flex-start',
                gap: 3,
                flex: '1 1 auto',
                minWidth: 0,
                overflow: 'hidden',
              }}
            >
              {/* 「在项目中工作」= 会话工作空间（对应 goose 底栏的 DirSwitcher 位置）。
                  它是这次会话的沙箱边界，所以未选时也不留白：药丸显示动作「选择目录」，
                  hover 说明未指定时由平台分配，不让用户以为这个设置没生效。 */}
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, minWidth: 0, maxWidth: '100%' }}>
                <span
                  style={{
                    flex: 'none',
                    fontFamily: tokens.fontMono,
                    fontSize: 11.5,
                    color: tokens.textSecondary,
                    whiteSpace: 'nowrap',
                  }}
                >
                  在项目中工作
                </span>
                <WorkspacePicker value={workspace} onChange={pickWorkspace} disabled={sending} />
              </span>
              {/* 「这个会话用哪些能力」= 会话级能力选定（M3）。默认不限制，限了才占位显示明细：
                  与工作空间那栏同样的理由——不留白，但也不给未设置的状态编一个假值。 */}
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, minWidth: 0, maxWidth: '100%' }}>
                <span
                  style={{
                    flex: 'none',
                    fontFamily: tokens.fontMono,
                    fontSize: 11.5,
                    color: tokens.textSecondary,
                    whiteSpace: 'nowrap',
                  }}
                >
                  可用的能力
                </span>
                <CapabilityPicker
                  agentId={defaultAgent?.id}
                  value={capabilities}
                  onChange={setCapabilities}
                  disabled={sending}
                />
              </span>
              <button
                type="button"
                title="点击切换发送键：Enter 发送 / Enter 换行"
                onClick={() => setSendMode((m) => toggleSendKey(m))}
                className="fg-hint-kbd"
                style={{ fontFamily: tokens.fontMono, fontSize: 10.5, color: tokens.textPlaceholder }}
              >
                {sendKeyHint(sendMode)} ⇄
              </button>
            </span>

            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
              {/* 会话级模型选择器：关闭态只显示名称；候选、描述与空目录提示都在下拉里
                  （下拉即「浏览全部」，不再单独开弹窗）。创建时经 set_config_option 应用。
                  宽度由 .fg-model-pill 管（静息贴合文案、聚焦展开），这里不再固定 240px。 */}
              <ModelSelect
                values={modelOptions}
                value={model}
                onChange={(v) => setModel(v)}
                allowClear
                size="small"
                placeholder={modelOptions.length === 0 ? '运行时无可用模型' : '模型 · 跟随默认'}
                emptyNote={[catalogNote, authHint].filter(Boolean).join(' ｜ ') || '运行时未提供模型目录'}
              />

              <Button
                type="primary"
                shape="circle"
                icon={<ArrowUpOutlined />}
                disabled={!canSend}
                loading={sending}
                onClick={() => void send()}
                style={{ width: 38, height: 38, boxSizing: 'border-box', flex: 'none' }}
              />
            </span>
          </div>
        </div>

        {/* 示例指令 */}
        <div style={{ width: '100%', display: 'flex', flexDirection: 'column', gap: 4 }}>
          <div style={{ fontFamily: tokens.fontMono, fontSize: 11.5, color: tokens.textSecondary, marginBottom: 6 }}>
            试试这些指令
          </div>
          {EXAMPLES.map((ex) => (
            <button
              key={ex}
              type="button"
              onClick={() => setDraft(ex)}
              style={{
                textAlign: 'left',
                background: 'transparent',
                border: 'none',
                padding: '6px 2px',
                cursor: 'pointer',
                color: tokens.textSecondary,
                fontSize: 13.5,
                lineHeight: 1.6,
                fontFamily: 'inherit',
              }}
            >
              {ex}
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
