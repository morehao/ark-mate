import { useEffect, useMemo, useRef, useState } from 'react'
import { Button, Select, Tooltip } from 'antd'
import { ArrowUpOutlined, FolderOpenOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { CommandInfo } from '@ark-mate/types'
import { getSendKey, sendKeyHint, shouldSendKey, toggleSendKey, type SendKeyMode } from '../console/sendKey'
import { DELIVERY_HINT, DELIVERY_LABEL, type DeliveryChoice } from '../console/queueView'
import { applyCommandHint, commandHintQuery, matchCommands, moveHintIndex } from '../console/commandHints'

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
  /**
   * 底座自报的可用斜杠命令（`session.commands` 全量事件的投影；ADR-17）。
   * `undefined` = 该底座不提供命令发现面（dsh 即如此）→ 不显示候选；
   * `[]` = 通告了"当前没有命令" → 同样不显示候选，但两者语义不同（见 SessionView.commands）。
   */
  commands?: CommandInfo[]
}

/**
 * 输入区（console.html .composer）：自动增高 textarea + 底部提示行（可切发送键档）+ 发送按钮。
 *
 * §6：会话忙时输入框**不再锁死**——底座跑着的那一轮不该让人干等。忙时多出一个投递模式选择
 * （排队 / 打断并发送），选什么就发什么；空闲时该选择器不出现（没有「忙」可谈，服务端按普通轮处理）。
 *
 * ADR-17 命令发现面：草稿以 `/` 开头且还没打空格时，按底座通告的命令给候选（见 commandHints.ts）。
 * 命令**不是平台的声明**——平台只回显底座自报的清单，选中后也只是把 `/name ` 填进输入框，
 * 发不发、怎么发由用户决定。
 */
export function Composer({ busy, busyNote, placeholder, onSend, autoFocus, workspace, commands }: Props) {
  const [draft, setDraft] = useState('')
  const [sendMode, setSendMode] = useState<SendKeyMode>(getSendKey)
  // 投递模式是「这次怎么送」的选择，默认排队——打断当前轮是有代价的动作，不该是默认值。
  const [delivery, setDelivery] = useState<DeliveryChoice>('after_run')
  // 候选高亮下标；`hintOff` 记录用户按 Esc 主动关掉弹层（继续打字即恢复）。
  const [hintIndex, setHintIndex] = useState(0)
  const [hintOff, setHintOff] = useState(false)
  const taRef = useRef<HTMLTextAreaElement | null>(null)

  const empty = draft.trim().length === 0

  // 候选只在「正在输入命令名」时出现：`commandHintQuery` 判上下文，`matchCommands` 按前缀筛。
  // 没有命令时（dsh 不通告，或底座通告了空列表）自然为空 → 弹层不出现，**不编造占位候选**。
  const hints = useMemo(() => {
    if (hintOff) return []
    const query = commandHintQuery(draft)
    if (query === null) return []
    return matchCommands(commands, query)
  }, [draft, commands, hintOff])
  const hintOpen = hints.length > 0
  // 继续打字会改变候选数：把下标夹回范围内，避免高亮停在已消失的那一条上。
  const hintAt = hintOpen ? Math.min(hintIndex, hints.length - 1) : 0

  const acceptHint = (cmd: CommandInfo) => {
    setDraft(applyCommandHint(cmd))
    setHintIndex(0)
    taRef.current?.focus()
  }

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
    setHintIndex(0)
    onSend(text, busy ? delivery : undefined)
    taRef.current?.focus()
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // 弹层打开时先吃掉这几个键：Enter 是"填入选中的命令"，不是"发送"——
    // 否则用户刚敲完 `/rev` 就被把半截命令发出去了。Esc 关掉弹层后 Enter 恢复发送。
    if (hintOpen) {
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setHintIndex((i) => moveHintIndex(i, 1, hints.length))
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setHintIndex((i) => moveHintIndex(i, -1, hints.length))
        return
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault()
        acceptHint(hints[hintAt])
        return
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        setHintOff(true)
        return
      }
    }
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
        {hintOpen && (
          // 候选列表（贴输入框上方，走文档流：弹层出现时输入框整体上移，不盖住正在打的字）。
          // mousedown 而不是 click：click 要等 mouseup，而 textarea 会先因失焦收起弹层。
          // 页脚放在**滚动区之外**：候选多时（实测通告过 20 余条）说明文字不该被滚没。
          <div className="fg-cmd-pop" role="listbox" aria-label="底座提供的命令">
            <div className="fg-cmd-list">
              {hints.map((c, i) => (
                <button
                  key={c.name}
                  type="button"
                  role="option"
                  aria-selected={i === hintAt}
                  className={i === hintAt ? 'fg-cmd-item fg-on' : 'fg-cmd-item'}
                  title={c.description || undefined}
                  onMouseEnter={() => setHintIndex(i)}
                  onMouseDown={(e) => {
                    e.preventDefault()
                    acceptHint(c)
                  }}
                >
                  <span className="fg-cmd-name">/{c.name}</span>
                  {c.hint && <span className="fg-cmd-hint">{c.hint}</span>}
                  {c.description && <span className="fg-cmd-desc">{c.description}</span>}
                </button>
              ))}
            </div>
            <div className="fg-cmd-foot">↑↓ 选择 · Enter 填入 · Esc 关闭 —— 命令由底座提供，平台只做回显</div>
          </div>
        )}
        <textarea
          ref={taRef}
          rows={1}
          value={draft}
          autoFocus={autoFocus}
          placeholder={
            busy ? `agent 正在回复…（可${DELIVERY_LABEL[delivery]}：Enter 送入队列）` : placeholder ?? '向当前 Agent 提问…'
          }
          onChange={(e) => {
            setDraft(e.target.value)
            // 用户继续打字即恢复弹层（Esc 只关这一次，不是永久静音）。
            if (hintOff) setHintOff(false)
          }}
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
                options={(['after_run', 'interrupt_then_prompt'] as DeliveryChoice[]).map((d) => ({
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
