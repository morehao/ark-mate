import { useEffect, useRef, useState } from 'react'
import { Button, Modal } from 'antd'
import { tokens } from '@ark/ui'
import { agentStateMeta, Dot } from '@ark/ui'
import { useForge } from '../store'
import { shortFor } from '../console/util'

interface Props {
  open: boolean
  onClose: () => void
  onCreated: (id: string) => void
}

/** 新建会话模态（console.html #new-modal）：选择 Agent 底座 + 首轮 prompt → 创建并发送。 */
export function NewSessionModal({ open, onClose, onCreated }: Props) {
  const agents = useForge((s) => s.agents)
  const createAndRun = useForge((s) => s.createAndRun)

  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [prompt, setPrompt] = useState('')
  const [creating, setCreating] = useState(false)
  const promptRef = useRef<HTMLTextAreaElement | null>(null)

  // 打开时：清空 prompt；默认选中第一个可用 agent
  useEffect(() => {
    if (!open) return
    setPrompt('')
    setCreating(false)
    if (agents.length > 0 && !agents.some((a) => a.id === selectedId)) {
      setSelectedId(agents[0].id)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, agents.length])

  const selected = agents.find((a) => a.id === selectedId) ?? null
  const canStart = !!selected && prompt.trim().length > 0 && !creating

  const start = async () => {
    if (!canStart || !selected) return
    setCreating(true)
    try {
      const id = await createAndRun(selected.id, prompt.trim())
      if (id) onCreated(id)
    } finally {
      setCreating(false)
    }
  }

  return (
    <Modal
      open={open}
      onCancel={onClose}
      width={600}
      footer={null}
      title={
        <div>
          <div style={{ fontSize: 17, fontWeight: 660, letterSpacing: '-0.01em', lineHeight: 1.3 }}>新建会话 · 选择 Agent 底座</div>
          <div style={{ marginTop: 3, fontSize: 12.5, color: tokens.textSecondary }}>
            一次会话固定绑定一个 Agent 底座；首轮 prompt 将随创建命令直接发起。
          </div>
        </div>
      }
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14, marginTop: 6 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, overflowY: 'auto', maxHeight: 264, paddingRight: 4 }}>
          {agents.length === 0 && (
            <div style={{ color: tokens.textPlaceholder, fontSize: 12.5, padding: '10px 2px' }}>
              暂无可用 Agent（服务不可达或未播种）。可回到 Agents 页查看底座状态。
            </div>
          )}
          {agents.map((a) => {
            const sel = a.id === selectedId
            const st = agentStateMeta(a.state)
            return (
              <div
                key={a.id}
                role="radio"
                aria-checked={sel}
                className={`fg-agent-opt${sel ? ' fg-sel' : ''}`}
                onClick={() => setSelectedId(a.id)}
                style={{
                  display: 'flex',
                  gap: 14,
                  alignItems: 'flex-start',
                  padding: '12px 14px',
                  color: tokens.text,
                }}
              >
                <span
                  style={{
                    flex: 'none',
                    width: 34,
                    height: 34,
                    borderRadius: 9,
                    border: `1px solid ${tokens.border}`,
                    background: tokens.softFill,
                    display: 'grid',
                    placeItems: 'center',
                    fontFamily: tokens.fontMono,
                    fontSize: 11,
                    fontWeight: 700,
                    color: tokens.text,
                  }}
                >
                  {shortFor(a.runtime)}
                </span>
                <span style={{ minWidth: 0, flex: 1 }}>
                  <span style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 10, flexWrap: 'wrap' }}>
                    <span style={{ fontFamily: tokens.fontMono, fontSize: 13, fontWeight: 640 }}>{a.name}</span>
                    <span
                      style={{
                        display: 'inline-flex',
                        alignItems: 'center',
                        gap: 5,
                        fontFamily: tokens.fontMono,
                        fontSize: 10.5,
                        color: tokens.textSecondary,
                      }}
                    >
                      <Dot color={st.color} size={6} />
                      {a.state === 'running' ? '可对话' : '已停止'} · {a.runtime}
                    </span>
                  </span>
                  <span style={{ display: 'block', marginTop: 4, fontSize: 12, color: tokens.textSecondary, fontFamily: tokens.fontMono }}>
                    {a.id}
                  </span>
                </span>
              </div>
            )
          })}
        </div>

        <textarea
          ref={promptRef}
          rows={3}
          value={prompt}
          disabled={creating}
          placeholder="首轮 prompt：新建即后台起真实会话并开始回答…"
          onChange={(e) => setPrompt(e.target.value)}
          style={{
            width: '100%',
            boxSizing: 'border-box',
            border: `1px solid ${tokens.border}`,
            borderRadius: 10,
            padding: '9px 11px',
            fontSize: 13.5,
            lineHeight: 1.6,
            background: tokens.surface,
            color: tokens.text,
            resize: 'vertical',
            fontFamily: 'inherit',
            outline: 'none',
          }}
        />

        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
          <span style={{ fontFamily: tokens.fontMono, fontSize: 10.5, color: tokens.textSecondary }}>
            REST 创建会话 → WS attach 收流
          </span>
          <span style={{ display: 'inline-flex', gap: 8 }}>
            <Button onClick={onClose} disabled={creating}>
              取消
            </Button>
            <Button type="primary" disabled={!canStart} loading={creating} onClick={() => void start()}>
              开始问答
            </Button>
          </span>
        </div>
      </div>
    </Modal>
  )
}
