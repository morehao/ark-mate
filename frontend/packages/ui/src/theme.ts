import type { ThemeConfig } from 'antd'

/**
 * ark-mate 前端设计令牌（唯一事实源）。
 *
 * 设计方向：**精致冷白（Refined Cool White）**——近乎无色的冷白表面 + 极细 hairline 分层 +
 * 近黑实心主按钮；强调色（靛蓝）刻意稀有：只出现在选中态、焦点环、品牌点与状态点，
 * 主操作按钮用近黑（不是彩色），彩色出现面积 < 5%。字重与字距讲究、数字 tabular-nums。
 * 无卡片阴影（阴影仅留给 Modal / toast 浮层）。改动请同步 frontend/DESIGN.md 的 front matter。
 */
export const tokens = {
  // 品牌 Brand：近黑主操作 + 稀有靛蓝强调（accent 不铺满页面）
  primary: '#0a0a0a', // 主操作（按钮实心）/ 选中文字——近黑，非彩色
  primaryHover: '#000000',
  primaryActive: '#0a0a0a',
  accent: '#3d5afe', // 稀有强调色：选中态/焦点环/链接/品牌点
  accentSoft: '#eef1ff', // 强调色柔和底（选中 chip / 焦点晕）
  primarySoft: '#f2f2f2', // 中性柔和底（历史名，指中性填充）
  // 品牌渐变 = 与 favicon.svg 完全一致（靛蓝→紫）。这是**品牌资产**，
  // 不随中性色盘/强调色调整；改它就会让应用内 logo 和网页图标对不上。
  gradient: 'linear-gradient(135deg, #4f6ef7 0%, #7a5af8 55%, #a855f7 100%)',

  // 表面 Surface（近白冷灰阶）
  bg: '#fcfcfd', // 布局底（近白）
  surface: '#ffffff', // 卡片/侧栏/输入区
  softFill: '#f6f7f9', // 中性填充底：chip、hover、用户气泡
  codeBg: '#fafafa', // 代码/内容块底
  selectedBg: '#f2f4ff', // 选中项柔和底（偏靛蓝）

  // 边框 Border（极细 hairline 分层主语言）
  border: '#eceef2',
  borderStrong: '#e1e4ea',

  // 文字 Text（三级灰阶，近黑主色）
  text: '#111827',
  textSecondary: '#6b7280',
  textPlaceholder: '#9aa1ac',

  // 实心底上的前景色（主按钮/危险按钮/强调底的文字）——统一在这里，别处不写 #fff
  onPrimary: '#ffffff',

  // 语义色 Semantic（状态专用；低饱和、克制）
  success: '#12b76a',
  warning: '#b54708',
  warningBg: '#fffaeb',
  warningBorder: '#fde9c4',
  error: '#d92d20',

  // 排版
  fontFamily:
    "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif",
  fontMono: "ui-monospace, 'SF Mono', 'JetBrains Mono', Menlo, Consolas, monospace",

  // 圆角（精致冷白：克制的小圆角，不外扩成大圆角）
  radiusSm: 7,
  radiusMd: 10,
  radiusLg: 12,
  radiusPill: 999, // 圆形按钮（+ / 发送键）与 pill 形态 chip
} as const

export const themeConfig: ThemeConfig = {
  token: {
    // 全局 primary = accent 靛蓝：输入框聚焦环、开关、勾选框、滑杆、分页激活等
    // 一律走品牌强调色（近黑只用于正文与显式的深色文字，不当交互态色）。
    colorPrimary: tokens.accent,
    colorInfo: tokens.accent, // 信息/链接/选中态走稀有靛蓝
    colorLink: tokens.accent,
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
    controlHeight: 32,
    controlHeightSM: 28, // 行内 small 按钮不再 24px 瘦小，和工具行同一档手感
    borderRadius: tokens.radiusSm,
    borderRadiusLG: tokens.radiusMd,
    fontFamily: tokens.fontFamily,
    fontSize: 14,
  },
  components: {
    // 主按钮走「tinted」口径：浅靛蓝底 + 靛蓝字（轻量、有品牌感，不砸一块黑砖）。
    // antd 由 colorPrimary 派生 hover/active，浅底派生仍是浅底，视觉稳定；primaryColor 定字色。
    Button: {
      colorPrimary: tokens.accentSoft, // primary 底色（浅靛蓝）
      colorPrimaryHover: '#e3e8ff',
      colorPrimaryActive: '#dbe2ff',
      primaryColor: tokens.accent, // primary 文字色（靛蓝）
      primaryShadow: 'none',
      fontWeight: 600, // 用真实字重档（560 无对应字形，合成会发虚）
      defaultShadow: 'none',
      // 默认（ghost/图标钮）按钮：白底极细边，hover 抬边，与搜索框同一族
      defaultBg: tokens.surface,
      defaultColor: tokens.text, // 默认按钮字色用主文字深色（灰字低对比会显得发虚/糊）
      defaultBorderColor: tokens.borderStrong,
      defaultHoverBg: tokens.surface,
      defaultHoverColor: tokens.text,
      defaultHoverBorderColor: tokens.textPlaceholder,
      defaultActiveBg: tokens.softFill,
      defaultActiveColor: tokens.text,
      defaultActiveBorderColor: tokens.textPlaceholder,
    },
  },
}
