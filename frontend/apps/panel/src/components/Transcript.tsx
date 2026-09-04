import type { CSSProperties, ReactNode } from 'react'
import { CheckOutlined, CloseOutlined } from '@ant-design/icons'
import { tokens } from '@ark/ui'
import type { StreamEntry } from '@ark/types'

const roleLabel: CSSProperties = {
  fontFamily: tokens.fontMono,
  fontSize: 10,
  letterSpacing: '0.07em',
  textTransform: 'uppercase',
  color: tokens.textPlaceholder,
  display: 'flex',
  alignItems: 'center',
  gap: 8,
}

function Role({ children }: { children: ReactNode }) {
  return <div style={roleLabel}>{children}</div>
}

function toolStatusMeta(status: string): { label: string; color: string; pulse?: boolean } {
  const st = status.toLowerCase()
  if (['completed', 'ok', 'success', 'done'].includes(st)) return { label: 'ok', color: tokens.success }
  if (['running', 'started', 'pending', 'queued'].includes(st)) return { label: status || 'running', color: tokens.primary, pulse: true }
  if (['failed', 'error', 'err'].includes(st)) return { label: 'failed', color: tokens.error }
  return { label: status || '…', color: tokens.textSecondary }
}

const toneColor: Record<NonNullable<StreamEntry['tone']>, string> = {
  ok: tokens.success,
  err: tokens.error,
  cancel: tokens.warning,
  info: tokens.textSecondary,
}

const systemIcon = {
  ok: <CheckOutlined style={{ fontSize: 11, color: tokens.success }} />,
  err: <CloseOutlined style={{ fontSize: 11, color: tokens.error }} />,
  cancel: <CloseOutlined style={{ fontSize: 11, color: tokens.warning }} />,
  info: <CheckOutlined style={{ fontSize: 11, color: tokens.textSecondary }} />,
}

/** 一条流式会话条目（console.html .msg 家族：气泡 / 思考 / 工具卡 / meta 行）。 */
export function EntryView({ e }: { e: StreamEntry }) {
  switch (e.kind) {
    case 'user':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 5, alignItems: 'flex-end' }}>
          <Role>user</Role>
          <div
            style={{
              maxWidth: 'min(78%, 560px)',
              background: tokens.softFill,
              border: `1px solid ${tokens.border}`,
              borderRadius: '14px 14px 4px 14px',
              padding: '9px 13px',
              fontSize: 15,
              lineHeight: 1.65,
              whiteSpace: 'pre-wrap',
              overflowWrap: 'anywhere',
              color: tokens.text,
            }}
          >
            {e.text}
          </div>
        </div>
      )

    case 'assistant':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 5, minWidth: 0 }}>
          <Role>assistant</Role>
          <div
            style={{
              color: tokens.text,
              fontSize: 15,
              lineHeight: 1.72,
              whiteSpace: 'pre-wrap',
              overflowWrap: 'anywhere',
              padding: '0 2px',
            }}
          >
            {e.text}
          </div>
        </div>
      )

    case 'thought':
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 5, minWidth: 0 }}>
          <Role>assistant · thinking</Role>
          <div
            style={{
              display: 'flex',
              gap: 10,
              borderLeft: `2px solid ${tokens.border}`,
              padding: '2px 0 2px 12px',
              color: tokens.textSecondary,
              fontSize: 13.5,
              lineHeight: 1.7,
              minWidth: 0,
            }}
          >
            <span
              style={{
                flex: 'none',
                fontFamily: tokens.fontMono,
                fontSize: 10,
                letterSpacing: '0.08em',
                textTransform: 'uppercase',
                color: tokens.textPlaceholder,
                paddingTop: 3,
              }}
            >
              思考
            </span>
            <span style={{ minWidth: 0, overflowWrap: 'anywhere', whiteSpace: 'pre-wrap' }}>{e.text}</span>
          </div>
        </div>
      )

    case 'tool': {
      const meta = toolStatusMeta(e.status ?? '')
      return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 5, minWidth: 0 }}>
          <Role>assistant · tool</Role>
          <div
            style={{
              border: `1px solid ${tokens.border}`,
              borderRadius: 12,
              background: tokens.surface,
              padding: '10px 13px',
              display: 'flex',
              flexDirection: 'column',
              gap: 8,
              maxWidth: 720,
              minWidth: 0,
            }}
          >
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 7,
                fontFamily: tokens.fontMono,
                fontSize: 11.5,
                minWidth: 0,
              }}
            >
              <span style={{ color: tokens.text, fontWeight: 600 }}>tool_call</span>
              <span style={{ color: tokens.textPlaceholder }}>·</span>
              <span
                style={{
                  color: tokens.textSecondary,
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                  minWidth: 0,
                }}
              >
                {e.title}
              </span>
            </div>
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 6,
                fontFamily: tokens.fontMono,
                fontSize: 10.5,
                color: tokens.textSecondary,
              }}
            >
              {meta.pulse ? (
                <span
                  className="fg-anim-pulse"
                  style={{ flex: 'none', width: 7, height: 7, borderRadius: '50%', background: meta.color, display: 'inline-block' }}
                />
              ) : meta.label === 'ok' ? (
                <CheckOutlined style={{ fontSize: 11, color: meta.color }} />
              ) : (
                <span style={{ flex: 'none', width: 7, height: 7, borderRadius: '50%', background: meta.color, display: 'inline-block' }} />
              )}
              <span style={{ color: tokens.text, fontWeight: 500 }}>{meta.label}</span>
              <span style={{ marginLeft: 'auto' }}>tool_result · {e.status || 'running'}</span>
            </div>
          </div>
        </div>
      )
    }

    case 'system': {
      const tone = (e.tone ?? 'info') as NonNullable<StreamEntry['tone']>
      return (
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            gap: 7,
            margin: '4px 0',
            fontFamily: tokens.fontMono,
            fontSize: 10.5,
            color: toneColor[tone],
          }}
        >
          {systemIcon[tone]}
          <span>{e.text}</span>
        </div>
      )
    }
  }
}
