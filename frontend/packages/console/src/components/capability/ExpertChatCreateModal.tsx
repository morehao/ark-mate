import { useState } from 'react'
import { Alert, Button, Modal, Space } from 'antd'
import { CopyOutlined, MessageOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { tokens } from '@ark-mate/ui'
import { useMate } from '../../store'

/**
 * 「通过对话创建专家」的提示词。
 *
 * 它同时是**产品文案**与**操作指引**：用户把它发给助手，助手再调平台能力工具面
 * （`mate mcp platform` 的 expert_* 工具）建出专家。提示词里不出现内部字段名与协议细节，
 * 只用用户能理解的说法；并明确「建好不会让当前会话变成该专家」这条语义。
 */
const EXPERT_CREATE_PROMPT = `请帮我创建一个专家（一组会话能力预设），并保存到当前平台。

专家信息：
- 名称：
- 一句话定位（擅长什么）：
- 推荐能力：用到哪些技能 / 连接器（可选）
- 推荐模型：（可选，不填则用默认）
- 角色设定：这位专家该怎么说话、按什么标准做事（可选）

要求：
1. 用平台的能力工具把它保存下来（expert_create），id 用小写字母数字连字符；
2. 角色设定写得具体，但不要塞入模板占位符或可疑标记；
3. 保存后告诉我专家的名称，并说明「下次新建会话时可以选择它」。`

/** 通过对话创建专家：给一段可直接发送的提示词 + 复制 + 去对话（复用 `pendingPrompt` 预填进新建面板）。 */
export function ExpertChatCreateModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate()
  const showToast = useMate((s) => s.showToast)
  const setPendingPrompt = useMate((s) => s.setPendingPrompt)
  const [copied, setCopied] = useState(false)

  const copy = async (): Promise<void> => {
    try {
      await navigator.clipboard.writeText(EXPERT_CREATE_PROMPT)
      setCopied(true)
      showToast('提示词已复制', 'success')
    } catch {
      showToast('复制失败：请手动选中文本复制')
    }
  }

  const goChat = (): void => {
    setPendingPrompt(EXPERT_CREATE_PROMPT)
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
          <div style={{ fontSize: 15, fontWeight: 600 }}>通过对话创建专家</div>
          <div style={{ marginTop: 2, fontSize: 11, color: tokens.textPlaceholder }}>
            说出需求，助手帮你建好并保存在本地。
          </div>
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
        message="在对话里把专家说清楚，助手会帮你创建并保存"
        description="保存前会自动校验角色设定是否安全。"
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
        {EXPERT_CREATE_PROMPT}
      </pre>
    </Modal>
  )
}
