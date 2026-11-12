---
version: 2.5
name: ark-mate-frontend-design
description: "ark-mate Web 问答控制台（frontend/apps/web）统一视觉语言。**精致冷白（Refined Cool White）**：近乎无色的冷白表面 + 极细 hairline 分层 + **克制的两档控件语言——主按钮品牌实心（靛蓝底 + 白字），次按钮白底可见描边**。**全站只有一个彩色：靛蓝 #3d5afe**（主操作 / 品牌 mark / 选中态 / 焦点环 / 链接），近黑只当正文文字色、不做色块；无卡片阴影（唯一例外：输入卡 composer 的极轻浮起），浮层/toast/抽屉才用阴影；文字三级灰阶、字重字距讲究、全站 tabular-nums。列表优先用「行 + 细分割线」而不是卡片网格。代码事实源：packages/ui/src/theme.ts 的 tokens 对象（本文件与之一一对应，改动需同步两处）。"

colors:
  brand-primary: "#0a0a0a"          # 近黑：正文/标题最重的字色；也用于极少数"最高强调"场景
  brand-primary-hover: "#000000"
  brand-primary-active: "#0a0a0a"
  accent: "#3d5afe"                # 强调色：主按钮实心底 / 选中态 / 焦点环 / 链接 / 品牌点
  accent-hover: "#2f4ae0"          # 主按钮 hover 底（压暗，白字对比度只升不降）
  accent-active: "#2639c4"         # 主按钮 active 底
  accent-soft: "#eef1ff"           # 强调色柔和底（选中 chip / 焦点晕）——只当底，不做主按钮填充
  surface-layout: "#fcfcfd"
  surface-card: "#ffffff"
  surface-soft-fill: "#f6f7f9"
  surface-selected-bg: "#f2f4ff"
  surface-active: "#eceff3"        # 按下态填充 / Segmented 轨道底（与选中白块拉得开）
  surface-code: "#fafafa"
  border-hairline: "#eceef2"
  border-strong: "#d5d9e0"         # 控件边：输入框 / 次按钮 / chip
  border-hover: "#9aa1ac"          # 控件 hover 抬边（2.6:1）
  danger-soft: "#fef3f2"           # 危险按钮 hover 底
  danger-border: "#f5a9a4"         # 危险按钮 hover 边
  on-primary: "#ffffff"            # 实心底上的前景色（主按钮/危险按钮/强调底文字）
  text-primary: "#111827"
  text-secondary: "#6b7280"
  text-placeholder: "#9aa1ac"
  semantic-success: "#12b76a"
  semantic-warning: "#b54708"
  semantic-warning-bg: "#fffaeb"
  semantic-warning-border: "#fde9c4"
  semantic-error: "#d92d20"

shadows:
  composer: "0 12px 32px -22px rgba(15, 23, 42, 0.34), 0 0 0 0.5px rgba(15, 23, 42, 0.05)"  # 输入卡唯一例外；其余卡片无阴影

typography:
  family: "-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif"
  mono: "ui-monospace, 'SF Mono', 'JetBrains Mono', Menlo, Consolas, monospace"
  body-size: 14px
  chat-message: 15px

rounded:
  control: 7px
  card: 10px
  option: 12px
  composer: 18px
  pill: 999px

spacing:
  base: 4px
  rail-width: 288px
  rail-brand-height: 48px
  rail-foot-height: 40px
  nav-item-height: 32px
  nav-subitem-height: 30px
  mobile-bar-height: 52px
  chat-column-max-width: 840px     # 聊天页单一内容列（转录/确认区/队列/输入框/新建面板共用）
  chat-column-gutter: 20px         # 聊天列左右保底留白（外层提供，不与 max-width 叠加）
  page-max-width: 1600px
  page-gutter: 40px                # padding-inline 上限：clamp(16px, 2.4vw, 40px)
---

# ark-mate 前端 DESIGN.md

> 供 AI 编码代理与开发者共同遵循的前端「统一风格约定」。
> **代码唯一事实源是 `packages/ui/src/theme.ts` 的 `tokens` 对象**：改任何色值/圆角/间距必须先改 tokens，再同步本文件 front matter（二者必须一致）。页面与组件一律引用 tokens / antd token，禁止出现 `#4f6ef7`、`#e6e9ef` 之类的裸色值。

## 1. 视觉主题总览

- **定位**：ark-mate Web 问答控制台（会话问答优先，Agent/Runtime 管理为次要入口）。
- **风格**：**精致冷白（Refined Cool White）**——参考 Stripe / Linear 的克制高级感：近乎无色的冷白表面、极细 hairline 分层、讲究的字重与字距，把"高级"交给留白和层次而不是颜色。
- **画布**：页面底 `surface-layout`（近白 `#fcfcfd`）；左栏 / 卡片 / 输入区落在白色表面 `surface-card`，以 `1px solid border-hairline`（`#eceef2`，比旧版更浅）分层、**无卡片阴影**（阴影仅 Modal / toast 浮层 / 移动端抽屉，**外加输入卡 `shadowComposer` 一处例外**——它是视线落点，不是内容容器）。
- **交互态色 = accent**：全局 `colorPrimary = tokens.accent`（靛蓝）。输入框聚焦环、开关、勾选框、滑杆、分页/标签激活一律靛蓝；近黑 `tokens.primary` 只当正文与显式深色文字，**不当交互态色**。主按钮底色见 §3（品牌实心）。
- **单一彩色（v2.2 关键纪律）**：全站**只有靛蓝一个彩色**。它承担：主操作实心底、品牌 mark、选中态、焦点环、链接、开关与语义强调标签。**近黑 `primary` 只当正文文字色**，不做按钮底、不做 logo 底、不做任何色块——冷白底上大块近黑会压视觉（2026-11-06 反馈）。因此**不再有第三个色相**：紫色品牌渐变已删除，`favicon.svg` 同步改单色 mark。
- **品牌 mark**：圆角方形、底 `accent`、字 `onPrimary`，文字 `ark`（不是单个字母 `a`）。四处使用同一组件：左栏品牌行、起稿页品牌行、消息头、左栏 footer 身份行。
- **强调色使用律（关键纪律）**：主操作按钮是**品牌实心**（底 `accent` `#3d5afe` + 白字，hover 压暗到 `accent-hover`），一眼是主操作；次操作是白底 + 可见描边（`border-strong`）+ 深字。2026-11-06 纠偏：**撤掉"浅靛蓝底 + 靛蓝字"的 tinted 主按钮**——那套在白卡上只有 1.1:1 的底，hover/active 时字底对比度还会跌破 WCAG AA，读起来像 Tag、像禁用；"浅底 + 饱和色字"是标签语法，不是按钮语法。靛蓝的其它出现面限于选中态 / 焦点环 / 链接 / 品牌点 / 状态强调标签，仍然不铺满页面。
- **列表优先**：管理页用「行 + 细分割线」的列表形态（信息密度高、更克制），而不是卡片网格。行 hover 出浅底 + 右端动作；分割线用 `border-hairline`。
- **问答正文**：用户消息为中性软灰气泡（`soft-fill`），assistant 正文为纯文字 + 流式光标；思考用左侧竖线弱化区；工具调用为白卡 hairline 卡片；系统/meta 行居中 mono 弱字。
- 全站 `tabular-nums`（由 `@ark-mate/ui` AppShell 根容器开启），seq / 计数 / 技能数等数字等宽。

## 2. 布局骨架

```
AppShell（antd ConfigProvider zhCN + themeConfig + tabular-nums）
└─ <div .app 100dvh flex-row>
   ├─ NavRail（左栏 288px 白底 + 右侧 hairline；常驻，唯一导航面）
   │   ├─ 品牌区（48px）：ark·mate → /conversations
   │   ├─ 导航区：一级 5 条（问答 / 扩展 / 应用 / 用量 / 定时任务）32px；「扩展」是组（专家 · 技能 · 连接器），
   │   │            「应用」是组（应用市场 · 我的应用）；组行只切换展开（组没有落地页）
   │   ├─ 会话区（**常驻**，所有页面都在）：视图切换（会话/归档）+ 新建会话 + 搜索 + 会话列表（ConversationPane）
   │   └─ footer：固定左下，这一行**只有「设置」**。点它 → **直接开设置弹窗**（一体式：
   │               标题「设置」+ 左列分节 通用 / 模型 / 命令规则 / 关于 + 右列内容）
   ├─ 右列（flex-1 flex-col）
   │   ├─ 窄屏顶条（≤920px：burger + 当前入口名；宽屏隐藏）
   │   ├─ reconnecting 横幅（WS 非 open 时）
   │   ├─ 路由区
   │   │   ├─ /conversations[/:id]  ChatPage：
   │   │   │   ├─ 无 id（未选中会话）→ StartPanel 新建面板（hero + 当前 Agent 行 + 输入卡 + 示例指令）
   │   │   │   ├─ 有 id → chat：ChatHead（标题 + 当前 Agent 胶囊）/ Transcript（聊天列 tokens.chatColumnMaxWidth 居中）/ Composer
   │   │   ├─ /extensions/skills|/connectors|/experts  扩展二级页（PageContainer 白卡列表；无总览页）
   │   │   ├─ /apps/market /apps/my-apps  应用二级页（PageContainer + AppCard 卡片网格）
   │   │   ├─ /usage /automations  管理页（PageContainer 白卡列表）
   │   │   └─ 历史路径：/extensions、/capabilities → /extensions/skills；/extensions/market → /apps/market、/extensions/my-apps → /apps/my-apps；/settings、/settings/agents、/agents → 打开对应设置弹窗并落回问答页
   │   │       （设置不是页面：分节定义 `console/settingsView.tsx`，弹窗宿主 `components/settings/SettingsModals.tsx`）
   │   └─ StatusBar（30px，**常驻每个页面**）：eventbus 水位（左）+ 会话语义提示（仅问答页）+ WS 连接态（最右）
   └─ toast（右上浮层，仅浮层可带阴影；三态图标：错误红 ⚠ / 成功绿 ✓ / 中性灰 ℹ）
```

移动端 ≤920px：左栏抽屉化（fixed + translateX，top 0——顶栏已撤，抽屉盖满全高），窄屏顶条出现 burger（唯一开启入口），点导航项/遮罩即收起。

## 3. 组件规格

- **按钮（Button）基本口径**：两字中文按钮的字距由 antd 强插 `0.34em` 导致「编 辑」「删 除」被拉散，已在 `console.css.ts` **全站**抹平（不只工具行）；按钮文字一律推独立合成层按整像素光栅（消发虚）；字重走真实档 600；small 控件高度 `controlHeightSM=28`、常规 `controlHeight=32`；圆角统一 `radiusSm`（7px，`borderRadiusSM` 也对齐 7，避免行内出现 5px/7px 两种）。
- **按钮（Button）四档**：
  1. **主按钮**（新建 / 保存 / 发送）= **品牌实心** `accent` 底 + `on-primary` 白字，hover `accent-hover`、active `accent-active`，无投影（`primaryShadow:none`），经 `themeConfig.components.Button` 全局生效。对比度白字 on `#3d5afe` = 5.1:1（过 AA）。
  2. **次按钮 / 图标按钮**（刷新 / 编辑 / 运行史 / 获取工具）= 白底 + `borderStrong`（`#d5d9e0`，比 hairline 深一档）描边 + `text` 深字；hover 底 `softFill` + 边抬到 `borderHover`（`#9aa1ac`，2.6:1）、active 底 `surfaceActive`。**不要用 hairline `#eceef2` 给可点控件做边**——白底上只有 1.16:1，按钮会读成"静止的框"。
  3. **危险按钮**（删除 / 撤销）= 静息**中性边 + 红字**，只有 hover / active 才染红底（`danger-soft`）与红边（`danger-border`）；`dangerShadow` 必须为 `none`（antd 默认给危险按钮加红晕投影，一排动作里最抢眼）。红是语义，不是常驻装饰。
  4. **文字按钮**（折叠 / 收起 / 收起运行史）= 无底无边，中性字 `textSecondary`，hover 转 `text` + `softFill` 底。
  工具行（`.fg-toolbar`）内主按钮与图标按钮同高 32px、同一描边/圆角家族；行内动作按钮用 small（28px），与开关、计数 chip 同一条基线。
  5. **行内状态标签**（`.fg-rowtag`：模型/供应商行上的「文本+图片 / 默认 / 思考 / 容量由环境兜底 / 缺端点」…）——**一套 pill，别各自整形**：高度与行内动作按钮同为 **28px**（`controlHeightSM`，`box-sizing:border-box` 拉齐文字盒，避免 `<button>` 与 `<span>` 差 2px）、圆角 `radiusPill`、字号 11.5、`padding:0 10px`；静息 = `borderStrong` 细边 + 透明底 + `textSecondary` 字；**只有真状态强调**（默认 / 思考 / 已声明图片输入）用 `.fg-info` = `accentSoft` 底 + `accent` 字，其余（如容量说明）保持中性——一排都刷成同色会读成按钮。2026-11-07 纠偏：旧实现写死 `19px` + `6px` 圆角 + 默认 `primary` 边，既比同行按钮矮一截、又用了个不属于本项目的圆角值。
- **菜单项交互态（v2.3 统一口径，2026-11-06）**：所有「可选中项」——左栏一级/二级导航、新建会话、会话行、设置弹窗分节、左栏·会话/归档 tab、工作空间分组行、命令补全行——**只共用一组 CSS 变量**（`console.css.ts` 顶部 `:root` 的 `--fg-menu-*`，值仍取自 tokens）：**默认**文字 `textSecondary` / 图标 `textPlaceholder`；**hover** 文字 `text` + 底 `surfaceActive`（比左栏底 `softFill` 深一档，必须看得见）；**选中** 文字与图标 `accent` + 底 `selectedBg`（**不加粗**：字重变化会让同一栏出现第三种强调）。这样「灰 → 有颜色」只在选中时发生，且新增可选中面不需要再自定一套色——改一处 = 全站一致。**边界（v2.4）**：单选 chip 类（权限 chip `.fg-chip`、投递 pill `.fg-delivery-pill`）**不是**「可选中项列表」，它们沿用 chip 交互态（hover 出软底 + 抬边），不走 `--fg-menu-*`；这只针对"下拉里选一个"的单选小控件，不适用于会保持选中底色的列表行。
- **分段控件（Segmented）**：轨道底必须比页面底深一档（`surfaceActive` `#eceff3`），选中项是白色 thumb + 轻投影——不要沿用 antd 默认的 `trackBg = colorBgLayout`（本仓布局底近白 `#fcfcfd`，会让整条分段控件隐形，只剩几个灰字）。
- **会话条目**：单行——标题（省略号，title 全名）+ 置顶钉 + 右端「状态点 + 日期」；标题颜色走菜单项交互态（默认 `textSecondary`、hover `text`、当前会话 `accent`；归档整行降透明度）；hover / 当前行时右端整格淡出让位给 `⋯`（位置不跳），状态词进 title 与会话信息浮层。状态点六态各一色：运行中（`primary` + 呼吸）、待授权（`warning` 实心）、失败（`error`）、已完成（`success`）、已取消（`textPlaceholder` 灰——作废不是失败）、已中断（`warning` **空心环**——可重发 ≠ 正在等你）。日期口径 `MM-DD`（跨年 `YYYY-MM-DD`），中性灰、等宽数字。
- **消息条目**：role 标签 mono 10 大写弱字（user / assistant / assistant · thinking / assistant · tool）；用户气泡右对齐 `14px 14px 4px 14px`；工具卡 header `tool_call · <name>` mono + 状态行（ok 绿勾 / running 脉冲点 / failed 红）。
- **AI 免责提示**（2026-11-06）：输入卡下方**常驻一行** mono 10.5 弱字「内容由 AI 生成，请核对关键信息后再采用」（`.fg-ai-disclaimer`，`textPlaceholder`、居中、`user-select:none`）。**只挂在对话页 `Composer`**；新建会话面板（`StartPanel`）不展示（2026-11-06 反馈）。**不随轮次状态变化**——进行中 / 已完成 / 失败 / 取消都在。它是"这个助手生成的内容需要你核对"的长期说明，不是某一轮的脚注，所以钉在输入区、不跟着消息走。
- **Composer**：白卡 18px 圆角 hairline 边 + focus-within 靛蓝边 + 3px `accent-soft` ring；textarea 自动增高 ≤180px。**忙态（2026-11-06，v2.4）**：卡片用**品牌描边（`accent`）+ 极淡外晕 + 2.2s 幅度呼吸（`.fg-running`）**表达"在跑"，取代原先塞进控件行的「run 进行中」旁白（旁白已移除；真实阶段词如「正在执行 Bash」在 ChatHead 的 metaText 里说）；等待态（等授权/等回答）卡转 **`warning` 边（`.fg-waiting`）**，说明行贴在**卡上方一行**（`.fg-wait-banner`，不塞控件行）。**投递选择（`.fg-delivery-pill`，v2.4）**：从带框 antd Select 降为**无框轻 pill**，**只在「忙 + 草稿非空」时出现**（空输入不占位），点开是「排队 / 打断并发送」两项（各带副说明）；忙且有草稿时主按钮由 ■停止 变 ↑发送（按 pill 方式投递）。**工作区 chip 在已有会话里不再展示**（2026-11-06 反馈）：工作空间创建即定稿、不可改，每次在输入框下方重复一行「默认」/目录名是常驻噪声；工作空间仍可在会话信息浮层「所属空间」与新建面板看到。**不再有常驻发送键提示**（2026-11-05 反馈）：键位固定 Enter 发送 / Shift+Enter 换行（⌘/Ctrl+Enter 也可发送），只在「设置 · 通用 · 键盘操作」里**只读展示**，不提供切换。
- **模型选择器（ModelSelect）**：关闭态是**药丸**不是表单框——`.fg-model-pill`（h=28 / 999 圆角 / `borderStrong` 边 / mono 11.5 值文案 / 10px 灰箭头），静息贴合文案（min 132、max 240），聚焦展开到 216 并出靛蓝环，hover 抬边+软底；下拉浮层 `.fg-model-pop`（hairline + 12 圆角 + 投影，仅浮层可有阴影）宽约 340，选项为**两行行**（名称 12.5 + 描述 11 灰字单行截断，hover 出全文），分组标题 mono 10.5 弱字。
- **新建会话面板（StartPanel）**：未选中会话时问答区即为该面板（不再有独立「开始问答」空态页，也无 `/new` 独立路由）——hero 标题 + 当前 Agent 标识行 + 内嵌输入卡（模型 pill / 主色圆形发送）+ 示例指令列表；新建固定用默认 Agent，不展示 agent 选项卡（也不提供跳转，只如实标出用哪个 Agent）。
- **当前 Agent 标识（AgentBadge）**：**纯展示、不可点**——mono 胶囊 = 状态点（running 绿 / stopped 灰）+「Agent · 名称」（+ 默认 `primary-soft` 小标）；hover 出详情浮层（状态 / provider / runtime 候选 / id）。会话与 Agent 一对一绑定，故只在新建面板与 ChatHead 各标一处，不逐条消息重复。
- **左栏导航（NavRail）**：条目只在 `packages/console/src/console/navView.tsx` 定义（含 order 与图标），交互态（hover / 选中 / 展开）只在 `console.css.ts` 的 `.fg-rail*` / `.fg-nav*` 类。组的展开态是 `navExpanded[组] ?? 当前路由是否命中该组`：会话页不展开「扩展」（把左栏高度让给会话列表，1440×800 下会话列表可视 ≈7 条），命中扩展二级页时自动展开以露出当前二级项；用户显式收起过的组不再自动展开。**组没有落地页**（2026-11-05 去掉「扩展总览」）：组行整行只切换展开，箭头的视觉与点击面合一；旧 `/extensions`、`/capabilities` 落到 `/extensions/skills`。一级叶子 32px、二级 30px（缩进 26px）；**一级项带 16px 图标**（图标与条目同源在 `navView.tsx`，走统一的 `--fg-menu-*` 交互态：未选中 `textPlaceholder`、hover `text`、选中 `accent`），**二级项不给图标**（缩进子项挂图标会把层级读平）；选中 = `selectedBg` 底 + `accent` 文字（一级/二级同一套）；未实现的项（`pending`）**可读不可点**，右侧挂 mono「待开发」标签并给 hover 说明——不留占位页、不注册路由。主导航只放内容入口（问答 / 扩展 / 应用 / 用量 / 定时任务），**「设置」不是路由入口**（它是左栏 footer 的菜单，见下一条）；会话区恒常驻所以 footer 自然在栏底，`margin-top: auto` 只是兜底。**footer 那一行只有「设置」**（令牌自 2026-10-10 起收进设置菜单，见下一条）。
- **底部状态条（StatusBar）**：**按需出现**（挂在 app.tsx 右列末尾，不在 ChatPage 里），30px + top hairline，**只在问答页且确有运行失败时**才渲染一条「助手最近一次运行失败（code）」，正常时返回 null、把高度让还给内容区。**它不再承载会话数量与连接状态**（2026-11-06 对齐修复）：`共 N 个会话` 挪进左栏「会话 / 归档」tab（`会话 36 · 归档 2`，两态计数由 `store.sessionCounts` 提供，因为 `sessions` 一次只装当前视图那一态）；WS 连接态收进左栏 footer「设置」那一行的右端（`.fg-wsdot`：正常只有一枚静息绿点，连接中/断开/未连接才补一行小字）。原因是这些信息原先压在横跨整个窗口的底条里，与上方左栏 288px、主区不同列，永远对不齐。WS 状态只此一处（左栏 footer），底条不再重复。
- **设置（左栏一个按钮 + 一体式弹窗）**：设置是**同一配置面下的若干动作**，不是并列的内容面——所以它既不是路由页，也不进左栏主导航，更不再有"左栏二级菜单 → 每节一个小弹窗"的两跳：左栏左下「设置」（`.fg-rail-footrow` 里唯一一项，`fg-navitem` 同一套 hover/选中底、与主导航图标同一条竖线）**点一下直接开弹窗**。弹窗（`SettingsDialog`，`centered` + `destroyOnHidden` + `footer={null}`，宽 1120、高 `min(64vh, 640px)`）自己分两列：左列 `.fg-setdlg-nav` 是分节列表（208px，图标 18px + label 13px + hint 11px 两行，命中态 = `selectedBg` 底 + `accent` 文字/图标（与其他菜单项同一套；不再靠 600 字重强调）），右列 `.fg-setdlg-body` 是当前节内容（各节自己出 `.fg-setsec` = 顶部说明 + 右侧动作 + `.fg-setsec-card` 白卡），两列各自滚动、**换节时弹窗高矮不变**；≤920px 分节列表横过来当顶条。**不换页、不改 URL**：从任何页面都能就地改设置、分节之间一步切换；旧深链接（`/settings`、`/settings/agents`、`/agents`）由 `SettingsDeepLink` 打开弹窗对应分节后落回问答页。现有四节：**通用**（工作区目录白名单）、**模型**（供应商 / 模型清单 / 默认模型 / 生效结论）、**命令规则**（哪些命令可直接执行 / 必须确认 / 禁止）、**关于**（产品名 / 核心版本 / 产品身份）。新增一节只改 `console/settingsView.tsx` + `SettingsDialog` 里的一行渲染。理由与 workdsh 一致
- **管理页（PageContainer + CardToolbar / SectionHeader 工具行）**：`PageContainer` 只出「标题 + 描述 + 白卡外壳」——**页头不带工具**；搜索 / 新建 / 筛选等页面工具下移到白卡顶部的 `SectionHeader` 工具行：左侧是「模块名 + 计数 chip + 说明」的一个语义组，右侧 `spacer` 撑开留白后放 `tools`（搜索/新建）再跟「刷新」。**模块名与工具分层、同一条基线对齐**，不再把"标题 + 刷新"挤成一条细线（这是 v2.0 的版式纠偏）。列表用行 + hairline 分隔，行 hover 出浅底 + 右端动作；ID 与小号码用 mono + tabular-nums。工具行统一收口在两个组件、同一套 `.fg-toolbar` 尺寸：
  - `capability/shared.tsx` 的 **`SectionHeader`**：有「我的 X」小节的页面（专家 / 技能 / 连接器）——左侧标题组 + 右侧搜索/新建/刷新；
  - `components/CardToolbar.tsx` 的 **`CardToolbar`**：没有小节标题的页面（用量 / 定时任务 / 资料库）——左侧放筛选（时间范围 / 分类分段）或计数标签，右侧放动作。**不要给这种页面单独开一条只有按钮的工具栏**（左侧会整条留空）。
  两个组件结构同源，视觉一定一致。`PageContainer` 的 `extra`（页头右上）从此不再承载页面工具。

## 4. Do's and Don'ts

### Do
- 改设计值：先改 `packages/ui/src/theme.ts` 的 `tokens`，再同步本文件 front matter（两处一致）。
- 引用颜色一律 `tokens.*`（`import { tokens } from '@ark-mate/ui'`）或 antd `theme.useToken()`。
- 状态/语义展示用 `@ark-mate/ui` 的 `sessionStatusMeta / agentStateMeta / Dot / Pill`。
- 主操作按钮用**品牌实心**（底 `tokens.accent` + 字 `tokens.onPrimary`，经 `themeConfig.components.Button` 全局生效）；次按钮 / 图标按钮用白底 + 可见描边（`borderStrong`），hover 软底 + 抬边到 `borderHover`、字保持 `text`；危险按钮静息中性边 + 红字、hover 才染红。页面表面近白冷灰阶 + 白卡 + 极细 hairline。
- 可点控件（输入框 / 按钮 / chip / 分段控件轨道）的边界与底色必须与所在表面拉得开：控件边用 `borderStrong`，不要用 hairline `border`；填充底用 `softFill` / `surfaceActive`，不要让控件靠"白的微妙差异"表达状态。
- 数据列表优先用「行 + 细分隔线」（更高密度、更克制），而非卡片网格。
- 品牌 mark 只用 `accent` 实心 + `onPrimary` 字（`ark` 三字），不要再引渐变或第二个品牌色。
- 输入卡用 `radiusComposer` + `shadowComposer`；其他卡片仍禁止加阴影。

### Don't
- 禁止在业务代码硬编码色值（包括 `rgba(...)` 品牌投影、antd 组件上的字面 hex）。
- 禁止新增 css 文件、禁止在页面散落重复的 `<style>`。聊天的交互态 CSS（`:hover` / `::before` / `@keyframes` / `@media`）**唯一收口在 `packages/console/src/console.css.ts`**，由 app.tsx 注入一次（对齐 ark-iam「结构级微修正统一收口」的哲学；此文件是既含 hover 也含 keyframes 的“单点样式单”）。
- 不要把聊天正文样式搬进 antd（bubble/think/tool 卡为控制台专属自绘组件，`Transcript.tsx`）。
- 删除无引用 import，避免 `noUnusedLocals` 报错。
- 不要给任何非浮层元素加阴影（唯一例外已在 §1 登记：输入卡）。

## 5. 已知差距（与 ark-iam 相同问题）

1. `console.css.ts` 以字符串承载少量交互态 CSS（hover/keyframes/媒体查询），antd/类库无法用 inline style 表达，属既定偏差并在 §4 登记。
2. `theme.ts` 与 DESIGN.md front matter 为人工同步，暂无 lint 强制一致。
