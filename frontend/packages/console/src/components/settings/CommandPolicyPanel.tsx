import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Input, Select, Spin, Tag } from 'antd'
import { DeleteOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import { IconAction } from '../capability/shared'
import { getCommandPolicy, toErrorMessage, updateCommandPolicy } from '@ark-mate/api'
import type { CommandDecision, CommandPolicyResp, CommandPolicyRule } from '@ark-mate/types'
import { useMate } from '../../store'
import { CardHeader } from './CardHeader'

/**
 * 设置 · 命令规则（A2）：把 `<MATE_HOME>/command_policy.json` 的**规则表**做成可编辑面。
 *
 * 三条界面口径：
 *  - **只编辑 pattern/decision/name**；`match_samples` / `not_match_samples` 是加载期的样板断言，
 *    没有可视编辑（手写文件才能加），保存时**原样保留**——删掉它们等于悄悄削弱校验；
 *  - **保存即生效**（服务端先校验再落盘，成功后热更新裁决点），所以没有"重启后生效"的模糊态；
 *  - **复杂命令不归 allow 管**：含引号/变量/管道/`;`/重定向等语法的命令一律走人工确认，
 *    这句话必须出现在界面上——否则用户会以为写了 allow 就再也不会被问。
 */

type DraftRule = {
  name: string
  /** 空格分隔的 token；每个 token 内用 `|` 表示备选（与规则文件的 `pattern` 同义）。 */
  patternText: string
  decision: CommandDecision
  match_samples?: string[][]
  not_match_samples?: string[][]
  host_executable?: string[]
}

const DECISION_OPTIONS: Array<{ value: CommandDecision; label: string }> = [
  { value: 'allow', label: '允许（直接执行）' },
  { value: 'prompt', label: '需确认（默认）' },
  { value: 'forbidden', label: '禁止' },
]

function patternToText(pattern: string[][]): string {
  return (pattern ?? []).map((alts) => alts.join('|')).join(' ')
}

function textToPattern(text: string): string[][] {
  const tokens = text.trim().split(/\s+/).filter(Boolean)
  return tokens.map((t) => t.split('|').filter(Boolean))
}

function fromResp(resp: CommandPolicyResp): DraftRule[] {
  return (resp.rules ?? []).map((r) => ({
    name: r.name ?? '',
    patternText: patternToText(r.pattern),
    decision: r.decision,
    match_samples: r.match_samples,
    not_match_samples: r.not_match_samples,
    host_executable: r.host_executable,
  }))
}

function toRules(drafts: DraftRule[]): CommandPolicyRule[] {
  return drafts.map((d) => ({
    name: d.name.trim() || undefined,
    pattern: textToPattern(d.patternText),
    decision: d.decision,
    // 样板断言不在这张表单里编辑，但**必须原样带回去**（见文件头）。
    match_samples: d.match_samples,
    not_match_samples: d.not_match_samples,
    host_executable: d.host_executable,
  }))
}

/** 一条规则的"可保存"判据：pattern 至少一个 token（与后端 Validate 同义，只做本地预检）。 */
function ruleValid(d: DraftRule): boolean {
  return textToPattern(d.patternText).length > 0
}

export function CommandPolicyPanel() {
  const showToast = useMate((s) => s.showToast)
  const [resp, setResp] = useState<CommandPolicyResp | null>(null)
  const [drafts, setDrafts] = useState<DraftRule[]>([])
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const r = await getCommandPolicy()
      setResp(r)
      setDrafts(fromResp(r))
    } catch (err) {
      showToast(toErrorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [showToast])

  useEffect(() => {
    void load()
  }, [load])

  const enabled = resp?.enabled ?? false
  const dirty =
    resp != null && JSON.stringify(toRules(drafts)) !== JSON.stringify(resp.rules ?? [])

  const patch = (i: number, next: Partial<DraftRule>) => {
    setDrafts((ds) => ds.map((d, j) => (j === i ? { ...d, ...next } : d)))
  }

  const save = async () => {
    if (drafts.some((d) => !ruleValid(d))) {
      showToast('每条规则至少要有 1 个词（空格分隔；备选用 |）')
      return
    }
    setSaving(true)
    try {
      const r = await updateCommandPolicy(toRules(drafts))
      setResp(r)
      setDrafts(fromResp(r))
      showToast('命令规则已保存并立即生效', 'success')
    } catch (err) {
      showToast(toErrorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <section style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      <CardHeader
        title="命令规则"
        hint="哪些命令可以直接执行 / 必须确认 / 禁止；保存后立即生效"
        extra={
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>
            重新读取
          </Button>
        }
      />

      {loading ? (
        <div style={{ padding: 24, textAlign: 'center' }}>
          <Spin />
        </div>
      ) : !enabled ? (
        <Alert
          type="warning"
          showIcon
          message="命令门禁未配置"
          description="设置 MATE_COMMAND_POLICY 指向一个规则文件（默认 ~/.ark-mate/command_policy.json）后重启，本页才能编辑规则。"
        />
      ) : (
        <>
          <Alert
            type="info"
            showIcon
            message="只对简单命令自动裁决"
            description="命令里出现引号、变量、管道、;、&&、重定向、子 shell、通配符或换行时，一律走人工确认——allow 规则不会直接放行它们。规则按顺序匹配，第一条命中说了算。"
          />

          {drafts.length === 0 && (
            <div style={{ padding: '10px 2px', color: tokens.textPlaceholder, fontSize: 12.5 }}>
              还没有规则。默认行为是「每条 shell 命令都问人」。
            </div>
          )}

          {drafts.map((d, i) => (
            <div
              key={i}
              style={{
                display: 'grid',
                gridTemplateColumns: '160px minmax(0, 1fr) 150px 32px',
                gap: 8,
                alignItems: 'center',
                padding: '8px 10px',
                border: `1px solid ${tokens.border}`,
                borderRadius: 10,
              }}
            >
              <Input
                value={d.name}
                placeholder="规则名（可选）"
                onChange={(e) => patch(i, { name: e.target.value })}
              />
              <Input
                value={d.patternText}
                placeholder="命令词，例如：git push origin（备选用 |）"
                status={ruleValid(d) ? undefined : 'error'}
                onChange={(e) => patch(i, { patternText: e.target.value })}
              />
              <Select
                value={d.decision}
                options={DECISION_OPTIONS}
                onChange={(v) => patch(i, { decision: v })}
              />
              <IconAction
                icon={<DeleteOutlined />}
                title="删除这条规则"
                danger
                onClick={() => setDrafts((ds) => ds.filter((_, j) => j !== i))}
              />
              {(d.match_samples?.length || d.not_match_samples?.length) ? (
                <div style={{ gridColumn: '1 / -1', fontSize: 11, color: tokens.textPlaceholder }}>
                  <Tag bordered={false}>样板断言</Tag>
                  这条规则自带加载期断言（只读；改 pattern 后若不再命中会被保存拒绝）
                </div>
              ) : null}
            </div>
          ))}

          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Button
              icon={<PlusOutlined />}
              onClick={() =>
                setDrafts((ds) => [...ds, { name: '', patternText: '', decision: 'prompt' }])
              }
            >
              添加规则
            </Button>
            <span style={{ flex: 1 }} />
            <Button type="primary" loading={saving} disabled={!dirty} onClick={() => void save()}>
              保存并生效
            </Button>
          </div>
        </>
      )}
    </section>
  )
}
