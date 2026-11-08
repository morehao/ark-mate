import { useMemo, useState } from 'react'
import { EyeOutlined } from '@ant-design/icons'
import type { ArtifactMeta } from '@ark-mate/types'
import { tokens } from '@ark-mate/ui'
import { usePreview } from './preview/PreviewProvider'

/** 字节数 → 人类可读（与 SessionDetail 同口径，独立实现避免导出内部）。 */
function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i += 1
  }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

const KIND_LABEL: Record<ArtifactMeta['kind'], string> = {
  file: '文件',
  text: '文本',
  table: '表格',
  link: '链接',
}

/** 默认折叠阈值：超过就收成「全部 N 个」，避免一屏产物把回答撑得太长（对齐 DSH）。 */
const COLLAPSED_COUNT = 4

/**
 * 回答末尾的产物区（对齐 DSH 的 turnTail 交付物）：
 * - 放在某一轮回答的**最下面**，就近可点预览（不再必须开「会话信息」面板）。
 * - 卡片网格 `repeat(2, minmax(0,1fr))`，单卡 60px；全部 `min-width:0` + 卡内 ellipsis，
 *   从结构上保证不打横条（与 Part A 的宽度纪律一致）。
 * - `file_id` 缺失 → 卡置灰不可点（沿用 SessionDetail 的口径，不报错）。
 * - 点击整卡 → 统一预览壳 `openPreview({file_id,name,size_bytes})`。
 * - 超过 COLLAPSED_COUNT 收成「全部 N 个 / 收起」。
 */
export function TurnArtifacts({ artifacts }: { artifacts: ArtifactMeta[] }) {
  const { openPreview } = usePreview()
  const [expanded, setExpanded] = useState(false)

  // 同路径只保留最新版（artifacts 已按 created_at 降序；与 SessionDetail 的去重口径一致）。
  const latest = useMemo(() => {
    const seen = new Set<string>()
    return artifacts.filter((a) => {
      const key = a.path || a.id
      if (seen.has(key)) return false
      seen.add(key)
      return true
    })
  }, [artifacts])

  if (latest.length === 0) return null

  const collapsible = latest.length > COLLAPSED_COUNT
  const shown = collapsible && !expanded ? latest.slice(0, COLLAPSED_COUNT) : latest
  const single = shown.length === 1

  return (
    <div
      style={{
        marginTop: 10,
        minWidth: 0,
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
      }}
      aria-label="本轮产物"
    >
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: single ? 'minmax(0, 1fr)' : 'repeat(2, minmax(0, 1fr))',
          gap: 10,
          minWidth: 0,
        }}
      >
        {shown.map((a) => (
          <ArtifactCard key={a.id} artifact={a} onPreview={openPreview} />
        ))}
      </div>
      {collapsible && (
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          aria-expanded={expanded}
          style={{
            alignSelf: 'center',
            padding: '1px 11px',
            border: 0,
            borderRadius: 6,
            background: 'transparent',
            color: tokens.textSecondary,
            cursor: 'pointer',
            font: 'inherit',
            fontSize: 12,
            lineHeight: '18px',
          }}
        >
          {expanded ? '收起' : `全部 ${latest.length} 个`}
        </button>
      )}
    </div>
  )
}

function ArtifactCard({
  artifact: a,
  onPreview,
}: {
  artifact: ArtifactMeta
  onPreview: ReturnType<typeof usePreview>['openPreview']
}) {
  const clickable = !!a.file_id
  const kicker = KIND_LABEL[a.kind] ?? a.kind
  return (
    <div
      style={{
        position: 'relative',
        boxSizing: 'border-box',
        display: 'flex',
        alignItems: 'center',
        gap: 10,
        minWidth: 0,
        height: 60,
        padding: '8px 12px',
        overflow: 'hidden',
        border: `1px solid ${tokens.border}`,
        borderRadius: 12,
        background: tokens.surface,
      }}
    >
      {/* 整卡透明覆盖层：点击 → 预览。file_id 缺失时不渲染，卡不可点。 */}
      {clickable && (
        <button
          type="button"
          aria-label={`预览 ${a.title}`}
          title={`预览 ${a.title}`}
          onClick={() => onPreview({ file_id: a.file_id!, name: a.title, size_bytes: a.size_bytes })}
          style={{
            position: 'absolute',
            inset: 0,
            zIndex: 1,
            width: '100%',
            padding: 0,
            border: 0,
            borderRadius: 'inherit',
            background: 'transparent',
            cursor: 'pointer',
          }}
        />
      )}
      <span
        aria-hidden="true"
        style={{
          position: 'relative',
          zIndex: 2,
          flex: 'none',
          display: 'grid',
          placeItems: 'center',
          width: 34,
          height: 34,
          borderRadius: 9,
          border: `1px solid ${tokens.border}`,
          background: tokens.softFill,
          color: clickable ? tokens.primary : tokens.textPlaceholder,
          pointerEvents: 'none',
        }}
      >
        <EyeOutlined style={{ fontSize: 15 }} />
      </span>
      <div
        style={{
          position: 'relative',
          zIndex: 2,
          display: 'flex',
          flex: 1,
          flexDirection: 'column',
          gap: 2,
          minWidth: 0,
          pointerEvents: 'none',
        }}
      >
        <span
          title={a.title}
          style={{
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
            fontSize: 13,
            fontWeight: 500,
            lineHeight: '20px',
            color: clickable ? tokens.text : tokens.textPlaceholder,
          }}
        >
          {a.title}
        </span>
        <span
          style={{
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
            fontSize: 11,
            lineHeight: '16px',
            color: tokens.textSecondary,
          }}
        >
          {clickable ? `${kicker} · ${fmtBytes(a.size_bytes)} · 点击预览` : `${kicker} · ${fmtBytes(a.size_bytes)} · 不可预览`}
        </span>
      </div>
    </div>
  )
}
