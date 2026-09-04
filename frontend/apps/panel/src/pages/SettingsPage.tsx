import { PageContainer, tokens } from '@ark/ui'

const M2_ITEMS = [
  '自定义 Agent 注册向导（definitions 目录条目 + 校验）',
  'provider 配置注入（env cascade）与凭证管理',
  '能力基线 / 权限请求裁剪入口（工具权限交互面）',
]

/** Settings（M2 占位）：注册 / 配置面预留。 */
export function SettingsPage() {
  return (
    <PageContainer
      title="设置"
      description="自定义 Agent / provider 配置将在 M2 提供编辑面；当前 M1 控制台为只读问答形态。"
    >
      <div style={{ padding: '18px 20px' }}>
        <div style={{ fontSize: 13, color: tokens.textSecondary, lineHeight: 1.8 }}>
          M2 计划能力（见 docs/design-and-roadmap.md §5.3）：
        </div>
        <ul style={{ margin: '8px 0 0', paddingLeft: 20, color: tokens.text, fontSize: 13, lineHeight: 2 }}>
          {M2_ITEMS.map((t) => (
            <li key={t}>{t}</li>
          ))}
        </ul>
      </div>
    </PageContainer>
  )
}
