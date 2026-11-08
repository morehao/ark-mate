import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import type { ArtifactMeta } from '@ark-mate/types'
import { TurnArtifacts } from '../../src/components/TurnArtifacts'

// 预览壳用 spy 替身：验证「整卡点击 → openPreview({file_id,name,size_bytes})」的接线，
// 不真正打开预览（那是 PreviewProvider 自己的测试）。
const openPreview = vi.fn()
vi.mock('../../src/components/preview/PreviewProvider', () => ({
  usePreview: () => ({ openPreview, close: () => {} }),
}))

function art(p: Partial<ArtifactMeta> & { id: string; title: string }): ArtifactMeta {
  return {
    session_id: 's1',
    origin: 'tool_write',
    kind: 'file',
    size_bytes: 2048,
    dirty: false,
    created_at: 1,
    updated_at: 1,
    ...p,
  } as ArtifactMeta
}

describe('TurnArtifacts（回答末尾产物区）', () => {
  it('空产物不渲染任何内容', () => {
    const { container } = render(<TurnArtifacts artifacts={[]} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('渲染卡片；点击整卡触发 openPreview 且带 file_id/name/size_bytes', () => {
    render(<TurnArtifacts artifacts={[art({ id: 'a1', title: '天气报告.png', file_id: 'f1', size_bytes: 4096 })]} />)
    const btn = screen.getByRole('button', { name: /预览 天气报告\.png/ })
    fireEvent.click(btn)
    expect(openPreview).toHaveBeenCalledWith({ file_id: 'f1', name: '天气报告.png', size_bytes: 4096 })
  })

  it('file_id 缺失 → 卡片不可点（无预览按钮），文案标「不可预览」', () => {
    render(<TurnArtifacts artifacts={[art({ id: 'a2', title: '无字节.txt' })]} />)
    expect(screen.queryByRole('button', { name: /预览/ })).toBeNull()
    expect(screen.getByText(/不可预览/)).toBeInTheDocument()
  })

  it('超过 4 个折叠为「全部 N 个」，展开后全部可见', () => {
    const many = Array.from({ length: 6 }, (_, i) => art({ id: `a${i}`, title: `文件${i}.md`, file_id: `f${i}` }))
    render(<TurnArtifacts artifacts={many} />)
    // 折叠态只显示 4 张卡
    expect(screen.getAllByRole('button', { name: /^预览 / }).length).toBe(4)
    const toggle = screen.getByRole('button', { name: /全部 6 个/ })
    fireEvent.click(toggle)
    expect(screen.getAllByRole('button', { name: /^预览 / }).length).toBe(6)
  })

  it('同路径多版本只保留最新（去重）', () => {
    render(
      <TurnArtifacts
        artifacts={[
          art({ id: 'n', title: 'report.md', path: 'out/report.md', file_id: 'fn' }),
          art({ id: 'o', title: 'report.md', path: 'out/report.md', file_id: 'fo' }),
        ]}
      />,
    )
    expect(screen.getAllByRole('button', { name: /^预览 / }).length).toBe(1)
  })
})
