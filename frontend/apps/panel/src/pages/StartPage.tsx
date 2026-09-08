import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button } from 'antd'
import { ArrowUpOutlined } from '@ant-design/icons'
import { tokens, Pill, agentStateMeta, Dot } from '@ark/ui'
import { useForge } from '../store'
import { ConversationRail } from '../components/ConversationRail'
import { StatusBar } from '../components/StatusBar'
import { shortFor } from '../console/util'
import { getSendKey, sendKeyHint, shouldSendKey, toggleSendKey, type SendKeyMode } from '../console/sendKey'

// 示例指令（参照 AionUi /guid「试试这些指令」：`pages/guid/GuidPage.tsx`）
const EXAMPLES = [
  '了解一下这个项目，用大白话告诉我它是做什么的、从哪里入手',
  '帮我找出这个文件夹里重复和没用的文件，列出来让我确认',
  '帮我做一个精美的个人主页网页，做好后直接打开给我看',
]

const LS_LAST_AGENT = 'forge.panel.lastAgentId'

/**
 * 新建会话整页（路由 /new，参照 AionUi /guid：无弹窗）。
 * hero + 助手胶囊栏（选 Agent）+ 内嵌输入卡（模型 pill + 发送）+ 示例指令。
 */
export function StartPage() {
  const navigate = useNavigate()
  const agents = useForge((s) => s.agents)
  const createAndRun = useForge((s) => s.createAndRun)

  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const [sendMode, setSendMode] = useState<SendKeyMode>(getSendKey)

  // 预选逻辑：① URL 显式 ?agent= 优先（软清空跳转带同 Agent）；② 否则上次使用（localStorage）；③ 否则第一个可用。
  useEffect(() => {
    if (selectedId && agents.some((a) => a.id === selectedId)) return
    const urlAgent = new URLSearchParams(window.location.search).get('agent')
    const fromUrl = urlAgent ? agents.find((a) => a.id === urlAgent) : undefined
    if (fromUrl) {
      pick(fromUrl.id)
      return
    }
    const last = localStorage.getItem(LS_LAST_AGENT)
    const prev = agents.find((a) => a.id === last)
    setSelectedId(prev?.id ?? agents[0]?.id ?? null)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agents, selectedId])

  const selected = agents.find((a) => a.id === selectedId) ?? null
  const empty = draft.trim().length === 0
  const canSend = !!selected && !empty && !sending

  const pick = (id: string) => {
    setSelectedId(id)
    try {
      localStorage.setItem(LS_LAST_AGENT, id)
    } catch {
      /* localStorage 不可用时忽略（仅丢失记忆） */
    }
  }

  const send = async () => {
    if (!canSend || !selected) return
    setSending(true)
    try {
      const id = await createAndRun(selected.id, draft.trim())
      if (id) navigate(`/conversations/${id}`)
    } finally {
      setSending(false)
    }
  }

  return (
    <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', background: tokens.bg }}>
      <div style={{ flex: 1, minHeight: 0, display: 'flex' }}>
        <ConversationRail onCreate={() => navigate('/new')} />

        <section style={{ flex: 1, display: 'flex', flexDirection: 'column', minWidth: 0, minHeight: 0, overflowY: 'auto', background: tokens.bg }}>
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

          {/* 助手胶囊栏（选定 Agent） */}
          {agents.length === 0 ? (
            <div style={{ color: tokens.textPlaceholder, fontSize: 12.5 }}>暂无可用 Agent（服务不可达或未播种）</div>
          ) : (
            <div
              className="fg-agent-picker"
              style={{ display: 'flex', flexWrap: 'wrap', gap: 10, justifyContent: 'center' }}
            >
              {agents.map((a) => {
                const sel = a.id === selectedId
                const st = agentStateMeta(a.state)
                return (
                  <div
                    key={a.id}
                    role="radio"
                    aria-checked={sel}
                    title={`${a.name}${a.state === 'running' ? '' : '（已停止）'}`}
                    onClick={() => pick(a.id)}
                    style={{
                      display: 'inline-flex',
                      alignItems: 'center',
                      gap: 9,
                      padding: '8px 14px 8px 8px',
                      borderRadius: 999,
                      cursor: 'pointer',
                      border: `1px solid ${sel ? tokens.primary : tokens.border}`,
                      background: sel ? tokens.softFill : tokens.surface,
                      color: tokens.text,
                      userSelect: 'none',
                      boxShadow: sel ? `0 0 0 1px ${tokens.primary}` : 'none',
                    }}
                  >
                    <span
                      style={{
                        width: 26,
                        height: 26,
                        borderRadius: '50%',
                        border: `1px solid ${tokens.border}`,
                        background: tokens.softFill,
                        display: 'grid',
                        placeItems: 'center',
                        fontFamily: tokens.fontMono,
                        fontSize: 10,
                        fontWeight: 700,
                        color: tokens.text,
                      }}
                    >
                      {shortFor(a.name || a.id)}
                    </span>
                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 7 }}>
                      <span style={{ fontFamily: tokens.fontMono, fontSize: 13.5, fontWeight: sel ? 700 : 550, whiteSpace: 'nowrap' }}>
                        {a.name}
                      </span>
                      <Dot color={st.color} size={6} />
                    </span>
                  </div>
                )
              })}
            </div>
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
                {/* 模型 pill（只读展示，不做选择器——决策 v6③） */}
                <Pill title="模型由 Agent 自身默认决定；本面板不在此选模型。">
                  模型 · Agent 默认
                </Pill>

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
        </section>
      </div>

      <StatusBar />
    </div>
  )
}
