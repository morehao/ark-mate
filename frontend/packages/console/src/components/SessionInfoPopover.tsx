import type { ReactNode } from 'react'
import { Popover } from 'antd'
import type { SessionSummary } from '@ark-mate/types'
import { relTime } from '../console/railView'
import { titleFor } from '../console/util'
import { useMate } from '../store'

interface Props {
  session: SessionSummary
  children: ReactNode
}

/**
 * 会话信息浮层（悬停会话行出现，移开淡出；v14 / 设计 §13–§19）。
 *
 * 内容**只**用后端有来源的四项（设计 §18 / §19.3）：
 *   1. 标题（`title`）
 *   2. 所属空间（`workspace_title`）
 *   3. 模型（`model`）
 *   4. 更新时间（`updated_at`）
 * 任一行**取值为空就跳过该行**（不渲染"未知"占位）——设计 §19 的口径是"四行都可为空时跳过"。
 *
 * 明确**不含**设备 / 环境标签（如 `本地电脑 / 日常办公`）：那是 WorkBuddy 的产品字段，
 * 我方 `SessionSummary` 没有来源（设计 §18 已删）。
 *
 * 定位与淡出交给 antd `Popover`（portal + 贴边翻转 + 悬停意图防误触），本组件不手算坐标。
 */
export function SessionInfoPopover({ session, children }: Props) {
  const views = useMate((s) => s.views)
  const title = titleFor(session, views[session.id], undefined, 'plain')

  const rows: Array<{ key: string; label: string; value: string }> = []
  if (session.workspace_title) {
    rows.push({ key: 'ws', label: '所属空间', value: session.workspace_title })
  }
  if (session.model) {
    rows.push({ key: 'model', label: '模型', value: session.model })
  }
  const updated = relTime(session.updated_at)
  if (updated) {
    rows.push({ key: 'updated', label: '更新于', value: `${updated} · ${session.updated_at}` })
  }

  const content = (
    <div className="fg-sess-info" aria-label="会话信息">
      <div className="fg-sess-info-title" title={title}>
        {title || '未命名会话'}
      </div>
      {rows.map((r) => (
        <div className="fg-sess-info-row" key={r.key}>
          <span className="fg-sess-info-key">{r.label}</span>
          <span className="fg-sess-info-val" title={r.value}>
            {r.value}
          </span>
        </div>
      ))}
    </div>
  )

  return (
    <Popover
      content={content}
      placement="right"
      trigger="hover"
      mouseEnterDelay={0.35}
      mouseLeaveDelay={0.12}
      destroyOnHidden
      arrow={false}
      overlayClassName="fg-sess-info-pop"
    >
      {children}
    </Popover>
  )
}
