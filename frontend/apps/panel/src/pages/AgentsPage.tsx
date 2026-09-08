import { useCallback, useEffect, useState } from 'react'
import { Button, Form, Input, Modal, Popconfirm, Select, Space, Tag } from 'antd'
import { PlusOutlined, ReloadOutlined, StopOutlined, PlayCircleOutlined } from '@ant-design/icons'
import { agentStateMeta, Dot, PageContainer, Pill, tokens } from '@ark/ui'
import { createAgent, deleteAgent, listProviders, patchAgent, startAgent, stopAgent, upsertProvider, toErrorMessage } from '@ark/api'
import type { Agent, Provider } from '@ark/types'
import { useForge } from '../store'

const AGENT_ID_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/
const isSeed = (id: string): boolean => id.startsWith('agent-')

interface AgentFormValues {
  id?: string
  name?: string
  runtimes: string[]
  provider?: string
}

interface ProviderFormValues {
  type: string
  model?: string
  key?: string
}

/** Agents 管理页（P2）：注册向导（definitions 持久化）+ 启停 + 热更新 + 删除 + provider 注入面。 */
export function AgentsPage() {
  const agents = useForge((s) => s.agents)
  const refreshAgents = useForge((s) => s.refreshAgents)
  const showToast = useForge((s) => s.showToast)

  const [providers, setProviders] = useState<Provider[]>([])
  const [busyId, setBusyId] = useState<string | null>(null)
  const [agentModal, setAgentModal] = useState<{ mode: 'create' } | { mode: 'edit'; agent: Agent } | null>(null)
  const [providerModal, setProviderModal] = useState<{ id: string; existing: Provider | null } | null>(null)
  const [agentForm] = Form.useForm<AgentFormValues>()
  const [providerForm] = Form.useForm<ProviderFormValues>()

  const loadProviders = useCallback(async () => {
    try {
      setProviders(await listProviders())
    } catch (err) {
      showToast(toErrorMessage(err))
    }
  }, [showToast])

  useEffect(() => {
    void refreshAgents()
    void loadProviders()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const afterMutate = (): void => {
    void refreshAgents()
    void loadProviders()
  }

  const guard = async <T,>(p: Promise<T>): Promise<T | undefined> => {
    try {
      return await p
    } catch (err) {
      showToast(toErrorMessage(err))
      return undefined
    }
  }

  const onToggleRunning = async (a: Agent): Promise<void> => {
    setBusyId(a.id)
    if (a.state === 'running') {
      await guard(stopAgent(a.id))
    } else {
      await guard(startAgent(a.id))
    }
    setBusyId(null)
    afterMutate()
  }

  const openCreate = (): void => {
    agentForm.setFieldsValue({ runtimes: [] })
    setAgentModal({ mode: 'create' })
  }

  const openEdit = (a: Agent): void => {
    agentForm.setFieldsValue({ name: a.name, runtimes: [], provider: a.provider })
    setAgentModal({ mode: 'edit', agent: a })
  }

  const submitAgent = async (): Promise<void> => {
    if (!agentModal) return
    const v = await agentForm.validateFields()
    if (agentModal.mode === 'create') {
      const ok = await guard(
        createAgent({ id: v.id ?? '', name: v.name, runtimes: v.runtimes ?? [], provider: v.provider || undefined }),
      )
      if (ok === undefined) return
    } else {
      const ok = await guard(
        patchAgent(agentModal.agent.id, {
          name: v.name,
          provider: v.provider || undefined,
          runtimes: v.runtimes?.length ? v.runtimes : undefined,
        }),
      )
      if (ok === undefined) return
    }
    setAgentModal(null)
    agentForm.resetFields()
    afterMutate()
  }

  const submitProvider = async (): Promise<void> => {
    if (!providerModal) return
    const v = await providerForm.validateFields()
    const patch: { type: string; model?: string; key?: string } = { type: v.type, model: v.model || undefined }
    if (v.key != null && v.key !== '') patch.key = v.key
    const ok = await guard(upsertProvider(providerModal.id, patch))
    if (ok === undefined) return
    setProviderModal(null)
    providerForm.resetFields()
    afterMutate()
  }

  const providerOptions = providers.map((p) => p.id)

  return (
    <PageContainer
      title="Agents"
      description="自定义 Agent 注册向导（不写代码新增，definitions 持久化）+ 启停/热更新。下方 provider 档案供密钥注入（只写不读回）。"
      extra={
        <Space>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => { void refreshAgents(); void loadProviders() }}>
            刷新
          </Button>
          <Button size="small" type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            注册 Agent
          </Button>
        </Space>
      }
    >
      {agents.length === 0 && (
        <div style={{ padding: 32, textAlign: 'center', color: tokens.textPlaceholder, fontSize: 13 }}>
          暂无 Agent（服务不可达或未播种）。检查后端 forge serve 是否在运行。
        </div>
      )}
      {agents.map((a, i) => {
        const st = agentStateMeta(a.state)
        const seed = isSeed(a.id)
        return (
          <div
            key={a.id}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 12,
              padding: '12px 18px',
              borderBottom: i === agents.length - 1 ? 'none' : `1px solid ${tokens.border}`,
            }}
          >
            <Dot color={st.color} size={7} />
            <b style={{ fontSize: 13.5, fontWeight: 600 }}>{a.name}</b>
            {a.provider && <Tag style={{ marginInlineEnd: 0, fontSize: 11 }}>{a.provider}</Tag>}
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
              <span style={{ color: st.color }}>{st.label}</span>
              {seed && <span style={{ color: tokens.textPlaceholder }}>（种子）</span>}
            </span>
            <span style={{ marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 8 }}>
              <span style={{ fontFamily: tokens.fontMono, fontSize: 11.5, color: tokens.textSecondary }}>{a.id}</span>
              <Button
                size="small"
                type={a.state === 'running' ? 'default' : 'primary'}
                loading={busyId === a.id}
                icon={a.state === 'running' ? <StopOutlined /> : <PlayCircleOutlined />}
                onClick={() => void onToggleRunning(a)}
              >
                {a.state === 'running' ? '停止' : '启动'}
              </Button>
              <Button size="small" onClick={() => openEdit(a)}>
                编辑
              </Button>
              {!seed && (
                <Popconfirm
                  title={`删除 ${a.name}？`}
                  description="仅删除自定义 agent 定义（不写 Go 代码、不影响运行进程）。"
                  okText="删除"
                  okButtonProps={{ danger: true }}
                  onConfirm={async () => {
                    if ((await guard(deleteAgent(a.id))) !== undefined) afterMutate()
                  }}
                >
                  <Button size="small" danger>
                    删除
                  </Button>
                </Popconfirm>
              )}
            </span>
          </div>
        )
      })}

      {/* —— provider 档案区（P2 注入面：密钥只写不读回） —— */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '20px 18px 8px' }}>
        <b style={{ fontSize: 13 }}>Provider 档案</b>
        <Button size="small" icon={<PlusOutlined />} onClick={() => { providerForm.resetFields(); setProviderModal({ id: '', existing: null }) }}>
          新建 Provider
        </Button>
      </div>
      {providers.length === 0 && (
        <div style={{ padding: '0 18px 12px', color: tokens.textPlaceholder, fontSize: 12 }}>
          暂无档案。新建后可被 agent 引用，启动时以其密钥注入子进程 env。
        </div>
      )}
      {providers.map((p) => (
        <div
          key={p.id}
          style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '7px 18px', fontSize: 12.5 }}
        >
          <span style={{ fontFamily: tokens.fontMono, fontSize: 12 }}>{p.id}</span>
          <Pill>{p.type}</Pill>
          {p.model && <span style={{ color: tokens.textSecondary }}>{p.model}</span>}
          <Tag style={{ marginInlineEnd: 0, fontSize: 11 }} color={p.has_key ? 'green' : 'default'}>
            {p.has_key ? '密钥已存' : '无密钥'}
          </Tag>
          <span style={{ marginLeft: 'auto' }}>
            <Button size="small" onClick={() => { providerForm.setFieldsValue({ type: p.type, model: p.model, key: '' }); setProviderModal({ id: p.id, existing: p }) }}>
              编辑
            </Button>
          </span>
        </div>
      ))}

      {/* Agent 注册/编辑向导 */}
      <Modal
        title={agentModal?.mode === 'edit' ? `编辑 ${agentModal.agent.id}` : '注册 Agent'}
        open={agentModal !== null}
        onCancel={() => { setAgentModal(null); agentForm.resetFields() }}
        onOk={() => void submitAgent()}
        okText="保存"
        width={440}
        destroyOnClose
      >
        <Form form={agentForm} layout="vertical" initialValues={{ name: '', provider: undefined }} onFinish={() => void submitAgent()}>
          {agentModal?.mode === 'create' && (
            <Form.Item
              name="id"
              label="ID（definitions 文件名 <id>.json；种子保留前缀 agent- 不可用）"
              rules={[
                { required: true, message: '必填' },
                { pattern: AGENT_ID_RE, message: '字母/数字开头，可含 _ -，最长 64' },
                () => ({
                  validator: (_r, value: string) =>
                    !value || !isSeed(value) ? Promise.resolve() : Promise.reject(new Error('agent- 为种子保留前缀')),
                }),
              ]}
            >
              <Input placeholder="如 alias-echo" style={{ fontFamily: tokens.fontMono }} autoComplete="off" />
            </Form.Item>
          )}
          <Form.Item name="name" label="名称">
            <Input placeholder="展示名（留空 = 用 ID）" autoComplete="off" />
          </Form.Item>
          <Form.Item
            name="runtimes"
            label="Runtimes（逗号/回车分隔，可多个）"
            rules={agentModal?.mode === 'create' ? [{ required: true, message: '至少一个 runtime' }] : []}
          >
            <Select
              mode="tags"
              open={false}
              placeholder="如 opencode, deepseek-harness"
              tokenSeparators={[',']}
            />
          </Form.Item>
          <Form.Item name="provider" label="Provider 档案（可选）">
            <Select
              allowClear
              placeholder="选择下方档案；留空 = 默认（不注入密钥）"
              options={providerOptions.map((id) => ({ value: id, label: id }))}
            />
          </Form.Item>
        </Form>
      </Modal>

      {/* Provider 档案编辑（新建时 id 必填） */}
      <Modal
        title={providerModal?.existing ? `编辑 ${providerModal.id}` : '新建 Provider'}
        open={providerModal !== null}
        onCancel={() => { setProviderModal(null); providerForm.resetFields() }}
        onOk={() => void submitProvider()}
        okText="保存"
        width={440}
        destroyOnClose
      >
        <Form form={providerForm} layout="vertical" onFinish={() => void submitProvider()}>
          {providerModal && !providerModal.existing && (
            <Form.Item name="id" label="ID" rules={[{ required: true, message: '必填' }, { pattern: AGENT_ID_RE, message: '字母/数字开头，可含 _ -，最长 64' }]}>
              <Input placeholder="如 openai / deepseek / my-custom" style={{ fontFamily: tokens.fontMono }} autoComplete="off" />
            </Form.Item>
          )}
          <Form.Item name="type" label="Type（env 变量名映射：openai→OPENAI_API_KEY，未知取大写 type + _API_KEY）" rules={[{ required: true, message: '必填' }]}>
            <Input placeholder="openai / anthropic / deepseek …" autoComplete="off" />
          </Form.Item>
          <Form.Item name="model" label="Model">
            <Input placeholder="如 gpt-4o（仅展示元数据）" autoComplete="off" />
          </Form.Item>
          <Form.Item name="key" label={providerModal?.existing?.has_key ? '密钥（已存；输入即覆盖）' : '密钥'}>
            <Input.Password placeholder="只写不读回；留空 = 不变更" autoComplete="new-password" />
          </Form.Item>
        </Form>
      </Modal>
    </PageContainer>
  )
}
