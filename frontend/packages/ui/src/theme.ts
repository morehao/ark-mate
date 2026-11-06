import type { ThemeConfig } from 'antd'

/**
 * ark-mate 前端设计令牌（唯一事实源）。
 *
 * 设计方向：**精致冷白（Refined Cool White）**——近乎无色的冷白表面 + 极细 hairline 分层 +
 * 克制的控件层级。**全站只有一个彩色：靛蓝（accent）。** 它承担主操作（实心 + 白字）、
 * 品牌 mark、选中态、焦点环与链接；近黑（primary）只当正文文字色，**不做任何色块**。
 * 无卡片阴影——唯一例外是输入卡（composer）的极轻浮起，见 `shadowComposer`。
 * 改动请同步 frontend/DESIGN.md 的 front matter。
 */
export const tokens = {
  // 品牌 Brand：主操作 = 品牌实心；近黑只当正文与显式深色文字
  primary: '#0a0a0a', // 正文/标题最重字色；也用于极少数"最高强调"场景
  primaryHover: '#000000',
  primaryActive: '#0a0a0a',
  accent: '#3d5afe', // 强调色：主按钮底色 / 选中态 / 焦点环 / 链接 / 品牌点
  accentHover: '#2f4ae0', // 主按钮 hover（实心底压暗；白字对比度只升不降）
  accentActive: '#2639c4', // 主按钮 active
  accentSoft: '#eef1ff', // 强调色柔和底（选中 chip / 焦点晕）——只当底，不做主按钮填充
  primarySoft: '#f2f2f2', // 中性柔和底（历史名，指中性填充）
  // 品牌 mark 直接用 accent 实心 + onPrimary 字。2026-11-06 删除「靛蓝→紫」渐变：
  // 同一产品里出现第三个色相（紫）与「单一彩色」规则冲突；favicon.svg 同步改单色。

  // 表面 Surface（近白冷灰阶）
  bg: '#fcfcfd', // 布局底（近白）
  surface: '#ffffff', // 卡片/侧栏/输入区
  softFill: '#f6f7f9', // 中性填充底：chip、hover、用户气泡、默认按钮 hover
  codeBg: '#fafafa', // 代码/内容块底
  selectedBg: '#f2f4ff', // 选中项柔和底（偏靛蓝）
  surfaceActive: '#eceff3', // 按下态填充 / Segmented 轨道底（要与选中白块拉得开）

  // 边框 Border（极细 hairline 分层主语言；控件边比 hairline 深一档，保证"可点"可读）
  border: '#eceef2', // hairline 分割线 / 卡片外框
  borderStrong: '#d5d9e0', // 控件边（输入框 / 次按钮 / chip）——比 hairline 深，白底上可见
  borderHover: '#9aa1ac', // 控件 hover 抬边（2.6:1，明确可点）

  // 文字 Text（三级灰阶，近黑主色）
  text: '#111827',
  textSecondary: '#6b7280',
  textPlaceholder: '#9aa1ac',

  // 危险操作（删除/撤销）：静息中性边 + 红字，hover 才染红底——不抢占主视觉
  dangerSoft: '#fef3f2',
  dangerBorder: '#f5a9a4',

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

  // 阴影：全站无卡片阴影，**唯一例外**是输入卡（它是视线落点，不是内容容器）
  shadowComposer: '0 12px 32px -22px rgba(15, 23, 42, 0.34), 0 0 0 0.5px rgba(15, 23, 42, 0.05)',

  // 布局：管理页内容容器上限（配合 padding-inline: clamp(16px, 2.4vw, 40px)）
  pageMaxWidth: 1600,

  // 圆角（精致冷白：克制的小圆角，不外扩成大圆角）
  radiusSm: 7,
  radiusMd: 10,
  radiusLg: 12,
  radiusComposer: 14, // 输入卡：比卡片大一档（它是"会话的桌面"，不是内容卡）
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
    borderRadiusSM: tokens.radiusSm, // 小按钮与常规按钮同一圆角，避免行内出现 5px/7px 两种
    borderRadiusLG: tokens.radiusMd,
    fontFamily: tokens.fontFamily,
    fontSize: 14,
  },
  components: {
    // 主按钮 = **品牌实心 + 白字**（主流口径）：一眼是主操作。
    // 旧的「浅靛蓝底 + 靛蓝字」tinted 方案撤掉——白卡上只有 1.1:1 的底、hover/active 还会
    // 跌破对比度，读起来像 Tag / 像禁用。次按钮靠可见描边立住（见 default* 一组）。
    Button: {
      colorPrimary: tokens.accent, // 主按钮底
      colorPrimaryHover: tokens.accentHover, // 主按钮 hover 底
      colorPrimaryActive: tokens.accentActive, // 主按钮 active 底
      primaryColor: tokens.onPrimary, // 主按钮字色
      primaryShadow: 'none',
      dangerShadow: 'none', // 撤掉 antd 给危险按钮的红晕投影（一排按钮里最跳的那块红边）
      fontWeight: 600, // 真实字重档，合成会发虚
      // 次按钮：白底 + 可见描边 + 深字；hover 抬边到 2.6:1 并给中性软底
      defaultBg: tokens.surface,
      defaultColor: tokens.text,
      defaultBorderColor: tokens.borderStrong,
      defaultHoverBg: tokens.softFill,
      defaultHoverColor: tokens.text,
      defaultHoverBorderColor: tokens.borderHover,
      defaultActiveBg: tokens.surfaceActive,
      defaultActiveColor: tokens.text,
      defaultActiveBorderColor: tokens.borderHover,
      defaultShadow: 'none',
      // 文字按钮（折叠 / 收起这类既不是卡片也不是控件的动作）：中性字 + hover 软底
      textTextColor: tokens.textSecondary,
      textTextHoverColor: tokens.text,
      textTextActiveColor: tokens.text,
      textHoverBg: tokens.softFill,
    },
    // 分段控件：默认轨道色是 colorBgLayout（近白），在近白页面上整条隐形。
    // 轨道必须比页面底深一档，选中白块才有"thumb"感。
    Segmented: {
      trackBg: tokens.surfaceActive,
      trackPadding: 2,
      itemColor: tokens.textSecondary,
      itemHoverColor: tokens.text,
      itemSelectedBg: tokens.surface,
      itemSelectedColor: tokens.text,
    },
  },
}
