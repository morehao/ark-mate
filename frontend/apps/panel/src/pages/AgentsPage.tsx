import { useEffect } from 'react'
import { Button } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { agentStateMeta, Dot, PageContainer, Pill, tokens } from '@ark/ui'
import { useForge } from '../store'

/** Agents 只读页（console 管理入口，M1）：每 runtime 一个默认播种 Agent。 */
export function AgentsPage() {
  const agents = useForge((s) => s.agents)
  const refreshAgents = useForge((s) => s.refreshAgents)

  useEffect(() => {
    void refreshAgents()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <PageContainer
      title="Agents"
      description="控制面已注册的 Agent。每个 runtime 播种一个默认 Agent；自定义注册 / 能力标记 / provider 注入为 M2。"
      extra={
        <Button size="small" icon={<ReloadOutlined />} onClick={() => void refreshAgents()}>
          刷新
        </Button>
      }
    >
      {agents.length === 0 && (
        <div style={{ padding: 32, textAlign: 'center', color: tokens.textPlaceholder, fontSize: 13 }}>
          暂无 Agent（服务不可达或未播种）。检查后端 forge serve 是否在运行。
        </div>
      )}
      {agents.map((a, i) => {
        const st = agentStateMeta(a.state)
        return (
          <div
            key={a.id}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 12,
              padding: '12px 18px',
              borderBottom: i === agents.length - 1 ? 'none' : `1px solid ${tokens.border}`,
            }}
          >
            <Dot color={st.color} size={7} />
            <b style={{ fontSize: 13.5, fontWeight: 600 }}>{a.name}</b>
            <Pill>{a.runtime}</Pill>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
              <span style={{ color: st.color }}>{st.label}</span>
            </span>
            <span style={{ marginLeft: 'auto', fontFamily: tokens.fontMono, fontSize: 11.5, color: tokens.textSecondary }}>{a.id}</span>
          </div>
        )
      })}
    </PageContainer>
  )
}
