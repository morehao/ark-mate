import type { ThemeConfig } from 'antd'

/**
 * ark-forge 前端设计令牌（唯一事实源）。
 *
 * 设计方向：以 ark-forge-console.html 为基准的 modern-minimal 浅色应用壳
 * （Linear / Vercel 语感），纪律对齐 ark-iam DESIGN：
 * 靛蓝只出现在主按钮 / 链接 / 选中态 / 品牌点；页面表面中性灰阶、hairline 分层；
 * 阴影只留给 Modal 等浮层。改动请同步 frontend/DESIGN.md 的 front matter。
 */
export const tokens = {
  // 品牌 Brand：靛蓝（稀有资源）
  primary: '#4f6ef7',
  primaryHover: '#6b86ff',
  primaryActive: '#3a55d6',
  primarySoft: 'rgba(79, 110, 247, 0.12)', // 选中态/激活态柔和底
  gradient: 'linear-gradient(135deg, #4f6ef7 0%, #7a5af8 55%, #a855f7 100%)',

  // 表面 Surface（中性灰阶）
  bg: '#f7f8fa', // 布局底（冷白）
  surface: '#ffffff', // 卡片/侧栏/输入区
  softFill: '#f1f3f5', // 中性填充底：chip、hover、用户气泡
  codeBg: '#fafafa', // 代码/内容块底
  selectedBg: 'rgba(79, 110, 247, 0.10)', // 会话选中项柔和底

  // 边框 Border（hairline 分层主语言）
  border: '#e6e9ef',
  borderStrong: '#d3d8e0',

  // 文字 Text（三级灰阶）
  text: '#1f2430',
  textSecondary: '#56637a',
  textPlaceholder: '#94a3b8',

  // 语义色 Semantic（状态专用）
  success: '#22c55e',
  warning: '#f59e0b',
  warningBg: '#fffbe6',
  warningBorder: '#ffe58f',
  error: '#ef4444',

  // 排版
  fontFamily:
    "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif",
  fontMono: "ui-monospace, 'SF Mono', 'JetBrains Mono', Menlo, Consolas, monospace",

  // 圆角
  radiusSm: 8,
  radiusMd: 12,
  radiusLg: 14,
} as const

export const themeConfig: ThemeConfig = {
  token: {
    colorPrimary: tokens.primary,
    colorInfo: tokens.primary,
    colorLink: tokens.primary,
    colorSuccess: tokens.success,
    colorWarning: tokens.warning,
    colorError: tokens.error,
    colorBgLayout: tokens.bg,
    colorBorder: tokens.borderStrong,
    colorBorderSecondary: tokens.border,
    colorText: tokens.text,
    colorTextSecondary: tokens.textSecondary,
    colorTextPlaceholder: tokens.textPlaceholder,
    colorFillTertiary: tokens.softFill,
    colorBgContainer: tokens.surface,
    borderRadius: tokens.radiusSm,
    borderRadiusLG: tokens.radiusMd,
    fontFamily: tokens.fontFamily,
    fontSize: 14,
  },
}
