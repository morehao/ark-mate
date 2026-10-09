import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Alert,
  Button,
  Checkbox,
  Drawer,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Segmented,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { DeleteOutlined, EditOutlined, HistoryOutlined, PlayCircleOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import { Dot, PageContainer, tokens } from '@ark/ui'
import {
  createAutomation,
  deleteAutomation,
  listAutomationRuns,
  listAutomations,
  previewAutomation,
  runAutomationNow,
  toErrorMessage,
  updateAutomation,
} from '@ark/api'
import type { Automation, AutomationPreviewItem, AutomationRunRecord } from '@ark/types'
import { useNavigate } from 'react-router-dom'
import { useMate } from '../store'
import { WorkspacePicker } from '../components/WorkspacePicker'
import {
  KIND_OPTIONS,
  RUN_STATUS_META,
  STATE_META,
  TIMEZONES,
  WEEKDAYS,
  describeSchedule,
  fmtWhen,
  formFromAutomation,
  lastRunText,
  localTimezone,
  previewBody,
  runLine,
  writeBody,
  type ScheduleFormValues,
} from '../console/automationView'

// ——— 定时任务页（M4-6）———
//
// 三件事：**看得见**（列表 + 运行史，含没跑成的那些）、**说得清**（preview：时区/DST/weekly
// 叠加后的"到底什么时候跑"）、**改得动**（定义文件是事实源，页面只是它的编辑器）。
// 编辑时任何改动都先按"只改出现过的键"发 PUT，所以换 kind 时会显式把旧字段置 null 删掉。

interface FormValues extends ScheduleFormValues {
  id?: string
  name: string
  enabled: boolean
  agent_id: string
  runtime_hint?: string
  cwd: string
  model?: string
  prompt: string
  max_run_duration?: string
}

const toneColor = (tone: 'success' | 'muted' | 'warning' | 'danger'): string => {
  switch (tone) {
    case 'success':
      return tokens.success
    case 'warning':
      return tokens.warning
    case 'danger':
      return tokens.error
    default:
      return tokens.textSecondary
  }
}

export function AutomationsPage() {
  const showToast = useMate((s) => s.showToast)
  const agents = useMate((s) => s.agents)
  const refreshAgents = useMate((s) => s.refreshAgents)
  const navigate = useNavigate()

  const [rows, setRows] = useState<Automation[]>([])
  const [loading, setLoading] = useState(false)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [editor, setEditor] = useState<{ mode: 'create' } | { mode: 'edit'; a: Automation } | null>(null)
  const [runs, setRuns] = useState<{ id: string; records: AutomationRunRecord[]; limit: number } | null>(null)
  const [preview, setPreview] = useState<{ items: AutomationPreviewItem[]; warnings: string[] } | null>(null)
  const [previewErr, setPreviewErr] = useState<string | null>(null)
  const [form] = Form.useForm<FormValues>()
  // 受控的 kind/timezone（Segmented 与预览都要立刻反映，不等 Form 提交）
  const [kind, setKind] = useState<FormValues['kind']>('daily')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setRows(await listAutomations())
    } catch (err) {
      showToast(toErrorMessage(err))
    }
    setLoading(false)
  }, [showToast])

  useEffect(() => {
    void load()
    void refreshAgents()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const guard = async <T,>(p: Promise<T>): Promise<T | undefined> => {
    try {
      return await p
    } catch (err) {
      showToast(toErrorMessage(err))
      return undefined
    }
  }

  const agentOptions = useMemo(
    () => agents.map((a) => ({ label: `${a.name}（${a.id}）`, value: a.id })),
    [agents],
  )

  /** 某 agent 声明的 runtime 候选（`runtime_hint` 只能从这里选——选了别的服务端会 40401）。 */
  const runtimeOptionsFor = (agentID?: string): { label: string; value: string }[] => {
    const a = agents.find((x) => x.id === agentID)
    return (a?.runtimes ?? []).map((r) => ({ label: r, value: r }))
  }

  const openCreate = (): void => {
    form.resetFields()
    form.setFieldsValue({
      kind: 'daily',
      timezone: localTimezone(),
      hour: 9,
      minute: 0,
      days_of_week: [],
      enabled: true,
      agent_id: agents[0]?.id ?? 'dsh',
      cwd: '',
      prompt: '',
    })
    setKind('daily')
    setPreview(null)
    setPreviewErr(null)
    setEditor({ mode: 'create' })
  }

  const openEdit = (a: Automation): void => {
    form.setFieldsValue(formFromAutomation(a))
    setKind(a.schedule.kind)
    setPreview(null)
    setPreviewErr(null)
    setEditor({ mode: 'edit', a })
  }

  /** 预览：不落盘、不建会话——所以它敢在"保存之前"点。 */
  const onPreview = async (): Promise<void> => {
    setPreviewErr(null)
    try {
      const v = await form.validateFields()
      const body = previewBody(v, 5)
      const resp = await previewAutomation(body)
      setPreview({ items: resp.occurrences ?? [], warnings: resp.warnings ?? [] })
    } catch (err) {
      // 校验失败与领域拒绝都在这里显形：预览的整个意义就是"先把话说清楚"。
      // 注意 antd 的 `validateFields()` 失败时 reject 的**不是 Error**，而是 `{ errorFields, ... }`
      // 这种普通对象——对它走 `String(err)` 界面上就只剩一句 `[object Object]`（真机验收截图抓到）。
      // 所以"表单没填完"这一类单独说清楚，其余才回落 toErrorMessage。
      setPreview(null)
      setPreviewErr(describeFormError(err))
    }
  }

  const onSubmit = async (): Promise<void> => {
    const v = await form.validateFields()
    if (editor?.mode === 'create') {
      const created = await guard(createAutomation(writeBody(v, { create: true })))
      if (!created) return
      showToast(`已新建 ${created.id}`)
    } else if (editor?.mode === 'edit') {
      const updated = await guard(updateAutomation(editor.a.id, writeBody(v, { create: false })))
      if (!updated) return
      showToast(`已保存 ${updated.id}`)
    }
    setEditor(null)
    void load()
  }

  const onToggle = async (a: Automation, enabled: boolean): Promise<void> => {
    setBusyId(a.id)
    await guard(updateAutomation(a.id, { enabled }))
    setBusyId(null)
    void load()
  }

  const onRun = async (a: Automation): Promise<void> => {
    setBusyId(a.id)
    try {
      const out = await runAutomationNow(a.id)
      if (out.status === 'claimed') {
        showToast(`已触发 ${a.id}${out.conversation_id ? `（会话 ${out.conversation_id}）` : ''}`)
        if (out.conversation_id) navigate(`/conversations/${out.conversation_id}`)
      } else {
        showToast(`${a.id} 未触发：${RUN_STATUS_META[out.status].label}${out.reason ? ` —— ${out.reason}` : ''}`)
      }
    } catch (err) {
      // 手动触发失败**既报错又留痕**：错误信封就是原因，运行史里也已经有这一条
      showToast(toErrorMessage(err))
    }
    setBusyId(null)
    void load()
  }

  const onDelete = async (a: Automation): Promise<void> => {
    setBusyId(a.id)
    await guard(deleteAutomation(a.id))
    setBusyId(null)
    void load()
  }

  const openRuns = async (a: Automation): Promise<void> => {
    const resp = await guard(listAutomationRuns(a.id))
    if (!resp) return
    setRuns({ id: a.id, records: resp.runs ?? [], limit: resp.limit })
  }

  const columns: ColumnsType<Automation> = [
    {
      title: '任务',
      dataIndex: 'id',
      key: 'id',
      render: (_: unknown, a) => (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <Space size={6} align="center">
            <span style={{ fontWeight: 600 }}>{a.name || a.id}</span>
            <Tag style={{ marginInlineEnd: 0 }}>{a.id}</Tag>
            {isNeedsAttention(a) && (
              <Tooltip title={`连续 ${a.consecutive_failures} 次没跑成，平台不自动重试（避免把同一次错过重放）`}>
                <Tag color="error" style={{ marginInlineEnd: 0 }}>
                  需处理
                </Tag>
              </Tooltip>
            )}
          </Space>
          <span style={{ fontSize: 12, color: tokens.textSecondary }}>{describeSchedule(a.schedule)}</span>
          {a.error ? (
            <span style={{ fontSize: 12, color: tokens.error }}>{a.error}</span>
          ) : (
            <span style={{ fontSize: 12, color: tokens.textSecondary }}>
              {a.next_due_at ? `下次 ${fmtWhen(a.next_due_at)}` : '没有下一次'} · {a.target.agent_id}
              {a.target.runtime_hint ? ` / ${a.target.runtime_hint}` : ''}
            </span>
          )}
        </div>
      ),
    },
    {
      title: '状态',
      dataIndex: 'state',
      key: 'state',
      width: 120,
      render: (_: unknown, a) => {
        const meta = STATE_META[a.state] ?? STATE_META.inactive
        return (
          <Space size={6}>
            <Dot color={toneColor(meta.tone)} pulse={a.state === 'active'} />
            <span style={{ fontSize: 12.5 }}>{meta.label}</span>
          </Space>
        )
      },
    },
    {
      title: '最近一次',
      dataIndex: 'last_status',
      key: 'last',
      render: (_: unknown, a) => (
        <Tooltip title={a.last_reason ?? undefined}>
          <span
            style={{
              fontSize: 12.5,
              color: a.last_status === 'skipped' ? tokens.error : tokens.textSecondary,
            }}
          >
            {lastRunText(a)}
          </span>
        </Tooltip>
      ),
    },
    {
      title: '操作',
      key: 'ops',
      width: 260,
      render: (_: unknown, a) => (
        <Space size={4} wrap>
          <Button
            size="small"
            icon={<PlayCircleOutlined />}
            loading={busyId === a.id}
            disabled={!a.enabled || a.state === 'archived'}
            onClick={() => void onRun(a)}
          >
            立即运行
          </Button>
          <Button size="small" icon={<HistoryOutlined />} onClick={() => void openRuns(a)}>
            运行史
          </Button>
          <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(a)}>
            编辑
          </Button>
          <Switch
            size="small"
            checked={a.enabled}
            loading={busyId === a.id}
            onChange={(v) => void onToggle(a, v)}
          />
          <Popconfirm title={`删除 ${a.id}？`} description="只删定义文件；已产生的会话不受影响。" onConfirm={() => void onDelete(a)}>
            <Button size="small" danger icon={<DeleteOutlined />} />
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <PageContainer
      title="定时任务"
      description={
        <span>
          定义文件（<code>{'<AutomationDir>/<id>.json'}</code>）是唯一事实源：任务到点建一条会话，运行史
          <b>含没跑成的那些</b>（重复/上一次还在跑/触发失败）。停机会在启动时收敛一次——过期的
          <code>once</code> 不补跑，<code>daily</code>/<code>weekly</code> 补最近一次。
        </span>
      }
      extra={
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>
            刷新
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            新建任务
          </Button>
        </Space>
      }
    >
      <Table<Automation>
        rowKey="id"
        size="middle"
        loading={loading}
        dataSource={rows}
        columns={columns}
        pagination={false}
        locale={{ emptyText: <Empty description="还没有定时任务（定义文件落 <AutomationDir>/<id>.json）" /> }}
      />

      {/* 编辑器：预览与保存同屏——"什么时候跑"必须能在保存之前看到 */}
      <Modal
        open={editor !== null}
        title={editor?.mode === 'create' ? '新建定时任务' : `编辑 ${editor?.mode === 'edit' ? editor.a.id : ''}`}
        width={720}
        onCancel={() => setEditor(null)}
        onOk={() => void onSubmit()}
        okText="保存"
        destroyOnHidden
      >
        <Form form={form} layout="vertical" requiredMark={false}>
          {editor?.mode === 'create' && (
            <Form.Item
              name="id"
              label="ID（文件名，创建后不可改）"
              rules={[{ required: true, pattern: /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/, message: '字母数字开头，≤64 位，不含路径分隔符' }]}
            >
              <Input placeholder="daily-deps-check" />
            </Form.Item>
          )}
          <Form.Item name="name" label="名称" rules={[{ required: true, message: '给个名字' }]}>
            <Input placeholder="每日依赖巡检" />
          </Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch />
          </Form.Item>

          <Form.Item label="调度" required>
            <Space direction="vertical" style={{ width: '100%' }} size={8}>
              <Form.Item name="kind" noStyle>
                <Segmented
                  options={KIND_OPTIONS}
                  onChange={(v) => {
                    setKind(v as FormValues['kind'])
                    setPreview(null)
                    setPreviewErr(null)
                  }}
                />
              </Form.Item>
              <Form.Item name="timezone" noStyle rules={[{ required: true, message: '时区必须显式指定' }]}>
                <Select
                  showSearch
                  style={{ width: 260 }}
                  options={TIMEZONES.map((tz) => ({ label: tz, value: tz }))}
                  placeholder="IANA 时区（如 Asia/Shanghai）"
                />
              </Form.Item>
              {kind === 'once' && (
                <Form.Item name="once_at" noStyle rules={[{ required: true, message: '选一个时刻' }]}>
                  <Input type="datetime-local" style={{ width: 220 }} />
                </Form.Item>
              )}
              {kind !== 'once' && (
                <Space size={8}>
                  <Form.Item name="hour" noStyle>
                    <InputNumber min={0} max={23} style={{ width: 80 }} addonAfter="时" />
                  </Form.Item>
                  <Form.Item name="minute" noStyle>
                    <InputNumber min={0} max={59} style={{ width: 80 }} addonAfter="分" />
                  </Form.Item>
                </Space>
              )}
              {kind === 'weekly' && (
                <Form.Item name="days_of_week" noStyle rules={[{ required: true, message: '每周至少要选一天' }]}>
                  <Checkbox.Group
                    options={WEEKDAYS.map((w) => ({ label: w.label, value: w.value }))}
                  />
                </Form.Item>
              )}
              <Space>
                <Button size="small" onClick={() => void onPreview()}>
                  预览接下来 5 次
                </Button>
                <span style={{ fontSize: 12, color: tokens.textSecondary }}>
                  预览不落盘、不建会话；时区与夏令时都按上面的选择算
                </span>
              </Space>
              {previewErr && <Alert type="error" showIcon message={previewErr} />}
              {preview && (
                <div style={{ border: `1px solid ${tokens.border}`, borderRadius: tokens.radiusMd, padding: '8px 10px' }}>
                  {preview.items.length === 0 ? (
                    <span style={{ fontSize: 12.5, color: tokens.textSecondary }}>没有未来的到期时刻</span>
                  ) : (
                    <ul style={{ margin: 0, paddingInlineStart: 18, fontSize: 12.5, lineHeight: 1.9 }}>
                      {preview.items.map((o) => (
                        <li key={o.at}>
                          <b>{o.wall}</b>（{o.timezone}） · <span style={{ color: tokens.textSecondary }}>{o.at}</span>
                          {o.warnings?.map((w) => (
                            <div key={w} style={{ color: tokens.warning }}>
                              ⚠ {w}
                            </div>
                          ))}
                        </li>
                      ))}
                    </ul>
                  )}
                  {preview.warnings.length > 0 && (
                    <div style={{ marginTop: 6, fontSize: 12, color: tokens.warning }}>
                      {preview.warnings.map((w) => (
                        <div key={w}>⚠ {w}</div>
                      ))}
                    </div>
                  )}
                </div>
              )}
            </Space>
          </Form.Item>

          <Form.Item name="agent_id" label="Agent" rules={[{ required: true, message: '选一个 agent' }]}>
            <Select options={agentOptions} placeholder="由哪个 agent 执行" />
          </Form.Item>
          <Form.Item
            noStyle
            shouldUpdate={(prev, cur) => prev.agent_id !== cur.agent_id}
          >
            {({ getFieldValue }) => {
              const opts = runtimeOptionsFor(getFieldValue('agent_id') as string | undefined)
              return (
                <Form.Item
                  name="runtime_hint"
                  label="Runtime（可空 = 按 agent 的路由策略）"
                  extra="只能选该 agent 声明的候选；平台绝不静默回落到别的 runtime"
                >
                  <Select allowClear options={opts} placeholder="留空即按路由策略" />
                </Form.Item>
              )
            }}
          </Form.Item>
          <Form.Item
            name="cwd"
            label="工作空间（创建时钉住，须为存在的绝对路径）"
            rules={[{ required: true, message: '需要一个绝对路径的工作空间' }]}
          >
            <CwdPicker />
          </Form.Item>
          <Form.Item name="model" label="模型（可空 = 用 agent 默认）">
            <Input placeholder="留空即不指定" />
          </Form.Item>
          <Form.Item name="prompt" label="Prompt" rules={[{ required: true, message: '任务内容不能为空' }]}>
            <Input.TextArea rows={3} placeholder="每次触发时发给 agent 的话" />
          </Form.Item>
          <Form.Item name="max_run_duration" label="单轮上限（可空，如 10m）" extra="只收紧，不替代平台既有的轮次超时看门狗">
            <Input placeholder="10m" style={{ width: 160 }} />
          </Form.Item>
        </Form>
      </Modal>

      {/* 运行史：含没跑成的那些——"没跑成"才是最需要被看见的 */}
      <Drawer
        open={runs !== null}
        onClose={() => setRuns(null)}
        width={520}
        title={runs ? `${runs.id} 的运行史` : ''}
        extra={<span style={{ fontSize: 12, color: tokens.textSecondary }}>最多保留 {runs?.limit ?? 20} 条</span>}
      >
        {runs && runs.records.length === 0 ? (
          <Empty description="还没有触发记录" />
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {runs?.records.map((r, i) => {
              const meta = RUN_STATUS_META[r.status]
              return (
                <div
                  key={`${r.scheduled_for}-${i}`}
                  style={{
                    border: `1px solid ${tokens.border}`,
                    borderLeft: `3px solid ${toneColor(meta.tone)}`,
                    borderRadius: tokens.radiusMd,
                    padding: '8px 10px',
                    display: 'flex',
                    flexDirection: 'column',
                    gap: 4,
                  }}
                >
                  <Space size={6}>
                    <span style={{ fontWeight: 600, fontSize: 12.5 }}>{meta.label}</span>
                    {r.trigger && <Tag style={{ marginInlineEnd: 0 }}>{r.trigger}</Tag>}
                  </Space>
                  <span style={{ fontSize: 12.5, color: tokens.textSecondary }}>{runLine(r)}</span>
                  {r.conversation_id && (
                    <Button
                      type="link"
                      size="small"
                      style={{ paddingInline: 0, alignSelf: 'flex-start' }}
                      onClick={() => {
                        setRuns(null)
                        navigate(`/conversations/${r.conversation_id}`)
                      }}
                    >
                      查看会话 {r.conversation_id}
                    </Button>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </Drawer>
    </PageContainer>
  )
}

/**
 * 工作空间输入（Form.Item 会把 value/onChange 注进来，TS 从 props 上看不到，所以显式声明为可选）。
 * `null` ↔ `''` 的换算在这里做：WorkspacePicker 的"未指定"是 null，而写面要求 cwd 非空。
 */
function CwdPicker({
  value,
  onChange,
}: {
  value?: string | null
  onChange?: (v: string | null) => void
}) {
  return <WorkspacePicker value={value ?? null} onChange={(v) => onChange?.(v ?? '')} />
}

function isNeedsAttention(a: Automation): boolean {
  return a.state === 'needs_attention' || (a.consecutive_failures ?? 0) > 0
}

/** Form.Item 字段名 → 界面标签：出错时只说人话，别把字段名直接甩给用户。 */
const FIELD_LABEL: Record<string, string> = {
  id: 'ID',
  name: '名称',
  kind: '调度',
  timezone: '时区',
  once_at: '触发时刻',
  hour: '时',
  minute: '分',
  days_of_week: '每周的哪几天',
  agent_id: 'Agent',
  runtime_hint: 'Runtime',
  cwd: '工作空间',
  model: '模型',
  prompt: 'Prompt',
  max_run_duration: '单轮上限',
}

/**
 * 把预览失败翻译成一句人话。
 *
 * 关键一条：antd 表单校验失败时 `validateFields()` reject 的**不是 Error**，而是
 * `{ errorFields: [{ name: ['cwd'], errors: [...] }] }` 这样的普通对象——老代码对它走
 * `String(err)`，界面上就只剩 `[object Object]`（真机验收截图抓到，恰好把"预览就是为了
 * 先把话说清楚"反过来）。所以校验失败单独列字段与原因，领域拒绝（HTTP 信封带 message）
 * 照常走 toErrorMessage，其余兜底成 JSON 而不是 `[object Object]`。
 */
function describeFormError(err: unknown): string {
  const fields = (err as { errorFields?: { name?: (string | number)[]; errors?: string[] }[] } | null)?.errorFields
  if (Array.isArray(fields) && fields.length > 0) {
    const parts = fields.map((f) => {
      const key = String(f.name?.[f.name.length - 1] ?? '')
      const label = FIELD_LABEL[key] ?? key
      const why = f.errors?.[0]
      return why ? `${label}（${why}）` : label
    })
    return `表单还没填完：${parts.join('、')}`
  }
  if (typeof err === 'object' && err !== null) return JSON.stringify(err)
  return String(err)
}
