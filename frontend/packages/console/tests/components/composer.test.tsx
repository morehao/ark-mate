import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

/**
 * 刀 1 · Composer（输入区）——渲染与发送交互。
 *
 * 纯逻辑（sendKey / commandHints）已有 node --test 覆盖；这里只验**组件层**：
 *   - 空草稿发送钮禁用；
 *   - 输入后点击发送 → onSend(文本) 且清空；
 *   - Enter 触发发送（默认 enter-send 档）；
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
  localStorage.clear()
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
    expect(baseProps.onSend).toHaveBeenCalledWith('你好世界', undefined, undefined)
    expect(ta.value).toBe('')
  })

  it('Enter 触发发送（默认 enter-send 档）', async () => {
    const user = userEvent.setup()
    render(<Composer {...baseProps} />)
    const ta = screen.getByRole('textbox')
    await user.type(ta, '快捷键发送{Enter}')
    expect(baseProps.onSend).toHaveBeenCalledWith('快捷键发送', undefined, undefined)
  })

  it('忙且草稿为空 → 停止按钮 → onStop', async () => {
    const onStop = vi.fn()
    const user = userEvent.setup()
    render(<Composer {...baseProps} busy canStop onStop={onStop} />)
    await user.click(screen.getByRole('button', { name: '停止' }))
    expect(onStop).toHaveBeenCalledTimes(1)
  })

  it('injectDraft 把正文塞回输入框', async () => {
    render(<Composer {...baseProps} injectDraft={{ text: '被取回的草稿', nonce: 1 }} />)
    const ta = screen.getByRole('textbox') as HTMLTextAreaElement
    expect(ta.value).toBe('被取回的草稿')
  })
})
