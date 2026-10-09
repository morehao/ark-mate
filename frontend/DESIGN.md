---
version: 1.0
name: ark-mate-frontend-design
description: "ark-mate Web 问答控制台（frontend/apps/web）统一视觉语言。以 ark-mate-console.html 原型为基准的 modern-minimal 浅色应用壳（Linear / Vercel 语感），纪律对齐 ark-iam DESIGN：靛蓝 #4f6ef7 是唯一强调色，仅用于主按钮 / 链接 / 选中态 / 品牌点；页面表面一律中性灰阶；卡片与内容区用冷调 hairline 边框分层、不使用卡片阴影（阴影仅留给 Modal / toast 浮层）；文字为三级灰阶并全站开启 tabular-nums。代码事实源：packages/ui/src/theme.ts 的 tokens 对象（本文件与之一一对应，改动需同步两处）。"

colors:
  brand-primary: "#4f6ef7"
  brand-primary-hover: "#6b86ff"
  brand-primary-active: "#3a55d6"
  brand-primary-soft: "rgba(79, 110, 247, 0.12)"
  brand-gradient: "linear-gradient(135deg, #4f6ef7 0%, #7a5af8 55%, #a855f7 100%)"
  surface-layout: "#f7f8fa"
  surface-card: "#ffffff"
  surface-soft-fill: "#f1f3f5"
  surface-selected-bg: "rgba(79, 110, 247, 0.10)"
  surface-code: "#fafafa"
  border-hairline: "#e6e9ef"
  border-strong: "#d3d8e0"
  text-primary: "#1f2430"
  text-secondary: "#56637a"
  text-placeholder: "#94a3b8"
  semantic-success: "#22c55e"
  semantic-warning: "#f59e0b"
  semantic-warning-bg: "#fffbe6"
  semantic-warning-border: "#ffe58f"
  semantic-error: "#ef4444"

typography:
  family: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif"
  mono: "ui-monospace, 'SF Mono', 'JetBrains Mono', Menlo, Consolas, monospace"
  body-size: 14px
  chat-message: 15px

rounded:
  control: 8px
  card: 12px
  option: 13px
  composer: 16px

spacing:
  base: 4px
  appbar-height: 52px
  session-rail-width: 288px
  transcript-max-width: 840px
---

# ark-mate 前端 DESIGN.md

> 供 AI 编码代理与开发者共同遵循的前端「统一风格约定」。
> **代码唯一事实源是 `packages/ui/src/theme.ts` 的 `tokens` 对象**：改任何色值/圆角/间距必须先改 tokens，再同步本文件 front matter（二者必须一致）。页面与组件一律引用 tokens / antd token，禁止出现 `#4f6ef7`、`#e6e9ef` 之类的裸色值。

## 1. 视觉主题总览

- **定位**：ark-mate Web 问答控制台（会话问答优先，Agent/Runtime 管理为次要入口）。
- **基准**：`ark-mate-console.html`（~/Downloads 下的高保真原型，不入库；仅作设计基准）——modern-minimal 浅色应用壳（Linear / Vercel 语感）。
- **画布**：页面底 `surface-layout`（冷白）；会话侧栏 / 卡片 / 输入区落在白色表面 `surface-card`，以 `1px solid border-hairline` hairline 分层、**无卡片阴影**（阴影仅 Modal / toast 浮层 / 移动端抽屉）。
- **品牌**：靛蓝 `#4f6ef7` 是唯一强调色（主按钮 / 链接 / 选中态 / 焦点环 / 品牌点「ark·forge」的 ·）；会话选中态与 agent 选项卡用半透明 `primary-soft` / `selected-bg` 柔和底，主色不铺满页面。
- **问答正文**：用户消息为中性软灰气泡（`soft-fill`），assistant 正文为纯文字 + 流式光标；思考用左侧竖线弱化区；工具调用为白卡 hairline 卡片；系统/meta 行居中 mono 弱字。
- 全站 `tabular-nums`（由 `@ark/ui` AppShell 根容器开启），seq / 计数等数字等宽。

## 2. 布局骨架

```
AppShell（antd ConfigProvider zhCN + themeConfig + tabular-nums）
└─ <div .app 100dvh flex-col>
   ├─ TopBar（52px 白底 hairline 底）：burger / logo(→/conversations) / 视图 chip / 管理入口 / WS 状态
   ├─ reconnecting 横幅（WS 非 open 时）
   ├─ 路由区
   │   ├─ /conversations[/:id]  ChatPage：
   │   │   ├─ ConversationRail（288px 白侧栏：新建会话 + 会话列表 + 底座 foot）
   │   │   ├─ 无 id（未选中会话）→ StartPanel 新建面板（hero + 当前 Agent 行 + 输入卡 + 示例指令）
   │   │   ├─ 有 id → chat：ChatHead（标题 + 当前 Agent 胶囊）/ Transcript（840px 居中）/ Composer
   │   │   └─ StatusBar（30px：WS + eventbus seq）
   │   └─ /agents /capabilities /usage /settings  管理次要页（PageContainer 白卡列表）
   └─ toast（右上浮层，仅浮层可带阴影）
```

移动端 ≤920px：burger 出现、会话侧栏抽屉化（fixed + translateX）、管理入口收纳。

## 3. 组件规格

- **会话条目**：标题两行截断；meta 行 mono 10.5：agent 名胶囊 + 状态点/文案 + 右侧 `seq N`；选中态 = `selected-bg` 柔和底 + 左侧 2.5px 靛蓝竖条（CSS ::before）。
- **消息条目**：role 标签 mono 10 大写弱字（user / assistant / assistant · thinking / assistant · tool）；用户气泡右对齐 `14px 14px 4px 14px`；工具卡 header `tool_call · <name>` mono + 状态行（ok 绿勾 / running 脉冲点 / failed 红）。
- **Composer**：白卡 16px 圆角 hairline 边 + focus-within 靛蓝 ring（3px `primary-soft`）；textarea 自动增高 ≤180px；底部 mono 提示 + 主色发送按钮（Enter 发送 / Shift+Enter 换行；流式期间显示「单飞 turn 门控」+ 忙碌态）。
- **模型选择器（ModelSelect）**：关闭态是**药丸**不是表单框——`.fg-model-pill`（h=28 / 999 圆角 / hairline 边 / mono 11.5 值文案 / 10px 灰箭头），静息贴合文案（min 132、max 240），聚焦展开到 216 并出靛蓝环，hover 抬边+软底；下拉浮层 `.fg-model-pop`（hairline + 12 圆角 + 投影，仅浮层可有阴影）宽约 340，选项为**两行行**（名称 12.5 + 描述 11 灰字单行截断，hover 出全文），分组标题 mono 10.5 弱字。
- **新建会话面板（StartPanel）**：未选中会话时问答区即为该面板（不再有独立「开始问答」空态页，也无 `/new` 独立路由）——hero 标题 + 当前 Agent 标识行 + 内嵌输入卡（模型 pill / 主色圆形发送）+ 示例指令列表；新建固定用默认 Agent，不展示 agent 选项卡（也不提供跳转，只如实标出用哪个 Agent）。
- **当前 Agent 标识（AgentBadge）**：**纯展示、不可点**——mono 胶囊 = 状态点（running 绿 / stopped 灰）+「Agent · 名称」（+ 默认 `primary-soft` 小标）；hover 出详情浮层（状态 / provider / runtime 候选 / id）。会话与 Agent 一对一绑定，故只在新建面板与 ChatHead 各标一处，不逐条消息重复。
- **管理页**：`PageContainer`（标题/描述/右上操作 + 白卡行列表），行间 hairline 分隔，ID 用 mono。

## 4. Do's and Don'ts

### Do
- 改设计值：先改 `packages/ui/src/theme.ts` 的 `tokens`，再同步本文件 front matter（两处一致）。
- 引用颜色一律 `tokens.*`（`import { tokens } from '@ark/ui'`）或 antd `theme.useToken()`。
- 状态/语义展示用 `@ark/ui` 的 `sessionStatusMeta / agentStateMeta / Dot / Pill`。
- 主色仅用于主操作 / 链接 / 选中态 / 焦点；页面表面中性灰阶 + 白卡 + hairline。

### Don't
- 禁止在业务代码硬编码色值（包括 `rgba(...)` 品牌投影、antd 组件上的字面 hex）。
- 禁止新增 css 文件、禁止在页面散落重复的 `<style>`。聊天的交互态 CSS（`:hover` / `::before` / `@keyframes` / `@media`）**唯一收口在 `apps/web/src/console.css.ts`**，由 app.tsx 注入一次（对齐 ark-iam「结构级微修正统一收口」的哲学；此文件是既含 hover 也含 keyframes 的“单点样式单”）。
- 不要把聊天正文样式搬进 antd（bubble/think/tool 卡为控制台专属自绘组件，`Transcript.tsx`）。
- 删除无引用 import，避免 `noUnusedLocals` 报错。

## 5. 已知差距（与 ark-iam 相同问题）

1. `console.css.ts` 以字符串承载少量交互态 CSS（hover/keyframes/媒体查询），antd/类库无法用 inline style 表达，属既定偏差并在 §4 登记。
2. `theme.ts` 与 DESIGN.md front matter 为人工同步，暂无 lint 强制一致。
