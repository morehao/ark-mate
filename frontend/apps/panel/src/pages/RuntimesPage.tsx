import { useEffect } from 'react'
import { Button } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { Dot, PageContainer, Pill, tokens } from '@ark/ui'
import { useForge } from '../store'

/** Runtimes 只读页（console 管理入口，M1）：可用 ACP runtime（opencode / deepseek-harness）。 */
export function RuntimesPage() {
  const runtimes = useForge((s) => s.runtimes)
  const refreshRuntimes = useForge((s) => s.refreshRuntimes)

  useEffect(() => {
    void refreshRuntimes()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <PageContainer
      title="Runtimes"
      description="可接入的 ACP runtime 底座与版本（由 runtime adapter 能力探测上报）。会话问答在「新建会话」里按底座发起。"
      extra={
        <Button size="small" icon={<ReloadOutlined />} onClick={() => void refreshRuntimes()}>
          刷新
        </Button>
      }
    >
      {runtimes.length === 0 && (
        <div style={{ padding: 32, textAlign: 'center', color: tokens.textPlaceholder, fontSize: 13 }}>
          暂无 runtime 信息（服务不可达）。
        </div>
      )}
      {runtimes.map((rt, i) => (
        <div
          key={rt.name}
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 12,
            padding: '12px 18px',
            borderBottom: i === runtimes.length - 1 ? 'none' : `1px solid ${tokens.border}`,
          }}
        >
          <Dot color={tokens.success} size={7} />
          <b style={{ fontSize: 13.5, fontWeight: 600 }}>{rt.name}</b>
          <Pill mono>{rt.version}</Pill>
          <span style={{ marginLeft: 'auto', fontSize: 12, color: tokens.textSecondary }}>
            opencode（opencode acp）/ deepseek-harness（dsh --profile acp）等 ACP 兼容执行器
          </span>
        </div>
      ))}
    </PageContainer>
  )
}
