import { useEffect, useRef, useState } from 'react'
import { Button } from 'antd'
import { ArrowUpOutlined } from '@ant-design/icons'
import { tokens } from '@ark/ui'
import { getSendKey, sendKeyHint, shouldSendKey, toggleSendKey, type SendKeyMode } from '../console/sendKey'

interface Props {
  busy: boolean
  busyNote?: string
  placeholder?: string
  onSend: (text: string) => void
  autoFocus?: boolean
}

/** 输入区（console.html .composer）：自动增高 textarea + 底部提示行（可切发送键档）+ 发送按钮。 */
export function Composer({ busy, busyNote, placeholder, onSend, autoFocus }: Props) {
  const [draft, setDraft] = useState('')
  const [sendMode, setSendMode] = useState<SendKeyMode>(getSendKey)
  const taRef = useRef<HTMLTextAreaElement | null>(null)

  const empty = draft.trim().length === 0

  // 自动增高
  useEffect(() => {
    const ta = taRef.current
    if (!ta) return
    ta.style.height = 'auto'
    ta.style.height = `${Math.min(ta.scrollHeight, 180)}px`
  }, [draft])

  const send = () => {
    if (busy || empty) return
    const text = draft.trim()
    setDraft('')
    onSend(text)
    taRef.current?.focus()
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (shouldSendKey(e, sendMode)) {
      e.preventDefault()
      send()
    }
  }

  // 粘贴时若带富文本（HTML）则改为纯文本插入，避免样式/链接污染正文。
  const onPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    if (!e.clipboardData) return
    if (!e.clipboardData.types.includes('text/html')) return
    const plain = e.clipboardData.getData('text/plain')
    e.preventDefault()
    const ta = taRef.current
    if (!ta) return
    const start = ta.selectionStart
    const end = ta.selectionEnd
    const next = draft.slice(0, start) + plain + draft.slice(end)
    setDraft(next)
    requestAnimationFrame(() => {
      const p = start + plain.length
      ta.setSelectionRange(p, p)
    })
  }

  return (
    <div style={{ flex: 'none', padding: '10px 20px 6px', background: tokens.bg }}>
      <div className="fg-composer" style={{ maxWidth: 840, margin: '0 auto', padding: '10px 12px 8px', display: 'flex', flexDirection: 'column', gap: 6 }}>
        <textarea
          ref={taRef}
          rows={1}
          value={draft}
          disabled={busy}
          autoFocus={autoFocus}
          placeholder={placeholder ?? (busy ? 'agent 正在回复…' : '向当前 Agent 提问…')}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
          style={{
            width: '100%',
            border: 0,
            resize: 'none',
            background: 'transparent',
            color: tokens.text,
            font: 'inherit',
            fontSize: 14.5,
            lineHeight: 1.6,
            minHeight: 24,
            maxHeight: 180,
            padding: '2px 2px',
            outline: 'none',
            opacity: busy ? 0.55 : 1,
          }}
        />
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
          <span
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 12,
              minWidth: 0,
              fontFamily: tokens.fontMono,
              fontSize: 10.5,
              color: tokens.textSecondary,
              whiteSpace: 'nowrap',
              overflow: 'hidden',
            }}
          >
            {busy && busyNote && <span style={{ color: tokens.text, overflow: 'hidden', textOverflow: 'ellipsis' }}>{busyNote}</span>}
            <button
              type="button"
              title="点击切换发送键：Enter 发送 / Enter 换行"
              onClick={() => setSendMode((m) => toggleSendKey(m))}
              className="fg-hint-kbd"
            >
              {sendKeyHint(sendMode)} ⇄
            </button>
            {busy && <span>单飞 turn 门控</span>}
          </span>
          <Button
            type="primary"
            icon={<ArrowUpOutlined style={{ fontSize: 13 }} />}
            disabled={busy || empty}
            onClick={send}
            style={{ borderRadius: 9, fontWeight: 560 }}
          >
            发送
          </Button>
        </div>
      </div>
    </div>
  )
}
