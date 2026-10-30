import { useState } from 'react'
import type { SessionCapabilities, SessionCapabilitiesPatch } from '@ark-mate/types'
import { useMate } from '../store'
import { CapabilityBar } from './CapabilityBar'
import { choiceFromCapabilities } from '../console/capabilityEdit'

interface Props {
  sessionId: string
  /** 会话钉住的能力选定（读面快照；`null`/缺失 = 三格都未声明）。 */
  capabilities?: SessionCapabilities | null
  /** 会话不能继续时禁用入口（如底座与构建期不一致的历史会话）。 */
  disabled?: boolean
}

/**
 * 会话内的能力编辑：`CapabilityBar` 的**落库壳**。
 *
 * 表现（`+` 菜单 / 三个选择器 / chip）全在 `CapabilityBar`，这里只做两件事：
 *
 *  1. 把读面快照折成三态选择（`choiceFromCapabilities`）；
 *  2. 把补丁发给 `PATCH /sessions/:id/capabilities`——写入即生效于**下一个轮次边界**
 *     （忙时服务端顺延，当前轮不打断），界面只认响应里的新快照。
 *
 * 与新建会话面板的区别只有"落库方式"：那边是本地草稿、随 `POST /sessions` 首轮提交。
 */
export function ComposerCapabilities({ sessionId, capabilities, disabled }: Props) {
  const updateCapabilities = useMate((s) => s.updateSessionCapabilities)
  // 一次只发一个补丁：补丁是**相对服务端当前值**合并的，而界面在回执回来前还拿着旧快照——
  // 连点两下会各自基于旧值算出"只勾了这一个"，后到的那条把前一条覆盖掉（表现为"点了 A 丢了 B"）。
  // 本地一次往返只有几毫秒，串行化用户感知不到，但语义是对的。
  const [saving, setSaving] = useState(false)

  const onChange = (patch: SessionCapabilitiesPatch) => {
    if (saving) return
    setSaving(true)
    void updateCapabilities(sessionId, patch).finally(() => setSaving(false))
  }

  return (
    <CapabilityBar
      value={choiceFromCapabilities(capabilities)}
      onChange={onChange}
      disabled={disabled}
      saving={saving}
      applyHint="next-round"
    />
  )
}
