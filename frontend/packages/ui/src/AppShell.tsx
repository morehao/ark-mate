import type { ReactNode } from 'react'
import { App as AntdApp, ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import dayjs from 'dayjs'
import 'dayjs/locale/zh-cn'
import { themeConfig } from './theme'

dayjs.locale('zh-cn')

interface Props {
  children: ReactNode
}

/**
 * 应用外壳：注入 Ant Design 全局主题（中文语言包 + 设计令牌），每个 app 的 main.tsx
 * 用 <AppShell> 包裹。根容器开启 tabular-nums，让 ID/时间/seq 等数字等宽对齐
 * （Stripe 数据细节，纪律见 frontend/DESIGN.md）。
 */
export function AppShell({ children }: Props) {
  return (
    <div style={{ fontVariantNumeric: 'tabular-nums' }}>
      <ConfigProvider locale={zhCN} theme={themeConfig}>
        <AntdApp>{children}</AntdApp>
      </ConfigProvider>
    </div>
  )
}
