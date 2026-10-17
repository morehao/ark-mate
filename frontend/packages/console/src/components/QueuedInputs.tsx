import { Button, Tooltip } from 'antd'
import { ArrowDownOutlined, ArrowUpOutlined, CloseOutlined, EditOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { QueuedInput } from '@ark-mate/types'
import { deliveryLabel, queueExcerpt, queueSummary } from '../console/queueView'

interface Props {
  queue: QueuedInput[]
  /** 撤下一条队项（服务端收敛以 session.input_cancelled 事件为准）。 */
  onCancel: (inputId: string) => void
  /** ADR-10 动作①：重排（`beforeId` 缺省 = 追加到末尾）。 */
  onMove: (inputId: string, beforeId?: string) => void
  /** ADR-10 动作②：立即发送（忙时排到队首，空闲时立刻起一轮）。 */
  onSendNow: (inputId: string) => void
  /** ADR-10 动作③：取出编辑（正文回填到待发送文案）。 */
  onPop: (inputId: string) => void
  /** 正在撤下的队项 id（按钮转圈，避免连点）。 */
  cancelling?: string | null
}

/**
 * §6 排队输入条：贴在输入框上方，把「已经说了但还没发出去的话」摆在眼前。
 *
 * 之所以不放在会话详情抽屉里：队列的意义是「这些话还没发出去」，离输入框最近才有用；
 * 抽屉是事后查看的地方（原设计稿写在 SessionDetail，实施时改到这里）。
 *
 * 内容只来自事件投影（`session.input_queued` 建项、delivered/cancelled 出队），所以没有
 * 「本地乐观项」：刷新后由历史回放重建出同一份队列。
 */
export function QueuedInputs({ queue, onCancel, onMove, onSendNow, onPop, cancelling }: Props) {
  if (queue.length === 0) return null
  return (
    <div style={{ flex: 'none', padding: '0 20px', background: tokens.bg }}>
      <div style={{ maxWidth: 840, margin: '0 auto' }}>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            padding: '6px 10px',
            border: `1px dashed ${tokens.border}`,
            borderRadius: 10,
            marginBottom: 6,
          }}
        >
          <span style={{ fontSize: 11.5, color: tokens.textPlaceholder, whiteSpace: 'nowrap' }}>
            {queueSummary(queue)}
          </span>
          <span style={{ flex: 1 }} />
          <Tooltip title="队列只由服务端事件驱动：投递或撤下都会在这里出队，并留下对应事实">
            <span style={{ fontSize: 11, color: tokens.textPlaceholder, cursor: 'default' }}>尚未发出</span>
          </Tooltip>
        </div>
        {queue.map((q, i) => (
          <div
            key={q.inputId}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              padding: '5px 10px',
              border: `1px solid ${tokens.border}`,
              borderRadius: 10,
              background: tokens.surface,
              marginBottom: 4,
            }}
          >
            <Tooltip title={q.text}>
              <span
                style={{
                  flex: 1,
                  minWidth: 0,
                  fontSize: 12.5,
                  color: tokens.text,
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                }}
              >
                {queueExcerpt(q.text, 120)}
              </span>
            </Tooltip>
            <Tooltip
              title={
                q.delivery === 'interrupt_then_prompt'
                  ? '插话：先取消当前轮，再立刻发出这条'
                  : '排队：当前轮跑完后作为新的一轮自动发出'
              }
            >
              <span style={{ fontSize: 10.5, color: tokens.textPlaceholder, whiteSpace: 'nowrap' }}>
                {deliveryLabel(q.delivery)}
              </span>
            </Tooltip>
            {/* ADR-10 三动作（Q4 选①：行内小图标，不藏进菜单——它们都是「我现在就想做」的动作）。
                重排用 ↑↓ 而不是拖拽：拖拽在滚动容器里会与页面滚动抢手势，而队列最多几条。 */}
            <Button
              size="small"
              type="text"
              icon={<ArrowUpOutlined style={{ fontSize: 11 }} />}
              disabled={i === 0}
              onClick={() => onMove(q.inputId, queue[i - 1]?.inputId)}
              title="上移一位"
            />
            <Button
              size="small"
              type="text"
              icon={<ArrowDownOutlined style={{ fontSize: 11 }} />}
              disabled={i === queue.length - 1}
              onClick={() => onMove(q.inputId, queue[i + 2]?.inputId)}
              title="下移一位"
            />
            <Tooltip title="立即发送：让它成为下一轮（当前轮仍在跑时它排到队首，等这一轮结束后先发它）">
              <Button
                size="small"
                type="text"
                icon={<ThunderboltOutlined style={{ fontSize: 11 }} />}
                onClick={() => onSendNow(q.inputId)}
              />
            </Tooltip>
            <Tooltip title="取出编辑：取回草稿并从队列撤下，改完再发">
              <Button
                size="small"
                type="text"
                icon={<EditOutlined style={{ fontSize: 11 }} />}
                onClick={() => onPop(q.inputId)}
              />
            </Tooltip>
            <Button
              size="small"
              type="text"
              icon={<CloseOutlined style={{ fontSize: 11 }} />}
              loading={cancelling === q.inputId}
              onClick={() => onCancel(q.inputId)}
              title="从队列撤下（不影响已经发出的内容）"
            />
          </div>
        ))}
      </div>
    </div>
  )
}
