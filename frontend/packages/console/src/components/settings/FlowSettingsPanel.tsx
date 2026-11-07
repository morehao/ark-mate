import { Select, Switch, Tooltip } from 'antd'
import { QuestionCircleOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { ReactNode } from 'react'
import {
  QUEUE_SEND_OPTIONS,
  TOOL_DISPLAY_OPTIONS,
  setQueueSend,
  setShowFileChanges,
  setToolDisplay,
  useFlowPrefs,
} from '../../console/flowPrefs'
import { CardHeader } from './CardHeader'

/**
 * 设置 · 对话——**作为居中弹窗的内容体**渲染（见 `settings/SettingsDialog.tsx`）。
 *
 * 三行本机偏好（不落后端，见 `console/flowPrefs.ts`）：
 *   1. 显示文件修改过程（开关）——过程流里是否展开每次文件改动的 diff；
 *   2. 队列发送方式（排队 / 立即发送）——对话进行中新消息的默认发送行为；
 *   3. 对话过程展示方式（简洁 / 详细）——过程流里工具调用的展开策略。
 *
 * 「欢迎语」属个性化设置，按本次范围**不在此节**。
 *
 * 词汇纪律（`TestNoConversationVocabulary`）：本文件不得出现 `conversation`——统一用「对话 / 过程」。
 */

/** 一行：左标题（可带悬浮说明的「?」）+副说明，右控件（对齐设置面既有行卡语汇）。 */
function SettingRow({ title, hint, titleExtra, control, last }: {
  title: ReactNode
  hint: ReactNode
  /** 标题后跟的悬浮说明（问号 tooltip）；为空则不渲染。 */
  titleExtra?: ReactNode
  control: ReactNode
  last?: boolean
}) {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'flex-start',
        gap: 20,
        padding: '14px 16px',
        borderBottom: last ? 'none' : `1px solid ${tokens.border}`,
      }}
    >
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontSize: 14, fontWeight: 500, color: tokens.text, display: 'flex', alignItems: 'center', gap: 6 }}>
          {title}
          {titleExtra}
        </div>
        <div style={{ fontSize: 12.5, color: tokens.textSecondary, marginTop: 4, lineHeight: 1.55 }}>{hint}</div>
      </div>
      <div style={{ flex: 'none', paddingTop: 2 }}>{control}</div>
    </div>
  )
}

/** 标题后的「?」：悬浮展示整段选项说明（多项各占一行，避免 `。；` 这类混排标点）。 */
function FieldHelp({ options }: { options: { value: string; label: string; hint: string }[] }) {
  const body = (
    <div style={{ fontSize: 12, lineHeight: 1.7, maxWidth: 320 }}>
      {options.map((o) => (
        <div key={o.value} style={{ marginBottom: 2 }}>
          <b>{o.label}</b>：{o.hint}
        </div>
      ))}
    </div>
  )
  return (
    <Tooltip title={body} placement="top">
      <QuestionCircleOutlined
        aria-label="说明"
        style={{ fontSize: 13, color: tokens.textPlaceholder, cursor: 'help' }}
      />
    </Tooltip>
  )
}

export function FlowSettingsPanel() {
  const prefs = useFlowPrefs()
  return (
    <div className="fg-setsec">
      <div className="fg-setsec-card">
        <CardHeader
          title="对话"
          hint="对话过程的呈现方式，以及对话进行中新消息的发送行为。这些是本机偏好，只影响这台设备上的呈现与发送。"
        />
        <SettingRow
          title="显示文件修改过程"
          hint="开启后，过程流里会展开每次文件改动的 diff 详情；关闭则只用一行摘要表示改动。"
          control={(
            <Switch
              checked={prefs.showFileChanges}
              onChange={(v) => setShowFileChanges(v)}
              aria-label="显示文件修改过程"
            />
          )}
        />
        <SettingRow
          title="队列发送方式"
          titleExtra={<FieldHelp options={QUEUE_SEND_OPTIONS} />}
          hint="对话进行中你新发的消息，是按顺序排队等待，还是立刻插入当前对话引导 Agent。"
          control={(
            <Select
              value={prefs.queueSend}
              onChange={(v) => setQueueSend(v)}
              style={{ minWidth: 180 }}
              aria-label="队列发送方式"
              options={QUEUE_SEND_OPTIONS.map((o) => ({
                value: o.value,
                label: o.label,
                title: o.hint,
              }))}
            />
          )}
        />
        <SettingRow
          title="对话过程展示方式"
          titleExtra={<FieldHelp options={TOOL_DISPLAY_OPTIONS} />}
          hint="工具调用记录在过程流里的展开程度：简洁只展开正在执行的那条，详细则展开某一阶段的全部记录。"
          last
          control={(
            <Select
              value={prefs.toolDisplay}
              onChange={(v) => setToolDisplay(v)}
              style={{ minWidth: 180 }}
              aria-label="对话过程展示方式"
              options={TOOL_DISPLAY_OPTIONS.map((o) => ({
                value: o.value,
                label: o.label,
                title: o.hint,
              }))}
            />
          )}
        />
      </div>
    </div>
  )
}
