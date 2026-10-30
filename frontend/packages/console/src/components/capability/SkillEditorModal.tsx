import { useEffect, useState, type ReactNode } from 'react'
import { Alert, Button, Input, Modal, Space } from 'antd'
import { tokens } from '@ark-mate/ui'
import type { Skill } from '@ark-mate/types'
import { skillFormValues, type SkillFormValues } from '../../console/capabilityView'

const SKILL_ID_RE = /^[a-z0-9]+(?:-[a-z0-9]+)*$/
const DEFAULT_BODY = '# 技能名\n\n## 何时使用\n\n- 说明触发场景。\n\n## 工作步骤\n\n1. 第一步。\n'

/**
 * 技能编辑器（720）：只编辑名称、描述与技能内容。
 *
 * 投放对象 / 作用域 / 高级 JSON 这些内部配置不面向普通用户，已从界面移除；
 * 「调用名」是技能的内部标识（同时用于 @ 调用），只读展示、不可编辑。
 */
export function SkillEditorModal({
  open,
  skill,
  busy,
  error,
  onCancel,
  onSubmit,
}: {
  open: boolean
  skill: Skill | null
  busy: boolean
  error: string | null
  onCancel: () => void
  onSubmit: (values: SkillFormValues) => Promise<void>
}) {
  const created = skill === null
  const [values, setValues] = useState<SkillFormValues>(() =>
    created
      ? { id: '', name: '', description: '', runtimes: [], scope: 'global', body: DEFAULT_BODY }
      : skillFormValues(skill),
  )
  const [localErr, setLocalErr] = useState<string | null>(null)

  // 每次打开都从当前定义重建表单：上一条的草稿不能串到下一条。
  useEffect(() => {
    if (!open) return
    setLocalErr(null)
    setValues(
      skill
        ? skillFormValues(skill)
        : { id: '', name: '', description: '', runtimes: [], scope: 'global', body: DEFAULT_BODY },
    )
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, skill?.id])

  const patch = (next: Partial<SkillFormValues>): void => setValues((cur) => ({ ...cur, ...next }))

  const submit = (): void => {
    const id = values.id.trim()
    if (created) {
      if (!id) return setLocalErr('调用名必填')
      if (!SKILL_ID_RE.test(id) || id.length > 64) {
        return setLocalErr('调用名只能用英文小写字母/数字，用连字符分段（例如 weekly-report），≤64 位')
      }
    }
    if (!values.name.trim()) return setLocalErr('名称必填')
    setLocalErr(null)
    void onSubmit({ ...values, id })
  }

  const field = (label: string, node: ReactNode, hint?: string): ReactNode => (
    <div style={{ marginTop: 10 }}>
      <div style={{ fontSize: 11, color: tokens.textSecondary, marginBottom: 4 }}>{label}</div>
      {node}
      {hint && <div style={{ marginTop: 3, fontSize: 10.5, color: tokens.textPlaceholder }}>{hint}</div>}
    </div>
  )

  return (
    <Modal
      open={open}
      onCancel={onCancel}
      width={720}
      destroyOnHidden
      title={
        <div>
          <div style={{ fontSize: 15, fontWeight: 600 }}>{created ? '新建技能' : '编辑技能'}</div>
          <div style={{ marginTop: 2, fontSize: 11, color: tokens.textPlaceholder }}>
            {created ? '创建后调用名不可修改' : `调用名：${values.id}`}
          </div>
        </div>
      }
      footer={
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
          <Button onClick={onCancel}>取消</Button>
          <Space size={8}>
            <Button type="primary" loading={busy} onClick={submit}>
              {created ? '新建' : '保存'}
            </Button>
          </Space>
        </div>
      }
    >
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
        <div>
          <div style={{ fontSize: 11, color: tokens.textSecondary, marginBottom: 4 }}>名称 *</div>
          <Input value={values.name} placeholder="周报生成器" onChange={(e) => patch({ name: e.target.value })} />
        </div>
        <div>
          <div style={{ fontSize: 11, color: tokens.textSecondary, marginBottom: 4 }}>调用名</div>
          <Input
            value={values.id}
            disabled
            placeholder="weekly-report"
            onChange={(e) => patch({ id: e.target.value })}
          />
          <div style={{ marginTop: 3, fontSize: 10.5, color: tokens.textPlaceholder }}>
            {created ? '英文小写字母/数字/连字符；创建后不可修改。' : '创建后不可修改。'}
          </div>
        </div>
      </div>

      {field(
        '描述',
        <Input
          value={values.description}
          placeholder="一句话说明这个技能做什么、什么时候用"
          onChange={(e) => patch({ description: e.target.value })}
        />,
      )}

      {field(
        '技能内容',
        <Input.TextArea
          value={values.body}
          onChange={(e) => patch({ body: e.target.value })}
          autoSize={{ minRows: 10, maxRows: 18 }}
          spellCheck={false}
          style={{ fontFamily: tokens.fontMono, fontSize: 12.5 }}
        />,
        '描述这个技能在什么场景使用、按什么步骤做事。保存前会自动检查内容。',
      )}

      {localErr && <Alert type="error" showIcon style={{ marginTop: 10 }} message={localErr} />}
      {error && <Alert type="error" showIcon style={{ marginTop: 10 }} message="保存失败" description={error} />}
    </Modal>
  )
}
