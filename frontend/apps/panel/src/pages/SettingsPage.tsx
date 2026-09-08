import { PageContainer, tokens } from '@ark/ui'

const P3_ITEMS = [
  '能力基线 / 权限请求裁剪入口（工具权限交互面）',
  '消息搜索分页与全文高亮（服务端 messages 表已落库）',
  '多 Agent 编排与批量调度面',
]

/** Settings（M2 占位 → P2 起引导到 Agents 管理页）。 */
export function SettingsPage() {
  return (
    <PageContainer
      title="设置"
      description="自定义 Agent / provider 配置已由「Agents」页提供（注册向导 + 启停/热更新 + provider 密钥注入，密钥只写不读回）。"
    >
      <div style={{ padding: '18px 20px' }}>
        <div style={{ fontSize: 13, color: tokens.textSecondary, lineHeight: 1.8 }}>
          仍属后续计划的能力（见 docs/design-and-roadmap.md §5.3）：
        </div>
        <ul style={{ margin: '8px 0 0', paddingLeft: 20, color: tokens.text, fontSize: 13, lineHeight: 2 }}>
          {P3_ITEMS.map((t) => (
            <li key={t}>{t}</li>
          ))}
        </ul>
      </div>
    </PageContainer>
  )
}
