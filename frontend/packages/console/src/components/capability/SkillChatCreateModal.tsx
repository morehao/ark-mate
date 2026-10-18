import { useState } from 'react'
import { Alert, Button, Modal, Space } from 'antd'
import { CopyOutlined, MessageOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { tokens } from '@ark-mate/ui'
import { useMate } from '../../store'

/**
 * 「通过对话创建」的提示词。
 *
 * 它同时是**产品文案**与**操作指引**：用户把它发给 Agent，Agent 再走平台的能力写面。
 * `name` 取 id 词法（小写字母/数字/连字符）是底座的硬契约，所以提示词里直接写清，
 * 免得用户拿到一条「创建成功但底座读不到」的技能。
 */
const SKILL_CREATE_PROMPT = `请帮我创建一个技能，并保存为当前平台的 skill 定义。

技能信息：
- 名称：
- 用途（一句话）：
- 触发场景（什么时候该用）：
- 工作步骤：

要求：
1. SKILL.md 的 YAML front matter 必须包含 name 与 description；name 用小写字母/数字/连字符（例如 weekly-report），它是定义 id；
2. 正文写清「何时使用」与「工作步骤」，不要写成营销文案；
3. 保存后确认它出现在「扩展 · 技能」列表里，并告诉我 id。`

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
      showToast('提示词已复制')
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
          <div style={{ marginTop: 2, fontSize: 11, color: tokens.textPlaceholder }}>创建个性化技能，将保存在本地使用。</div>
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
        message="在对话里把需求说清楚，Agent 会按平台的能力写面创建它"
        description="保存时会走与表单相同的闸门：正文扫描不过（critical）→ 拒载且零字节落盘。"
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
