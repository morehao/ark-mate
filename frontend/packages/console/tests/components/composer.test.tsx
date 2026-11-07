import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

/**
 * 刀 1 · Composer（输入区）——渲染与发送交互。
 *
 * 纯逻辑（sendKey / commandHints）已有 node --test 覆盖；这里只验**组件层**：
 *   - 空草稿发送钮禁用；
 *   - 输入后点击发送 → onSend(文本) 且清空；
 *   - Enter 触发发送；Shift+Enter 不发送（固定键位，无档位）；
 *   - 忙且草稿为空 → 停止钮 → onStop；
 *   - injectDraft（队列「取出编辑」）把正文塞回输入框。
 *
 * 依赖 mock：@ark-mate/api（useAttachmentDraft 内的 uploadAttachment）。
 * 不传 sessionId，避开 ComposerCapabilities 的网络依赖。
 */

vi.mock('@ark-mate/api', () => ({
  uploadAttachment: vi.fn(async () => ({ id: 'att_1', name: 'x' })),
}))

import { Composer } from '../../src/components/Composer'

const baseProps = {
  busy: false,
  canStop: true,
  onStop: () => {},
  onSend: vi.fn(),
}

beforeEach(() => {
  baseProps.onSend = vi.fn()
  baseProps.onStop = () => {}
})

describe('Composer', () => {
  it('空草稿时发送按钮禁用', () => {
    render(<Composer {...baseProps} />)
    const btn = screen.getByRole('button', { name: '发送' })
    expect(btn).toBeDisabled()
  })

  it('输入后点击发送 → onSend(文本) 并清空输入', async () => {
    const user = userEvent.setup()
    render(<Composer {...baseProps} />)
    const ta = screen.getByRole('textbox') as HTMLTextAreaElement
    await user.type(ta, '你好世界')
    await user.click(screen.getByRole('button', { name: '发送' }))
    expect(baseProps.onSend).toHaveBeenCalledWith('你好世界', undefined, undefined, undefined)
    expect(ta.value).toBe('')
  })

  it('Enter 触发发送（固定键位）', async () => {
    const user = userEvent.setup()
    render(<Composer {...baseProps} />)
    const ta = screen.getByRole('textbox')
    await user.type(ta, '快捷键发送{Enter}')
    expect(baseProps.onSend).toHaveBeenCalledWith('快捷键发送', undefined, undefined, undefined)
  })

  it('Shift+Enter 换行，不触发发送', async () => {
    const user = userEvent.setup()
    render(<Composer {...baseProps} />)
    const ta = screen.getByRole('textbox') as HTMLTextAreaElement
    await user.type(ta, '第一行')
    fireEvent.keyDown(ta, { key: 'Enter', shiftKey: true })
    expect(baseProps.onSend).not.toHaveBeenCalled()
    expect(ta.value).toBe('第一行')
  })

  it('忙且草稿为空 → 停止按钮 → onStop', async () => {
    const onStop = vi.fn()
    const user = userEvent.setup()
    render(<Composer {...baseProps} busy canStop onStop={onStop} />)
    await user.click(screen.getByRole('button', { name: '停止' }))
    expect(onStop).toHaveBeenCalledTimes(1)
  })

  it('忙 + 草稿非空 → 主按钮变「排队发送」并调用 onSend(delivery=after_run)', async () => {
    const onStop = vi.fn()
    const user = userEvent.setup()
    render(<Composer {...baseProps} busy canStop onStop={onStop} />)
    const ta = screen.getByRole('textbox')
    await user.type(ta, '插一句')
    // 主按钮不再是"停止"，而是按投递模式的"排队发送"
    const btn = screen.getByRole('button', { name: '排队发送' })
    await user.click(btn)
    expect(onStop).not.toHaveBeenCalled()
    expect(baseProps.onSend).toHaveBeenCalledWith('插一句', 'after_run', undefined, undefined)
  })

  it('忙 + 草稿非空 → 出现投递 pill；忙 + 空 → 不出现', async () => {
    const user = userEvent.setup()
    const { unmount } = render(<Composer {...baseProps} busy canStop />)
    // 空草稿：默认排队 pill 不渲染（不占位）
    expect(document.querySelector('.fg-delivery-pill')).toBeNull()
    await user.type(screen.getByRole('textbox'), 'x')
    expect(document.querySelector('.fg-delivery-pill')).not.toBeNull()
    unmount()
    // 空闲态：也没有投递 pill
    render(<Composer {...baseProps} />)
    expect(document.querySelector('.fg-delivery-pill')).toBeNull()
  })

  it('等待态（busy 且不可停）→ 卡上方 banner 透传 busyNote，主按钮禁用', () => {
    render(<Composer {...baseProps} busy canStop={false} busyNote="等待你确认授权" />)
    expect(screen.getByRole('status')).toHaveTextContent('等待你确认授权')
    const btn = screen.getByRole('button', { name: '等待你回答后才能停止' })
    expect(btn).toBeDisabled()
    // 等待态不给投递选择
    expect(document.querySelector('.fg-delivery-pill')).toBeNull()
  })

  it('忙态卡片带 fg-running；等待态带 fg-waiting', () => {
    const { container, unmount } = render(<Composer {...baseProps} busy canStop />)
    expect(container.querySelector('.fg-composer.fg-running')).not.toBeNull()
    unmount()
    const { container: c2 } = render(<Composer {...baseProps} busy canStop={false} />)
    expect(c2.querySelector('.fg-composer.fg-waiting')).not.toBeNull()
  })

  it('injectDraft 把正文塞回输入框', async () => {
    render(<Composer {...baseProps} injectDraft={{ text: '被取回的草稿', nonce: 1 }} />)
    const ta = screen.getByRole('textbox') as HTMLTextAreaElement
    expect(ta.value).toBe('被取回的草稿')
  })

  it('AI 免责提示常驻输入卡下方，且不随 busy 状态变化', () => {
    const text = '内容由 AI 生成，请核对关键信息后再采用'
    // 空闲态：应在。
    const { unmount } = render(<Composer {...baseProps} />)
    expect(screen.getByText(text)).toBeInTheDocument()
    unmount()
    // 进行中（busy）：仍在。
    render(<Composer {...baseProps} busy />)
    expect(screen.getByText(text)).toBeInTheDocument()
  })
})
