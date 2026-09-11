import { useEffect, useRef, useState } from 'react'
import { Button, Select, Tooltip } from 'antd'
import { ArrowUpOutlined, FolderOpenOutlined } from '@ant-design/icons'
import { tokens } from '@ark/ui'
import { getSendKey, sendKeyHint, shouldSendKey, toggleSendKey, type SendKeyMode } from '../console/sendKey'
import { DELIVERY_HINT, DELIVERY_LABEL, type DeliveryChoice } from '../console/queueView'

/** 末级目录名（路径末段；根目录回落到原串，避免展示成空）。 */
function workspaceName(path: string): string {
  const p = path.replace(/[/\\]+$/, '')
  const i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'))
  return i >= 0 ? p.slice(i + 1) || p : p
}

interface Props {
  busy: boolean
  busyNote?: string
  placeholder?: string
  /** 空闲时的发送；忙时按所选投递模式发送（§6）。 */
  onSend: (text: string, delivery?: DeliveryChoice) => void
  autoFocus?: boolean
  /**
   * 本会话的工作空间（只读展示）。工作空间创建时定稿、之后不可变——它是底座 ACP session
   * 的 cwd，也是原生权限 `workspace-write` 的沙箱边界，所以这里没有"改目录"入口：
   * 要换目录就新开一个会话。`temporary` 为服务端权威标志（平台代建）。
   */
  workspace?: { path: string; temporary: boolean }
}

/**
 * 输入区（console.html .composer）：自动增高 textarea + 底部提示行（可切发送键档）+ 发送按钮。
 *
 * §6：会话忙时输入框**不再锁死**——底座跑着的那一轮不该让人干等。忙时多出一个投递模式选择
 * （排队 / 打断并发送），选什么就发什么；空闲时该选择器不出现（没有「忙」可谈，服务端按普通轮处理）。
 */
export function Composer({ busy, busyNote, placeholder, onSend, autoFocus, workspace }: Props) {
  const [draft, setDraft] = useState('')
  const [sendMode, setSendMode] = useState<SendKeyMode>(getSendKey)
  // 投递模式是「这次怎么送」的选择，默认排队——打断当前轮是有代价的动作，不该是默认值。
  const [delivery, setDelivery] = useState<DeliveryChoice>('after_turn')
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
    if (empty) return
    const text = draft.trim()
    setDraft('')
    onSend(text, busy ? delivery : undefined)
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
          autoFocus={autoFocus}
          placeholder={
            busy ? `agent 正在回复…（可${DELIVERY_LABEL[delivery]}：Enter 送入队列）` : placeholder ?? '向当前 Agent 提问…'
          }
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
            {/* 本会话在哪工作：只读（工作空间创建即定稿，要换目录就新开会话）。
                形态对齐 goose 的 DirSwitcher：图标 + 目录名，完整路径走 tooltip；
                平台分配的目录没有有意义的目录名（末级是会话 id），故直接用「平台分配的目录」
                作标签——不去显示一串 uuid 冒充目录名。 */}
            {workspace && (
              <Tooltip
                title={
                  <span style={{ fontFamily: tokens.fontMono, fontSize: 11 }}>
                    {workspace.path}
                    {workspace.temporary && (
                      <>
                        <br />
                        <span style={{ fontFamily: tokens.fontFamily, color: tokens.textPlaceholder }}>
                          平台分配：会话创建时自动分配，创建后不可更改
                        </span>
                      </>
                    )}
                  </span>
                }
                placement="top"
              >
                <span className="fg-ws-pill" style={{ height: 22, flex: 'none', cursor: 'default' }}>
                  <FolderOpenOutlined style={{ fontSize: 10, color: tokens.textPlaceholder }} />
                  <span className="fg-ws-pill-name">
                    {workspace.temporary ? '平台分配的目录' : workspaceName(workspace.path)}
                  </span>
                </span>
              </Tooltip>
            )}
            <button
              type="button"
              title="点击切换发送键：Enter 发送 / Enter 换行"
              onClick={() => setSendMode((m) => toggleSendKey(m))}
              className="fg-hint-kbd"
            >
              {sendKeyHint(sendMode)} ⇄
            </button>
            {busy && (
              // 忙时投递模式：排队（等这轮完）或打断（先取消再发）。解释放在下拉项里，
              // 不塞进按钮——按钮上只留动作本身。
              <Select
                size="small"
                value={delivery}
                onChange={(v) => setDelivery(v)}
                style={{ minWidth: 118 }}
                options={(['after_turn', 'interrupt_then_prompt'] as DeliveryChoice[]).map((d) => ({
                  value: d,
                  label: DELIVERY_LABEL[d],
                  title: DELIVERY_HINT[d],
                }))}
              />
            )}
          </span>
          <Button
            type="primary"
            icon={<ArrowUpOutlined style={{ fontSize: 13 }} />}
            disabled={empty}
            onClick={send}
            style={{ borderRadius: 9, fontWeight: 560 }}
          >
            {busy ? DELIVERY_LABEL[delivery] : '发送'}
          </Button>
        </div>
      </div>
    </div>
  )
}
