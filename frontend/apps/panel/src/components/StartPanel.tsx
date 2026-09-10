import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button } from 'antd'
import { ArrowUpOutlined } from '@ant-design/icons'
import { tokens } from '@ark/ui'
import { modelValues } from '@ark/types'
import { useForge } from '../store'
import { ModelSelect } from './ModelSelect'
import { ModelPickerModal } from './ModelPickerModal'
import { getSendKey, sendKeyHint, shouldSendKey, toggleSendKey, type SendKeyMode } from '../console/sendKey'

// 新建会话时记住的「上次所选模型」。
const LAST_MODEL_KEY = 'forge.panel.lastModel'

// 示例指令（参照 AionUi /guid「试试这些指令」：`pages/guid/GuidPage.tsx`）
const EXAMPLES = [
  '了解一下这个项目，用大白话告诉我它是做什么的、从哪里入手',
  '帮我找出这个文件夹里重复和没用的文件，列出来让我确认',
  '帮我做一个精美的个人主页网页，做好后直接打开给我看',
]

/**
 * 新建会话面板（hero + 内嵌输入卡 + 示例指令）。
 * 未选中会话时的问答区就是这个面板——不再有独立的「开始问答」空态页，
 * 也不再单独占一条路由（`/conversations` 无 id 即新建）。新建固定用默认 Agent。
 */
export function StartPanel() {
  const navigate = useNavigate()
  const agents = useForge((s) => s.agents)
  const createAndRun = useForge((s) => s.createAndRun)
  const modelCatalog = useForge((s) => s.modelCatalog)
  const loadModelCatalog = useForge((s) => s.loadModelCatalog)

  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [sendMode, setSendMode] = useState<SendKeyMode>(getSendKey)
  const [model, setModel] = useState<string | null>(() => {
    const v = localStorage.getItem(LAST_MODEL_KEY)
    return v && v.trim() ? v : null
  })
  const [pickerOpen, setPickerOpen] = useState(false)

  // 新建固定用默认 Agent（后端在 agent_id 缺省时落到 dsh），前端无需再传 agent。
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

  // 拉取当前默认 Agent 的模型目录（后端按 agent 从运行时会话/缓存/resume 取样）。
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

  const send = async () => {
    if (!canSend) return
    setSending(true)
    try {
      const id = await createAndRun(
        draft.trim(),
        model ? { modelID: model } : undefined,
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

        {/* Agent 列表已隐去：新建固定用默认 Agent，不在此展示可选 agent/runtime。 */}
        {agents.length === 0 && (
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
            <span
              style={{
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'flex-start',
                gap: 1,
                minWidth: 0,
              }}
            >
              <span style={{ fontFamily: tokens.fontMono, fontSize: 11.5, color: tokens.textSecondary, whiteSpace: 'nowrap' }}>
                在项目中工作
              </span>
              <button
                type="button"
                title="点击切换发送键：Enter 发送 / Enter 换行"
                onClick={() => setSendMode((m) => toggleSendKey(m))}
                className="fg-hint-kbd"
                style={{
                  border: 'none',
                  background: 'transparent',
                  padding: 0,
                  margin: 0,
                  cursor: 'pointer',
                  fontFamily: tokens.fontMono,
                  fontSize: 10.5,
                  color: tokens.textPlaceholder,
                }}
              >
                {sendKeyHint(sendMode)} ⇄
              </button>
            </span>

            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
              {/* 会话级模型选择器：值取自运行时会话配置项，创建时经 set_config_option 应用。 */}
              <ModelSelect
                values={modelOptions}
                value={model}
                onChange={(v) => setModel(v)}
                allowClear
                size="small"
                style={{ minWidth: 180 }}
                placeholder={modelOptions.length === 0 ? '运行时无可用模型' : '模型 · 跟随默认'}
              />

              <Button
                size="small"
                type="text"
                onClick={() => setPickerOpen(true)}
                style={{ color: tokens.textSecondary }}
              >
                浏览全部
              </Button>

              <Button
                type="primary"
                shape="circle"
                icon={<ArrowUpOutlined />}
                disabled={!canSend}
                loading={sending}
                onClick={() => void send()}
                style={{ width: 38, height: 38, boxSizing: 'border-box' }}
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

      <ModelPickerModal
        open={pickerOpen}
        onClose={() => setPickerOpen(false)}
        values={modelOptions}
        value={model}
        emptyHint={[catalogNote, authHint].filter(Boolean).join(' ｜ ') || '运行时未提供模型目录'}
        onSelect={(v) => setModel(v)}
      />
    </div>
  )
}
