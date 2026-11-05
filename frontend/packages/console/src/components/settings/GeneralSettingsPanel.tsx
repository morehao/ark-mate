import { useState } from 'react'
import { Segmented } from 'antd'
import { tokens } from '@ark-mate/ui'
import type { ReactNode } from 'react'
import { useMate } from '../../store'
import { getSendKey, setSendKey, type SendKeyMode } from '../../console/sendKey'
import { CardHeader } from './CardHeader'

/**
 * 设置 · 通用——**作为居中弹窗的内容体**渲染。
 *
 * 设置不是页面：左栏 footer 的「设置」点开二级菜单，菜单项点开这个居中的 Modal
 * （见 `settings/SettingsModals.tsx`）。所以这里只出内容，不再自带 PageContainer 骨架。
 *
 * 本节只做两件事：
 *   1. 只读展示**平台分配的默认工作区**路径（每个新会话未指定目录时的落点）；
 *   2. 输入区的键盘操作：**发送键档位可切换**（2026-11-05 起开关从输入卡右下角搬到
 *      这里——那条常驻提示被用户判定为噪声），并说明该档位下发送/换行各是哪个键。
 *
 * **不含**「工作区起始位置」的可编辑偏好列表：v14.6 起前后端一起去掉了这个用户设置面。
 * 目录浏览的起点偏好只由部署级配置（`MATE_WORKSPACE_ROOTS` / config.json）决定，普通用户无需设置。
 */

const mono = { fontFamily: tokens.fontMono, fontSize: 12.5 } as const

/** 一行只读键值对（左标签、右值）。 */
function KVRow({ label, value, last }: { label: ReactNode; value: ReactNode; last?: boolean }) {
  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        padding: '10px 16px',
        borderBottom: last ? 'none' : `1px solid ${tokens.border}`,
      }}
    >
      <span style={{ fontSize: 12.5, color: tokens.textSecondary, flex: 'none' }}>{label}</span>
      <span
        style={{
          marginLeft: 'auto',
          paddingLeft: 12,
          minWidth: 0,
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
        }}
      >
        {value}
      </span>
    </div>
  )
}

export function GeneralSettingsPanel() {
  // 默认工作区（只读）：取自登记表的 default_id 那一项（平台分配的落地目录）。
  const workspaces = useMate((s) => s.workspaces)
  const defaultWs =
    workspaces?.items.find((w) => w.id === workspaces.default_id) ?? workspaces?.items.find((w) => w.is_default)
  // 发送键档位：读一次本地值做初值；改动即写回（输入区每次按键现读，所以立刻生效）。
  const [sendMode, setMode] = useState<SendKeyMode>(getSendKey)
  const changeSendMode = (next: SendKeyMode) => {
    setMode(next)
    setSendKey(next)
  }

  return (
    <div className="fg-setsec">
      <div className="fg-setsec-card">
        <CardHeader
          title="默认工作区"
          hint="每个会话都在一个「工作区」里工作——目录就是助手能读写的边界。这里是平台分配的默认工作区路径。"
        />
        <KVRow
          label="默认工作区"
          last
          value={
            <span style={{ ...mono, color: tokens.text }}>{defaultWs?.path || '（默认工作区未登记）'}</span>
          }
        />
      </div>

      <div className="fg-setsec-card" style={{ marginTop: 14 }}>
        <CardHeader
          title="键盘操作"
          hint="发送消息与换行的快捷键；发送键档位在这里切换（⌘/Ctrl + Enter 在任何档位下都能直接发送）。"
        />
        <KVRow
          label="发送键"
          value={
            <Segmented<SendKeyMode>
              size="small"
              value={sendMode}
              onChange={changeSendMode}
              options={[
                { label: 'Enter 发送', value: 'enter-send' },
                { label: 'Shift+Enter 发送', value: 'enter-newline' },
              ]}
            />
          }
        />
        <KVRow
          label="发送消息"
          value={
            <kbd style={{ ...mono, color: tokens.text }}>
              {sendMode === 'enter-send' ? 'Enter' : 'Shift + Enter'}
            </kbd>
          }
        />
        <KVRow
          label="换行"
          last
          value={
            <kbd style={{ ...mono, color: tokens.text }}>
              {sendMode === 'enter-send' ? 'Shift + Enter' : 'Enter'}
            </kbd>
          }
        />
      </div>
    </div>
  )
}
