import { useCallback, useEffect, useRef, useState } from 'react'
import { Popover, Spin, Tag } from 'antd'
import { AuditOutlined } from '@ant-design/icons'
import { getSkillMaterialization } from '@ark-mate/api'
import type { SkillMaterializationResp, SkillMaterializationState } from '@ark-mate/types'
import { materializationMeta } from '../console/capabilityView'
import { fmtShortDateTime } from '../console/time'

/**
 * 技能行的**物化体检** popover（§13.4 S2 审计面，只读）。
 *
 * 设计要点（对齐计划 WP-B1）：
 *  - 复用既有 `getSkillMaterialization(id)`（`api/resources/mate.ts`），本组件是它的首个消费者；
 *    接口契约不变，**不得**因任何清理把它删掉。
 *  - 逐 `resp.states[]` 按 `root/runtime` 渲染一行，状态→`{color,label,hint}` 一律走
 *    `materializationMeta(status)`（`console/capabilityView.ts`）；9 个真实 status 以该表为准，
 *    组件里**不另造词**。未知状态由 `materializationMeta` 兜底成 original status。
 *  - 空/无目标（`states` 为空）时显示 `resp.notes[]`（后端解释为什么没有逐项状态）。
 *  - **只读**：本组件不发任何写请求、不写盘、不做修复——重校走既有的启用/刷新路径。
 *  - 加载/错误态沿用既有 `fg-cap-*` 语言（Spin + 一句人话）。
 *
 * 样式类名统一 `fg-matck-*`（material-check 缩写），由 lead 写入 `console.css.ts`；
 * 这里只引用类名，不内联大段样式，与 `fg-cap-*` 的收口方式一致。
 */

interface Props {
  /** 技能定义 id。 */
  skillId: string
  /** 技能展示名（入口 title / 空态文案里用它，缺省回退 id）。 */
  skillName?: string
}

/** 单行：一个物化目标（root + 可选 runtime）的现状。 */
function StateRow({ state }: { state: SkillMaterializationState }) {
  const meta = materializationMeta(state.status)
  const target = state.runtime ? `${state.root} · ${state.runtime}` : state.root
  return (
    <div className="fg-matck-row" data-status={state.status}>
      <div className="fg-matck-row-head">
        <span className="fg-matck-target" title={target}>
          {target || '（未命名目标）'}
        </span>
        <Tag color={meta.color} title={meta.hint} className="fg-matck-tag">
          {meta.label}
        </Tag>
      </div>
      {state.dir && (
        <div className="fg-matck-dir" title={state.dir}>
          {state.dir}
        </div>
      )}
      <div className="fg-matck-hint">{meta.hint}</div>
      {state.detail && <div className="fg-matck-detail">{state.detail}</div>}
      {state.written_at !== undefined && (
        <div className="fg-matck-time">写入于 {fmtShortDateTime(state.written_at)}</div>
      )}
    </div>
  )
}

/** popover 内容（拆出来，便于在获得首个响应后直接渲染，无需再走一遍 Popover 状态）。 */
function Body({
  resp,
  error,
  skillName,
}: {
  resp: SkillMaterializationResp | null
  error: string | null
  skillName: string
}) {
  if (error) {
    return (
      <div className="fg-cap-pop fg-matck-pop" role="alert">
        <div className="fg-cap-loading">{error}</div>
      </div>
    )
  }
  if (!resp) {
    return (
      <div className="fg-cap-pop fg-matck-pop">
        <div className="fg-cap-loading">
          <Spin size="small" /> 正在体检「{skillName}」…
        </div>
      </div>
    )
  }
  const notes = resp.notes ?? []
  const hasStates = resp.states.length > 0
  return (
    <div className="fg-cap-pop fg-matck-pop">
      <div className="fg-matck-head">
        <span className="fg-matck-title">物化体检 · {skillName}</span>
        <span className="fg-matck-flag">{resp.enabled ? '已启用投放' : '已禁用（副本按设计撤回）'}</span>
      </div>
      {hasStates ? (
        <div className="fg-matck-list">
          {resp.states.map((s, i) => (
            <StateRow key={`${s.root}-${s.runtime ?? ''}-${i}`} state={s} />
          ))}
        </div>
      ) : (
        <div className="fg-matck-empty">
          {notes.length > 0 ? (
            notes.map((n, i) => (
              <div key={i} className="fg-matck-note-line">
                {n}
              </div>
            ))
          ) : (
            <div className="fg-matck-note-line">这个技能当前没有物化目标。</div>
          )}
        </div>
      )}
      {/* 有逐项状态时，notes 若还带补充说明也照常显示在后端口径里，避免信息丢失。 */}
      {hasStates && notes.length > 0 && (
        <div className="fg-matck-notes">
          {notes.map((n, i) => (
            <div key={i} className="fg-matck-note-line">
              {n}
            </div>
          ))}
        </div>
      )}
      {/* 只读纪律：这是审计面，不提供任何修复动作——重校走既有启用/刷新。 */}
      <div className="fg-cap-note fg-matck-foot">只读体检：不写盘、不修复；重校请走开启/刷新。</div>
    </div>
  )
}

/**
 * 「体检」入口：一个图标钮，点开懒加载并展示物化体检。
 *
 * 懒加载策略：只在**首次打开**时请求，同一次打开期间不重复打；关掉再打开会重新取最新
 * （物化状态随时会因重新投放而变），但用 `reload` 标记避免每次都无谓请求。
 */
export function MaterializationPopover({ skillId, skillName }: Props) {
  const name = skillName && skillName !== '' ? skillName : skillId
  const [open, setOpen] = useState(false)
  const [resp, setResp] = useState<SkillMaterializationResp | null>(null)
  const [error, setError] = useState<string | null>(null)
  // 已加载的 skillId：换了技能或重新打开时据此判断是否需要重新取数。
  const loadedFor = useRef<string | null>(null)

  const load = useCallback(
    (id: string) => {
      setResp(null)
      setError(null)
      let alive = true
      getSkillMaterialization(id)
        .then((r) => {
          if (alive) setResp(r)
        })
        .catch((err) => {
          if (alive) setError(err instanceof Error ? err.message : String(err))
        })
      return () => {
        alive = false
      }
    },
    [],
  )

  useEffect(() => {
    // 打开且尚未为该技能取过数 → 取一次；关掉再开会因 loadedFor 被清空而重新取最新。
    if (open && loadedFor.current !== skillId) {
      loadedFor.current = skillId
      return load(skillId)
    }
    if (!open) loadedFor.current = null
  }, [open, skillId, load])

  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      trigger="click"
      placement="leftTop"
      arrow={false}
      destroyOnHidden
      content={<Body resp={resp} error={error} skillName={name} />}
      styles={{ content: { padding: 6 } }}
    >
      <button type="button" className="fg-matck-entry" title="物化体检" aria-label={`物化体检：${name}`}>
        <AuditOutlined style={{ fontSize: 12 }} />
      </button>
    </Popover>
  )
}
