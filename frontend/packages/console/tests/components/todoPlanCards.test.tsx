import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'

import { PlanCard, TodoCard } from '../../src/components/TodoPlanCards'
import type { ToolDetail } from '../../src/console/transcriptView'

const todoDetail = (todos: NonNullable<ToolDetail['todos']>): ToolDetail => ({
  kind: 'todo',
  todos,
  notes: [],
})

const planDetail = (over: Partial<ToolDetail> = {}): ToolDetail => ({
  kind: 'plan',
  planMD: '# 计划标题\n\n1. 第一步\n2. 第二步',
  notes: [],
  ...over,
})

describe('TodoCard 任务清单卡', () => {
  it('渲染进度头（完成数/总数 + 进度条）与全部清单项', () => {
    render(
      <TodoCard
        detail={todoDetail([
          { content: '读文件', status: 'completed' },
          { content: '写代码', status: 'in_progress' },
          { content: '跑测试', status: 'pending' },
        ])}
      />,
    )
    // 进度头：1/3
    expect(screen.getByText('任务清单')).toBeInTheDocument()
    expect(screen.getByText('1 / 3')).toBeInTheDocument()
    // 进度条 aria
    const bar = screen.getByRole('progressbar')
    expect(bar).toHaveAttribute('aria-valuenow', '1')
    expect(bar).toHaveAttribute('aria-valuemax', '3')
    // 清单项
    expect(screen.getByText('读文件')).toBeInTheDocument()
    expect(screen.getByText('写代码')).toBeInTheDocument()
    expect(screen.getByText('跑测试')).toBeInTheDocument()
  })

  it('各项按 status 挂对应类名（completed / in_progress / pending）', () => {
    const { container } = render(
      <TodoCard
        detail={todoDetail([
          { content: '甲', status: 'completed' },
          { content: '乙', status: 'in_progress' },
          { content: '丙', status: 'pending' },
        ])}
      />,
    )
    const items = container.querySelectorAll('.fg-todo-item')
    expect(items).toHaveLength(3)
    expect(items[0].className).toContain('completed')
    expect(items[1].className).toContain('in_progress')
    expect(items[2].className).toContain('pending')
    // in_progress 的圆点高亮脉冲类
    expect(container.querySelector('.fg-todo-mark.in_progress')).toBeTruthy()
  })

  it('空清单不崩溃：进度 0 / 0', () => {
    render(<TodoCard detail={todoDetail([])} />)
    expect(screen.getByText('0 / 0')).toBeInTheDocument()
  })
})

describe('PlanCard 计划卡（只读，审批按钮占位）', () => {
  it('渲染 markdown 正文', () => {
    render(<PlanCard detail={planDetail()} />)
    expect(screen.getByText('计划标题')).toBeInTheDocument()
    expect(screen.getByText('第一步')).toBeInTheDocument()
  })

  it('未定：批准/继续规划按钮为禁用占位态，附 title 说明，不发请求', () => {
    render(<PlanCard detail={planDetail()} />)
    const approve = screen.getByRole('button', { name: /批准并执行/ })
    const continueBtn = screen.getByRole('button', { name: /继续规划/ })
    // 硬纪律：两个按钮都必须 disabled
    expect(approve).toBeDisabled()
    expect(continueBtn).toBeDisabled()
    // title 说明需底座支持
    expect(approve).toHaveAttribute('title', '需底座支持计划审批（ACP 未暴露）')
    expect(continueBtn).toHaveAttribute('title', '需底座支持计划审批（ACP 未暴露）')
    // 徽标显示待确认
    expect(screen.getByText('待确认')).toBeInTheDocument()
  })

  it('approved=true：收敛为「已批准」只读态', () => {
    render(<PlanCard detail={planDetail({ approved: true })} />)
    expect(screen.getByText('已批准')).toBeInTheDocument()
    expect(screen.queryByText('待确认')).toBeNull()
    // 已批准态按钮仍禁用（本期不接线）
    expect(screen.getByRole('button', { name: /批准并执行/ })).toBeDisabled()
  })
})
