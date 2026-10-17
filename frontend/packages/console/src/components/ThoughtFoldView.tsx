import { useEffect, useRef, useState } from 'react'
import { tokens } from '@ark-mate/ui'
import type { ThoughtFold } from '../console/transcriptView'

interface Props {
  fold: ThoughtFold
  /**
   * 用户显式展开/收起。页面用它判断「用户正在读历史」——流式中有人手动展开了旧思考，
   * 就不该每一帧把他拉回底部（D6 的滚动口径）。
   */
  onUserToggle?: (id: string, open: boolean) => void
  /** 展开/收起动画结束后通知页面重算贴底（收起时高度骤降，不重算会停在半空）。 */
  onSettled?: () => void
}

/**
 * 一段思考的折叠块（需求 6 下半，对齐 WorkBuddy 的 reasoning 折叠）。
 *
 * 两条状态规则：
 *  - `streaming`：自动展开、标题 shimmer、**不渲染箭头**，正文内部自动滚到底；
 *  - `completed`：自动收起、出现 12px 线性箭头，点标题才展开。
 *
 * 正文**首次展开才挂载**（WB 同款）：长转录里几十段思考各自挂一份正文，DOM 会明显变重。
 * 代价是收起时 Ctrl+F 搜不到正文——那是 WB 的既有取舍，记在风险表 R5。
 */
export function ThoughtFoldView({ fold, onUserToggle, onSettled }: Props) {
  const streaming = fold.variant === 'streaming'
  // null = 跟随默认值（streaming -> 展开 / completed -> 收起）；非 null = 用户显式切换过。
  const [userOpen, setUserOpen] = useState<boolean | null>(null)
  const open = userOpen ?? streaming
  const [mounted, setMounted] = useState(streaming)
  const bodyRef = useRef<HTMLDivElement | null>(null)
  const settleRef = useRef<number | null>(null)

  // 流式结束：回到默认收起。用户此刻的显式选择不保留——下一帧起它就是"历史思考"了。
  useEffect(() => {
    if (!streaming) setUserOpen(null)
  }, [streaming])

  useEffect(() => {
    if (open) setMounted(true)
  }, [open])

  // 流式中且展开着：正文内部自动滚到底（WB 只在 `streaming && open` 时这么做）。
  useEffect(() => {
    if (!streaming || !open) return
    const el = bodyRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [fold.text, streaming, open])

  useEffect(
    () => () => {
      if (settleRef.current) window.clearTimeout(settleRef.current)
    },
    [],
  )

  const toggle = () => {
    // 拖选正文后松手不该被当成"点标题"（WB 同款：先清掉选区）。
    window.getSelection()?.removeAllRanges()
    const next = !open
    setUserOpen(next)
    onUserToggle?.(fold.id, next)
    if (settleRef.current) window.clearTimeout(settleRef.current)
    settleRef.current = window.setTimeout(() => onSettled?.(), 300)
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }}>
      <button
        type="button"
        className="fg-thought-head"
        aria-expanded={open}
        // 流式中是"正在生长的内容"：不给收起动作，也不画箭头（验收 A7）。
        disabled={streaming}
        onClick={toggle}
      >
        <span className={streaming ? 'fg-shimmer' : undefined}>深度思考</span>
        {!streaming && (
          <svg
            className={`fg-thought-chevron${open ? ' fg-open' : ''}`}
            viewBox="0 0 24 24"
            width="12"
            height="12"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            {/* 朝右的线性 chevron：展开时 rotate(90deg) 朝下（D3 的同一套语汇）。 */}
            <path d="M9 18l6-6-6-6" />
          </svg>
        )}
      </button>
      <div
        className="fg-thought-clip"
        data-open={open ? 'true' : undefined}
        style={{ maxHeight: open ? 200 : 0, opacity: open ? 1 : 0 }}
        aria-hidden={!open}
      >
        {mounted && (
          <div ref={bodyRef} className="fg-thought-body" style={{ color: tokens.textSecondary }}>
            {fold.text}
          </div>
        )}
      </div>
    </div>
  )
}
