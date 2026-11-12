import { useState } from 'react'
import { Button, Input } from 'antd'
import {
  CheckOutlined,
  DownOutlined,
  EditOutlined,
  FileDoneOutlined,
} from '@ant-design/icons'
import type { ToolDetail } from '../console/transcriptView'
import { Md } from './Md'

/**
 * 对话流里的「任务清单卡」与「计划卡」（只读展示）。
 *
 * 两端都由 transcriptView 的 `toolDetail()` 派生：
 *  - `todo`：todo_write 的入参清单 → 进度头 + 可展开勾选清单；
 *  - `plan`：exit_plan_mode 的计划正文 → markdown + 审批条。
 *
 * 只读纪律：计划卡的「批准 / 继续规划」按钮为**禁用占位态**——exit_plan_mode 的审批
 * 需要底座在 ACP 上暴露命令面（当前未暴露），本期不发任何请求、也不假装成功。
 * 按钮的 `title` 与旁注如实写明这一点，等底座补齐后再另开 WP 接线。
 */

const APPROVAL_PENDING_TITLE = '需底座支持计划审批（ACP 未暴露）'

/** 任务清单卡：进度头（完成数/总数 + 细进度条）+ 可展开勾选清单，默认展开。 */
export function TodoCard({ detail }: { detail: ToolDetail }) {
  const todos = detail.todos ?? []
  const [open, setOpen] = useState(true)
  const total = todos.length
  const done = todos.filter((t) => t.status === 'completed').length
  const percent = total > 0 ? Math.round((done / total) * 100) : 0

  return (
    <div className={`fg-todo-card${open ? ' fg-open' : ''}`} data-fg-todo-card="">
      <button
        type="button"
        className="fg-todo-head"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <span className="fg-todo-ico" aria-hidden="true">
          <FileDoneOutlined />
        </span>
        <span className="fg-todo-title">任务清单</span>
        <span className="fg-todo-count">
          {done} / {total}
        </span>
        <span
          className="fg-todo-bar"
          role="progressbar"
          aria-valuemin={0}
          aria-valuemax={total}
          aria-valuenow={done}
        >
          <i className="fg-todo-bar-fill" style={{ width: `${percent}%` }} />
        </span>
        <DownOutlined className="fg-todo-chev" />
      </button>
      {open && (
        <div className="fg-todo-list" role="list">
          {todos.map((todo, index) => (
            <div className={`fg-todo-item ${todo.status}`} role="listitem" key={`${index}-${todo.content}`}>
              <span className={`fg-todo-mark ${todo.status}`} aria-hidden="true">
                {todo.status === 'completed' ? <CheckOutlined /> : null}
              </span>
              <span className="fg-todo-txt">{todo.content}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

/** 计划卡：markdown 正文（复用 Md）+ 审批条；approved 已定时收敛为「已批准」只读态。 */
export function PlanCard({ detail }: { detail: ToolDetail }) {
  const planMD = detail.planMD ?? ''
  const approved = detail.approved === true
  return (
    <div className="fg-plan-card" data-fg-plan-card="">
      <div className="fg-plan-head">
        <span className="fg-plan-ico" aria-hidden="true">
          <EditOutlined />
        </span>
        <span className="fg-plan-title">计划</span>
        <span className={`fg-plan-badge${approved ? ' approved' : ''}`}>
          {approved ? '已批准' : '待确认'}
        </span>
      </div>
      <div className="fg-plan-body">
        <Md text={planMD} />
      </div>
      <div className="fg-plan-foot">
        <Input
          className="fg-plan-feedback"
          placeholder="对计划的修改意见（本期仅供记录）"
          size="small"
          disabled={approved}
        />
        <Button
          size="small"
          type="primary"
          disabled
          title={APPROVAL_PENDING_TITLE}
        >
          批准并执行
        </Button>
        <Button size="small" disabled title={APPROVAL_PENDING_TITLE}>
          继续规划
        </Button>
        <span className="fg-plan-muted">{APPROVAL_PENDING_TITLE}</span>
      </div>
    </div>
  )
}
