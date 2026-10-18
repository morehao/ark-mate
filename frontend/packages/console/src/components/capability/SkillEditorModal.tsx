import { useEffect, useState, type ReactNode } from 'react'
import { Alert, Button, Input, Modal, Select, Space } from 'antd'
import { tokens } from '@ark-mate/ui'
import type { Skill } from '@ark-mate/types'
import { skillFormValues, type SkillFormValues } from '../../console/capabilityView'

const SKILL_ID_RE = /^[a-z0-9]+(?:-[a-z0-9]+)*$/
const DEFAULT_BODY = '# 技能名\n\n## 何时使用\n\n- 说明触发场景。\n\n## 工作步骤\n\n1. 第一步。\n'

/**
 * 技能编辑器（720）：表单 + 「高级（JSON）」。
 *
 * 表单只改**已知键**，且编辑时只提交发生变化的键（patch 在 `capabilityView.ts` 里成文）。
 * 未知键与 `null`/`[]` 这类精确表达走高级入口——那正是原文 JSON 编辑器存在的理由，不删。
 */
export function SkillEditorModal({
  open,
  skill,
  busy,
  error,
  onCancel,
  onSubmit,
  onAdvanced,
}: {
  open: boolean
  skill: Skill | null
  busy: boolean
  error: string | null
  onCancel: () => void
  onSubmit: (values: SkillFormValues) => Promise<void>
  onAdvanced: (values: SkillFormValues) => void
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
      if (!id) return setLocalErr('id 必填：它同时是定义文件名与投放目录名')
      if (!SKILL_ID_RE.test(id) || id.length > 64) {
        return setLocalErr('id 须小写字母/数字、用连字符分段（形如 weekly-report），≤64 位')
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
          <div style={{ fontSize: 15, fontWeight: 600 }}>{created ? '新建 skill' : '编辑 skill'}</div>
          <div style={{ marginTop: 2, fontSize: 11, color: tokens.textPlaceholder, fontFamily: tokens.fontMono }}>
            {created ? '定义与投放目录都用 id' : values.id}
          </div>
        </div>
      }
      footer={
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
          <Button onClick={onCancel}>取消</Button>
          <Space size={8}>
            <Button type="text" onClick={() => onAdvanced(values)}>
              高级（JSON）
            </Button>
            <Button type="primary" loading={busy} onClick={submit}>
              {created ? '新建' : '保存'}
            </Button>
          </Space>
        </div>
      }
    >
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
        <div>
          <div style={{ fontSize: 11, color: tokens.textSecondary, marginBottom: 4 }}>id *</div>
          <Input
            value={values.id}
            disabled={!created}
            placeholder="weekly-report"
            onChange={(e) => patch({ id: e.target.value })}
          />
          <div style={{ marginTop: 3, fontSize: 10.5, color: tokens.textPlaceholder }}>
            {created ? '小写字母/数字/连字符；创建后不可改。' : '创建后不可改（要换 id 就删掉重建）。'}
          </div>
        </div>
        <div>
          <div style={{ fontSize: 11, color: tokens.textSecondary, marginBottom: 4 }}>名称 *</div>
          <Input value={values.name} placeholder="周报生成器" onChange={(e) => patch({ name: e.target.value })} />
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

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
        <div>
          <div style={{ fontSize: 11, color: tokens.textSecondary, margin: '10px 0 4px' }}>投放对象</div>
          <Select
            mode="tags"
            style={{ width: '100%' }}
            value={values.runtimes}
            placeholder="留空 = 全部 runtime"
            onChange={(v: string[]) => patch({ runtimes: v })}
            options={[{ value: 'dsh', label: 'dsh' }]}
          />
          <div style={{ marginTop: 3, fontSize: 10.5, color: tokens.textPlaceholder }}>
            留空 = 投放给所有支持 skill 投影的 runtime，不是「没有投放对象」。
          </div>
        </div>
        <div>
          <div style={{ fontSize: 11, color: tokens.textSecondary, margin: '10px 0 4px' }}>作用域</div>
          <Select
            style={{ width: '100%' }}
            value={values.scope}
            onChange={(v: 'global' | 'workspace') => patch({ scope: v })}
            options={[
              { value: 'global', label: 'global' },
              { value: 'workspace', label: 'workspace' },
            ]}
          />
          <div style={{ marginTop: 3, fontSize: 10.5, color: tokens.textPlaceholder }}>
            workspace 目前只落声明（按工作区落点待实测）。
          </div>
        </div>
      </div>

      {field(
        'SKILL.md',
        <Input.TextArea
          value={values.body}
          onChange={(e) => patch({ body: e.target.value })}
          autoSize={{ minRows: 10, maxRows: 18 }}
          spellCheck={false}
          style={{ fontFamily: tokens.fontMono, fontSize: 12.5 }}
        />,
        '保存前会扫描正文：critical 规则直接拒载，且零字节落盘。',
      )}

      {localErr && <Alert type="error" showIcon style={{ marginTop: 10 }} message={localErr} />}
      {error && <Alert type="error" showIcon style={{ marginTop: 10 }} message="保存失败" description={error} />}
    </Modal>
  )
}
