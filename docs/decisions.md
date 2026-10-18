# 决策登记（decision registry）

> 本文件是 ark-mate **定位口径与关键决策的唯一事实源**。`AGENTS.md`、`README.md`、`internal/archtest`
> 的守卫注释与源码注释里凡引用「决策 vNN」处，均以本文件为准；`docs/roadmap.md` 只排期，不改口径。

> **本文件由旧文档 `docs/design-and-roadmap.md` 的文首决策块原样迁移而来**（2026-10-16 文档重建）。
> 决策正文**逐字未改**，只有编排上的增补：本文的索引、编号说明、以及 v24 条里一处外部产品名的中性化
> 措辞。旧文档的正文（§0–§7，描述的是 2026-09 的状态）已整体删除，历史留在 git 里。

## 怎么读这份登记

- **决策一旦登记就不再回改正文**。后来的推翻以**新条目**表达，新条目标明它取代了哪一条，旧条目原样
  保留——「当时为什么这么定」与「现在为什么改了」是两份证据，删掉任何一半都会让下一次推翻失去依据。
- **每条决策的必要组成部分是「代价如实写」**。本项目把「这次换来什么、失去什么」当成决策正文的一部分，
  不是事后补记。某条看不到代价，说明该条还没写完。
- **v2–v6 标题里的「本节以下均已按此更新」**指原设计文档的正文；本文档不含那段正文，该短语保留是为了
  不改动历史条目。

## 编号说明（有洞，且与数据模型版本号**不同源**，不要混用）

两套编号在旧文档里并行用了很久，这是最容易踩的一处：

- **决策号（v2…v27）**：本文件的条目编号，登记「口径与形态」的变化。
- **数据模型版本号（v1…v27）**：`backend/internal/store` 的迁移版本（`migrate_vNN.go`），登记 schema 的变化。

两者**大部分不同步**。已知的对应与空缺照实登记，不做补齐式改写：

| 决策号 | 涵盖的数据模型版本 | 说明 |
|---|---|---|
| v12 | 数据模型 v15 | 会话词汇统一 + 主键前缀化 |
| — | 数据模型 v16 | 表/列/索引命名收口，**未单独登记决策条目**（设计见 `.dsh/docs/specs/2026-10-11-data-model-v16-design.md`） |
| v13 | 数据模型 v17 | 事件身份与位次分离 + runtime 词汇收口 |
| v21 | 数据模型 v21 / v22 / v23 | 三步一个迁移窗口，各自独立可提交与回滚 |
| v24 | 数据模型 v24 / v25 / v26 | 同上（provider/model 身份与命名空间收口三步） |
| v29 | 数据模型 v29（原生权限模式进 `ConnKey`） | **不相干**：决策 v29 是 L2 的逐条决策，数据模型 v29 属 P1-f 下半之二；`docs/unimplemented.md` 里「v29 起模式进 `ConnKey`」说的是**数据模型**那一版，不是本决策 |

因此 **v14–v17、v22–v23、v25–v26 这几个决策号是空的**：它们被同期占用的数据模型版本号挤掉，
不要去找对应的决策条目。

## 索引

| 决策 | 标题 | 状态 |
|---|---|---|
| v2 | 实时通道仅 WebSocket + 无跨 agent 协作 | 仍有效（② 的「多 runtime 选择切换」由 v8② 收窄为「接边有界」） |
| v3 | 定位边界：能力不重造 / 数据管理是平台职责 / 单机单操作员 | 仍有效（三条红线的锚点） |
| v4 | 管理面范围校准（权限策略位 / 会话数据管理面 / 会话态与 runtime 态分离 / P1+ 数据契约） | 仍有效 |
| v5 | WS 载荷契约校准（五处） | 仍有效 |
| v6 | 面板「发起问答」交互校准 | 部分被取代（①「会话绑底座不可中途换」仍有效；②③ 的交互形态随界面改版落地，措辞已过时） |
| v7 | 定位调整 + 项目改名 `ark-forge` → `ark-mate` | 仍有效（② 改名与仓库结构；① 的主次叙述由 v8① 取代） |
| v8 | 两层结构 + 接边有界 + 聚焦桌面端 + 口径统一 | 仍有效（② 的「两个一等适配」由 v18 修正；④ 的「开发态含 HMR」由 v10 取代） |
| v9 | 交付形态收敛：核心不自带资产，删 `go:embed` 与单二进制 | 仍有效（① 的「唯一 HMR 循环」由 v10 取代） |
| v10 | 开发靶与交付靶共用同一份产物 | 仍有效（③ 的「要手动刷新」由 P6-A 的自动整页刷新取代；模块级 Fast Refresh 仍是长期项） |
| v11 | 能力面收窄到「有消费者」的集合 | 仍有效 |
| v12 | 数据模型 v15：会话词汇统一 + 主键前缀化 + 一次性破坏性重建 | 仍有效 |
| v13 | 数据模型 v17：事件身份与位次分离 + runtime 词汇收口 | ① **部分被 v21 取代**（全局位次 → 会话内位次；`events.id` 与身份守卫保留）；④ runtime 词汇收口仍有效 |
| v18 | 底座接入面收口：第二个内置适配下线 + adapter 层补完 | 仍有效 |
| v19 | agent 概念整个并入 runtime + 数据目录布局 | 仍有效 |
| v20 | 凭据去 vault 化：密钥进库 + 明文镜像 + 四层回落 | 仍有效 |
| v21 | 数据模型 v21：位次回归每会话 + 轮次成为实体 + 消息白名单投影 | 仍有效（**推翻 v13① 的全局位次**） |
| v24 | provider/model 的「身份与命名空间」收口（数据模型 v24/v25/v26 三步） | 仍有效（② 的「候选照旧全量下发 + 标注置灰」由 v27 取代） |
| v27 | 模型下拉只出现「设置」里配置过的模型 | 仍有效（**推翻 v24② 的「标注 + 置灰」**） |
| v28 | WorkBuddy 对齐（2026-10-16）：轮次状态成为服务端事实 + 工作空间默认值 + 队列三动作 + 超时转 pending；**五条刻意偏离参照产品** + 一条长期纪律 | 仍有效 |
| v29 | L2：未实现清单 §2 十四条逐条决策（关闭 11 条 / 保留排期 3 条） | 仍有效 |

## 正文

> **决策变更 v2（本节以下均已按此更新）**：
> ① **实时通道仅 WebSocket**——「REST 命令 + WS 事件推送」为唯一实时形态（codeg 同款），
>    **不设 SSE 出口**：既有 SSE 端点/适配器代码随 M0 一并移除，事件流五条范式（§2-3）不变，仅换载体。
>    理由与取舍见 §4.6。
> ② **无跨 agent 协作**——ark-mate 只做「一次会话/一次运行**指定一个 agent runtime**」；
>    多 runtime 的意义 = 注册表 + 按会话/运行选择与切换，**不做** @委托/子 agent/并行协作（codeg 的
>    delegation 相关内容从路线中移除，见 §5.4 矩阵更新）。

> **决策变更 v3（定位边界，2026-09-04；本节以下均已按此更新）**：
> ① **能力不重造**——skills/MCP/推理等「运行时能力」一律**直接复用底层 runtime 的原生实现**
>    （skill 执行、MCP 工具调用等归 runtime），ark-mate **不实现**任何 agent 运行时功能本身。
> ② **数据管理是平台职责**——平台只做三类数据的**管理层**：会话数据（session/run/wire transcript、
>    M2 起落库）、skill 数据（技能包存储/分发/启用的数据面）、MCP 数据（服务器定义/注册/注入面）。
> ③ **单机单操作员（无多人协作）**——当前**不做**多用户/团队共享语义：M3 的「基础鉴权与多用户」取消、
>    P3+ 聊天/webhook/IM 远程通道位从路线移除；daemon 面向单操作员、默认仅监听本机，多会话并发与
>    runtime 切换仍保留。对外暴露时的「单操作员 token」仅在确有需求时最小评估（当前不排期）。
> ④ 无跨 agent 协作（决策 v2②）与无多人协作（v3③）是两条独立边界，叠加后平台完全面向
>    「单个操作员 · 一次运行单 runtime」。

> **决策变更 v4（管理面范围校准，2026-09-04；基于对 iOfficeAI/AionUi 的只读调研，本节以下均已按此更新）**：
> AionUi 是**应用型** Agent 工作台（Electron/Web 壳 + 外部 aioncore(Rust) 引擎 + ACP 接入外部 CLI
> agent），与 ark-mate 同为「薄控制面 + 外部宿主内核」结构；其「数据/配置管理层」做得厚，可整体当
> 蓝本。校准结论（执行与协作边界不因参照改变，仍守 v2② / v3③ / 能力不重造）：
> ① **权限策略位**（M2 纳入）：权限请求 = 数据管理面——语义收敛 allow/reject × once/always，确认
>    通道与消息流解耦、策略记忆落库（现状：`acl.AllowPermission` 布尔回调默认一律拒绝，无 confirm
>    通道、无记忆）。
> ② **会话数据管理面做厚**（M3 纳入）：侧栏分组读模型 + 游标（置顶/项目目录/时间线）、软归档切面、
>    全局消息搜索、批量导出；usage 按「per-turn 明细（input/output/thought/cached）+ 会话快照」粒度
>    落库（`runs.usage_json`）。
> ③ **会话态与 runtime 态分离**（M3）：`session.status` 与「连接 runtime 态（ensure/restart/cancel/
>    lease + pending_confirmations + can_send）」分开建模；会话 = 外部会话镜像（remote_session_id 锚点），
>    连接可被杀后重 spawn 续会话；daemon 侧进程注册表 JSON 兜底回收孤儿进程。
> ④ **P1+/P2 数据契约具体化**：automation 先定 cron 数据契约（schedule at/every/cron+tz × target/
>    message × execution_mode existing|new_conversation × state{next_run,retry}，执行归 runtime 后随
>    P1 排期）；MCP 位数据模型采用判别联合 stdio(command/args/env)/sse/http/streamable_http(url/headers)
>    + toggle/test-connection/OAuth/original_json 保真 + per-agent 子集 + 会话启用集「仅建会话时注入」；
>    skills 位最小闭环 = 注册与目录管理（import/scan，与物化下发分离）+ 会话级技能快照 + 聊天头指示器。
> 参照细节与「不借鉴清单」（内置引擎/Team 协作/远程通道/扩展 Hub 生态）见 §5.8。

> **决策变更 v5（WS 载荷契约校准，2026-09-04；基于对 AionUi 的 WS 流式契约调研与 acp-go-sdk v0.13.5
> wire 字段核验，本节以下均已按此更新）**：
> 传输层不动：`seq / agg / attach / snapshot-重放` 契约保留（AionUi 用「msg_id 幂等 + 后端 DB 权威补拉」
> 替代游标——其事件流只是渲染消息、权威在 DB；ark-mate 事件流是事实源，不降级）。
> 载荷语义层校准五处：
> ① **消息分组键透传**——text/thought/user 增量 payload 增**可选** `msg_id`（ACP unstable messageId，uuid；
>    runtime 未实现则为空）；前端并入同 `(kind, msg_id)` 文本段（跨工具穿插可归并），无 msg_id 回退
>    「同 kind 尾拼接」。
> ② **工具事件补内容**——`session.tool_call/tool_update` payload 增**可选** `kind`（ACP ToolKind）/
>    `input`/`output`（rawInput/rawOutput）；状态沿用 ACP 四态 pending/in_progress/completed/failed
>    （`acl.ToolStatus*`）。
> ③ **usage 载荷字段化**——`session.usage` 载荷定死 per-turn 明细 shape（与 M3 `runs.usage_json` 同源，
>    见 §4.5），turn 结束随 `PromptResponse.usage` 填实；无数据时仅发占位帧（现状）。
> ④ **确认/运行态事件契约预留**（M2 起发布，先冻结）——`session.permission_request`（挂起态：
>    call_id/title/options[]，与留痕事件 `session.permission` 分离，对应 v4① 权限策略位）与
>    `session.runtime`（phase/pending_confirmations/can_send，对应 v4③ 会话态分离）。
> ⑤ **前端 seq 幂等去重落地**——store `applyEvent` 前置 `seq ≤ lastSeq` 丢弃（§4.5 承诺的幂等，此前未实现）。
> 影响面：载荷均为**可选增字段**，事件 type 枚举不变（除 ④ 预留两个）、transcript JSONL schema v1 不变
> （append-only，多出的可选键直接随行落盘）。

> **决策变更 v6（面板「发起问答」交互校准，2026-09-04；AionUi 只读调研，详见 §5.8）**：
> ① **会话绑底座不可中途换，维持现状**——AionUi 同构：会话创建即按 runtime 定型 + assistant 快照
>    （`storage.ts:154-160,237-296`），renderer 侧 conversation IPC 无「切底座」通道；「换底座 = 新开会话」。
> ② **新建会话 = 整页 `/new`（对照 AionUi /guid，无弹窗，已落地）**——hero「Hi，今天有什么安排？」+
>    助手胶囊条（选底座，含状态点）+ 内嵌输入卡（右下模型 pill **只读展示** + 圆形发送）+「试试这些指令」
>    示例；记住上次底座（localStorage，参照 AionUi lastAssistantId 持久化）。仅不照搬 assistant 人格
>    包装 / 目录与 model·权限 override 选择器（见 ③）。
> ③ **首轮前明示「模型 = 底座自身默认」，面板不设模型选择器**——2026-09-04 实测：opencode ACP 忽略客户端
>    传入的模型选择（[issue #13644](https://github.com/anomalyco/opencode/issues/13644)，与当日 big-pickle
>    默认模型限流同源）；AionUi 对 CLI(ACP) 底座同样**不传 model override**（未显式选 = 交 CLI 默认，
>    `useGuidSend.ts:140-171`）。AionUi 的 model/permission override 交互只适用于内嵌引擎/遵守 config
>    option 的后端，**不作为 ACP 底座交互照搬**（权限 mode override 留待 M2 v4①，按 runtime 支持度再议）。

> **决策变更 v7（定位调整 + 项目改名，2026-10-09；本节以下均已按此更新）**：
> ① **主体换位**——旧版把「通用底座/平台」放在**首位**、把控制台当作平台的演示面；本次把**个人 Agent**
>    放在首位，把底座能力降为**同一内核顺带提供的第二层**。技术实现不变，受众与叙事变了：面向
>    「我自己的助手」，而不是「来建一个 Agent 平台」。
> ② **改名**——`ark-forge` → `ark-mate`：仓库、Go module（`github.com/morehao/ark-mate`）、npm 作用域
>    （`@ark/*` → `@ark-mate/*`）、CLI（`mate`）、数据目录（`~/.ark-mate`）、路由前缀（`/v1/mate`）、
>    桌面 scheme（`mate-app://`）、环境变量（`FORGE_*` → `MATE_*`）。命名隐喻由「锻造所」改为「伙伴」，
>    与 v7① 的新定位一致；**干净切断，不保留兼容层**（执行计划见
>    `.dsh/docs/plans/2026-10-09-ark-mate-rename-plan.md`，授权口径见 `docs/agents/authorization.md`）。
> ③ **不变的红线**——决策 v3 的三条（能力不重造 / 数据管理是平台职责 / 单机单操作员）与 v2②「无跨
>    agent 协作」逐条不变：本次只换叙事与名字，不扩边界。「底座能力」仍指「把控制面接自己的 runtime」，
>    不是「再做一个通用 Agent 框架」。

> **决策变更 v8（定位层次重述 + 接边收窄 + 桌面优先，2026-10-10；本节以下均已按此更新）**：
> ① **两层结构**——本项目自上而下分两层：**底座层**（`mate` 内核：多 runtime 统一接入 + 会话与能力数据面
>    + REST/WS 契约 + 交付形态）与**产品层**（底座之上的**个人 Agent 助手**，面向用户、聚焦桌面端）。
>    两层职责与承诺并列成立；**不再使用**「先是产品、其次底座 / 第二重身份 / 顺带提供」这类主次句式
>    （v7① 的「主体换位」叙述由本条取代，改名与仓库结构仍从 v7②）。
> ② **runtime 接边有界**——底座支持多个 agent runtime 接入，当前为 `dsh` 与 `opencode` **两个一等适配**
   （**该口径已由决策 v18 修正**：内置适配收敛为 `dsh` 一个，**adapter 层完整保留**——本条其余部分
   〔接边有界的理由与成本清单〕一字不变）；
>    **不追求数量，不承诺接入任意 ACP 生态**。「不写代码注册新 agent / 远程 ACP 端点」作为**工程能力**
>    留在功能清单里，但**不作定位卖点、不作里程碑出口**。原因如实写：接一个 runtime 要同时过隔离表三份
>    （`backend/pkg/runtime/isolation.go`、`frontend/apps/desktop/scripts/gen-runtime-bin.mjs`、
>    `frontend/scripts/desktop-runtime-smoke.mjs`）、投影声明与实测 `Tested`、能力粒度声明
>    （`CapabilityScope`，未实测不得声明比 `process` 更细）、随包载荷与 ABI 断言——**成本在维护面，不在 adapter**。
> ③ **聚焦桌面端**——交付重心是桌面应用（macOS / Windows；随包运行时，用户机器不需要 Go / Node / 预装底座）。
>    **主线排期只推进桌面**；单二进制 + Web 面板、容器、远程 ACP 端点为**服务端/开发形态**，冻结为**维护态**
>    （保留可用、继续被测试、可修缺陷；**不加新功能、不进主线排期**）。其中**浏览器入口只是开发过程使用的
>    形态**，不列入交付面；服务端形态的界面按「自用 / 开发」对待，不承诺 UI 体验。形态清单与「维护态」的
>    精确定义（含保留项、不做项、退出条件）见
>    `.dsh/docs/specs/2026-10-10-positioning-v8-alignment-design.md` §3.1–§3.4。
> ④ **口径统一（消歧）**——**一份 UI 产物（`apps/web/dist`），两种加载方式**：开发态（vite，含 HMR；**此处的
>    "vite / 含 HMR" 已被 v10 取代——开发态同样只服务构建产物、没有 Fast Refresh**）与
>    交付态（Electron 壳；资产由壳与核心两条路径服务，**深链回退归核心**）。文档不再出现「两个端 /
>    两个并列产品」。删掉 `apps/web` 等于删掉整个控制台 UI（壳内只有 `welcome` / `boot-failed` 两张静态页），
>    且核心资产层是桌面态的承重墙（`scheme.ts` 对非静态路径一律 `proxyToCore`）——**不做单端化**。
> ⑤ **不变与澄清**——决策 v3 三条红线（能力不重造 / 数据管理是平台职责 / 单机单操作员）、v2②（无跨 agent
>    协作）、v7② 的改名与仓库结构逐条不变。**「聚焦桌面端」不等于做「桌面 IDE 外壳」**：编辑器 / 终端 /
>    文件树 / Split View / canvas 仍不做。desktop 设计的 R1「同一份产物 + 同一套契约 + 同一份数据 +
>    同一套 DoD」不变量保留——但它的第二个靶（浏览器入口）是**开发期验收靶**，不是第二个产品。

> **决策变更 v9（交付形态收敛：核心不自带资产，删掉 `go:embed` 与单二进制，2026-10-10）**：
> ① **形态清单收敛**——v8③ 列的「单二进制 + Web 面板」**整条删除**（`make all` 与仓库根 `./mate` 产物
>    不再存在）。服务端形态只剩两种：**容器**（镜像内自带一份 dist）与**自建二进制 + 外挂 dist**
>    （`go build ./cmd/mate` + `serve --web-dir …`）；两者仍是**维护态**。桌面（`make desktop/*`）与
>    浏览器开发态（`make dev`，唯一 HMR 循环）不变。
> ② **资产单源**——**核心永远不自带前端资产**：资产只有一份 `frontend/apps/web/dist`，由调用方经
>    `--web-dir` / `MATE_WEB_DIR` 显式交给核心，**没给就拒绝启动（退出码 3）**。这条把 v8④「一份 UI 产物、
>    两种加载方式」从**纪律**变成**结构**：`//go:embed` 的存在本身就是"第二份资产"，桌面核因此被明令
>    禁止带 `-tags release`——现在这个例外连同理由一起消失了。
> ③ **构建标签维度消失**——`-tags release` / `!release` 全仓不再有（平台标签 `unix`/`!unix` 保留），
>    连带 `make test/release`：它的存在理由是"带 release tag 的代码默认 `go test` 编译不到（历史上
>    `NoRoute` 的非 API 行为就这么长期没人验）"，理由随代码一起删除。**发起布的那份与开发时跑的那份
>    现在是同一个构建配置。**
> ④ **代价与不变**——失去"一个文件拷走就能跑"（需要者自行 `tar` 一份"二进制 + web/"，属分钟级操作）；
>    容器镜像体积不变（原本资产也在镜像里，只是从二进制内部挪到镜像内一个目录）；`/meta` 少一个
>    恒为 `dir` 的 `webSource` 字段（`webPath`/`webDigest`/`webBuildId` 保留）。v3 三条红线、v2②、
>    v7②、v8① 两层结构 / v8② 接边有界 / v8⑤ 逐条不变。证据与实测见
>    `.dsh/docs/notes/2026-10-10-embed-removal-notes.md`。

> **决策变更 v10（开发态的"从源码现编译"那条路取消：开发靶与交付靶共用同一份产物，2026-10-11）**：
> ① **取消源码靶**——v9① 里那句「浏览器开发态（`make dev`，**唯一 HMR 循环**）不变」中**加粗那半句作废**：
>    `make dev` 现在跑的是 `frontend/scripts/dev-loop.mjs`——**构建一次 → `tsc -b --watch` +
>    `vite build --watch --no-emptyOutDir` 重建产物 → 核心 `--web-dir` 服务同一份 `apps/web/dist`**，
>    浏览器开 `http://127.0.0.1:10001`。原来的 vite dev server（`:5173`，**从源码现编译**）被
>    `apps/web/vite.config.ts` 在 `serve` 上**直接报错拦住**（照抄参照实现 DSH 的 `rejectStandaloneServe`）。
> ② **为什么必须取消**（不是偏好，是纪律）——v9② 已经把"资产单源"从纪律变成结构，但**开发靶当时仍是第二
>    份产物**：它现编译、无 CSP、无 `build-id.txt`、antd 多打 `css-dev-only-*` 类名、还会显示 React 的
>    dev-only 警告。于是"**已验证过的功能从未在交付件形态上跑过**"——而这件事**不会让任何别的测试变红**
>    （动态实测两轮、逐场景对照见 `.dsh/docs/notes/2026-10-11-dev-delivery-parity-baseline-notes.md`）。
>    参照实现 DSH 在这一点上更彻底：它**没有**源码靶，两个入口与两条验收都打构建产物。
> ③ **代价如实写**——**当时没有 Fast Refresh 了**：改完源码要等一次增量重建（实测约 0.4–0.5 s）再刷新浏览器。
>    **2026-10-11 P6-A 把"再刷新浏览器"这一步去掉了**：`make dev` 的核心带 `--asset-watch`，盯着它自己
>    正在服务的那份 dist，**内容真的变了**才经 `GET /dev/assets/events`（SSE）通知页面整页刷新；交付形态
>    （桌面壳 / 容器 / 自建二进制）不传这个开关，那条路由根本不存在。**但"保状态的 Fast Refresh"仍未回来**
>    ——那是整页刷新，页面状态会丢；要做到 DSH 那种 `entries.reload(id, rev)` 的模块级重载，需要**运行期
>    客户端插件内核**（参照实现的 `client-hmr` 形态），仍作为长期项立项（见
>    `docs/desktop-architecture-design-20260928.md`、`.dsh/docs/specs/2026-10-11-client-plugin-kernel-design.md`
>    与 `.dsh/docs/plans/2026-10-11-dev-delivery-parity-plan.md` §4-D7）。
> ④ **不变**——"一份 UI 产物、两种加载方式"（v8④）**更强**了：现在连开发态也只有这一份；桌面仍是唯一交付面
>    （v8③）；v3 三条红线、v2②、v7②、v8①②⑤、v9②③④ 逐条不变。机器判据见
>    `backend/internal/archtest/dev_parity_test.go`（`TestDevLoopServesBuiltArtifact`，第 36 条守卫）。

> **决策变更 v11（能力面收窄到"有消费者"的集合：声明面与执行面都不再描述"曾经想做的东西"，2026-10-11）**：
> ① **执行面删五个零调用成员**——`native.{pathFor,openPath,openExternal}` 与整块
>    `platform.{setBounds,close}` 全部删除（契约 + preload + 主进程 IPC + 越权拒绝都实现得挺完整，
>    但唯一的界面（`apps/web/src`，2026-10-11 P5 起为 `packages/console/src`）**一次都没调用**；`pathFor` 只有桌面验收探针在用）。**删的理由不是省代码**：
>    一份"能跑但没人用"的执行面让 IPC 面看起来有人用，越权校验、载体判定这些代码都在为不存在的行为服务，
>    而下一个读契约的人会以为产品支持"用系统程序打开路径"。要做这类功能**按功能单独立项**（连同浏览器侧
>    的等价交互一起设计），不是从清单里往回捡。
> ② **声明面同步收窄**——`GET /v1/mate/capabilities` 从六条（`tray`/`native-dialog`/`open-path`/
>    `diagnostics-export`/`deep-link`/`update`）收窄到**两条**（`tray`/`native-dialog`），
>    连带删掉四个从没实现过的 `browserFallback`（`ui:show-path-copy`/`http:download`/`url:direct`/
>    `deployer:replace-binary`）。语义不变且更清楚：**托盘按入口形态**（桌面 `true` / 浏览器 `false`）、
>    **原生对话框按宿主事实**（两端可能都为 `true`）。
> ③ **一处死 UI 收起来**——桌面壳里「设置 → 访问令牌」不再渲染：壳的反代**覆盖**（而不是追加）客户端带来的
>    `Authorization`，在那儿粘的令牌根本不会生效。判据取服务端声明 `entries.desktop`（**不嗅探客户端**），
>    声明拉不到时照常渲染（少一块功能比多一块无效界面更糟）。
> ④ **V3 从口径变成清单**——设计文档 §7.3 新增「桌面与浏览器的差异清单」（6 项：每项给出浏览器入口的等价
>    路径 + 两侧靶），§12 的 V3 改为**清单驱动**。两份机器判据：声明的每条能力必须在界面源码（2026-10-11 P5 起为 `packages/console/src`，见 archtest 的 `uiSrcRoots()`）被真实
>    引用（确需保留的必须登记 + 写理由，如 `tray`）、差异清单每项的桌面靶必须**逐字**出现在桌面靶脚本里
>    （`capability_consumer_test.go` / `diff_list_test.go`，两条都做过反证）。
> ⑤ **代价如实写**——原契约里"桌面能多给"的四个方向（打开文件/文件夹、导出诊断包、深链接、自更新）
>    现在**明确不在产品面内**：它们从来没有实现过，收窄只是把这件事写实。要做得单独立项。
> ⑥ **不变**——"一份 UI 产物、两种加载方式"（v8④）与 v10 逐条不变；`tray` 仍是唯一允许 `browserFallback: none`
>    的纯增强能力。实测与收尾见 `.dsh/docs/notes/2026-10-11-dev-delivery-parity-baseline-notes.md` §8。

> **决策变更 v12（数据模型 v15：会话词汇统一 + 主键前缀化 + 一次性的破坏性重建，2026-10-11）**：
> ① **一个东西一个词**——平台聚合概念此前在**同一份代码**里有四个叫法（表名 `conversations`、
>    REST `/conversations`、Go 类型 `Conversation`、wire `session`）。v15 起**只有一个词：`session`**
>    （表名 `sessions`、路径 `/sessions`、Go 类型 `SessionEntity`、包名 `dtosession`/`svcsession`/`ctrsession`、
>    前端 `packages/types/src/session.ts`）。收敛判据是机器判据：`internal/archtest` 的
>    `TestNoConversationVocabulary` 在扫描面内对 `conversation` **零命中**（白名单逐条写明理由）。
>    消歧规则见 `docs/agents/vocabulary.md`。
> ② **主键前缀化**——平台铸造的 7 类 id 从裸 uuid 统一成 `<前缀>_<26 位 ULID>`
>    （`ses_`/`msg_`/`run_`/`inp_`/`wsp_`/`mdl_`/`pem_`，唯一实现 = `backend/pkg/object/objid`）。
>    ULID 的字典序 = 铸造序，因此 `messages`/`inputs` 靠 `id ASC` 兜底的两处排序语义**逐字不变**；
>    `messages`/`permission_memories` 同时消灭了 `AUTOINCREMENT`（主键 TEXT、由 Go 侧铸号）。
>    **底座透传过来的会话 id 平台侧一律加固定壳 `rt_`**（`rt_ses_…`/`rt_<uuid>`）：平台不改底座、
>    只加壳，于是"平台 id 与底座 id 形状可分"这条判据**由平台单方面定义**（底座换实现、换前缀、
>    发裸 uuid 都不影响）。包装 4 处 / 解包 3 处，调用点白名单由守卫②钉住。
> ③ **表行结构体对齐参照工程**——`internal/model` 从 583 行的单文件拆成**一表一文件**，
>    结构体一律 `<域词>Entity` / `<域词>EntityList`、表名常量 `TableName<域词>` 随实体文件走
>    （不做汇总 `table.go`）；同时删掉 `internal/store` 的 15 个实体类型别名（一个表行类型只留一个名字）。
> ④ **v15 是一次性例外，不构成先例**（用户原话：「v15 重建吧，但只有这一次，而不是一个标准 sop」）——
>    这次走**破坏性重建**（7 张表 DDL 重建 + 元数据清理，`<db>.bak-v15-<ts>` 是回退窗口），
>    **不写逐行数据迁移**。四条约束把它钉成例外：**不给内核加能力**（`migration` 结构体字段集合不变、
>    不加 `--rebuild` 之类的开关）、`DROP TABLE` 白名单、v15 的 name/backup 显式标注、
>    本决策记录 + 发布说明（守卫⑥ `TestDestructiveRebuildIsOneShot`，含 R6 的
>    「DROP 清单 ↔ 元数据清理清单一一对应」）。**下一次数据模型变更回到"有序追加 + 数据搬运"**；
>    要再次重建必须 owner 单独拍板，不能援引 v15。
> ⑤ **`models` 是唯一例外**：它不进 DROP 清单（模型定义含 `image_input` 能力声明、且**没有文件来源**，
>    丢了只能人工重建），改为在同一事务里把旧行 id 重铸成 `mdl_<ULID>` 并改指**唯一存活的引用**
>    `meta.default_model`（R7，单测 `TestMigrateV15RecastsModelIDs` 钉住，含反证）。
>    **代价如实写**：外部握着的旧 `mdl_` id 会失效（脚本/书签/诊断包），这件事没有任何 Go 测试能发现，
>    由 `APIContract` `v1 → v2` 与发布说明负责。
> ⑥ **代价**：`~/.ark-mate/mate.db` 是 `make dev` 与桌面核**共用的同一个库**，所以这次破坏同时命中
>    两种形态；既有的会话/事件/轮次/消息/排队输入/权限记忆/工作空间登记会被清空
>    （`automations.last_fired_at` 刻意保留：它是认领游标）。旧 transcript（文件名即旧会话 id）在装载期被
>    **跳过 + WARN**，处置靠 `mate transcripts prune`（不自动删）——`mate doctor` 新增一条体检项
>    把这件事变成随时可查（R3）。**不为旧数据写任何兼容或派生代码**（用户 2026-10-11 拍板）。
> ⑦ **不变**——v3 三条红线、v2②、v7②、v8①②、v10、v11 逐条不变；wire 上的 `session.*` 事件与
>    `session_id` 字段**冻结不动**（v15 只是让平台领域词追上它们）。

> **决策变更 v13（数据模型 v17：事件身份与位次分离 + runtime 词汇收口，2026-10-12）**：
> 设计全文见 `.dsh/docs/specs/2026-10-12-data-model-v17-design.md`。两件事一次做完，因为它们
> 服从同一条判据（J4：**身份与位次必须随数据走到每一个载体**）。
> （v16 的表/列/索引命名收口没有单独登记决策条目，见
> `.dsh/docs/specs/2026-10-11-data-model-v16-design.md`；本条目只记 v17。）
> ① **`events` 的身份与位次分开**——新增主键 `id`（`evt_<26 位 ULID>`，`backend/pkg/object/objid` 铸造），
>    `seq` 从主键**降为全局唯一游标**（`UNIQUE(seq)`，`idx_events_session` 仍是
>    `(session_id, seq)`）。理由：事件的**位次**会被重排（重建、压实、换库），而**身份**不能——
>    把 `seq` 当身份等于承认"同一个事件换个号就变成另一个东西"，书签与对账随之失效。
>    机器判据 = 守卫 G1（`TestEventSeqStaysTheCursor`：实体/索引/DAO 三层，含"`id` 不得参与排序"）。
> ② **两个参照实现给的是相反答案，我们取交集**：DSH 的会话事件是 JSONL `{type, seq, time, data}`，
>    **没有 per-event id**；opencode 的 `EventTable` 有 `id` 主键（`evt_` + 26 字符），但它的 `seq`
>    是 **per-aggregate 稠密序号**（`uniqueIndex(aggregate_id, seq)`，另有独立序号分配表）。
>    v17 = opencode 的**身份** + 我们自己的**全局位次**（跨会话回放、`MAX(seq)` 水位都靠它）。
>    **刻意不抄**：per-aggregate 稠密 seq、opencode 的索引命名形态。
> ③ **判据 J4 逐载体落地**（守卫 G2 `TestEventIdentityTravelsWithData`）：运行时**只有一个铸号点**
>    （`manager.publishRaw`），同一支 id 交给 store 落库、交给 bus 进帧、交给 transcript 落盘；
>    memory 装载与 sqlite 恢复都**原样**注入 bus（不再是"按行号重推 seq + `Publish` 现取号"）；
>    REST/WS 两条出口都带 `e.ID`；`mate migrate` 把 transcript 的 id 交给 store（不重铸，
>    缺 id 直接 fail-loud）；**只走实时通道的事件刻意不铸号**（`automation.run`）——于是
>    "帧上有 `id` ⟺ 它在 durable 日志里查得到"这条不变式成立。
>    修掉的是**真实缺陷**：memory 模式重启后事件身份丢失、位次被重编（transcript 写了 `seq`
>    却在装载时丢弃，且逐文件 `Publish` 让跨会话顺序由**文件名字典序**决定）——端到端判据 =
>    `TestMemoryModeRestartKeepsEventIdentity`（两个会话交错的位次，重启后逐条不变）。
> ④ **词汇收口**：`provider_session_id` → `runtime_session_id`、`runtime_hint` → `runtime_id`
>    （Go 侧 `ProviderSessionID` → `RuntimeSessionID`、`RuntimeHint` → `RuntimeID`；`objid` 的
>    `ProviderShell`/`WrapProvider` → `RuntimeShell`/`WrapRuntime`；日志键 `provider_session` →
>    `runtime_session`；前端 smoke 脚本同改）。理由：`provider` 在本仓的主流含义是**模型供应商**
>    （`providers` 表 / `ProviderID` / provider 档案），而这一列存的是"底座那边的会话 id"——
>    同一个词指两个东西。判据 = 守卫 G3（`TestNoProviderSessionVocabulary`，零命中，
>    白名单只有历史 DDL 与停在 v16 之前的夹具）。**automation 的 `target.runtime_hint`
>    刻意不改**：那是 v8 已删除的选择面留下的**拒绝键名**（`runtime_selection_removed`），
>    改名会让旧请求从"显式 40001"退化成静默忽略。
> ⑤ **transcript 格式 v2**（`ark-mate.wire-transcript.v2`：事件行新增 `id`）：v1 文件**不再可读**，
>    装载期**跳过 + WARN**，处置靠 `mate transcripts prune`（不自动删）；
>    跳过计数与"旧会话 id 形状"分开统计（`legacySchemaSkips` vs `legacyTranscriptSkips`）。
> ⑥ **迁移是"有序追加 + 数据搬运"**（回到 v15 立下的常态）：`migrate_v17.go` 是本仓**第一个
>    O(全部事件行) 的 Go 迁移步骤**——keyset 分页（`WHERE seq > ? ORDER BY seq LIMIT n`）逐行铸号、
>    对账（行数 / 最大 seq / id 去重）后才 `DROP TABLE events` + 换名 + 建索引，全在**一个事务**里；
>    **`meta.events_last_seq` 必须保留**（清掉它 → 下一条新事件从 1 起跳、撞 `UNIQUE(seq)`；
>    这是照抄 v15 清空动作的人一定会踩的一脚），由 `TestMigrateV17KeepsEveryEvent` 钉住。
> ⑦ **明确不做**：不给 `definition_revisions`/`meta`/`schema_migrations` 加平台 id（它们是
>    **审计/游标/版本**三张表，身份就是它们的自然键——`definition_revisions` 的位次由
>    `monotonicCursorEntities` 守卫单独钉）；不加 `GET /events/:id`（没有消费者之前不加端点）；
>    memory 模式的 `version` 仍由行序派生（显式非目标）。
> ⑧ **代价如实写**：事件行多一个 30 字节的 id 列 + 一条唯一索引；迁移期峰值内存为"一页 2000 行"
>    加事务内页面缓存（DB 变大时备份窗口相应变长）；`mate migrate` 导入保留 transcript 的 id，
>    但 `evt_` id 是**安装局部**的引用身份，**不保证跨机器一致**（跨库稳定的键仍是 `(session_id, seq)`）。
> ⑨ **不变**——v15 的会话词汇与主键前缀、v16 的表/列/索引命名逐条不变；wire 契约只**新增**一个
>    可选字段（`event` 帧 `{id?, type, seq, agg_id, version, payload}`），`session.*` 事件类型与
>    `session_id` 字段**冻结不动**。

> **决策变更 v18（底座接入面收口：第二个内置适配下线 + adapter 层补完，2026-10-12）**：
> 两件事一次做完，因为它们服从同一条验收口径：**接一个新底座 = 新建 `pkg/runtime/<id>/` 目录 +
> 一个 `<id>Spec()` + 挂端口实现，不需要改任何框架/内核文件**。这次把最后一个"回内核改代码"的
> 入口堵掉，同时把已经能承载第二个实现的机制**补完**——而不是把 adapter 层一起删掉。
> 编号说明：本条沿用本次收口在 `AGENTS.md`／archtest 守卫注释／前端验收脚本里**已经使用**的标号
> **v18**；v14–v17 号已被数据模型 v15 / v17 与 v16 的命名收口占用（v16 未单独登记决策条目，见
> `.dsh/docs/specs/2026-10-11-data-model-v16-design.md`）。
> ① **下线第二个内置适配**——`backend/pkg/runtime/opencode/`、`frontend/apps/desktop/runtime/opencode/`、
>    桌面随包载荷里的 opencode，以及 `Dockerfile` / `docker-compose.yml` / `Makefile` / `.env.example`
>    里的 opencode 选项全部删除。内置适配从两个收敛到一个（`dsh`），内置种子 agent 同步收敛为
>    `dsh` 一个（`builtinAgents`，`backend/internal/manager/agent_state.go`：每个内置 runtime 一行）。
>    判据见 ④ 的 `TestBuiltinAgentSeedsMatchBuiltinSpecs`。
> ② **下线不等于把接入机制删掉：adapter 层是这次被补完的对象**。验收口径是上面那句（"新目录 +
>    `<id>Spec()` + 端口实现，不动框架"），而它是在**下线之前**先按行为等价的抽取落实了两块——
>    当时 opencode 还在、全绿，它正是证明"这套机制能承载第二个实现"的那一个：
>    - **隔离表进声明**：`Spec.Isolation []IsolationVar`，三种取值 `IsolationPath` /
>      `IsolationLiteral` / `IsolationEmpty` + `LauncherFallback`；`backend/pkg/runtime/isolation.go`
>      不再认识任何具体底座，只做机械投影（`IsolationEnvFor` / `projectIsolation`）。
>    - **原生权限进端口**：`NativePermissionAdapter`（`RegisterNativePermission` / `NativePermission(id)`，
>      `backend/pkg/runtime/native_permission.go`），签名只收原语
>      `Env(mode, def string, tools map[string]string) (env []string, desc string)`；内核
>      `backend/internal/manager/native_permission.go` 只做"模式 → 取端口 → 转发"，动作取值与工具键
>      一概不归一（那是该底座的词表）。
> ③ **模型取值进端口（最后一块，也是这次收口最关键的一处）**：此前内核里活着一句 `modelValueFor` 的
>    `switch runtimeID { case "dsh": …; case "opencode": …; default: 报错 }`——那是**内核认识具体底座**的
>    教科书形态（它没有 import 任何 adapter 包，所以上一条 import 守卫抓不到它）。现在改成查注册表
>    **第三张可执行端口表** `ModelValueAdapter`（`RegisterModelValue` / `ModelValue(id)`，
>    `backend/pkg/runtime/model_value.go`）；未登记该形态的 id 如实返回 `model_value_unsupported`，
>    **不替它猜形状**（`backend/internal/manager/model_list.go` 的 `modelValueFor` 只剩分发）。于是
>    `Registry` 的形状固定为 `map[string]Spec`（**声明半场**）+ **三张可执行端口表**
>    （`Syncer` / `NativePermissionAdapter` / `ModelValueAdapter`；可执行对象不进 `Spec`）。
> ④ **新增守卫（这次收口的机器判据）**，三条挂在
>    `backend/internal/archtest/knowledge_dependency_test.go`：
>    `TestInternalDoesNotImportRuntimeAdapters`（`internal/**` 不得 import `pkg/runtime/<adapter>`）、
>    `TestNativePermissionModeLiteralsMatchWire`（原生权限 `native_mode` 取值在内核 wire 契约与
>    adapter 侧逐字一致）、**`TestInternalHasNoRuntimeIDSpecialCase`**（`internal/**` 剥注释后不得出现
>    任何内置适配 id 的 `case "<id>"` / `== "<id>"` / `!= "<id>"`；判据的 id 集合取自
>    `runtime.AllBuiltinSpecs()`，所以接新底座时不假红——它钉的正是 ③ 那个 `switch runtimeID`，
>    而那段代码**活过了此前全部守卫**）。另有 `backend/internal/manager/agent_state_test.go` 的
>    `TestBuiltinAgentSeedsMatchBuiltinSpecs`（内置种子 agent 与 `AllBuiltinSpecs()` 三方向比对，
>    含 `id == runtime` 这条形态不变式）。
> ⑤ **键名收口**：`OPENCODE_BIN` → `RUNTIME_BIN`（含 `Makefile` 内部的 `RUNTIME_BIN_FROM_FILE` /
>    `RUNTIME_BIN_ABS` / `RUNTIME_BIN_SOURCE`），`Makefile` 与 `.env.example` 同改。**语义不变**：
>    它是"显式覆盖**构建期固定的那个**底座的可执行文件"，**在任何构建上都生效**（转发 shim 名随
>    `$(RUNTIME)`）。保留的**不是**兼容别名——旧键名 `OPENCODE_BIN` 不认。
> ⑥ **`AllBuiltinRegistry()` / `AllBuiltinSpecs()` 的语义不变**（`backend/pkg/runtime/build.go`）：
>    产品装配走 `ProductRegistry`——**只注册构建期选中的那一个**，未注册即**启动失败**（fail-closed）；
>    `AllBuiltin*` 只服务测试与诊断（医生与能力矩阵守卫必须看到**全部**适配面，否则只注册一个会让
>    别的适配的守卫**静默空转**）。
> ⑦ **刻意保留的（不要读成"仓库里已零命中"）**：
>    - **宿主环境面**：`frontend/scripts/lib/decoy-bin.mjs` 的 `DECOY_NAMES` 里**留着** `opencode`，
>      `desktop-ui-acceptance.mjs`（`~/.config/opencode`）、`desktop-no-host-acceptance.mjs` 与
>      `desktop-skill-projection-acceptance.mjs`（`~/.local/state/opencode`）保留"用户真实目录零变化"
>      判据。理由：这些判据的语义是"**宿主机上装过什么与本产品无关**"，不是"本仓随包带哪些"。
>    - **禁词表**：A9 的 `TestInternalHasNoRuntimeConfigLiterals`（`knowledge_boundary_test.go`）里
>      `opencode.json` / `OPENCODE_CONFIG_CONTENT` / `opencode_json` **留着**，`settings_copy_test.go`
>      的禁词表也留着 `opencode`。理由：它们拦的是"这类字面量出现在内核就是知识回流"，删掉等于
>      **拆掉一层保护**。
>    - `dev_parity_test.go` 里"不得出现写死底座名的回显"仍然**逐字**拦两个历史值（`底座  dsh` /
>      `底座  opencode`）——它们拦的是同一类回归。
>    - **`IsolationEmpty`（空值）不得被翻译成回落路径**：2026-10-10 有过 EISDIR 事故（把
>      `$MATE_HOME/` 当一个目录传给配置变量，底座把它当文件读）。桌面启动器
>      `frontend/apps/desktop/scripts/gen-runtime-bin.mjs` 的 `if (sub === '')` 空值分支与守卫
>      （`isolation_parity_test.go` 里"空值分支不得出现 `MATE_HOME`"的断言）都留着；今天没有任何
>      launcher 用到空值，但那是**机制**，不是为某个底座写的。
> ⑧ **代价如实写**：内置适配只剩一个，意味着"第二个实现"提供的回归保护**弱了一层**。补法不是
>    喊一句"adapter 层还在"，而是 ②③ 的端口表 + ④ 的三条守卫 + 一个**合成的第二适配**：
>    `backend/internal/testutil/runtimetest`（id = `rtx`，刻意不在内置清单里）的
>    `Register(reg, launch)` 把 `Spec` 与三张端口表一起登记上；manager 侧最重要的那条端到端判据
>    `TestSyntheticAdapterIsFirstClass`（`backend/internal/manager/synthetic_adapter_test.go`）就挂在它
>    上面——断言：不指定 runtime 的 agent 回落到构建期固定那个、`modelValueFor` 对合成底座返回它
>    自己声明的形态、对未登记 id 报 `model_value_unsupported`，且这些 env 真的落到了子进程。
>    **顺带说明**：`pkg/runtime` 的测试里另有一个自造的 `rt-alt`（`isolation_test.go` /
>    `session_runtime_test.go`），因为 `pkg/` 不能 import `internal/`。
> ⑨ **存量数据：读面宽容 + `mate doctor` 存量体检（删掉适配不会让存量消失）**。用户 2026-10-12
>    拍板的口径是"**不做破坏性迁移**，但存量要能被看见"；这次补上的是后半句——在此之前它是
>    **完全静默**的：`mate doctor` 里逐底座采事实的那两段（`collectRuntimeModelFacts` /
>    `collectSensitiveFiles`）都按**注册表**枚举，适配一删，那个底座在报告里就整段消失
>    （报告一切正常，而磁盘上的东西一个没少）。
>    新增 `backend/internal/manager/retired_runtime.go`（采事实，只读）+$
>    `backend/internal/cliops/doctor_retired.go`（排版）。判据是「**在不在册**」，不是
>    「它是不是某某」——两条理由：内核不许认识任何具体底座（写死 id 会被 ④ 的
>    `TestInternalHasNoRuntimeIDSpecialCase` 判红），而写死也只能看见**这一次**下线的那个、
>    下一次下线又要回内核改一处。在册集合由**调用方**给出（`AllBuiltinSpecs()` ∪ `runtimes.json`
>    的远程端点，见 `knownRuntimeIDs`）并**印在报告里**：于是"判据给错了"与"真的没有遗留"在
>    报告上可分。
>    四面事实：① 磁盘 `<MATE_HOME>/runtime/<不在册 id>/`（列出顶层条目——里面可能就是那个底座的
>    **原生凭据文档**，即明文密钥，与 `mate.db` 同等看待；平台**不自动删**）；② `sessions.runtime_id`
>    指向不在册 id 的会话（**读得到、发消息才报 `runtime_not_found`**——"表面正常、用到才炸"正是
>    体检该提前说出来的形态）；③ skill 定义 `runtimes[]` 里的不在册候选（候选集为空 ⇒ 永不投放，
>    而界面上它看起来只是一条普通技能）；④ 投影覆盖键（`<runtime>:<kind>`，形状合法所以配置解析
>    不报错，但它永远匹配不上）。
>    **一律 warn、绝不 fail**：存量是**事实**，不是"事实与声明不符"；判红会把用户逼向"删数据"这
>    一个动作，而那正是明确不要的。三处"读不到"（没有可读的持久层 / 隔离根读不动 / 在册集合为空）
>    各自如实记"无法校验"或"判据失效"，**不许**当成"没有"——那是这一段最坏的假绿形态。
>    隔离根位置**从在册声明推**（每个 `Spec.Isolation` 里 `IsolationPath` 条目的父目录去重，换根时
>    判据自动跟上）∪ 历史约定 `<MATE_HOME>/runtime` 兜底：已下线的那份声明已经不在了，它的落点
>    推不出来，那一条是**约定**、改约定时要跟着改。
>    端到端取证（2026-10-13 实跑）：私有 `MATE_HOME` 下放一份已下线底座的 `runtime/<id>/`（含原生
>    凭据文档）+ 库里一条指向它的会话与一条 skill，`mate doctor --web-dir …` **退出码 0**
>    （0 fail / 8 warn，四面条目逐条点名）；把这些痕迹删掉后四条全 `ok`。
> ⑩ **不变**——wire 契约与事件类型、数据 schema、REST 路由与错误码、`session.*` 词汇、v15/v16/v17
>    的会话与命名收口、**构建期固定 runtime**（用户不能选）这条产品约束、`ProductRegistry` 的
>    fail-closed 语义，逐条不变；本次只**新增**上面这些端口与守卫、**删除** opencode 适配面、
>    **改名**一个构建期覆盖键，没有别的改动。

> **决策变更 v19（agent 概念整个并入 runtime；用户不可自定义 + 数据目录布局参照 DSH，2026-10-14）**：
> 三件事一次做完，因为它们是同一个判断的三个面：**agent 这个概念在本仓没有独立身份**。
> ① **用户永远不能自定义 agent**（本轮的直接触发）。此前用户面把 agent 当"可配置的资源"，而它的
>    每个字段要么是**派生事实**（`is_default` / `session_count` / `default_model`），要么是**平台事实**
>    （`permission_policy`、`provider`）。撤掉自定义面之后没有剩余字段可写——所以这不是"收紧权限"，
>    而是这个概念本身不该以"资源"的形态存在。
> ② **agent 并入 runtime**。`builtinAgents` 只有一行且与 `dsh` 的 Spec 一一对应（`id` 就是 runtime id），
>    `defaultAgentID()` 本来就返回构建期固定的那个 runtime id；会话已经同时带 `agent_id`（归属）与
>    `runtime_id`（执行事实，v17 名），事件 payload 甚至两个都带。于是"哪个 agent 在跑"这个问题在仓里
>    有**两个答案指向同一个东西**——收成一个：只留 runtime。
>    - **撤掉**：表 `agents`（v19 迁移 drop）、`DefinitionKindAgent`、`dtoagent`/`ctragent`/`svcagent`、
>      `/agents/*` 全部路由（含 `:id/default`、`:id/start`、`:id/stop`、`:id/capabilities`、
>      `:id/diagnostics`）、`mate agent import|export`、装配期 agent 目录导入、`/library?kind=agent`、
>      `mate mcp bridge --agent`、`gateway.Scope.AgentID`、`automations.target.agent_id`、
>      用量 `group_by=agent` 与 `?agent_id=`、事件 payload 的 `agent_id`、`sessions.agent_id` 列，
>      以及五个错误码（`agent_not_found`/`agent_exists`/`seed_agent_readonly`/`reserved_agent_id`/
>      `default_model_is_derived`）。
>    - **替换而不是新增**：诊断从 `/agents/:id/diagnostics` 改为 **`GET /runtime/diagnostics`**；
>      原 `/agents/:id/capabilities` 的能力面**折回**既有的 `GET /capabilities`（不新增平行面）；
>      `GET /meta` **新增** `runtime{id,name,version}`——`/meta` 是既有的装配事实出口，加字段比加路由
>      便宜（没有路由表churn，也不给用户新的选择面）。
>    - **`runtime_id` 的立场反转，记在此处**：v17 时 `internal/manager/service.go` 的注释写着
>      "P0 起不再记录 runtime 身份"；本轮反过来——runtime 是**唯一的身份事实**，那句话随本批删除。
> ③ **数据目录布局参照 DSH**（`packages/util/home-paths`；抄四条、不抄三条）。抄的是：① 单根 +
>    环境变量 + **显示名**（默认数据目录在人类可读输出里渲染成 `~/.ark-mate`，绝对路径只留机器面
>    `--json` / `/meta`）；② 可丢与不可丢分家——迁移备份收进 **`backups/`**（本机实测根下曾累积 7 个
>    `mate.db.bak-v12…v17`），而**老布局的备份照旧是有效退路**：`mate doctor` 如实报"遗留"并给处置
>    建议，**绝不自动搬移或删除**（那会改掉回退路径或直接关门）；③ 一词一目录：`agents/` →
>    `providers/`（`MATE_PROVIDERS`）——撤掉 agent 定义后那个目录只剩 `providers.json` 一件事——
>    并提供**一次性导入**（新目录没有 `providers.json` 而旧 `<MATE_HOME>/agents/providers.json` 存在时
>    导入一次 + WARN 指向新路径，**不删旧文件**）；④ 数据单份、安装分权。
>    **不抄**：`profiles/<入口>/`（DSH 用它分的是**安装**而不是数据；本仓数据本来就单份，真正的痛点是
>    "两个核抢一个 `mate.db`"，那要靠单持有解决，不是拆数据）、根下 `.env` 放密钥（见 v20，我们走
>    "库 + 0600 镜像"）、`storages/`（那是它的存储后端抽象，本仓对应物是 `runtime/<id>/` 与
>    `workspaces/`，概念不同，不硬套）。
> ④ **新增守卫**：`TestNoAgentVocabulary`（`internal/archtest/vocabulary_test.go` 家族，与 v15 的
>    `TestNoConversationVocabulary` 同形）钉"被撤掉的 agent 面词汇"零命中，白名单只放**别人的原文**
>    与历史（ACP 协议自身的 agent 概念、历史迁移 DDL、停在旧版本的测试夹具）；`TestSessionIdentityIsRuntimeOnly`
>    钉会话身份只剩 runtime（实体 / DAO / 迁移 / 用量聚合四处都不得再有 `agent_id`）。
> ⑤ **代价如实写**：撤掉 agent 自定义面意味着用户不能再为不同任务准备不同"人格 + 权限组合"。这是
>    **产品选择**（底座构建期固定、能力由会话级 `capabilities` 与平台默认决定），不是能力退化；
>    反面代价是本仓少了一层"按任务切分权限"的表达位——将来若要恢复，应作为 **runtime 的会话级配置**
>    出现，而不是把 agent 概念请回来。

> **决策变更 v20（凭据去 vault 化：密钥进库 + 明文镜像 + 四层回落，2026-10-14）**：
> 本条**推翻 v13 的硬不变量**「明文密钥结构上进不了库」，所以先写清它换来什么、代价是什么。
> ① **为什么可以推翻**：vault 的真实收益从来不是"防本机读取"——密钥与密文同目录同属主，而 dsh 那侧的
>    原生凭据文档（`<$DSH_HOME>/.credentials.yaml`，隔离根内）本来就是**明文**（`pkg/runtime/dsh/credentials.go`
>    就是写它的那一处）。vault 真正防住的是"密钥进 DB / DB 备份 / 诊断包 / 事件 / transcript"这条链。
>    既然只要保住那一条收益就够，那么加密带来的复杂度（主密钥的保管与丢失、`vault_unavailable` 与
>    "密文解不开"两类失败态、写面"vault 与 SQLite 无法同事务"的补偿逻辑及其 H4 故障注入面）
>    就是**可以省掉的成本**——这正是用户拍板的理由（"虽有加密但成本有点高"）。
> ② **新形态**：`providers.key` **列**（provider 密钥进库）+ 新表 **`mcp_secrets(ref, value, revision,
>    updated_at)`**（一个 MCP server 可以有多个 `secret_env`/`secret_headers` 引用，一列装不下；而
>    `mcp_servers.body` 会进审计表与导入导出载体，明文不能进那儿）+ **`<MATE_HOME>/credentials.json`
>    作为双写镜像**（严格 `ref → value` 映射，0600，原子写）。
> ③ **权威与冲突：DB 权威，文件是可重建镜像**。写路径先落库、再双写镜像；镜像写失败只 WARN + 记进
>    `problems[]`（可从库重建，不阻断也不回滚）；库里没有而文件里有 → **不自动回灌**，`mate doctor`
>    报出并指路导入。memory 模式（没有库）文件即事实源，那时写失败就是整笔失败。
> ④ **四层回落**（照 DSH `packages/credentials/credentials-local` 的分层与它自己写明的理由）：
>    **继承环境（只读，最高）> 托管文件 > `<cwd>/.env` > `<MATE_HOME>/.env`**。继承环境最高是因为
>    "这次运行的显式意图"不能被静默遮蔽（`DEEPSEEK_API_KEY=… mate serve`、CI secret、容器 `-e`）；
>    托管文件高于 `.env` 是因为"面板上写的密钥"不能被一份陈旧 `.env` 顶掉；项目级高于用户级是因为
>    "更具体的位置赢"。**顺带修一个既有静默问题**：dsh 侧的实测口径是"继承环境 > 存储文档"，也就是说
>    用户导出的同名变量今天就已经赢过平台存的密钥，而平台侧的 `has_key` /「设置 · 模型」对此一无所知
>    （面板说"已设置"，生效的是另一把）——分层落地后 `describe` 能如实报 `source`。
> ⑤ **必须守住的不变量**（缺一条就是纯降级）：① 托管文件 0600 且**权限不合 fail-loud**（dsh 自己在
>    启动与重载都拒绝"可被属主之外读取"的文档，同一口径）；② 密钥绝不进事件 / transcript / 日志 /
>    错误文案 / 诊断包 / argv（`ps eww`）；③ 继续用 `redact.RegisterDynamicSecret` 登记真值；
>    ④ 导入导出**不回写明文**；⑤ `has_key` 与 `problems[]` 的语义改写为"分层里读到值 / 权限或文件损坏"。
> ⑥ **迁移**：一次性把 `vault/{providers,mcp}/*.enc` 解出来写进 `providers.key` / `mcp_secrets` 与镜像
>    文件。**搬运只读不删**（`ImportLegacyVault` 不删任何东西）：`vault/` 与 `vault.key` 留在盘上，
>    `mate doctor` 报出「已迁移，可删」并说明密钥已进库，**删除是用户的显式动作**——被撤掉的机制
>    由医生如实报出、由用户确认后自己删，这一条与「显式拒绝面必须点名被撤掉的键」同一口径：
>    静默删掉用户的目录比留着它更糟（用户无法事后确认"我的密钥到底搬全了没有"）。
> ⑦ **代价如实写（这次是真代价，别轻描淡写）**：① **DB 与其备份现在含明文密钥**——`mate.db`、
>    `backups/mate.db.bak-v*`、诊断包、任何"把库发给我看看"的动作都会带走密钥；于是"备份目录 0700 /
>    文件 0600"从建议变成**硬要求**，`mate doctor` 的敏感文件清单要把 `mate.db` 与备份一并按密钥面
>    看待。② 主密钥丢失这条故障类别消失，换成"文件权限 / 内容损坏"。③ 从"库被读走也拿不到密钥"退到
>    "库被读走就等于密钥泄露"——本机单用户桌面形态下可接受，**共享或多用户机器上不再安全**，
>    这一条要写进 `docs/deployment.md`。
> ⑧ **`<cwd>/.env` 那一层的 `cwd` 是显式输入，不是各进程各自的 `os.Getwd()`**（2026-10-14 收尾裁决）：
>    网关是**底座 spawn 的子进程**，它的 cwd（会话工作区）不是用户选的；核心是自己起的，cwd 可解释。
>    所以语义定死为「**用户启动 `mate serve` 的那个目录**」，由核心在生成 `mate mcp bridge` 条目时
>    经该条目的 `env`（`MATE_MCP_CRED_CWD`）交下去——走 `env` 而不是 `Args`，因为 `Args` 逐字进能力
>    指纹，换个目录启动就会让所有会话换一次底座代际。网关侧优先级：显式入参 > 该环境变量 > 自己的
>    cwd，**落在第三支时打 WARN**（"两个进程用了不同的 cwd"不许静默）。这条 WARN 按 M7-2 的纪律落在
>    `<MATE_HOME>/logs/<日期>/ark-mate-mcp_full.log`（网关 stdout 是 JSON-RPC 协议流，诊断不走 stderr）。
> ⑨ **已知残留（本版刻意不解决，别当成漏做）**：① `mcp_secrets` 的值**没有界面写入口**（面板上填的是
>    环境变量名），所以 doctor 在 `store:` 引用解析不到时只指路 `env:<ENV_NAME>`，**不写**"把值写进库"
>    ——写不进去的出路等于没有出路（搬运/导入写进去的值不受影响）；② `SetProviderKey` 不落审计行
>    （与 v19 的等价动作一致）；③ `UpdateProvider` 非原子（内容 CAS 与 `key` 列两条写，失败在回执
>    `problems` 里如实报，不是静默）；④ 镜像里的陈旧值在用户重填前一直躺着，解析层**拒绝**"库不确认的
>    managed 命中"，doctor 用 `missingInStore` 报出且**不自动回灌**。

> **决策变更 v21（数据模型 v21：位次回归每会话 + 轮次成为实体 + 消息白名单投影，2026-10-14）**：
> 设计全文见 `.dsh/docs/specs/2026-10-14-data-model-v21-design.md`。本条**推翻 v17 的「全局位次」半条**
> （保留 `events.id` 与身份守卫 G2），并顺带拆开另外两处"投影兼职事实"。
> ① **为什么可以推翻全局位次**：v17 用来支撑全局序的两个消费者（`MAX(seq)` 水位、跨会话回放）
>    都只服务"让 `PublishAt` 不倒退"这一件事，换成 per-agg 计数之后**跨会话全序今天没有消费者**
>    （三处落点逐个换宿主）；而保留它的代价是"单会话内的 `seq` 有洞"——用户看到的
>    「第一条消息的 seq 为什么不是 1」就是它。所以 **`events.seq` = 会话内位次**（0 起、稠密），
>    取号器拆成 `event_sequence(session_id PK, seq)`，删除 `meta.events_last_seq` 与全局 `idx_events_seq`，
>    **不新增任何全局序列列**（判据 J2：位次是位次、身份是身份）。
> ② **`runs` 从终态投影升级为实体**：`run.started` 落库即 INSERT（`id = run_<ULID>`、`status='running'`、
>    `first_seq` = 该事件的位次、`last_seq = -1` 未收口），终态只 UPDATE，幂等由
>    `UNIQUE(session_id, first_seq)` 承担（J6：不变量靠约束）。为什么必须补 `run.started` 这条事件：
>    否则**进行中的轮在事件流里没有痕迹**、`runs` 行也不可从事件重建（破坏 J7）。刻意中断的轮
>    **永远留在 running**（会话侧记 `interrupted`）——既没成功也没失败，伪终态会污染失败轮列表与
>    重试入口。
> ③ **`messages` 改白名单投影**：只有 `session.user_message` / `session.message` 产生行，空正文行
>    丢弃（与 DSH `deriveEventMessage` 的 `default → null` 同一判据），补 `ordinal`（会话内 1 起、
>    `UNIQUE(session_id, ordinal)`）与 `kind`（扩展位，当前**恒空串**——J5：不留"以后可能有用"的
>    半个含义）。终态物化因此改成**整会话重铺**：`ordinal` 是会话内序号，只有整会话重排才保证稠密，
>    顺带清掉没有归属轮的历史孤儿行；live 路径与重建路径**共用同一段构建代码**（分叉过一次，代价是
>    "重建之后读模型看起来不一样"）。思考/工具不再进 `messages`——它们本来就没有读模型消费者，
>    时间线由事件读面渲染。
> ④ **`inputs` 补会话内位次 `seq`**（`UNIQUE(session_id, seq)`，插入时同一事务取号）：排队顺序从
>    `created_at, id` 换成 `seq`——"顺序"从此是队列自己的列，不再由时钟与 id 拼出来；
>    `target_run_id` 降为审计快照（它从来只是内存句柄的 `run_<ULID>`，不是库里的行 id）。
> ⑤ **一个迁移窗口、三个版本步**（v21 / v22 / v23，各自 `backup: "vNN"`、各自可独立提交与回滚；
>    不存在"回滚一半"的中间态）。每步末尾把 `meta.read_models_rebuild` 置成该版本号：**口径变了就
>    重建**，而不写"把旧口径的数搬成新口径"的一次性搬迁代码——重建只有一个实现
>    （`manager.RebuildSessionReadModels`），消费面两处：启动钩子与 `mate doctor --rebuild-read-models`。
> ⑥ **`mate doctor` 新增 `read-models` 一段（默认只读）**：孤儿消息（`messages.run_id` 指不到 `runs`）、
>    空正文行、每会话 `messages.ordinal` 是否 1..N、每会话 `events.seq` 是否 0..N-1、
>    `messages.events_json` 里的来源类型是否只在白名单内（挡"新事件类型偷偷进 messages"）。
>    **五条全 0 才 `ok`，任意非 0 即 `fail`**（"事实与声明不符"才影响退出码）；"无法校验"（memory
>    模式、没有库文件、采集不到）记 warn。`--rebuild-read-models` 是**显式 opt-in 的写动作**——
>    它是体检报红之后的**出路**，默认路径一个字节都不写（守卫 G7 钉住"只能在开关后面"）。
> ⑦ **机器判据（新旧六条）**：G1 重写（`events` 三列各司其职、全局序零命中）、G4（`events` 的列白名单
>    + 不存在只对 `seq` 的 UNIQUE 索引 + 无 `meta.events_last_seq` 读取）、G5（消息白名单投影：
>    `default` 不开行 + 空正文不产行 + 白名单执行面与声明面同源）、G6（轮次身份是铸造的
>    `run_<26 位 ULID>` 且 `(session_id, first_seq)` 唯一）、G7（读模型可重建：唯一实现 + 两个消费面
>    + 写路径只能在开关后面）；冻结事件清单加入 `run.started`（清单 ↔ 前端 `Ev` ↔ README 事件表三处同批）。
> ⑧ **代价与已知残留（如实写，别当成漏做）**：① 旧二进制对 v21/v22/v23 库**拒绝启动**
>    （`latestSchemaVersion` 前进），回退只能靠 `backups/<db>.bak-vNN-<ts>` + `git revert`；
>    ② 整会话重建 messages 在**单会话 > 5k 行**时可能变慢（本版不做增量投影，先按量级验收，超阈值再改
>    "从最早受影响的轮起重重建尾部"，风险表 R4）；③ `messages.tool_details_json` 现在**恒为 `[]`**、
>    `events_json` 只装该行自己的 `session.message` 增量（列刻意留着，读模型的行形状不随版本跳变；
>    真正的工具面在事件流里）；④ `mate doctor --rebuild-read-models` 会跑迁移（`store.Open` 自动迁移）——
>>   用户跑这条命令的典型时机正是"刚升级完、还没起 serve"，所以这里是特性而不是意外。

> **决策变更 v24（provider/model 的「身份与命名空间」收口：路由键隔离 + 容量声明 + 会话引用改平台 id，2026-10-15）**：
> 设计全文见 `.dsh/docs/specs/2026-10-15-provider-model-identity-design.md`，执行计划见
> `.dsh/docs/plans/2026-10-15-provider-model-identity-plan.md`。触发是用户两问——「provider 的主键 id 使用
> 底层 runtime 的值，db 层不该有自己的主键 id 吗」「`models` 中 `id` 和 `model_id` 两个字段也很有歧义」。
> 两点都是对的，但**根因不是"缺代理键"**：`providers.id` 本来就是**用户自己起的 slug**（`pkg/object/objid`
> 早已把"provider 刻意不铸造 id"登记成判据），真正发生的是**同一个字符串被用进了底座的命名空间**。
> 于是三次收口，各自一个迁移版本、一次原子提交、独立可回滚（`backup: "v24"/"v25"/"v26"`）：
> ① **命名收口（v24，零行为变化）**：`models.model_id` → `models.model`。`model_id` 在本仓一词三义
>    （"发给上游的模型名" / `models.id` / `sessions.model_id`），而 `model` 是外部生态的通用词（OpenAI
>    请求体 `{"model": …}`；各家自定义模型表也把"上游真实模型名"记为 `model`）。此后 `_id` 一律指
>    "指向某表 `id`"。**判据就是"别的什么都不变"**：`git diff testdata/` 为空。
> ② **路由键隔离 + 容量声明（v25）**：交给 dsh 的每个路由键带平台保留前缀 `mate-`
>    （`pkg/runtime/dsh/route_key.go` 是唯一派生点；`config.go` 两处 + `model_value.go` 一处必须同源，
>    只改一处就是"所有选了模型的会话立刻起不来"），`displayName` 交付**供应商展示名**（空则回落路由键）。
>    为什么必须隔离：`providers` 的 dict 键**就是交付面**——命中 pi-ai 已装 provider 时，那一家的端点/
>    协议/展示名/模型目录会变成这条路由的**逐字段默认值**，"平台配了 A、实际按 A 的目录解析出 B"静默发生。
>    连带一行：隔离后容量不再能从底座内置目录继承，于是 `models` 补 `max_input_tokens`/`max_output_tokens`
>    （**可空、无 DEFAULT**——0 当不了"未声明"的哨兵，底座 schema 是 `min(1)`；空 = 不下发该字段）。
> ③ **会话模型引用改平台行 id（v26，定案「辰」）**：`sessions.model_id` **列名保留、语义改**为
>    "指向 `models.id`"（`mdl_…`）；运行时的不透明值**不落库、不过 wire**，由 adapter 每次现算
>    （`resolveSessionModel` 是唯一解析点）。`sessions.provider_id` 出列（v19 之后它没有任何写入点、
>    线上恒空；供应商由**模型行**给出）。目录候选加 `model_id` 注解（指向平台哪一行；空 = 平台未声明 ⇒
>    不可选），这样"选运行时值、提交平台 id"一次取数就能做到。
> ④ **机器判据 8 条**（守卫⑤ + ①②③④ + ⑥⑦⑧，`internal/archtest` 69 → 77 条）：命名三面 + 一反向、
>    路由键前缀、`displayName` 来源、派生单点（静态 + 动态两半）、容量列可空无默认、会话写入点必须经解析、
>    `sessions.provider_id` 零残留、对不上清单必须**显式拒**。
> ⑤ **代价（如实写，别当成 bug）**：① **升级后原先靠"路由名恰好命中内置目录"拿到的容量不再生效**
>    （未声明 = 用底座那句自注 "a guess by construction" 的默认值，界面上如实标"容量由环境兜底"）；
>    ② `/models` 的键名 `model_id`→`model`、`POST/PATCH /sessions` 的 `model` 从"运行时目录的不透明值"
>    变成 `mdl_…`（**破坏性 wire 变更**，与 v15 同档）；③ **模型从未被平台声明过的会话，升级后失去模型
>    选择**——存量回填（`meta.sessions_model_ref_backfill` 标记 + 启动时 `backfillSessionModelRefs`）只认
>    **唯一命中**，零命中/多命中一律置空并计数（"算不出来"绝不打勾：打勾会把一次失败固化成事实）。
>    真要让那些会话活下来，出路是**先声明模型再升级**；④ 旧二进制对 v24/v25/v26 库**拒绝启动**，
>    回退靠 `backups/<db>.bak-vNN-<ts>` + `git revert`（三步彼此独立，不存在"回滚一半"的中间态）。
> ⑥ **刻意不做**：不加 provider 代理键（`prv_<ULID>`）、不做 `legacy_key`/`migrated_from` 改名能力、
>    不做 provider 级容量默认、不给 `models.model` 加别名或唯一约束、不解析运行时的不透明值
>    （比较只走"派生值对派生值"）。

> **决策变更 v27（模型下拉只出现「设置」里配置过的模型：把 v26 的「标注 + 置灰」改成「收窄」，2026-10-16）**：
> 触发是用户原话「**模型下拉列表应该只出现【设置】中配置的模型**」——截图那个下拉里，除了用户配过的
> `yygu / deepseek-flash`，还摆着运行时内置目录的两条（`DeepSeek-V41-Flash` / `DeepSeek-V4-Pro`，
> 标「平台未声明」并置灰）。**这是对 v26「刻意不过滤、如实标出」的推翻**，理由不是"看着乱"：那两条
> 候选**不承载用户的任何决策**——会话的模型引用只收平台行 id（`resolveSessionModel` 对不上就以 40401
> 拒），摆一行选不了的东西只会稀释"我配了什么"这唯一答案；而"运行时广告了、平台没声明"这条**事实**
> 本来就有落点：`GET /models` 的 `sync[].advertised` / `missing`（机器面、诊断面）。
> ① **收窄点只有一处**：`packages/types/src/model.ts` 的 `modelValues` = `advertisedModelValues` ∩
>    平台清单（按 `model_id` 注解过滤）。原取数改名为 `advertisedModelValues` 并标注**不得直接喂
>    选择器**——它只服务两条判断（目录到没到、空态说明里"广告了几个"）；被收窄掉的那部分是
>    `undeclaredModelValues`，只用来讲一句人话。
> ② **收窄不许是静默的**（v26 不肯过滤的那条理由照旧成立）：下拉为空且"广告了、但一条都没声明"时，
>    新建面板把那句换成 `当前模型目录里的 N 个候选都不是「设置 · 模型」里配置过的模型：先到那里添加
>    供应商与模型。`；pill 的空态文案从「运行时无可用模型」改成「模型 · 暂无可选」（收窄之后前一句
>    是假话——运行时可能广告了一堆，只是都没配过）。
> ③ **自愈闸门仍取广告面**（`advertised`）而不是收窄后的列表：那是两个问题——"目录到没到"看广告、
>    "这条能不能选"看 `model_id`。拿收窄后的列表当闸门，"平台清单为空、运行时却广告了一堆"会变成
>    "保持旧值"，而旧值正是发出去必被 40401 拒的那种（v26.1 修掉的死锁形状）。
> ④ **组件那一层保留兜底、删掉那个标签**：`ModelSelect` 仍对没有 `model_id` 的候选 `disabled`
>    （纵深防御：调用方补的「当前模型」那一行就是这种）；「平台未声明」标签**删除**——收窄之后它唯一
>    可能命中的就是那一行，而那一行恰恰是**平台声明过的行**（只是运行时此刻没广告它），贴上去是假话。
> ⑤ **契约零变化**：不动 wire、不新增端点，`config options` 里的候选**照旧全量下发**（带/不带注解），
>    收窄发生在客户端。这是有意的——服务端那份仍是"运行时说了什么"的事实面，将来要复现这条排障
>    不必再连一次底座；也让"要不要收窄"这种**呈现**决定留在它该在的那一层。
> ⑥ **机器判据 1 条**（`TestModelDropdownShowsDeclaredCandidatesOnly`，`internal/archtest` 78 → **79**）：
>    收窄点唯一且真的按 `model_id` 过滤 / 两个选择器调用点都经收窄入口且 JSX 里不出现广告面 /
>    "广告了但没声明"必须接到空态说明上 / 没有注解的候选必须仍被置灰、那个标签不许回来。
>    反证（实跑）：抽掉 `modelValues` 的 `filter` / 把 `values={advertised}` 喂给选择器 / 抽掉
>    `undeclaredModelValues` 的消费 / 抽掉 `disabled: !v.model_id` / 把「平台未声明」加回来 → **五者皆红**。
> ⑦ **界面那半也是实跑的**（真 Chrome + 跑着的真核心 + 真交付产物）：靶上 3 条广告 / 1 条声明 →
>    下拉**恰好 1 项**（`deepseek-flash`、可选）；`DeepSeek` 分组与它下面两条内置候选一起消失，
>    `yygu` 分组仍在，界面上零「平台未声明」。（探针跑完即弃，不进仓库验收集合。）

## v28 · WorkBuddy 对齐：五条刻意偏离 + 一条长期纪律（2026-10-16）

参照产品在**外部形态**上比我们想得全（目录选择器、工作空间持久化、会话归属、队列交互），所以
`.dsh/docs/specs/2026-10-16-workbuddy-alignment-design.md` 逐条对齐了它。但**对齐不等于照抄**：
下面五条是我们**看了它的做法之后仍然决定不做**的，以及为什么。

> 判断标准只有一条：**它多给的那半，在我们这里有没有一个能被用户看见的后果**。没有后果的
> 结构，抄过来只是多一份要维护的东西——而它会以"我们也有这个功能"的名义，让下一个人以为
> 平台支持了它。

### ① 不照抄「工作空间是弱实体」

它把工作空间做成可指向任意路径的**弱实体**（登记行不保证目录存在，读面每次现算）。
我们**保留**这条（读面现算 `dir_status`，不缓存），但**不**给它加"自动重挂"——见
`docs/unimplemented.md` O18。理由：自动重挂要把"目录没了"这件事**猜**成"它搬到哪去了"，
而猜错的后果是把一个会话的归属悄悄改到另一个项目里。

### ② 不建「沙箱信任裁决表」

它有一张"哪种模式允许写哪儿"的裁决表。我们**不建**：ACP 面读不到 `enforcement`（见
`docs/unimplemented.md` D10），那张表在平台侧只能是**猜**——而"猜出来的裁决"比"没有裁决"更糟，
因为它会让界面有底气说一句平台其实无法保证的话。所以沙箱的**真值**只在底座的签名表里
（`pkg/runtime/dsh/tool_failure.go` 的失败归类），平台只做**事后归类**，不做**事前承诺**。

### ③ 不照抄 13 态轮次模型

它有 13 个轮次状态（含 `settled` / `continued` 这类生命周期态）。我们用 **10 个活态 +
`interrupted`**，且**不**做"生命周期"与"执行态"的两层拆分。理由：13 态里有一半是为了表达
"这一轮之后还接了什么"，而我们的轮次**一次一条**（没有连续性概念）——多出来的状态没有
可观察的转移，只是让 `switch` 变长。

### ④ 不做 `surfaceOp: replace`

它的队列支持**就地替换**一条已排队输入的正文（`surfaceOp: replace` + `replaceText`）。
我们**不做**：取出编辑（`pop`）已经覆盖了用户意图（取回改完再发），而"就地替换"要引入
一个"这条队项的身份不变但正文变了"的状态——那会让"我看到的正文"与"最终发出去的正文"
可能不是同一份（中间被替换过）。代价是**位次会变**（改完排到队尾），这个代价我们接受。
见 `docs/unimplemented.md` O15。

### ⑤ 不做 17 方法的队列

它的待发送队列有 17 个方法。我们**只补三件**：重排 / 立即发送 / 取出编辑（刻意不做 17 个方法）。
判据是"有用户看得见的后果"——这三件各自对应一句用户会说的话（「顺序不对」「不想等了」
「发错了」）。其余十几件在我们的交互里没有对应的话。

### A4 · 长期纪律：沙箱化的半与未沙箱化的半**共享路径命名空间**

这条不是偏离，是对齐时**必须记住**的一条：

底座允许多种执行模式（`read-only` / `workspace-write` / `danger-full-access`），而它们**写的是同一批
路径**。所以：

- 「这个文件被改了」这件事**不能**按模式分桶记——同一次会话里模式可能变（升权），
  而路径是同一个；
- 任何"按模式决定要不要留痕/审计"的做法，都会在模式切换的那一瞬**丢一段历史**；
- 反过来：**沙箱边界本身不是审计边界**。隔着 `workspace-write` 写进去的文件，与
  `danger-full-access` 写进去的，事后在盘上无法区分——所以"是谁在什么权限下写的"
  只能由**平台的事件流**回答，而平台的答案是"这一轮用的什么模式 + 这一轮碰了哪些工具"。

## v29 · L2：未实现清单 §2 十四条逐条决策（2026-10-17）

`docs/roadmap.md` L2 的出口判据是「§2 不再有未决定的条目——每条要么有排期、要么有理由、要么改判」。
这一轮把 14 条逐条落成三种结论之一，并把「不做」写成**能被下一个人复核的判据**（不是"当前不需要"）。
结果：§2 从 14 条缩到 3 条（O3 / O4 / O5），其余进 §3 的 C25–C35，O14 改判为 D11。

> 判「不做」的门槛不是"现在没空"，而是：**这条能力缺口的替代出路是否已经存在且代价可接受**。
> 有替代出路的才关；只是"优先级低"的一律保留排期。

### ① 热重载与投影：O2、O10

配置热重载的正确形态是**整进程重建**：`runtimes.json` 决定 adapter、能力定义目录决定 MCP 客户端集合、
`config` 决定 store 与监听地址——它们与连接/进程生命周期强耦合。只重载其中一半，会得到「配置说 A、
进程跑 B」的分叉，而界面上没有任何一行会说这件事变过。本地形态有「改完重启」这条等价出路（秒级），
所以**不做半热**；触发条件（运维要求不重启变更）出现时按整进程重建做。

O10 的第一条（按会话/cwd 动态 skill root）**不是平台没做，而是 D2 已经把它判成 DEGRADED**：底座的
读根同时是它的通用 agents 家目录，按会话覆盖会越界，并让平台写根与底座读根悄悄分叉（skill 静默不
投放、平台侧仍自报 `in_sync`）。所以它并入 D2，不再作为平台侧待办重复出现。第二条（skill 文件变更
热重载）与 O2 同口径：变更走重启。

### ② 进程与队列的边界：O6、O15

O6 的两个子项都**不该做**：`setsid` 脱离进程组的孙进程是底座自主 detach 的结果，平台追过去等于把
"进程边界"变成一句空话；跨实例锁没有对象——单实例是架构前提（同一个库被两个核心同时写，本来就不
在支持矩阵里）。第三个子项（非 unix 无进程组回收）不是"不做"，而是**随 Windows 出包一起做**：当前
桌面 recipe 只出 macOS，Windows 的 NSIS 分支还没接；到 M4-3 时用 Job Object 重做回收，不在这一轮先
做半套。

O15 已在 v28 ④ 决策过（不做 `replace`），这一轮只是把它从"未决定"归位：取出编辑（pop）已覆盖用户
意图，就地替换会引入「身份不变、正文变了」的状态，代价是位次变化（改完排到队尾）——这个代价接受。

### ③ 规模触发项：O7、O9

O7（zip 打包 / 导出全部的服务端游标 / 分词与相关性排序）与 O9（虚拟滚动 / `after`+`before` 组合区间）
都是**规模触发**：本地形态的会话长度与数据量下，现有实现（`ids` 上限 50、50 条/页、尾部窗口 + 显示
更早）没有可感知的缺口，而补上它们各自要引入新的索引/词典/分页语义。O9 里还有一条是**伪缺口**：
「空视图点加载更早等价取末尾一页」在界面上不可达——按钮只在有内容时渲染。

### ④ 刻意的信息不对称：O12、O13

O12 的两条都是**设计**，不是欠账：
- 跨会话决策与「未知 id」同码同文案（`elicitation_not_found`，细节只在服务端 WARN）是 C3 的防泄露
  口径——区分它们等于告诉调用方「那个 id 在别的会话里存在」；
- `always` 回执是**请求意图**，不是"记忆已写入"。记忆的真值在 `GET /sessions/:id/permission-memories`
  读面；在回执里补一个"已记住"会让同一件事有两处事实，而它们迟早不一致。

O13 同理：事件写库失败不阻断会话（总线侧仍有 + WARN）是取舍——一轮跑到一半因为 append 失败而中断，
比"这条事件不在 REST 历史里"更糟。要让两侧完全一致得上事务性 outbox，代价大于收益。

### ⑤ 改判为 DEGRADED：O14

「同一进程内按会话切权限模式」需要底座提供运行时切换面。探针证明 `/permission` 命令面在 ACP 上
不存在，所以这不是平台没做，而是**底座不提供**。平台已经用「每模式一个暖进程 + `ConnKey` 基础键」
做到不同会话不同沙箱边界；同进程内切换不再列为平台待办，改记为 D11。

### ⑥ 做掉的两条：O8 的时区、O19 的并列

O8 里唯一**是缺陷而不是取舍**的是时区：界面给的是用户日历上的「9 月 1 日」，后端此前按 UTC 解释，
于是 UTC+8 用户每天 00:00–08:00 的轮次被算进前一天，而界面上看不出来。这一轮补 `?tz_offset=`
（东为正的分钟数，缺省 0/UTC 向后兼容）：日期参数按它解析、day 桶按它分键、响应回显生效偏移。
**边界如实写**：偏移按请求时刻取一个固定值，区间跨夏令时切换时那一天的日界仍会差 1 小时；要精确得传
IANA 时区名，而界面需要的只是「我看到的日期」，固定偏移是与之匹配的最小契约。
其余两条（图表、逐轮 token 明细）保留"不做"的理由（不引图表库；ACP 通道限制见 D4）。

O19（确认区只画一张卡）是交互缺陷：卡片之间没有先后依赖，藏起来只会把"我还要处理几件事"变成一道
来回翻看的记忆题。改为**并列显示全部未结算卡**（权限在前），标题吸顶、整块限高 46vh 自己滚动——
既不把输入框挤出屏幕，也不再让用户逐条等下一张。

### ⑦ 保留排期的三条：O3、O4、O5

- **O3**：做，拆两批——全局限流/排队（今天只有每会话串行，N 条会话可同时打到同一个远端 server）、
  调用结果进事件流与审计表。不做定时探测（常驻探测会给远端 server 增加无谓请求）。
- **O4 / O5**：做，但属**云端阶段**（roadmap B 段方向，不在本地阶段排期）——本地形态是单机单操作员，
  多租户鉴权与远端接入的鉴权面不在当前产品面的缺口里。

### ⑧ 判据

- 新增 1 条守卫（146 → **147**）：`TestUsageDayBucketsCarryClientZone` 钉住 O8 那条链的五段
  （存储层按 `Location` 分桶 / 用例层解析并传递并回显 / 控制器读 `tz_offset` / 前端类型声明 /
  api 拼参 / 页面真的算偏移）。缺任何一段都不会报错——后端照常按 UTC 分桶，界面照常渲染。
- O19 由既有 `TestPendingCardsLiveOutsideTranscript` 加两段：必须 `ordered.map(...)` 且每张带
  `key={en.id}`，并禁止 `pending[0]` / `pending.length - 1`（单卡形态的痕迹）。
- 反证（实跑）：把 `store/usage.go` 的分桶改回 `UTC()` → 红；抽掉 api 的 `p.set('tz_offset'` → 红；
  把确认区改回只画第一张 → 红。
