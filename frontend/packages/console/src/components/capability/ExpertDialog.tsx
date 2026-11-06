import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { Alert, Button, Input, Modal, Popconfirm, Select, Space, Spin, Switch } from 'antd'
import { DeleteOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { Expert, ExpertWriteBody } from '@ark-mate/types'
import {
  EXPERT_AVATARS,
  blankExpertForm,
  expertCreateBody,
  expertFormPatch,
  expertFormValues,
  instructionsByteLength,
  instructionsMarkers,
  missingExpertRefs,
  validateExpertForm,
  EXPERT_INSTRUCTIONS_MAX_BYTES,
  EXPERT_MAX_DESCRIPTION,
  EXPERT_MAX_NAME,
  EXPERT_MAX_TAGS,
  EXPERT_MAX_TAG_CHARS,
  type ExpertFormValues,
  type ExpertRefOption,
} from '../../console/expertView'

export interface ExpertDialogOptions {
  skills: ExpertRefOption[]
  mcps: ExpertRefOption[]
  models: ExpertRefOption[]
}

interface DialogProps {
  open: boolean
  created: boolean
  expert: Expert | null
  loading: boolean
  loadError: string | null
  options: ExpertDialogOptions
  busy: boolean
  error: string | null
  toggleBusy: boolean
  delBusy: boolean
  onClose: () => void
  onRetry: () => void
  onSave: (body: ExpertWriteBody) => Promise<void>
  onToggleEnabled: (enabled: boolean) => Promise<void>
  onDelete: () => Promise<void>
}

const AVATAR_LABEL: Record<string, string> = {
  spark: 'spark · 火花',
  chart: 'chart · 图表',
  code: 'code · 代码',
  pen: 'pen · 笔',
  search: 'search · 搜索',
  briefcase: 'briefcase · 公文包',
}

/**
 * 专家编辑器（720 居中弹窗）：标题两行、单栏表单、
 * footer 左启停（编辑态带删除）/ 右「取消 · 新建|保存」。
 *
 * 面向普通用户：不提供「高级（JSON）」原文编辑入口；「标识」是内部键，创建后不可修改。
 */
export function ExpertDialog(props: DialogProps) {
  const {
    open,
    created,
    expert,
    loading,
    loadError,
    options,
    busy,
    error,
    toggleBusy,
    delBusy,
    onClose,
    onRetry,
    onSave,
    onToggleEnabled,
    onDelete,
  } = props

  const [values, setValues] = useState<ExpertFormValues>(() => blankExpertForm())
  const [localErr, setLocalErr] = useState<string | null>(null)
  const [enabledDraft, setEnabledDraft] = useState(true)

  // 每次打开（或切到另一个专家）都从当前定义重建表单：上一条的草稿不能串到下一条。
  // 编辑态 detail 异步到达后 `expert?.id` 变化会再触发一次，填上正文。
  useEffect(() => {
    if (!open) return
    setLocalErr(null)
    setValues(created || !expert ? blankExpertForm() : expertFormValues(expert))
    setEnabledDraft(expert?.enabled ?? true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, created, expert?.id])

  const patch = (next: Partial<ExpertFormValues>): void => setValues((cur) => ({ ...cur, ...next }))

  const byteLen = instructionsByteLength(values.instructions)
  const markers = instructionsMarkers(values.instructions)
  const overBytes = byteLen > EXPERT_INSTRUCTIONS_MAX_BYTES

  const knownSkills = useMemo(() => new Set(options.skills.map((o) => o.id)), [options.skills])
  const knownMcps = useMemo(() => new Set(options.mcps.map((o) => o.id)), [options.mcps])
  const missing = useMemo(() => missingExpertRefs(values, knownSkills, knownMcps), [values, knownSkills, knownMcps])
  const hasMissing = missing.skills.length > 0 || missing.mcps.length > 0

  const enabled = created ? values.enabled : enabledDraft

  const submit = async (): Promise<void> => {
    const trimmed: ExpertFormValues = { ...values, id: values.id.trim(), name: values.name.trim() }
    const verr = validateExpertForm(trimmed, created)
    if (verr) {
      setLocalErr(verr)
      return
    }
    setLocalErr(null)
    const body = created ? expertCreateBody(trimmed) : expertFormPatch(expertFormValues(expert as Expert), trimmed)
    await onSave(body)
  }

  const onToggleSwitch = (next: boolean): void => {
    if (created) {
      patch({ enabled: next })
      return
    }
    setEnabledDraft(next)
    void onToggleEnabled(next)
  }

  const label = (text: string, extra?: ReactNode): ReactNode => (
    <div style={{ fontSize: 11, color: tokens.textSecondary, marginBottom: 4, display: 'flex', justifyContent: 'space-between' }}>
      <span>{text}</span>
      {extra}
    </div>
  )
  const hint = (text: string): ReactNode => (
    <div style={{ marginTop: 3, fontSize: 10.5, color: tokens.textPlaceholder }}>{text}</div>
  )

  const footer =
    loadError ? (
      <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
        <Button onClick={onClose}>关闭</Button>
      </div>
    ) : (
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
        <span style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 7, fontSize: 12, color: tokens.textSecondary }}>
            <Switch size="small" checked={enabled} loading={toggleBusy} disabled={loading} onChange={onToggleSwitch} />
            {enabled ? '启用' : '停用'}
          </span>
          {!created && (
            <Popconfirm
              title={`删除「${expert?.name || expert?.id}」？`}
              description="删除后不可恢复；已建会话不受影响。"
              okText="删除"
              okButtonProps={{ danger: true }}
              cancelText="取消"
              onConfirm={() => void onDelete()}
            >
              <Button type="text" danger size="small" loading={delBusy} disabled={loading} icon={<DeleteOutlined />}>
                删除
              </Button>
            </Popconfirm>
          )}
        </span>
        <Space size={8}>
          <Button onClick={onClose}>取消</Button>
          <Button type="primary" loading={busy} disabled={loading} onClick={() => void submit()}>
            {created ? '新建' : '保存'}
          </Button>
        </Space>
      </div>
    )

  return (
    <Modal
      open={open}
      onCancel={onClose}
      width={720}
      destroyOnHidden
      title={
        <div>
          <div style={{ fontSize: 15, fontWeight: 600 }}>{created ? '新建专家' : '编辑专家'}</div>
          <div style={{ marginTop: 2, fontSize: 11, color: tokens.textPlaceholder }}>
            {created ? '一组推荐能力 + 一段角色设定；创建后标识不可修改' : `标识：${expert?.id ?? ''}`}
          </div>
        </div>
      }
      footer={footer}
      styles={{ body: { maxHeight: '58vh', overflowY: 'auto', paddingRight: 4 } }}
    >
      {loading ? (
        <div style={{ display: 'grid', placeItems: 'center', minHeight: 200 }}>
          <Spin />
        </div>
      ) : loadError ? (
        <Alert
          type="error"
          showIcon
          message="角色设定取不到"
          description={loadError}
          action={
            <Button size="small" onClick={onRetry}>
              重试
            </Button>
          }
        />
      ) : (
        <>
          {hasMissing && (
            <div
              style={{
                fontSize: 11,
                color: tokens.warning,
                background: tokens.warningBg,
                border: `1px solid ${tokens.warningBorder}`,
                borderRadius: tokens.radiusSm,
                padding: '6px 10px',
                marginBottom: 12,
              }}
            >
              引用缺失：
              {missing.skills.length > 0 && `技能 ${missing.skills.join('、')}`}
              {missing.skills.length > 0 && missing.mcps.length > 0 ? '；' : ''}
              {missing.mcps.length > 0 && `连接器 ${missing.mcps.join('、')}`}
              {' '}指向不存在的项。保存仍会成功，使用该专家时这些项会被跳过并提示。
            </div>
          )}

          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
            <div>
              {label('名称 *')}
              <Input
                value={values.name}
                placeholder="数据分析师"
                maxLength={EXPERT_MAX_NAME}
                onChange={(e) => patch({ name: e.target.value })}
              />
            </div>
            <div>
              {label('标识')}
              <Input
                value={values.id}
                disabled={!created}
                placeholder="data-analyst"
                onChange={(e) => patch({ id: e.target.value })}
              />
              {hint(created ? '英文小写字母/数字/连字符，≤64；创建后不可修改' : '创建后不可修改')}
            </div>
          </div>

          <div style={{ marginTop: 13 }}>
            {label(
              '描述',
              <span style={{ fontFamily: tokens.fontMono, color: tokens.textPlaceholder }}>
                {[...values.description].length} / {EXPERT_MAX_DESCRIPTION}
              </span>,
            )}
            <Input
              value={values.description}
              placeholder="一句话说明这个专家做什么"
              onChange={(e) => patch({ description: e.target.value })}
            />
          </div>

          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10, marginTop: 13 }}>
            <div>
              {label('头像')}
              <Select
                style={{ width: '100%' }}
                value={values.avatar}
                onChange={(v: string) => patch({ avatar: v })}
                options={EXPERT_AVATARS.map((k) => ({ value: k, label: AVATAR_LABEL[k] ?? k }))}
              />
            </div>
            <div>
              {label('推荐模型')}
              <Select
                style={{ width: '100%' }}
                showSearch
                allowClear
                value={values.model || undefined}
                placeholder={options.models.length === 0 ? '模型目录不可达（可留空）' : '不推荐'}
                onChange={(v?: string) => patch({ model: v ?? '' })}
                optionFilterProp="label"
                options={options.models.map((m) => ({ value: m.id, label: m.name }))}
              />
            </div>
          </div>

          <div style={{ marginTop: 13 }}>
            {label('推荐技能', <span style={{ color: tokens.textPlaceholder }}>最多 50</span>)}
            <Select
              mode="multiple"
              style={{ width: '100%' }}
              value={values.skills}
              placeholder={options.skills.length === 0 ? '暂无可选技能（可留空）' : '从已装技能多选'}
              onChange={(v: string[]) => patch({ skills: v })}
              options={options.skills.map((s) => ({ value: s.id, label: s.name }))}
            />
            {hint('允许暂指不存在的项（保存后会标注「引用缺失」）')}
          </div>

          <div style={{ marginTop: 13 }}>
            {label('推荐连接器', <span style={{ color: tokens.textPlaceholder }}>最多 50</span>)}
            <Select
              mode="multiple"
              style={{ width: '100%' }}
              value={values.mcps}
              placeholder={options.mcps.length === 0 ? '暂无可选连接器（可留空）' : '从已装连接器多选'}
              onChange={(v: string[]) => patch({ mcps: v })}
              options={options.mcps.map((m) => ({ value: m.id, label: m.name }))}
            />
          </div>

          <div style={{ marginTop: 13 }}>
            {label('标签', <span style={{ color: tokens.textPlaceholder }}>≤{EXPERT_MAX_TAGS} 个，每个 ≤{EXPERT_MAX_TAG_CHARS} 字</span>)}
            <Select
              mode="tags"
              style={{ width: '100%' }}
              value={values.tags}
              placeholder="输入后回车；也可用逗号分隔，如：数据, 分析"
              tokenSeparators={[',', '，']}
              onChange={(v: string[]) => patch({ tags: v })}
            />
          </div>

          <div style={{ marginTop: 13 }}>
            {label(
              '角色设定',
              <span style={{ fontFamily: tokens.fontMono, color: overBytes ? tokens.error : tokens.textPlaceholder }}>
                {byteLen} / {EXPERT_INSTRUCTIONS_MAX_BYTES} 字节
              </span>,
            )}
            <Input.TextArea
              value={values.instructions}
              placeholder="写下这位专家是谁、怎么做事；对话开始时会自动生效"
              autoSize={{ minRows: 6, maxRows: 14 }}
              spellCheck={false}
              style={{ fontFamily: tokens.fontMono, fontSize: 12.5 }}
              onChange={(e) => patch({ instructions: e.target.value })}
            />
            {markers.length > 0 && (
              <div
                style={{
                  marginTop: 8,
                  fontSize: 11,
                  color: tokens.warning,
                  background: tokens.warningBg,
                  border: `1px solid ${tokens.warningBorder}`,
                  borderRadius: tokens.radiusSm,
                  padding: '6px 10px',
                }}
              >
                含不支持的写法：{markers.join('、')} —— 保存会被拒绝。
              </div>
            )}
            <div
              style={{
                border: `1px solid ${tokens.border}`,
                borderRadius: tokens.radiusSm,
                background: tokens.codeBg,
                padding: '10px 12px',
                marginTop: 8,
              }}
            >
              <div style={{ fontSize: 10.5, color: tokens.textPlaceholder, marginBottom: 6 }}>
                ▸ 预览（对话开始时自动生效，不会显示在对话里）
              </div>
              <div
                style={{
                  fontSize: 12,
                  lineHeight: 1.75,
                  color: tokens.textSecondary,
                  whiteSpace: 'pre-wrap',
                  maxHeight: 140,
                  overflow: 'auto',
                  fontFamily: tokens.fontMono,
                }}
              >
                {values.instructions ? values.instructions : <span style={{ color: tokens.textPlaceholder }}>未设置角色设定（仅能力预设）</span>}
              </div>
            </div>
          </div>

          {localErr && <Alert type="error" showIcon style={{ marginTop: 12 }} message={localErr} />}
          {error && <Alert type="error" showIcon style={{ marginTop: 12 }} message="保存失败" description={error} />}
        </>
      )}
    </Modal>
  )
}
