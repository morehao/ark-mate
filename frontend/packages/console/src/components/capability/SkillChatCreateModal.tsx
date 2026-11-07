import { useState } from 'react'
import { Alert, Button, Modal, Space } from 'antd'
import { CopyOutlined, MessageOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { tokens } from '@ark-mate/ui'
import { useMate } from '../../store'

/**
 * 「通过对话创建」的提示词。
 *
 * 它同时是**产品文案**与**操作指引**：用户把它发给助手，助手再走平台的能力写面。
 * 提示词里不出现内部文件格式与内部标识，只用用户能理解的说法。
 */
const SKILL_CREATE_PROMPT = `帮我起草一个技能，我会自己复制到平台的技能面板里保存。

技能信息：
- 名称：
- 用途（一句话）：
- 触发场景（什么时候该用）：
- 工作步骤：

请按下面的格式输出，方便我直接粘进「扩展 · 技能 → 新建」表单：
1. 一句**名称**（简短、能一眼看出用途）；
2. 一句**用途描述**（what/when，别写成营销文案）；
3. 一段**正文**（Markdown，写清「何时使用」与「工作步骤」，可以直接当技能正文）。

只给我这三样（名称 / 用途 / 正文），不要在对话里直接落库——我会在面板确认后再保存。`

/** 通过对话创建：给一段可直接发送的提示词 + 复制 + 去对话（D2 会把提示词预填进新建面板）。 */
export function SkillChatCreateModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate()
  const showToast = useMate((s) => s.showToast)
  const setPendingPrompt = useMate((s) => s.setPendingPrompt)
  const [copied, setCopied] = useState(false)

  const copy = async (): Promise<void> => {
    try {
      await navigator.clipboard.writeText(SKILL_CREATE_PROMPT)
      setCopied(true)
      showToast('提示词已复制', 'success')
    } catch {
      showToast('复制失败：请手动选中文本复制')
    }
  }

  const goChat = (): void => {
    setPendingPrompt(SKILL_CREATE_PROMPT)
    onClose()
    navigate('/')
  }

  return (
    <Modal
      open={open}
      onCancel={onClose}
      width={720}
      destroyOnHidden
      title={
        <div>
          <div style={{ fontSize: 15, fontWeight: 600 }}>通过对话创建</div>
          <div style={{ marginTop: 2, fontSize: 11, color: tokens.textPlaceholder }}>让助手帮你起草，确认后你自己保存到本地。</div>
        </div>
      }
      footer={
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <Button onClick={onClose}>取消</Button>
          <Space size={8}>
            <Button icon={<CopyOutlined />} onClick={() => void copy()}>
              {copied ? '已复制' : '复制提示词'}
            </Button>
            <Button type="primary" icon={<MessageOutlined />} onClick={goChat}>
              去对话创建
            </Button>
          </Space>
        </div>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ fontSize: 12 }}
        message="在对话里把需求说清楚，助手帮你拟一份技能草稿"
        description="把助手给的「名称 / 用途 / 正文」带回本页「新建」表单粘贴保存；保存前会自动检查内容是否安全。"
      />
      <pre
        style={{
          margin: '12px 0 0',
          padding: 12,
          background: tokens.codeBg,
          border: `1px solid ${tokens.border}`,
          borderRadius: tokens.radiusSm,
          fontFamily: tokens.fontMono,
          fontSize: 12,
          lineHeight: 1.65,
          whiteSpace: 'pre-wrap',
          maxHeight: 380,
          overflow: 'auto',
        }}
      >
        {SKILL_CREATE_PROMPT}
      </pre>
    </Modal>
  )
}
