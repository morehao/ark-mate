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
  pill: 999px

spacing:
  base: 4px
  rail-width: 288px
  rail-brand-height: 48px
  rail-foot-height: 40px
  nav-item-height: 32px
  nav-subitem-height: 30px
  mobile-bar-height: 52px
  transcript-max-width: 840px
---

# ark-mate 前端 DESIGN.md

> 供 AI 编码代理与开发者共同遵循的前端「统一风格约定」。
> **代码唯一事实源是 `packages/ui/src/theme.ts` 的 `tokens` 对象**：改任何色值/圆角/间距必须先改 tokens，再同步本文件 front matter（二者必须一致）。页面与组件一律引用 tokens / antd token，禁止出现 `#4f6ef7`、`#e6e9ef` 之类的裸色值。

## 1. 视觉主题总览

- **定位**：ark-mate Web 问答控制台（会话问答优先，Agent/Runtime 管理为次要入口）。
- **基准**：`ark-mate-console.html`（~/Downloads 下的高保真原型，不入库；仅作设计基准）——modern-minimal 浅色应用壳（Linear / Vercel 语感）。
- **画布**：页面底 `surface-layout`（冷白）；左栏 / 卡片 / 输入区落在白色表面 `surface-card`，以 `1px solid border-hairline` hairline 分层、**无卡片阴影**（阴影仅 Modal / toast 浮层 / 移动端抽屉）。
- **品牌**：靛蓝 `#4f6ef7` 是唯一强调色（主按钮 / 链接 / 选中态 / 焦点环 / 品牌点「ark·mate」的 ·）；会话选中态与 agent 选项卡用半透明 `primary-soft` / `selected-bg` 柔和底，主色不铺满页面。
- **问答正文**：用户消息为中性软灰气泡（`soft-fill`），assistant 正文为纯文字 + 流式光标；思考用左侧竖线弱化区；工具调用为白卡 hairline 卡片；系统/meta 行居中 mono 弱字。
- 全站 `tabular-nums`（由 `@ark-mate/ui` AppShell 根容器开启），seq / 计数等数字等宽。

## 2. 布局骨架

```
AppShell（antd ConfigProvider zhCN + themeConfig + tabular-nums）
└─ <div .app 100dvh flex-row>
   ├─ NavRail（左栏 288px 白底 + 右侧 hairline；常驻，唯一导航面）
   │   ├─ 品牌区（48px）：ark·mate → /conversations
   │   ├─ 导航区：一级 4 条（问答 / 扩展 / 用量 / 定时任务）32px；「扩展」是组（专家 · 技能 · 连接器），
   │   │            组行只切换展开（组没有落地页）
   │   ├─ 会话区（**常驻**，所有页面都在）：视图切换（会话/归档）+ 新建会话 + 搜索 + 会话列表（ConversationPane）
   │   └─ footer：固定左下，这一行**只有「设置」**。点它 → **直接开设置弹窗**（一体式：
   │               标题「设置」+ 左列分节 通用 / 模型 / 命令规则 / 关于 + 右列内容）
   ├─ 右列（flex-1 flex-col）
   │   ├─ 窄屏顶条（≤920px：burger + 当前入口名；宽屏隐藏）
   │   ├─ reconnecting 横幅（WS 非 open 时）
   │   ├─ 路由区
   │   │   ├─ /conversations[/:id]  ChatPage：
   │   │   │   ├─ 无 id（未选中会话）→ StartPanel 新建面板（hero + 当前 Agent 行 + 输入卡 + 示例指令）
   │   │   │   ├─ 有 id → chat：ChatHead（标题 + 当前 Agent 胶囊）/ Transcript（840px 居中）/ Composer
   │   │   ├─ /extensions/skills|/connectors|/experts  扩展二级页（PageContainer 白卡列表；无总览页）
   │   │   ├─ /usage /automations  管理页（PageContainer 白卡列表）
   │   │   └─ 历史路径：/extensions、/capabilities → /extensions/skills；/settings、/settings/agents、/agents → 打开对应设置弹窗并落回问答页
   │   │       （设置不是页面：分节定义 `console/settingsView.tsx`，弹窗宿主 `components/settings/SettingsModals.tsx`）
   │   └─ StatusBar（30px，**常驻每个页面**）：eventbus 水位（左）+ 会话语义提示（仅问答页）+ WS 连接态（最右）
   └─ toast（右上浮层，仅浮层可带阴影；三态图标：错误红 ⚠ / 成功绿 ✓ / 中性灰 ℹ）
```

移动端 ≤920px：左栏抽屉化（fixed + translateX，top 0——顶栏已撤，抽屉盖满全高），窄屏顶条出现 burger（唯一开启入口），点导航项/遮罩即收起。

## 3. 组件规格

- **会话条目**：单行——标题（省略号，title 全名）+ 置顶钉 + 右端「状态点 + 日期」；hover / 当前行时右端整格淡出让位给 `⋯`（位置不跳），状态词进 title 与会话信息浮层。状态点六态各一色：运行中（`primary` + 呼吸）、待授权（`warning` 实心）、失败（`error`）、已完成（`success`）、已取消（`textPlaceholder` 灰——作废不是失败）、已中断（`warning` **空心环**——可重发 ≠ 正在等你）。日期口径 `MM-DD`（跨年 `YYYY-MM-DD`），中性灰、等宽数字。
- **消息条目**：role 标签 mono 10 大写弱字（user / assistant / assistant · thinking / assistant · tool）；用户气泡右对齐 `14px 14px 4px 14px`；工具卡 header `tool_call · <name>` mono + 状态行（ok 绿勾 / running 脉冲点 / failed 红）。
- **Composer**：白卡 16px 圆角 hairline 边 + focus-within 靛蓝 ring（3px `primary-soft`）；textarea 自动增高 ≤180px；底部一行只有工作区 / 权限 chip + 主色发送按钮（流式期间显示「单飞 run 门控」+ 忙碌态）。**不再有常驻发送键提示**（2026-11-05 反馈）：键位固定 Enter 发送 / Shift+Enter 换行（⌘/Ctrl+Enter 也可发送），只在「设置 · 通用 · 键盘操作」里**只读展示**，不提供切换。
- **模型选择器（ModelSelect）**：关闭态是**药丸**不是表单框——`.fg-model-pill`（h=28 / 999 圆角 / hairline 边 / mono 11.5 值文案 / 10px 灰箭头），静息贴合文案（min 132、max 240），聚焦展开到 216 并出靛蓝环，hover 抬边+软底；下拉浮层 `.fg-model-pop`（hairline + 12 圆角 + 投影，仅浮层可有阴影）宽约 340，选项为**两行行**（名称 12.5 + 描述 11 灰字单行截断，hover 出全文），分组标题 mono 10.5 弱字。
- **新建会话面板（StartPanel）**：未选中会话时问答区即为该面板（不再有独立「开始问答」空态页，也无 `/new` 独立路由）——hero 标题 + 当前 Agent 标识行 + 内嵌输入卡（模型 pill / 主色圆形发送）+ 示例指令列表；新建固定用默认 Agent，不展示 agent 选项卡（也不提供跳转，只如实标出用哪个 Agent）。
- **当前 Agent 标识（AgentBadge）**：**纯展示、不可点**——mono 胶囊 = 状态点（running 绿 / stopped 灰）+「Agent · 名称」（+ 默认 `primary-soft` 小标）；hover 出详情浮层（状态 / provider / runtime 候选 / id）。会话与 Agent 一对一绑定，故只在新建面板与 ChatHead 各标一处，不逐条消息重复。
- **左栏导航（NavRail）**：条目只在 `packages/console/src/console/navView.tsx` 定义（含 order 与图标），交互态（hover / 选中 / 展开）只在 `console.css.ts` 的 `.fg-rail*` / `.fg-nav*` 类。组的展开态是 `navExpanded[组] ?? 当前路由是否命中该组`：会话页不展开「扩展」（把左栏高度让给会话列表，1440×800 下会话列表可视 ≈7 条），命中扩展二级页时自动展开以露出当前二级项；用户显式收起过的组不再自动展开。**组没有落地页**（2026-11-05 去掉「扩展总览」）：组行整行只切换展开，箭头的视觉与点击面合一；旧 `/extensions`、`/capabilities` 落到 `/extensions/skills`。一级叶子 32px、二级 30px（缩进 26px）；**一级项带 16px 图标**（图标与条目同源在 `navView.tsx`，未选中 `textPlaceholder`、hover `textSecondary`、选中 `primary`），**二级项不给图标**（缩进子项挂图标会把层级读平）；选中 = `soft-fill` 底 + 主色（二级）；未实现的项（`pending`）**可读不可点**，右侧挂 mono「待开发」标签并给 hover 说明——不留占位页、不注册路由。主导航只放内容入口（问答 / 扩展 / 用量 / 定时任务），**「设置」不是路由入口**（它是左栏 footer 的菜单，见下一条）；会话区恒常驻所以 footer 自然在栏底，`margin-top: auto` 只是兜底。**footer 那一行只有「设置」**（令牌自 2026-10-10 起收进设置菜单，见下一条）。
- **底部状态条（StatusBar）**：**常驻每个页面**（挂在 app.tsx 右列末尾，不在 ChatPage 里），30px + top hairline；左侧 mono 10.5 是 eventbus 水位（`已 attach <id>` / `未 attach 会话（共 N 个）`），右侧最靠边常驻 WS 连接态（`Dot` + `WS 已连接 / 连接中… / 断开（自动重连中…） / 未连接`），中间的会话语义提示（`问答会话绑定唯一 Agent · 切换 Agent = 新建会话`）只在问答页出现。WS 状态只此一处，左栏不重复。
- **设置（左栏一个按钮 + 一体式弹窗）**：设置是**同一配置面下的若干动作**，不是并列的内容面——所以它既不是路由页，也不进左栏主导航，更不再有"左栏二级菜单 → 每节一个小弹窗"的两跳：左栏左下「设置」（`.fg-rail-footrow` 里唯一一项，`fg-navitem` 同一套 hover/选中底、与主导航图标同一条竖线）**点一下直接开弹窗**。弹窗（`SettingsDialog`，`centered` + `destroyOnHidden` + `footer={null}`，宽 1120、高 `min(64vh, 640px)`）自己分两列：左列 `.fg-setdlg-nav` 是分节列表（208px，图标 18px + label 13px + hint 11px 两行，命中态 = soft-fill 底 + 主色图标 + 600 字重），右列 `.fg-setdlg-body` 是当前节内容（各节自己出 `.fg-setsec` = 顶部说明 + 右侧动作 + `.fg-setsec-card` 白卡），两列各自滚动、**换节时弹窗高矮不变**；≤920px 分节列表横过来当顶条。**不换页、不改 URL**：从任何页面都能就地改设置、分节之间一步切换；旧深链接（`/settings`、`/settings/agents`、`/agents`）由 `SettingsDeepLink` 打开弹窗对应分节后落回问答页。现有四节：**通用**（工作区目录白名单）、**模型**（供应商 / 模型清单 / 默认模型 / 生效结论）、**命令规则**（哪些命令可直接执行 / 必须确认 / 禁止）、**关于**（产品名 / 核心版本 / 产品身份）。新增一节只改 `console/settingsView.tsx` + `SettingsDialog` 里的一行渲染。理由与 workdsh 一致
- **管理页**：`PageContainer`（标题/描述/右上操作 + 白卡行列表），行间 hairline 分隔，ID 用 mono。

## 4. Do's and Don'ts

### Do
- 改设计值：先改 `packages/ui/src/theme.ts` 的 `tokens`，再同步本文件 front matter（两处一致）。
- 引用颜色一律 `tokens.*`（`import { tokens } from '@ark-mate/ui'`）或 antd `theme.useToken()`。
- 状态/语义展示用 `@ark-mate/ui` 的 `sessionStatusMeta / agentStateMeta / Dot / Pill`。
- 主色仅用于主操作 / 链接 / 选中态 / 焦点；页面表面中性灰阶 + 白卡 + hairline。

### Don't
- 禁止在业务代码硬编码色值（包括 `rgba(...)` 品牌投影、antd 组件上的字面 hex）。
- 禁止新增 css 文件、禁止在页面散落重复的 `<style>`。聊天的交互态 CSS（`:hover` / `::before` / `@keyframes` / `@media`）**唯一收口在 `packages/console/src/console.css.ts`**，由 app.tsx 注入一次（对齐 ark-iam「结构级微修正统一收口」的哲学；此文件是既含 hover 也含 keyframes 的“单点样式单”）。
- 不要把聊天正文样式搬进 antd（bubble/think/tool 卡为控制台专属自绘组件，`Transcript.tsx`）。
- 删除无引用 import，避免 `noUnusedLocals` 报错。

## 5. 已知差距（与 ark-iam 相同问题）

1. `console.css.ts` 以字符串承载少量交互态 CSS（hover/keyframes/媒体查询），antd/类库无法用 inline style 表达，属既定偏差并在 §4 登记。
2. `theme.ts` 与 DESIGN.md front matter 为人工同步，暂无 lint 强制一致。
