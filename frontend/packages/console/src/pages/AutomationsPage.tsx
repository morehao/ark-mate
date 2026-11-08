import { useCallback, useEffect, useState } from 'react'
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
  Spin,
  Switch,
  Tag,
  Tooltip,
} from 'antd'
import {
  ClockCircleOutlined,
  DeleteOutlined,
  EditOutlined,
  HistoryOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons'
import { Dot, PageContainer, tokens } from '@ark-mate/ui'
import {
  createAutomation,
  deleteAutomation,
  listAutomationRuns,
  listAutomations,
  previewAutomation,
  runAutomationNow,
  toErrorMessage,
  updateAutomation,
} from '@ark-mate/api'
import type { Automation, AutomationPreviewItem, AutomationRunRecord } from '@ark-mate/types'
import { useNavigate } from 'react-router-dom'
import { useMate } from '../store'
import { WorkspacePicker } from '../components/WorkspacePicker'
import { CardToolbar } from '../components/CardToolbar'
import { CapabilityCard, IconAction } from '../components/capability/shared'
import {
  KIND_OPTIONS,
  RUN_STATUS_META,
  STATE_META,
  TIMEZONES,
  WEEKDAYS,
  describeSchedule,
  formFromAutomation,
  lastRunText,
  localTimezone,
  previewBody,
  runLine,
  writeBody,
  type ScheduleFormValues,
} from '../console/automationView'
import { fmtShortDateTime } from '../console/time'

// ——— 定时任务页（M4-6）———
//
// 三件事：**看得见**（列表 + 运行史，含没跑成的那些）、**说得清**（preview：时区/DST/weekly
// 叠加后的"到底什么时候跑"）、**改得动**（定义文件是事实源，页面只是它的编辑器）。
// 编辑时任何改动都先按"只改出现过的键"发 PUT，所以换 kind 时会显式把旧字段置 null 删掉。

interface FormValues extends ScheduleFormValues {
  id?: string
  name: string
  enabled: boolean
  cwd: string
  model?: string
  prompt: string
  max_run_duration?: string
  delivery?: 'session' | 'new_session'
  session_id?: string
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
  const [delivery, setDelivery] = useState<'session' | 'new_session'>('session')

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

  // 表单初值走 **`initialValues`**（见下面 editorInitialValues），不在这里命令式写。
  //
  // 为什么（与设置 · 模型 那次是**同一个坑**，2026-10-10 一起修）：`Modal` 带 `destroyOnHidden`，
  // 关着时里面的 `Form` 根本没挂载；无论"打开那一刻"直接 `setFieldsValue`，还是放进 `useEffect`，
  // 都早于 rc-dialog 真正挂内容 → 值被丢掉（控制台留一句 `Instance created by useForm is not
  // connected to any Form element`）。后果是**静默的**：新建自动化时那些默认值（kind/时区/小时…）
  // 不生效，点保存只报一堆"必填"，弹窗不关——而用户以为自己没填。
  const openCreate = (): void => {
    setKind('daily')
    setDelivery('session')
    setPreview(null)
    setPreviewErr(null)
    setEditor({ mode: 'create' })
  }

  const openEdit = (a: Automation): void => {
    setKind(a.schedule.kind)
    setDelivery(a.target.delivery === 'new_session' ? 'new_session' : 'session')
    setPreview(null)
    setPreviewErr(null)
    setEditor({ mode: 'edit', a })
  }

  // 内容每次关闭都销毁、每次打开都重新挂载，所以 `initialValues` 每开一次都会重新生效。
  const editorInitialValues: Partial<FormValues> = editor?.mode === 'edit'
    ? formFromAutomation(editor.a)
    : {
        kind: 'daily',
        timezone: localTimezone(),
        hour: 9,
        minute: 0,
        days_of_week: [],
        every_seconds: 3600,
        day: 1,
        rrule: '',
        enabled: true,
        cwd: '',
        prompt: '',
        delivery: 'session',
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
      showToast(`已创建「${created.name || created.id}」`, 'success')
    } else if (editor?.mode === 'edit') {
      const updated = await guard(updateAutomation(editor.a.id, writeBody(v, { create: false })))
      if (!updated) return
      showToast(`已保存「${updated.name || updated.id}」`, 'success')
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
        showToast(`已启动「${a.name || a.id}」`, 'success')
        if (out.session_id) navigate(`/sessions/${out.session_id}`)
      } else {
        showToast(`「${a.name || a.id}」未运行：${RUN_STATUS_META[out.status].label}${out.reason ? ` —— ${out.reason}` : ''}`)
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

  // 卡片（与专家/技能/连接器三页同款 .fg-cap-card 网格）：时钟图标 + 名称/状态 + 调度
  // → 下次/上次（mono）→ hairline + 常显图标动作（立即运行 / 运行史 / 编辑 / 删除）。
  const renderAutoRow = (a: Automation) => {
    const meta = STATE_META[a.state] ?? STATE_META.inactive
    const name = a.name || a.id
    return (
      <CapabilityCard
        key={a.id}
        avatar={<ClockCircleOutlined />}
        dim={!a.enabled}
        title={name}
        tags={
          <>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
              <Dot color={toneColor(meta.tone)} pulse={a.state === 'active'} />
              <span style={{ fontSize: 11, color: tokens.textSecondary }}>{meta.label}</span>
            </span>
            {isNeedsAttention(a) && (
              <Tooltip title={`连续 ${a.consecutive_failures} 次没跑成，平台不自动重试（避免把同一次错过重放）`}>
                <Tag color="error" style={{ marginInlineEnd: 0, fontSize: 10.5, lineHeight: '17px', padding: '0 7px', borderRadius: 5 }}>
                  需处理
                </Tag>
              </Tooltip>
            )}
          </>
        }
        toggle={
          <Tooltip title={a.enabled ? '停用：到点不再触发' : '启用：到点自动触发'}>
            <Switch
              size="small"
              checked={a.enabled}
              loading={busyId === a.id}
              onChange={(v) => void onToggle(a, v)}
            />
          </Tooltip>
        }
        description={describeSchedule(a.schedule)}
        meta={
          <span style={{ fontFamily: tokens.fontMono, fontSize: 10.5 }}>
            {a.error ? (
              <span style={{ color: tokens.error }}>{a.error}</span>
            ) : (
              <Tooltip title={a.last_reason ?? undefined}>
                <span style={{ color: a.last_status === 'skipped' ? tokens.error : undefined }}>
                  {a.next_due_at ? `下次 ${fmtShortDateTime(a.next_due_at)}` : '没有下一次'}
                  {a.last_status ? ` · 上次 ${lastRunText(a)}` : ''}
                </span>
              </Tooltip>
            )}
          </span>
        }
        actions={
          <>
            <Tooltip title="立即运行一次">
              <IconAction
                icon={<PlayCircleOutlined />}
                title="立即运行"
                disabled={!a.enabled || a.state === 'archived' || busyId === a.id}
                onClick={() => void onRun(a)}
              />
            </Tooltip>
            <Tooltip title="运行史">
              <IconAction icon={<HistoryOutlined />} title="运行史" onClick={() => void openRuns(a)} />
            </Tooltip>
            <Tooltip title="编辑">
              <IconAction icon={<EditOutlined />} title={`编辑 ${name}`} onClick={() => openEdit(a)} />
            </Tooltip>
            <Popconfirm
              title={`删除「${name}」？`}
              description="删除后不可恢复；已产生的会话不受影响。"
              okText="删除"
              okButtonProps={{ danger: true }}
              cancelText="取消"
              onConfirm={() => void onDelete(a)}
            >
              <span>
                <IconAction icon={<DeleteOutlined />} title={`删除 ${name}`} danger disabled={busyId === a.id} />
              </span>
            </Popconfirm>
          </>
        }
      />
    )
  }

  return (
    <PageContainer title="定时任务" description="让助手按时间自动帮你做事：每天巡检、每周汇总、每月报告。">
      {/* 工具进内容区工具行（与扩展三页同一版式）：页头只留标题+描述。 */}
      <CardToolbar
        left={
          /* 无「我的 X」小节标题时，左侧给一句任务计数当语义锚点，避免工具行左端整条留空。 */
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
            <b style={{ fontSize: 13.5, fontWeight: 600 }}>任务列表</b>
            <span
              style={{
                fontSize: 11,
                fontVariantNumeric: 'tabular-nums',
                color: tokens.textSecondary,
                background: tokens.softFill,
                border: `1px solid ${tokens.border}`,
                borderRadius: 20,
                padding: '0 7px',
                lineHeight: '18px',
              }}
            >
              {rows.length}
            </span>
          </span>
        }
        tools={
          <>
            {/* 工具行口径与扩展三页一致：**新建在左、刷新在右**（此前是刷新在左、新建在右，
                与技能/专家/连接器三页相反，同一个动作在不同页面出现在不同位置）。
                两个按钮同为 small、同为「符号 + 文本」：＋ 新建 / ↻ 刷新。 */}
            <Button size="small" type="primary" icon={<PlusOutlined />} onClick={openCreate}>
              新建
            </Button>
            <Button size="small" icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>
              刷新
            </Button>
          </>
        }
      />
      {/* 列表卡（原型 §F）：一行一个任务，任务名 + 状态 + 调度 + 下次/上次 + 动作。 */}
      {loading && rows.length === 0 ? (
        <div style={{ padding: 24, textAlign: 'center' }}>
          <Spin />
        </div>
      ) : rows.length === 0 ? (
        /* 空态（v2.2）：轻图标 + 一句说明 + 入口。不写长文案、不放插画——
           空页留白会把视线从"能做什么"上移开（参考实现在空态里也只给一行小字 + 可点的下一步）。 */
        <div
          style={{
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            gap: 12,
            padding: '48px 24px 44px',
            textAlign: 'center',
          }}
        >
          <span
            style={{
              display: 'grid',
              placeItems: 'center',
              width: 44,
              height: 44,
              borderRadius: 999,
              background: tokens.softFill,
              color: tokens.textPlaceholder,
              fontSize: 18,
            }}
          >
            <ClockCircleOutlined />
          </span>
          <span style={{ fontSize: 12.5, color: tokens.textSecondary, lineHeight: 1.7 }}>
            还没有定时任务。建一个：让助手每天、每周或指定时刻替你跑同一件事。
          </span>
          <Button size="small" type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            新建定时任务
          </Button>
        </div>
      ) : (
        <div style={gridStyle}>
          {rows.map(renderAutoRow)}
        </div>
      )}

      {/* 编辑器：预览与保存同屏——"什么时候跑"必须能在保存之前看到 */}
      <Modal
        open={editor !== null}
        title={editor?.mode === 'create' ? '新建定时任务' : `编辑「${editor?.mode === 'edit' ? editor.a.name || editor.a.id : ''}」`}
        width={720}
        onCancel={() => setEditor(null)}
        onOk={() => void onSubmit()}
        okText="保存"
        destroyOnHidden
      >
        <Form form={form} layout="vertical" requiredMark={false} initialValues={editorInitialValues}>
          {editor?.mode === 'create' && (
            <Form.Item
              name="id"
              label="任务标识（创建后不可改）"
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
              {kind !== 'rrule' && (
                <Form.Item name="timezone" noStyle rules={[{ required: true, message: '时区必须显式指定' }]}>
                  <Select
                    showSearch
                    style={{ width: 260 }}
                    options={TIMEZONES.map((tz) => ({ label: tz, value: tz }))}
                    placeholder="IANA 时区（如 Asia/Shanghai）"
                  />
                </Form.Item>
              )}
              {kind === 'rrule' && (
                <Form.Item
                  name="rrule"
                  noStyle
                  rules={[{ required: true, message: '填写 DTSTART 与 RRULE 两行' }]}
                >
                  <Input.TextArea
                    rows={3}
                    style={{ width: '100%', fontFamily: 'ui-monospace, Menlo, monospace' }}
                    placeholder={'DTSTART:20260101T090000Z\nRRULE:FREQ=MONTHLY;BYDAY=2WE'}
                  />
                </Form.Item>
              )}
              {kind === 'rrule' && (
                <span style={{ fontSize: 12, color: tokens.textSecondary }}>
                  两行文本：先 DTSTART（时区由它自带，UTC Z 或 TZID=Asia/Shanghai），再 RRULE
                  （受限 RFC 5545，如 FREQ=DAILY / FREQ=MONTHLY;BYDAY=2WE；不支持 SECONDLY）。时区取自 DTSTART，不再单独选。
                </span>
              )}
              {kind === 'once' && (
                <Form.Item name="once_at" noStyle rules={[{ required: true, message: '选一个时刻' }]}>
                  <Input type="datetime-local" style={{ width: 220 }} />
                </Form.Item>
              )}
              {kind === 'interval' && (
                <Space size={8} align="center">
                  <span style={{ fontSize: 12.5, color: tokens.textSecondary }}>每</span>
                  <Form.Item name="every_seconds" noStyle rules={[{ required: true, message: '间隔不能为空' }]}>
                    <InputNumber min={60} step={60} style={{ width: 120 }} addonAfter="秒" />
                  </Form.Item>
                  <span style={{ fontSize: 12, color: tokens.textSecondary }}>
                    ≥60 秒；从上次触发起算，与墙钟/DST 无关
                  </span>
                </Space>
              )}
              {(kind === 'daily' || kind === 'weekly' || kind === 'monthly') && (
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
              {kind === 'monthly' && (
                <Space size={8} align="center">
                  <span style={{ fontSize: 12.5, color: tokens.textSecondary }}>每月</span>
                  <Form.Item name="day" noStyle rules={[{ required: true, message: '填一个日期（1–31）' }]}>
                    <InputNumber min={1} max={31} style={{ width: 90 }} addonAfter="日" />
                  </Form.Item>
                  <span style={{ fontSize: 12, color: tokens.textSecondary }}>
                    （当月无此日则跳过，如 31 号在小月）
                  </span>
                </Space>
              )}
              <Space>
                <Button size="small" onClick={() => void onPreview()}>
                  预览接下来 5 次
                </Button>
                <span style={{ fontSize: 12, color: tokens.textSecondary }}>
                  预览不会保存，也不会新建会话；时区与夏令时都按上面的选择计算
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
                          <b>{o.wall}</b>（{o.timezone}） · <span style={{ color: tokens.textSecondary }}>{fmtShortDateTime(o.at)}</span>
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

          {/* 投递模式（ADR v35）：缺省 session（投递回既有会话，共享历史）；new_session 新建会话。 */}
          <Form.Item name="delivery" label="投递方式">
            <Segmented
              options={[
                { label: '投递到会话', value: 'session' },
                { label: '新建会话', value: 'new_session' },
              ]}
              onChange={(v) => setDelivery(v as 'session' | 'new_session')}
            />
          </Form.Item>
          {delivery === 'session' ? (
            <Form.Item
              name="session_id"
              label="目标会话"
              extra="投递到这个会话里（共享它的历史）；留空或不存在会报错。"
              rules={[{ required: true, message: '请填一个目标会话 id' }]}
            >
              <Input placeholder="sess_…（从会话列表复制 id）" />
            </Form.Item>
          ) : (
            <Form.Item
              name="cwd"
              label="工作空间（新建会话时钉住，须为存在的绝对路径）"
              rules={[{ required: true, message: '新建会话需要一个绝对路径的工作空间' }]}
            >
              <CwdPicker />
            </Form.Item>
          )}
          <Form.Item name="model" label="模型（可空 = 使用默认模型）">
            <Input placeholder="留空即不指定" />
          </Form.Item>
          <Form.Item name="prompt" label="任务内容" rules={[{ required: true, message: '任务内容不能为空' }]}>
            <Input.TextArea rows={3} placeholder="每次触发时发给助手的话" />
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
                  {r.session_id && (
                    <Button
                      type="link"
                      size="small"
                      style={{ paddingInline: 0, alignSelf: 'flex-start' }}
                      onClick={() => {
                        setRuns(null)
                        navigate(`/sessions/${r.session_id}`)
                      }}
                    >
                      查看会话 {r.session_id}
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
 *
 * 自动化定义里是 **`cwd` 绝对路径**（不是登记 id）：定时任务要能在一个"没人登记过"的目录里跑，
 * 而且它的目标目录是**运行期**才用到的（`target.cwd` 随定义落库）。所以这里做一次映射：
 * 选中登记项 → 取下它的 `path` 写进表单；未指定 → 空串（写面会按"必填"拒）。
 *
 * 映射方向刻意是"id → path"而不是把 id 存进定义：定义一旦落库就不能依赖登记表还在
 * （用户删掉那条登记后，定时任务必须仍然知道该在哪个目录跑）。
 */
function CwdPicker({
  value,
  onChange,
}: {
  value?: string | null
  onChange?: (v: string | null) => void
}) {
  const workspaces = useMate((s) => s.workspaces)
  const root = /^\//.test(value ?? '') || /^[A-Za-z]:[\\/]/.test(value ?? '') ? value ?? '' : ''
  const matched = root ? (workspaces?.items ?? []).find((w) => w.path === root) : undefined
  return (
    <WorkspacePicker
      value={matched ? { id: matched.id } : { id: null, path: root || null }}
      onChange={(v) => {
        // 自动化存的是**路径字符串**（不落登记 id）：选中的登记项要换回它的路径，
        // 未登记的值原样保留——那种情况只可能来自既有配置，不该被面板静默清空。
        if (v.id) {
          const ws = (workspaces?.items ?? []).find((w) => w.id === v.id)
          onChange?.(ws?.path ?? '')
          return
        }
        onChange?.(v.path ?? '')
      }}
    />
  )
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
  day: '每月第几日',
  rrule: '重复规则（DTSTART + RRULE）',
  cwd: '工作空间',
  model: '模型',
  prompt: '任务内容',
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

// 卡片网格（与专家/技能/连接器三页同列宽）。
const gridStyle = {
  display: 'grid',
  gap: 14,
  padding: '18px 16px',
  gridTemplateColumns: 'repeat(auto-fill, minmax(320px, 1fr))',
} as const
