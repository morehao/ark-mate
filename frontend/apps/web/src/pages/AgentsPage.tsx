import { useCallback, useEffect, useState } from 'react'
import { AutoComplete, Button, Form, Input, Modal, Popconfirm, Select, Space, Tag, Tooltip } from 'antd'
import {
  PlusOutlined,
  ReloadOutlined,
  StopOutlined,
  PlayCircleOutlined,
  MedicineBoxOutlined,
  SwapOutlined,
} from '@ant-design/icons'
import { agentStateMeta, Dot, PageContainer, Pill, tokens } from '@ark/ui'
import {
  agentDiagnostics,
  createAgent,
  deleteAgent,
  listProviders,
  patchAgent,
  setDefaultAgent,
  startAgent,
  stopAgent,
  upsertProvider,
  toErrorMessage,
} from '@ark/api'
import type { Agent, AgentDiagnostics, NativePermissionMode, PermissionAction, Provider } from '@ark/types'
import { useForge } from '../store'
import { parsePolicyTools, policyColor, policySummary } from '../console/policyView'

const AGENT_ID_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/
/** 种子判定：服务端权威字段（内置播种 dsh/opencode + 历史保留前缀 agent-）。 */
const isSeed = (a: Pick<Agent, 'seed'>): boolean => a.seed === true
/** 新建 id 校验：`agent-` 为种子保留前缀（服务端同规则）。 */
const hasReservedPrefix = (id: string): boolean => id.startsWith('agent-')

interface AgentFormValues {
  id?: string
  name?: string
  runtimes: string[]
  provider?: string
  /** 权限策略（C1 起可编辑）：默认动作 + 原生模式 + 工具项 JSON。 */
  policy_default?: PermissionAction
  policy_native?: NativePermissionMode
  policy_tools?: string
}

const POLICY_DEFAULT_OPTIONS: { label: string; value: PermissionAction }[] = [
  { label: 'ask（征询）', value: 'ask' },
  { label: 'allow（自动放行）', value: 'allow' },
  { label: 'deny（自动拒绝）', value: 'deny' },
]

const POLICY_NATIVE_OPTIONS: { label: string; value: NativePermissionMode }[] = [
  { label: 'auto（按 default 推导）', value: 'auto' },
  { label: 'off（不注入原生配置）', value: 'off' },
  { label: 'read-only（只读沙箱）', value: 'read-only' },
  { label: 'workspace-write（工作区可写）', value: 'workspace-write' },
  { label: 'danger-full-access（完全访问）', value: 'danger-full-access' },
]



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
  // 切换「当前 Agent」的在途标记（与启停各自独立，避免互相盖住 loading）。
  const [busyDefaultId, setBusyDefaultId] = useState<string | null>(null)
  const [agentModal, setAgentModal] = useState<{ mode: 'create' } | { mode: 'edit'; agent: Agent } | null>(null)
  const [providerModal, setProviderModal] = useState<{ id: string; existing: Provider | null } | null>(null)
  // A3 诊断抽屉：{ agentId, data | null（加载中）, err }
  const [diag, setDiag] = useState<{ id: string; name: string; data: AgentDiagnostics | null; err: string | null } | null>(null)
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

  // 切换「当前 Agent」：全局默认单例（新建会话/模型目录缺省取它）；服务端落盘、重启保持。
  const onSetDefault = async (a: Agent): Promise<void> => {
    if (a.is_default) return
    setBusyDefaultId(a.id)
    const ok = await guard(setDefaultAgent(a.id))
    setBusyDefaultId(null)
    if (ok === undefined) return
    showToast(`已切换当前 Agent：${ok.name || ok.id}（新建会话将使用它）`)
    afterMutate()
  }

  // 删除自定义 agent：回执带影响面（绑定会话数 / 作废的活跃会话数 / 是否当前 Agent），
  // 如实转述给用户——归属不可变，被删 agent 的会话不会自动改绑，需要用户自己决定后续。
  const onDelete = async (a: Agent): Promise<void> => {
    const resp = await guard(deleteAgent(a.id))
    if (resp === undefined) return
    const parts = [`已删除 ${resp.deleted}`]
    if (resp.affected_conversations > 0) {
      parts.push(`${resp.affected_conversations} 条会话失去执行者（历史仍可读；用同一 id 重新注册可复活绑定）`)
    }
    if (resp.detached_conversations > 0) parts.push(`${resp.detached_conversations} 条活跃会话已作废`)
    if (resp.was_default) parts.push('「当前 Agent」已回落内置默认')
    showToast(parts.join('；'))
    afterMutate()
  }

  const openCreate = (): void => {
    agentForm.setFieldsValue({ runtimes: [] })
    setAgentModal({ mode: 'create' })
  }

  const openEdit = (a: Agent): void => {
    const p = a.permission_policy
    agentForm.setFieldsValue({
      name: a.name,
      runtimes: [],
      provider: a.provider,
      policy_default: p?.default ?? 'ask',
      policy_native: p?.native_mode ?? 'auto',
      policy_tools: p?.tools && Object.keys(p.tools).length > 0 ? JSON.stringify(p.tools) : '',
    })
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
      let tools: Record<string, PermissionAction> | undefined
      try {
        tools = parsePolicyTools(v.policy_tools)
      } catch (e) {
        showToast(toErrorMessage(e))
        return
      }
      const ok = await guard(
        patchAgent(agentModal.agent.id, {
          name: v.name,
          provider: v.provider || undefined,
          runtimes: v.runtimes?.length ? v.runtimes : undefined,
          permission_policy: {
            default: v.policy_default ?? 'ask',
            native_mode: v.policy_native ?? 'auto',
            tools,
          },
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

  // 打开/刷新诊断（A3：stderr 诊断尾巴外露，排障不必翻 daemon 日志）。
  const openDiagnostics = async (a: Agent) => {
    setDiag({ id: a.id, name: a.name, data: null, err: null })
    try {
      const data = await agentDiagnostics(a.id)
      setDiag({ id: a.id, name: a.name, data, err: null })
    } catch (e) {
      setDiag({ id: a.id, name: a.name, data: null, err: toErrorMessage(e) })
    }
  }
  const refreshDiagnostics = async () => {
    if (!diag) return
    const cur = diag
    setDiag({ ...cur, data: null, err: null })
    try {
      const data = await agentDiagnostics(cur.id)
      setDiag({ ...cur, data, err: null })
    } catch (e) {
      setDiag({ ...cur, data: null, err: toErrorMessage(e) })
    }
  }

  const providerOptions = providers.map((p) => p.id)

  return (
    <PageContainer
      title="Agents"
      description="自定义 Agent 注册向导（不写代码新增，definitions 持久化）+ 启停/热更新 + 切换「当前 Agent」（新建会话与模型目录缺省都用它，选择落 definitions 目录 default.json）。下方 provider 档案供密钥注入（只写不读回）。"
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
        const seed = isSeed(a)
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
            {a.is_default && (
              <Tooltip title="当前 Agent：新建会话与模型目录缺省都用它">
                <Tag color="blue" style={{ marginInlineEnd: 0, fontSize: 11 }}>
                  当前
                </Tag>
              </Tooltip>
            )}
            {a.provider && <Tag style={{ marginInlineEnd: 0, fontSize: 11 }}>{a.provider}</Tag>}
            <Tooltip
              title={
                a.mcps === undefined ? (
                  <div style={{ fontSize: 12 }}>
                    未声明白名单：该 agent 的会话按 runtime 支持的传输**全量注入**已注册 MCP。
                    <div style={{ color: tokens.textPlaceholder, fontFamily: tokens.fontMono }}>
                      {'PATCH /agents/:id {"mcps": [...]}'}
                    </div>
                  </div>
                ) : a.mcps.length === 0 ? (
                  <div style={{ fontSize: 12 }}>
                    **禁用 MCP 注入**（空白名单，不是"没设置"）：该 agent 的会话一个 MCP server 都不给。
                    <div style={{ color: tokens.textPlaceholder, fontFamily: tokens.fontMono }}>
                      {'PATCH /agents/:id {"mcps": null}'}
                    </div>
                  </div>
                ) : (
                  <div style={{ fontSize: 12 }}>
                    <div>MCP 白名单（只投这几个）：</div>
                    {a.mcps.map((id) => (
                      <div key={id} style={{ fontFamily: tokens.fontMono }}>
                        {id}
                      </div>
                    ))}
                    <div style={{ color: tokens.textPlaceholder }}>改动在**下一轮**生效（空闲会话重建后按新白名单注入）</div>
                  </div>
                )
              }
            >
              <Tag
                color={a.mcps === undefined ? 'default' : a.mcps.length === 0 ? 'red' : 'green'}
                style={{ marginInlineEnd: 0, fontSize: 11 }}
              >
                {a.mcps === undefined ? 'MCP 全量' : a.mcps.length === 0 ? 'MCP 禁用' : `MCP ${a.mcps.length}`}
              </Tag>
            </Tooltip>
            <Tooltip
              title={
                a.permission_policy ? (
                  <div style={{ fontSize: 12 }}>
                    <div>default：{a.permission_policy.default || 'ask'}</div>
                    {a.permission_policy.native_mode && a.permission_policy.native_mode !== 'auto' && (
                      <div>native_mode：{a.permission_policy.native_mode}</div>
                    )}
                    {Object.entries(a.permission_policy.tools ?? {}).length > 0 ? (
                      Object.entries(a.permission_policy.tools ?? {}).map(([k, v]) => (
                        <div key={k}>
                          {k}：{v}
                        </div>
                      ))
                    ) : (
                      <div style={{ color: tokens.textPlaceholder }}>无工具粒度项</div>
                    )}
                  </div>
                ) : (
                  '未声明权限策略（全 ask：每个工具都征询）'
                )
              }
            >
              <Tag color={policyColor(a.permission_policy?.default)} style={{ marginInlineEnd: 0, fontSize: 11 }}>
                策略 {policySummary(a.permission_policy)}
              </Tag>
            </Tooltip>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
              <span style={{ color: st.color }}>{st.label}</span>
              {seed && <span style={{ color: tokens.textPlaceholder }}>（种子）</span>}
            </span>
            {(a.conversation_count ?? 0) > 0 && (
              <Tooltip title="绑定该 agent 的会话数（含归档）：删除后历史仍可读，但无法继续提问">
                <span style={{ color: tokens.textPlaceholder, fontSize: 12 }}>{a.conversation_count} 会话</span>
              </Tooltip>
            )}
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
              {!a.is_default && (
                <Tooltip title="切换为当前 Agent：新建会话与模型目录缺省都用它（可随时切回）">
                  <Button
                    size="small"
                    loading={busyDefaultId === a.id}
                    icon={<SwapOutlined />}
                    onClick={() => void onSetDefault(a)}
                  >
                    设为当前
                  </Button>
                </Tooltip>
              )}
              <Button size="small" icon={<MedicineBoxOutlined />} onClick={() => void openDiagnostics(a)}>
                诊断
              </Button>
              <Button size="small" onClick={() => openEdit(a)}>
                编辑
              </Button>
              {!seed && (
                <Popconfirm
                  title={`删除 ${a.name}？`}
                  description={
                    <div style={{ maxWidth: 320 }}>
                      仅删除自定义 agent 定义（不写 Go 代码、不影响运行进程）。
                      {(a.conversation_count ?? 0) > 0 && (
                        <div style={{ marginTop: 4 }}>
                          有 {a.conversation_count} 条会话绑定它：删除后历史仍可读，但**无法继续提问**
                          （用同一 id 重新注册可复活绑定）。
                        </div>
                      )}
                      {a.is_default && <div style={{ marginTop: 4 }}>它当前是「当前 Agent」，删除后回落内置默认。</div>}
                    </div>
                  }
                  okText="删除"
                  okButtonProps={{ danger: true }}
                  onConfirm={() => void onDelete(a)}
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


      {/* A3 诊断（stderr 诊断尾巴 + 最近失败；连接态） */}
      <Modal
        title={diag ? `诊断 · ${diag.name}` : '诊断'}
        open={diag !== null}
        onCancel={() => setDiag(null)}
        footer={[
          <Button key="refresh" size="small" icon={<ReloadOutlined />} onClick={() => void refreshDiagnostics()}>
            重新采集
          </Button>,
          <Button key="close" size="small" type="primary" onClick={() => setDiag(null)}>
            关闭
          </Button>,
        ]}
        width={720}
      >
        {diag?.err ? (
          <div style={{ color: tokens.textSecondary, fontSize: 12.5, lineHeight: 1.7 }}>
            采集失败：{diag.err}
            <div style={{ marginTop: 6, color: tokens.textPlaceholder }}>
              daemon 可能不可达或 agent 已被删除。
            </div>
          </div>
        ) : !diag?.data ? (
          <div style={{ color: tokens.textPlaceholder, fontSize: 12.5, padding: '12px 0' }}>采集诊断快照…</div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10, fontSize: 12.5 }}>
            <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
              <span>状态：<b>{diag.data.state}</b></span>
              <span>runtime：<code>{diag.data.runtime}</code>{diag.data.runtime_name ? `（${diag.data.runtime_name}）` : ''}</span>
              <span>
                连接：
                <b style={{ color: diag.data.connected ? tokens.primary : tokens.textSecondary }}>
                  {diag.data.connected ? '已连接' : '未连接'}
                </b>
                {diag.data.connected && diag.data.pid ? ` · pid ${diag.data.pid}` : ''}
              </span>
            </div>
            {diag.data.command && (
              <div style={{ color: tokens.textSecondary }}>
                启动命令：<code>{diag.data.command}</code>
              </div>
            )}
            {diag.data.remediation && diag.data.remediation.length > 0 && (
              <div
                style={{
                  border: `1px solid ${tokens.warningBorder}`,
                  borderRadius: 10,
                  padding: '8px 12px',
                  background: tokens.warningBg,
                }}
              >
                <div style={{ fontFamily: tokens.fontMono, fontSize: 11, color: tokens.textPlaceholder, marginBottom: 4 }}>
                  建议处理步骤（按当前事实推导：命令不可执行 / 档案缺失 / 底座安装与登录）
                </div>
                <ol style={{ margin: 0, paddingLeft: 18, lineHeight: 1.7 }}>
                  {diag.data.remediation.map((step) => (
                    <li key={step}>{step}</li>
                  ))}
                </ol>
              </div>
            )}
            {diag.data.last_error && (
              <div
                style={{
                  border: `1px solid ${tokens.border}`,
                  borderRadius: 10,
                  padding: '8px 12px',
                  background: tokens.softFill,
                }}
              >
                <div style={{ fontFamily: tokens.fontMono, fontSize: 11, color: tokens.textPlaceholder }}>
                  最近失败 · {diag.data.last_error.at} · [{diag.data.last_error.class}/{diag.data.last_error.code}]
                </div>
                <div style={{ marginTop: 4 }}>{diag.data.last_error.details}</div>
                {diag.data.last_error.conversation_id && (
                  <div style={{ marginTop: 4, fontFamily: tokens.fontMono, fontSize: 11, color: tokens.textSecondary }}>
                    会话 {diag.data.last_error.conversation_id}
                  </div>
                )}
              </div>
            )}
            <div>
              <div style={{ fontFamily: tokens.fontMono, fontSize: 11, color: tokens.textPlaceholder, marginBottom: 4 }}>
                stderr 诊断尾巴 · 来源 {diag.data.stderr_source === 'live' ? '当前子进程（实时）' : diag.data.stderr_source === 'last_error' ? '上次失败时采集' : '无（无连接且无失败记录）'}
              </div>
              {diag.data.stderr_tail.length === 0 ? (
                <div style={{ color: tokens.textPlaceholder }}>
                  无输出。runtime 可能不写 stderr，或进程尚未产生输出。
                </div>
              ) : (
                <pre
                  style={{
                    margin: 0,
                    maxHeight: 260,
                    overflow: 'auto',
                    fontFamily: tokens.fontMono,
                    fontSize: 11,
                    lineHeight: 1.55,
                    whiteSpace: 'pre-wrap',
                    overflowWrap: 'anywhere',
                    border: `1px solid ${tokens.border}`,
                    borderRadius: 10,
                    padding: '8px 12px',
                    background: tokens.softFill,
                  }}
                >
                  {diag.data.stderr_tail.join('\n')}
                </pre>
              )}
            </div>
            <div style={{ color: tokens.textPlaceholder, fontSize: 11 }}>采集时刻：{diag.data.checked_at}</div>
          </div>
        )}
      </Modal>

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
                    !value || !hasReservedPrefix(value) ? Promise.resolve() : Promise.reject(new Error('agent- 为种子保留前缀')),
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
          {agentModal?.mode === 'edit' && (
            <>
              <Form.Item name="policy_default" label="权限策略 · 默认动作（未匹配工具时）">
                <Select options={POLICY_DEFAULT_OPTIONS} />
              </Form.Item>
              <Form.Item
                name="policy_native"
                label="权限策略 · 原生模式（投影到底座开关：dsh DSH_PERMISSION_MODE / opencode permission）"
              >
                <Select options={POLICY_NATIVE_OPTIONS} />
              </Form.Item>
              <Form.Item
                name="policy_tools"
                label='权限策略 · 工具项（JSON，如 {"bash":"deny"}；留空 = 无工具粒度项）'
                rules={[
                  () => ({
                    validator: (_r, value: string) => {
                      try {
                        parsePolicyTools(value)
                        return Promise.resolve()
                      } catch (e) {
                        return Promise.reject(e as Error)
                      }
                    },
                  }),
                ]}
              >
                <Input.TextArea rows={2} placeholder='{"bash":"deny","edit":"allow"}' style={{ fontFamily: tokens.fontMono, fontSize: 12 }} />
              </Form.Item>
            </>
          )}
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
          {/* Model 仅作档案留痕（模型选择的权威来源是运行时会话配置项，平台不内置清单）。 */}
          <Form.Item name="model" label="Model（档案留痕，可留空）">
            <AutoComplete placeholder="如 gpt-5 / deepseek-chat" allowClear options={[]} />
          </Form.Item>
          <Form.Item name="key" label={providerModal?.existing?.has_key ? '密钥（已存；输入即覆盖）' : '密钥'}>
            <Input.Password placeholder="只写不读回；留空 = 不变更" autoComplete="new-password" />
          </Form.Item>
        </Form>
      </Modal>
    </PageContainer>
  )
}
