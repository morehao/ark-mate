import { useState } from 'react'
import { Button, Input } from 'antd'
import { tokens } from '@ark-mate/ui'
import { useMate } from '../../store'
import { CardHeader } from './CardHeader'

/**
 * 设置 · 访问令牌（A6 单操作员令牌）——**作为居中弹窗的内容体**渲染。
 *
 * 原先它是左栏 footer 里「令牌」按钮弹出的小窗（与「设置」并排）；按反馈收进设置：
 * footer 那行只留「设置」，令牌成为设置的一节（顺带把 401 自动弹窗也指向这一节）。
 */
export function TokenSettingsPanel() {
  const token = useMate((s) => s.token)
  const saveToken = useMate((s) => s.saveToken)
  const [draft, setDraft] = useState(token)

  const dirty = draft.trim() !== token

  return (
    <div className="fg-setsec">
      <div className="fg-setsec-head">
        <span className="fg-setsec-desc">
          当应用启用了访问保护时，除健康检查外的所有操作都需要这个访问令牌。
        </span>
      </div>

      <div className="fg-setsec-card">
        <CardHeader title="访问令牌" hint={token ? '已配置（只存在本浏览器 localStorage）' : '未配置'} />
        <div style={{ padding: 16 }}>
          <Input.Password
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onPressEnter={() => saveToken(draft)}
            placeholder="粘贴访问令牌；留空表示清除"
            autoComplete="off"
          />
          <div style={{ marginTop: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
            <Button size="small" type="primary" disabled={!dirty} onClick={() => saveToken(draft)}>
              保存并重连
            </Button>
            <Button size="small" disabled={!token && !draft} onClick={() => saveToken('')}>
              清除令牌
            </Button>
            <span style={{ fontSize: 11.5, color: tokens.textPlaceholder }}>
              保存后会立即重连事件流并重新拉取数据；令牌不写入 URL 或日志。
            </span>
          </div>
        </div>
      </div>
    </div>
  )
}
