import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { CardHeader } from '../../src/components/settings/CardHeader'

// 刀 0 探针：只验证「工具链能渲染 React 组件 + jest-dom 断言 + user-event 交互」，
// 不追求覆盖度。选 CardHeader 是因为它纯展示、只依赖 @ark-mate/ui 的 tokens，无网络无 store。
describe('刀 0 探针 · CardHeader（纯展示组件）', () => {
  it('渲染标题与说明', () => {
    render(<CardHeader title="模型设置" hint="选择默认模型" />)
    expect(screen.getByText('模型设置')).toBeInTheDocument()
    expect(screen.getByText('选择默认模型')).toBeInTheDocument()
  })

  it('extra 插槽可放按钮并触发点击（user-event 通路）', async () => {
    const onClick = vi.fn()
    const user = userEvent.setup()
    render(
      <CardHeader
        title="连接器"
        hint="管理 MCP 连接"
        extra={<button onClick={onClick}>添加</button>}
      />,
    )
    await user.click(screen.getByRole('button', { name: '添加' }))
    expect(onClick).toHaveBeenCalledTimes(1)
  })
})
