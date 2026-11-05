import { useEffect, useMemo, useRef, useState } from 'react'
import { Select } from 'antd'
import { ArrowUpOutlined, PaperClipOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { AttachmentMeta, CommandInfo, ConfigOptionValue, SessionCapabilities } from '@ark-mate/types'
import { getSendKey, shouldSendKey } from '../console/sendKey'
import { DELIVERY_HINT, DELIVERY_LABEL, type DeliveryChoice } from '../console/queueView'
import { applyCommandHint, commandHintQuery, matchCommands, moveHintIndex } from '../console/commandHints'
import { useAttachmentDraft } from '../console/useAttachmentDraft'
import { WorkspaceChipStatic } from './WorkspacePicker'
import { ComposerCapabilities } from './ComposerCapabilities'
import { PermissionChip } from './PermissionChip'
import { ModelSelect } from './ModelSelect'
import { AttachmentChip } from './AttachmentChip'

interface Props {
  busy: boolean
  /** 这一轮能不能被停：等待授权/回答时**不可停**（停了等于把用户正要答的那一轮扔掉）。 */
  canStop: boolean
  /** 停止本轮（只在 `busy && canStop && 草稿为空` 时由按钮触发）。 */
  onStop: () => void
  busyNote?: string
  placeholder?: string
  /** 空闲时的发送；忙时按所选投递模式发送（§6）。v32：可携带待发附件。 */
  onSend: (text: string, delivery?: DeliveryChoice, attachments?: AttachmentMeta[]) => void
  /**
   * 当前会话模型是否支持图片输入（v32 门禁；`undefined` = 未知 → 不拦，交给服务端判）。
   * 为 `false` 时，选图片附件会置灰/提示（发送前拦），避免发出去被服务端 40001 model_no_image_input。
   */
  imageSupported?: boolean
  autoFocus?: boolean
  /**
   * 本会话的工作空间（只读展示，贴在输入卡**上沿左上角**的 chip）。工作空间创建时定稿、
   * 之后不可变——它是底座 ACP session 的 cwd，也是原生权限 `workspace-write` 的沙箱边界，
   * 所以这里没有"改目录"入口：要换目录就新开一个会话。`temporary` 为服务端权威标志（平台代建）。
   */
  workspace?: { path: string; temporary: boolean; isDefault: boolean; title?: string }
  /**
   * 底座自报的可用斜杠命令（`session.commands` 全量事件的投影；ADR-17）。
   * `undefined` = 该底座不提供命令发现面（dsh 即如此）→ 不显示候选；
   * `[]` = 通告了"当前没有命令" → 同样不显示候选，但两者语义不同（见 SessionView.commands）。
   */
  commands?: CommandInfo[]
  /**
   * 外部**塞进来的草稿**（`nonce` 变化即生效一次）。
   *
   * 唯一消费者是队列的「取出编辑」（ADR-10 动作③）：用户把一条还没投递的话取回来改，
   * 正文必须回到输入框里——否则"取出"就只是"删掉"。
   *
   * 为什么用 `{text, nonce}` 而不是受控 `value`：输入框的草稿是**组件自己的**状态（有光标、
   * 有本地拼写习惯、有 ↑ 翻历史），改成受控会把每一次按键都提给父组件。窄口进、状态自持。
   */
  injectDraft?: { text: string; nonce: number }
  /**
   * 当前会话 id（有值才渲染输入区的**能力编辑**入口：`+` 菜单 + 专家/技能/连接器选择器）。
   *
   * 新建会话页刻意**不给**：那里走的是 `StartPanel` 的创建流程（选定随 `POST /sessions` 提交），
   * 而这里是"已建会话改能力"（`PATCH /sessions/:id/capabilities`，下一轮生效）。两条路径的
   * 语义不同，入口也就该不同——把创建页也改成 `+` 菜单属于另一刀。
   */
  sessionId?: string
  /** 该会话钉住的能力选定（读面快照；缺失 = 三格都未声明）。 */
  capabilities?: SessionCapabilities | null
  /** 会话不能继续时禁用能力入口（底座与构建期不一致的历史会话：改了也跑不起来）。 */
  capabilitiesDisabled?: boolean
  /**
   * 会话级模型选择（v 原型 §12：模型 chip 靠右、紧贴发送键）。
   *
   * 三个 props 一起给才渲染模型 chip（缺一不显示）——它们来自 ChatPage（`modelCatalog` 与
   * `activeSum.model`），落库走既有的 `PATCH /sessions/:id`（`updateSessionModel`），零新接口。
   */
  model?: string | null
  modelOptions?: ConfigOptionValue[]
  onModelChange?: (value: string | null) => void
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
 *
 * v21 版面：工作空间 chip 从底栏移到卡片上沿左上角（对齐 DSH 的 composerStack：chip 是卡片的
 * 标题行，底栏留给"这次怎么发"）。
 */
export function Composer({
  busy,
  canStop,
  onStop,
  busyNote,
  placeholder,
  onSend,
  imageSupported,
  autoFocus,
  workspace,
  commands,
  injectDraft,
  sessionId,
  capabilities,
  capabilitiesDisabled,
  model,
  modelOptions,
  onModelChange,
}: Props) {
  const [draft, setDraft] = useState('')
  // 「取出编辑」塞回来的草稿：只在 nonce 变化时生效一次（同一条再取一次也要能生效，故用计数）。
  useEffect(() => {
    if (!injectDraft) return
    setDraft(injectDraft.text)
    taRef.current?.focus()
    // 依赖刻意只写 nonce：`injectDraft.text` 变了但 nonce 没变时不该覆盖用户正在打的字。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [injectDraft?.nonce])
  // 发送键档位不缓存进 state：设置里改完立刻生效，不用等这个组件重挂（2026-11-05 起开关在设置里）。
  // 投递模式是「这次怎么送」的选择，默认排队——打断当前轮是有代价的动作，不该是默认值。
  const [delivery, setDelivery] = useState<DeliveryChoice>('after_run')
  // 候选高亮下标；`hintOff` 记录用户按 Esc 主动关掉弹层（继续打字即恢复）。
  const [hintIndex, setHintIndex] = useState(0)
  const [hintOff, setHintOff] = useState(false)
  const taRef = useRef<HTMLTextAreaElement | null>(null)

  // ——— 附件（v32）———：待发附件 chip 的本地态（刷新不保留；pending 由服务端 TTL 收）。
  // 行为与新建会话页（`StartPanel`）**共用**同一份 hook，两处只差版面。
  const { pendingAtts, uploading, dragOver, setDragOver, fileRef, addFiles, removeAtt, clear } =
    useAttachmentDraft(imageSupported)

  const empty = draft.trim().length === 0 && pendingAtts.length === 0
  // 同一颗圆钮的三个状态（D7）：
  //  - 空闲：箭头（空文本禁用）；
  //  - 忙且可停、**草稿为空**：停止方块；忙且可停、草稿非空：按所选投递模式发送（排队/打断）；
  //  - 忙但不可停（等授权/等回答）：禁用的停止态 + 说明原因的 title（不给一个"点了也没用"的按钮）。
  const stopMode = busy && canStop && empty
  const waitMode = busy && !canStop
  const sendDisabled = waitMode || (!busy && empty)
  const buttonLabel = waitMode ? '等待你回答后才能停止' : stopMode ? '停止' : busy ? DELIVERY_LABEL[delivery] : '发送'

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
    if (empty || uploading) return
    const text = draft.trim()
    const atts = pendingAtts
    setDraft('')
    setHintIndex(0)
    clear()
    onSend(text, busy ? delivery : undefined, atts.length > 0 ? atts : undefined)
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
    if (shouldSendKey(e, getSendKey())) {
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
      <div style={{ maxWidth: 840, margin: '0 auto', display: 'flex', flexDirection: 'column' }}>
        {/* 输入卡（原型 §12–§14）：卡内只有 `+` … 模型 … 发送；工作区 / 权限 chip 移到卡下方一行。 */}
        <div
          className="fg-composer"
          style={{
            padding: '10px 12px 8px',
            display: 'flex',
            flexDirection: 'column',
            gap: 6,
            // 拖拽落区：拖文件入卡片时高亮边框（走 token，不硬编码色值）。
            outline: dragOver ? `2px dashed ${tokens.primary}` : 'none',
            outlineOffset: dragOver ? -4 : 0,
            borderRadius: tokens.radiusSm,
          }}
          onDragOver={(e) => {
            if (e.dataTransfer?.types?.includes('Files')) {
              e.preventDefault()
              setDragOver(true)
            }
          }}
          onDragLeave={() => setDragOver(false)}
          onDrop={(e) => {
            if (e.dataTransfer?.files && e.dataTransfer.files.length > 0) {
              e.preventDefault()
              setDragOver(false)
              void addFiles(e.dataTransfer.files)
            }
          }}
        >
          {/* 待发附件 chips（v32）：attachments 上传成功即出现在这里，可逐个移除。 */}
          {pendingAtts.length > 0 && (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
              {pendingAtts.map((a) => (
                <AttachmentChip key={a.id} attachment={a} onRemove={() => removeAtt(a.id)} />
              ))}
              {uploading && <span style={{ fontSize: 12, color: tokens.textSecondary }}>上传中…</span>}
            </div>
          )}
          {hintOpen && (
            // 候选列表（贴输入框上方，走文档流：弹层出现时输入框整体上移，不盖住正在打的字）。
            // mousedown 而不是 click：click 要等 mouseup，而 textarea 会先因失焦收起弹层。
            // 页脚放在**滚动区之外**：候选多时（实测通告过 20 余条）说明文字不该被滚没。
            <div className="fg-cmd-pop" role="listbox" aria-label="可用命令">
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
              <div className="fg-cmd-foot">↑↓ 选择 · Enter 填入 · Esc 关闭 —— 命令由助手提供</div>
            </div>
          )}
          <textarea
            ref={taRef}
            rows={1}
            value={draft}
            autoFocus={autoFocus}
            placeholder={
              busy ? `正在回复…（可${DELIVERY_LABEL[delivery]}：Enter 送入队列）` : placeholder ?? '向助手提问…'
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
          {/* 卡内底栏（原型 §12）：`+` 靠左；模型 chip + 发送圆钮靠右、紧挨。 */}
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
              {/* 隐藏的文件 input：`+` 菜单的「添加文件」与拖拽落区都走它。 */}
              <input
                ref={fileRef}
                type="file"
                multiple
                style={{ display: 'none' }}
                onChange={(e) => {
                  void addFiles(e.target.files)
                  e.target.value = '' // 允许再次选择同名文件
                }}
              />
              {sessionId ? (
                <ComposerCapabilities
                  sessionId={sessionId}
                  capabilities={capabilities}
                  disabled={capabilitiesDisabled || uploading}
                  onAddFile={() => fileRef.current?.click()}
                />
              ) : (
                // 没有会话（理论上会话页恒有 sessionId）：只留一颗裸 `+` 触发上传，不做能力编辑。
                <button
                  type="button"
                  className="fg-cap-add"
                  title="添加文件"
                  aria-label="添加文件"
                  disabled={uploading}
                  onClick={() => fileRef.current?.click()}
                >
                  <PaperClipOutlined style={{ fontSize: 13 }} />
                </button>
              )}
              {busy && busyNote && (
                <span
                  style={{
                    minWidth: 0,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    fontFamily: tokens.fontMono,
                    fontSize: 10.5,
                    color: tokens.textSecondary,
                  }}
                >
                  {busyNote}
                </span>
              )}
              {busy && (
                // 忙时投递模式：排队（等这轮完）或打断（先取消再发）。
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
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, flex: 'none' }}>
              {/* 模型 chip 靠右、紧贴发送键（原型 §12）。三 prop 齐备才渲染。 */}
              {modelOptions && onModelChange && (
                <ModelSelect
                  values={modelOptions}
                  value={model ?? null}
                  onChange={onModelChange}
                  allowClear
                  size="small"
                  placeholder={modelOptions.length === 0 ? '模型 · 暂无可选' : '模型 · 跟随默认'}
                  emptyNote="当前没有可选的模型：到「设置 · 模型」里添加供应商与模型，再选一条默认模型。"
                />
              )}
              <button
                type="button"
                className={`fg-send-btn${stopMode || waitMode ? ' fg-send-stop' : ''}${waitMode ? ' fg-send-wait' : ''}`}
                disabled={sendDisabled}
                title={buttonLabel}
                aria-label={buttonLabel}
                onClick={stopMode ? onStop : send}
              >
                {stopMode || waitMode ? (
                  <span className="fg-send-square" aria-hidden="true" />
                ) : (
                  <ArrowUpOutlined style={{ fontSize: 13 }} />
                )}
              </button>
            </span>
          </div>
        </div>
        {/* 卡下方一行（原型 §13）：工作区 chip + 权限 chip。
            2026-11-05 反馈：右下角那条「Enter 发送 · Shift+Enter 换行 ⇄」整条去掉——
            它是常驻噪声；发送键档位改到「设置 · 通用 · 键盘操作」里切换。 */}
        <div className="fg-composer-chips">
          {workspace && (
            <WorkspaceChipStatic
              path={workspace.path}
              temporary={workspace.temporary}
              isDefault={workspace.isDefault}
              title={workspace.title}
            />
          )}
          <PermissionChip disabled={busy && !canStop} />
        </div>
      </div>
    </div>
  )
}
