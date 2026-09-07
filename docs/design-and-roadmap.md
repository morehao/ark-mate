# ark-forge 详细设计方案与 Roadmap

> 本文档在 [architect-design.md](./architect-design.md)（已确认的架构基线与关键决策）之上，给出：
> ① 可执行的**详细设计方案**（领域模型、分层接口、API/事件契约、存储、前端架构）；
> ② 参照 **codeg**（同赛道成熟参考实现）的能力对照与取舍矩阵；
> ③ 分阶段 **Roadmap**（里程碑、每阶段范围/验收/风险）。
>
> 定位一句话：**ark-forge 是「面向 Agent 应用开发的通用底座/平台」——自托管的 ACP Host 控制面**；
> codeg 是同一 ACP Host 思路下「做深做厚成桌面 IDE」的产品天花板，二者是「平台 vs 应用」的关系，
> 设计上借鉴 codeg 的**宿主内核**（连接管理、事件流、可扩展注册）而非其桌面外壳。

**目录**：§0 为什么参考 codeg · §1 现状盘点 · §2 设计总则 · §3 Roadmap 总览 · §4 详细设计方案（领域模型
4.1 / acl 4.2 / runtime+manager 4.3 / REST 4.4 / 实时事件流(WS 唯一) 4.5 / 实时通道决策 4.6 / 存储 4.7 /
前端 4.8 / 演进位 4.9）
· §5 codeg 对照与取舍（纪律 5.1 / 服务端与事件流 5.2 / 可扩展面 5.3 / 取舍矩阵 5.4 / 定位差异 5.5 /
ACP 内核迁移清单 5.6 / 对齐修正 5.7 / 参照 AionUi 5.8） · §6 Roadmap 详解（M0–M3 + P1+） · §7 风险与验证

> **决策变更 v2（本节以下均已按此更新）**：
> ① **实时通道仅 WebSocket**——「REST 命令 + WS 事件推送」为唯一实时形态（codeg 同款），
>    **不设 SSE 出口**：既有 SSE 端点/适配器代码随 M0 一并移除，事件流五条范式（§2-3）不变，仅换载体。
>    理由与取舍见 §4.6。
> ② **无跨 agent 协作**——ark-forge 只做「一次会话/一次运行**指定一个 agent runtime**」；
>    多 runtime 的意义 = 注册表 + 按会话/运行选择与切换，**不做** @委托/子 agent/并行协作（codeg 的
>    delegation 相关内容从路线中移除，见 §5.4 矩阵更新）。

> **决策变更 v3（定位边界，2026-09-04；本节以下均已按此更新）**：
> ① **能力不重造**——skills/MCP/推理等「运行时能力」一律**直接复用底层 runtime 的原生实现**
>    （skill 执行、MCP 工具调用等归 runtime），ark-forge **不实现**任何 agent 运行时功能本身。
> ② **数据管理是平台职责**——平台只做三类数据的**管理层**：会话数据（session/run/wire transcript、
>    M2 起落库）、skill 数据（技能包存储/分发/启用的数据面）、MCP 数据（服务器定义/注册/注入面）。
> ③ **单机单操作员（无多人协作）**——当前**不做**多用户/团队共享语义：M3 的「基础鉴权与多用户」取消、
>    P3+ 聊天/webhook/IM 远程通道位从路线移除；daemon 面向单操作员、默认仅监听本机，多会话并发与
>    runtime 切换仍保留。对外暴露时的「单操作员 token」仅在确有需求时最小评估（当前不排期）。
> ④ 无跨 agent 协作（决策 v2②）与无多人协作（v3③）是两条独立边界，叠加后平台完全面向
>    「单个操作员 · 一次运行单 runtime」。

> **决策变更 v4（管理面范围校准，2026-09-04；基于对 iOfficeAI/AionUi 的只读调研，本节以下均已按此更新）**：
> AionUi 是**应用型** Agent 工作台（Electron/Web 壳 + 外部 aioncore(Rust) 引擎 + ACP 接入外部 CLI
> agent），与 ark-forge 同为「薄控制面 + 外部宿主内核」结构；其「数据/配置管理层」做得厚，可整体当
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
> 替代游标——其事件流只是渲染消息、权威在 DB；ark-forge 事件流是事实源，不降级）。
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

## 0. 为什么参考 codeg

codeg（`/Users/morehao/Documents/study/ai-agent/codeg`，Apache-2.0，Rust + Tauri/Next）与 ark-forge 共享同一个
核心命题：**作为 ACP Host，用统一控制面连接、运行、管理多个外部 Agent Runtime**（codeg 内置 15 个 agent，
ark-forge 首版 opencode + deepseek-harness）。codeg 是这一命题下**已到 0.30 版本、形态完整**的参考系，其
宿主角色的实现分层可直接映射到 ark-forge 的 `acl / runtime / manager / server`：

> 另有**第二参照物（应用型）AionUi**（iOfficeAI；Electron/Web 壳 + 外部 aioncore 引擎 + ACP 接入外部
> CLI agent，见 §5.8）：与 ark-forge 同为「薄控制面 + 外部宿主内核」结构，其**数据/配置管理层**
> （权限策略、会话管理、usage 快照、cron/MCP/skills 数据契约）可整体当蓝本；执行层（内置引擎）/
> Team 协作 / 远程通道 / 扩展 Hub 生态**不借鉴**（撞 v2② / v3③ / 能力不重造边界，见文首「决策变更 v4」）。

| codeg（参考实现） | 角色 | ark-forge 对应层 | 借鉴程度 |
|---|---|---|---|
| `src-tauri/src/acp/registry.rs` + `custom_registry.rs` + `remote_registry.rs` | Agent 定义目录 / 自定义注册 / 远程 registry | `internal/manager`(Agent 定义+Registry) | **照搬思路**（模型收敛） |
| `acp/manager.rs` + `connection.rs` + `lifecycle.rs` | 连接管理器：拉起子进程、ACP 会话生命周期 | `internal/acl` + `internal/runtime` | **照搬思路**（Go 重写，砍桌面特例） |
| `acp/event_stream.rs` + `internal_bus.rs` + `session_state.rs` | 归一化事件流与会话状态 | `internal/server/eventbus`（已有雏形） | **对齐语义**（补事件 schema） |
| `bin/codeg_server.rs` + `commands/remote_proxy.rs` | 独立服务端 + WebSocket 远程代理 | `internal/server`（Go daemon） | 传输取舍见 §4.6 |
| 前端 `stores/conversation-runtime-store.ts` + contexts | 会话运行时（流式渲染/工具卡片/错误） | `frontend/apps/panel` | **阶段化**（P0 流式 → P1 会话页） |
| delegation / work_task / automation / workspace | @协作 / 任务板(worktree) / 自动化 / 工程工作区 | （参考其工程纪律；协作类按决策 v2 不做） | 取舍矩阵 §5.4 |

参考时的**立场**（避免照抄成一个桌面 IDE）：

- ark-forge 交付的是**自托管 daemon（`./forge` 单二进制）+ Web 面板**，codeg 的编辑器/终端/Split View/桌面
  特性属于**应用层体验**，ark-forge 默认不做；当前定位为**单机单操作员**（决策 v3③），不保留
  「面向 Web 多用户/多租户」的能力位。
- codeg 桌面经 Tauri 事件通道直连内核、服务端经 WebSocket；ark-forge 采纳同款形态：**REST 命令 +
  WebSocket 事件推送为唯一实时通道**（不设 SSE 出口，决策 v2①，详见 §4.5/§4.6）。面板与 daemon 间
  **无多 agent 协作语义**：一次会话指定一个 runtime（决策 v2②）。
- 借鉴的是 codeg 的**领域建模与状态机**（连接/会话/错误分类/断线策略），这些与 UI 形态无关，可整体迁移。

## 1. 现状盘点（写本文档时的仓库实况）

### 1.1 ark-forge（本仓）

- **已就位**：monorepo 骨架（backend/ 单 Go module + frontend/ pnpm workspace）；`server` 层可冒烟起服：
  gin 版本化路由（`/v1/forge/{health,agents,sessions,runtimes}`）、`handler.Service` 抽象 + `mockService`
  占位、`eventbus`（全局 seq + history 回放 + `after` 续传）。**实时通道为 WS（M0 落地）**；脚手架期的
  SSE 端点/适配器已按决策 v2① 移除。
- **空壳**：`internal/acl`（ACP Host 层）、`internal/runtime`（runtime adapter）、`internal/manager`（生命周期）、
  `internal/infra`（config/store/logger）——只有 doc.go。
- **未启动**：`frontend/apps/panel` 只有 `<h1>ark-forge</h1>`；无 `coder/acp-go-sdk` 依赖、无 SQLite schema、
  无真实 runtime adapter、无 main 里 mock 之外的装配。
- 架构文档已锁定关键决策：ACP v1 / stdio / Host 角色 / 单 module 分层 / 会话事件流范式 / MVP 边界。
  （其「SSE 首选、WS 仅双向需求时引入」已被本文档决策 v2① 更新为 **仅 WebSocket**，见 §0/§4.5。）

### 1.2 codeg（参考实现，只读）

- **宿主内核（Rust）**：`acp/registry.rs`（内置 agent 定义）、`acp/custom_registry.rs`（自定义 agent 注册与安装）、
  `acp/manager.rs`（ConnectionManager）、`acp/connection.rs`（单连接全生命周期）、`acp/session_state.rs`、
  `acp/event_stream.rs` + `internal_bus.rs`、`acp/delegation/`（@ 协作）。
- **服务端形态**：`bin/codeg_server.rs`（axum 独立服务端，与桌面共用内核）+ `commands/remote_proxy.rs`（Web/移动端接入）。
- **扩展面**：model_provider / MCP / skills / 跨 agent 会话导入 / automations / chat channels。
- 详细机制摘要与 ark-forge 迁移要点见 §3-§5（由对 codeg 源码的逐文件精读提炼，标注文件证据）。

## 2. 设计总则（ark-forge 特有，区别于 codeg）

1. **协议是第一公民**：统一在 **ACP v1（stdio JSON-RPC/NDJSON）** 之上抽象 runtime；绝不与具体 runtime 的
   私有 API 耦合（仅允许为诊断/降级读取其 stderr / 配置文件）。
2. **能力不对称是常态，接口必须显式降级**（沿用架构文档 §6.2）：opencode 会话能力完整
   （load/resume/fork）；deepseek-harness（dsh）支持 list/resume/close、无 load/fork（核实见
   `docs/deepseekharness-acp-adaptation.md`）。所有上层 API 把「runtime 能力」当作一等数据返回，
   前端据此裁剪入口。
3. **事件流可重放、增量与终态分离**（沿用架构文档 §2 五条范式，载体见 §4.5/§4.6 决策 v2①）：单一事件
   通道 + 全局单调 seq + `after`/快照续传 + 内存缓冲；终态落库、流式增量 live-only。
4. **单机单操作员（无多人协作，决策 v3③）**：不做多用户/团队共享语义与登录/RBAC——daemon 面向单个
   操作员、默认仅监听本机（127.0.0.1）；鉴权不在当前路线，仅当确需对外暴露时按「单操作员 token」
   最小实现评估。控制面仍把 runtime 配置、密钥注入、会话归属**显式建模**（单操作员也要可配置、可备份、
   可审计），但不做本地 IDE 假设。
5. **层 seam 即未来模块边界**：`acl` 可抽独立 module/仓（对外发布 ACP Host 库）的演进路径保持开放（架构文档 §5）。
6. **一次运行只指定一个 runtime（无跨 agent 协作）**：会话/运行在创建时绑定并固定一个 Agent（其下唯一
   Runtime）；「多 runtime」只表现为注册表与**按会话/运行选择切换**，不存在 @委托、子 agent、并行跨 runtime
   协作。所有编排语义（任务/自动化等，若未来引入）同样遵守「一次运行单 runtime」约束。
7. **能力不重造，平台只做数据/管理层（决策 v3①②）**：skills/MCP/推理等执行能力一律复用底层 runtime
   原生实现——ark-forge **不实现** skill 执行器、MCP 客户端等运行时功能；平台职责限定为管理这些能力
   对应的**数据与配置**：会话数据、skill 包数据、MCP 服务器定义/注入面。

## 3. Roadmap 总览（先给骨架，细节见 §6）

| 阶段 | 名称 | 主题 | 结束标志（Exit） |
|---|---|---|---|
| **M0** | 打通 ACP 直连 | acl+runtime+manager 落地，真实拉起 opencode/harness，事件流入 eventbus 并经 **WS 端点（attach/快照/事件，无鉴权）** 出流 | `make server` 后经 WS 客户端（wscat/小 Go 客户端/后续面板）可见真实 token 流 |
| **M1** | 面板 MVP | Sessions 流式页 + Agents/Runtimes 列表，REST+WS 全链路 | 浏览器完成「选一个 agent → 发 prompt → 看流式回包 → 中断」 |
| **M2** | 可扩展底座 | 持久化(SQLite)、Agent 定义 CRUD + 自定义 ACP agent 注册、provider 配置注入、**权限策略位**（决策 v4①）、断线续传（快照/重放） | 不写代码即可接入第三个 ACP agent |
| **M3** | 平台化一期 | 多会话并发与恢复、切换指定 runtime、**会话数据管理面**（分组/归档/搜索/导出，决策 v4②）、用量/日志（per-turn 明细 + 会话快照） | 自托管 daemon 可作为单操作员底座稳定冒烟（多会话并发 + 重启恢复 + 会话管理 + 可观测） |
| **P1+** | 单 runtime 自动化与生态 | 任务板(worktree 隔离)、automation（先定 **cron 数据契约**，决策 v4④）、MCP/skills **数据管理**位（执行复用 runtime 能力，决策 v3①/v4④；均单 runtime 指定；无跨 agent 协作） | 见 §5.4 取舍矩阵 |
| **不做** | — | 桌面 IDE 外壳（编辑器/终端/Split View）、移动端、多人协作（多用户/团队共享/鉴权登录）、聊天/远程通道（webhook/IM）、跨 agent 协作/委托（决策 v2②/v3③） | 见 §5.4 |

> 说明：M0–M3 为当前工作焦点（本文档主体），P1+ 为方向性路线（能力位保留，不排期承诺）。
> 详细到「每个阶段：做什么 / 怎么做 / 验收」见 §6。

## 4. 详细设计方案（主体）

> 本节给出 M0–M3 范围内可直接落代码的设计：领域模型、分层接口、外部契约（REST + 实时事件流，§4.5）、
> 存储、前端架构。每节末尾标注与 codeg 的对齐/差异点（细节证据见 §5）。

### 4.1 领域模型

四类聚合 + 两类值对象，是全部接口与表结构的共同语言：

```
Runtime（运行时类型：opencode | deepseekharness | …）
  定义：如何拉起一个 ACP server 子进程（命令、参数模板、env 注入、能力基线）
  —— 对应 codeg registry 中每个内置 agent 的「安装/启动描述」部分
  —— 决策 v2②：Runtime 只做注册与选择，一次运行绑定其一，无跨 runtime 协作

Agent（Agent 定义 = Runtime 上的一个具名可运行实例）
  { ID, Name, Runtime, Config{model, provider, env, workspace?}, State }
  生命周期（长期）：draft → released；运行态：created → running → paused → stopped
  —— codeg 语境：一个已安装且可选的 agent CLI（其 registry 条目）；ark-forge 语境还要绑定运行参数

Session（一次可续谈的对话，绑定一个 Agent/Runtime 连接）
  { ID, AgentID, Runtime, RemoteSessionID?, Status, CreatedAt, UpdatedAt }
  能力差异：runtime 可能不支持持久会话（harness：连接断开即失效）——Session 记录必须携带
  `persistent` 能力标记与「最后活动连接」引用
  —— Session 创建时即固定 Agent/Runtime，会话期内不换 runtime（换 runtime = 开新会话）

Turn / Run（一次 prompt 的执行态，Session 内）
  RunStatus: created → in_progress → completed / failed / cancelled / required_action
  —— codeg 称之为 turn，并在此基础上叠加 usage/错误分类/重试等

Event（会话事件流的原子载荷，eventbus 单一通道，见 §4.5）
  { seq(全局), agg_id(session/run), version, type(判别), payload }
```

**事件类型（首版枚举，与实时事件流/前端强契约）**：

| type | 方向 | durable | 说明 |
|---|---|---|---|
| `session.started` / `session.ended` | 会话 | ✅ | 终态落库 |
| `run.started` / `run.completed` / `run.failed` / `run.cancelled` | Run 生命周期 | ✅ | 终态落库 |
| `turn.text`（message delta） | 流式增量 | ❌ live-only | token 级增量 |
| `turn.text.ended` | 终态 | ✅ | 该文本块完成 |
| `turn.thought` / `turn.thought.ended` | 推理增量 | ❌ / ✅ | 同上 |
| `turn.tool_call` / `turn.tool_result` | 工具 | ✅（结果） | 增量 live-only |
| `turn.error` | 错误分类 | ✅ | 携带稳定 code + actions（分类词汇见 §5.6） |
| `turn.required_action` | 权限/询问 | ✅ | 未来 question/permission 位 |

> **实装与契约校准（决策 v5）**：首版实装 type 以 `manager/events.go`（与前端 `events.ts` 同源）为准：
> `session.created / session.user_message / session.message / session.thought / session.tool_call /
> session.tool_update / session.usage / session.permission（acl 决策留痕）/ turn.ended / turn.error /
> run.cancelled`；一轮的流式片段由 `turn.ended/turn.error/run.cancelled` 统一收口，上表
> `turn.text.ended / turn.thought.ended` 等细粒度终态事件不再逐条实现。M2 预留
> `session.permission_request / session.runtime`（决策 v5④，仅契约先行）。各增量载荷的字段与归并语义
> 见 §4.5「事件载荷契约（决策 v5 校准）」。

> 对照 codeg：其会话事件先归一化为**统一 union**再达 UI（`session_state.rs`/`types.rs`）；ark-forge 用
> eventbus 的 `type` 判别 union + WS 事件帧承载（§4.5），等价但更薄。codeg 的事件更多维
> （usage/feedback/子代理卡片等——子代理卡片与决策 v2② 冲突，不引入），ark-forge 在 M2+ 以**扩展 type**
> 方式增长，不在首版堆满。

### 4.2 `internal/acl`（ACP Host 接入层）设计

架构文档 §6.1 已定职责；此处补齐**可行性结论与接口草案**：

**可行性结论（已在 acp-go-sdk v0.13.x 源码核实）**：`github.com/coder/acp-go-sdk` 提供
`NewClientSideConnection(client Client, peerInput io.Writer, peerOutput io.Reader)`，客户端侧方法覆盖：
`Initialize / Authenticate / Logout / NewSession / Prompt / Cancel / LoadSession / ResumeSession /
ListSessions / CloseSession / SetSessionConfigOption / SetSessionMode`，以及 unstable 扩展
（`ForkSession / DeleteSession / SetProvider / ListProviders / 文档通知 / NES` 等）。
stdin/stdout 即 `peerOutput/peerInput`，天然适配 stdio；架构文档 §9 的「client 侧完整度风险」显著缓解。

```
// transport.go —— 传输抽象（首版仅 StdioTransport）
type Transport interface {
    Start(ctx context.Context) error   // 拉起子进程并接入 stdio
    Stderr() io.Reader                // stderr 透出（诊断/日志/降级探测）
    io.Writer                         // 进程 stdin
    Close() error                     // 杀进程树（含孙进程）
}

// client.go —— 面向 runtime adapter 的高层句柄
type Client struct { conn *sdk.ClientSideConnection; caps Capabilities }
func Dial(ctx, t Transport) (*Client, error)   // Start + Initialize + 能力断言
func (c *Client) NewSession(ctx, req) (SessionHandle, error)
func (c *Client) LoadSession / ResumeSession / ListSessions / CloseSession / ForkSession ...
func (c *Client) Prompt(ctx, req) (PromptHandle, error)   // 返回后可 Cancel
```

**关键设计点**：
1. **能力断言在 `Dial/Initialize` 后一次性完成**并冻结在 `Client.caps`：`sessionCapabilities`（close/fork/
   list/resume）、`promptCapabilities`、`mcpCapabilities`、authMethods 等，供上层裁剪接口（与 codeg
   `connection.rs` initialize 期能力捕获同思路）。
2. **notification → 归一化回调**：SDK handler 里把 ACP notification（token/thought/tool/message/usage/…）
   归一化为 §4.1 事件，交给注入的回调（manager 的 event sink），acl 不感知总线。
3. **错误分类**：把 ACP/连接错误归类为可操作集合（connection/access/limit/request_rejected/service/…），
   与 codeg `feedback.rs` 对齐，随 `turn.error` 事件上抛，前端据此渲染「重试/重新登录/开新会话」条。
4. **断连语义**：连接 `Done()` 触发 manager 把进行中会话标记失败（终止 in-flight turn）；
   会话本体是否可续依 runtime 能力——harness（dsh）断开走 quiescent teardown，持久会话仍可
   list/resume；opencode 断开则其会话随之终止。resume 入口按 `Client.Capabilities()` 裁剪。

### 4.3 `internal/runtime`（adapter）与 `internal/manager`（编排）设计

runtime adapter 保持架构文档 §6.2 的 `Runtime/Session` 接口，补两个显式表达：

```
type Capabilities struct {
    SessionsPersistent bool          // 支持 load/resume/list/fork？（harness=false）
    MultiSessionConcurrent bool      // 同连接多会话
    Images bool                      // prompt 是否可带图
    EmbeddedContext bool
}
type Session interface {
    Prompt(ctx, PromptRequest) (PromptHandle, error)
    Cancel(ctx) error
    Close(ctx) error
    Capabilities() Capabilities      // 来自 acl.Client.caps，路由到各方法
}
```

`manager` 提供：
- **AgentService**（实现 `handler.Service`，替换 mock）：List/Create/Update/Delete Agent、Start/Stop；
- **SessionOrchestrator**：把「对 Agent 的一次会话」编排为 ACP 连接获取 → session/new（或 load/resume）→
  prompt → 事件流 → cancel/close；对 harness 将 `load/resume/fork` 显式返回「unsupported」；
- **连接池**：runtime 子进程按需拉起/空闲回收（对齐 codeg `idle_sweep`，M2 引入超时回收）；
- **事件注入**：归一化事件 Publish 进 eventbus（`server/handler` 无需感知域变化，seam 已就位）；
- **runtime 启动环境透传**：opencode 子进程透传 daemon 的 `OPENCODE_MODEL`。原因：opencode 的 ACP
  **忽略客户端传入的模型选择**（[issue #13644](https://github.com/anomalyco/opencode/issues/13644)），
  且默认模型不可用/被限流时可能「发起即静默挂起」（stream error 只写自身日志、不上报，
  [issue #24494](https://github.com/anomalyco/opencode/issues/24494)）。指定可用模型须走启动环境：
  `export OPENCODE_MODEL=yygu-v2/deepseek-v4-flash`（并在 opencode.json 配 `"model":"{env:OPENCODE_MODEL}"` 引用）。
  单机单 operator 下与 dsh 的 `DEEPSEEK_API_KEY` 透传同模式；
- **单轮 turn 看门狗**：`FORGE_TURN_TIMEOUT`（serve 亦支持 `--turn-timeout`，Go duration，0=关闭，
  默认关）——turn 超时自动 cancel（复用有界取消路径）并发终态事件，兜住「吞错挂起 → busy 永久占用」，
  前端配合「服务端忙水位 + 停止按钮」即可自愈。

### 4.4 REST API 契约（M1 冻结面）

沿用 `gincontext.Success/Fail`（`{code,requestID,msg,data}`）与路由前缀 `/v1/forge`：

| 方法 | 路径 | 用途 | M 归属 |
|---|---|---|---|
| GET | `/health` | 存活 | 已有 |
| GET | `/runtimes` | 可用 runtime + 能力基线 | 已有(mock) → M0 实装 |
| GET | `/agents` | Agent 列表（含 state） | 已有(mock) → M0 实装 |
| POST | `/agents` | 创建 Agent（选 runtime+config） | M2 |
| GET/PATCH/DELETE | `/agents/:id` | 详情/启停/删除 | M2 |
| POST | `/agents/:id/sessions` | 新建会话（返回 session_id + resume 能力） | M1 |
| GET | `/sessions` | 会话列表（带 agent/runtime/status） | 已有(mock) → M1 |
| GET | `/sessions/:id/history` | 会话历史（终态回放，分页） | M1 |
| POST | `/sessions/:id/prompt` | 发 prompt（返回 run_id；流式走实时事件流 §4.5） | M1 |
| POST | `/sessions/:id/cancel` | 取消当前 run | M1 |
| POST | `/runs/:id/cancel` | 取消指定 run | M1 |
| WS | `/ws/events` | 实时事件流唯一通道：attach/detach/ping、snapshot/replay/event 帧（§4.5） | M0 起（单操作员默认无鉴权，决策 v3③） |
| GET | `/providers` / `/providers/:id` | 模型提供商 CRUD | M2 |
| GET | `/agents/definitions`（POST 自定义注册） | Agent 定义目录 | M2 |

能力不一致（opencode vs harness）不通过「接口报错」表达，而是：
- `POST /agents/:id/sessions` 响应带 `capabilities:{persistent,resume,fork}`；
- 前端据此决定是否渲染「续谈/派生」入口（对 harness 隐藏）——与 codeg 对弱能力 agent 的降级一致。

### 4.5 实时事件流契约（仅 WebSocket，决策 v2①）

事件载荷与订阅语义完全承载于单条 WS；域层（manager/eventbus）不感知通道。

**事件帧（唯一载荷）**：

```
{ "type": "<事件type>", "seq": n, "agg_id": "s1|r1", "version": n, "payload": {...} }
```

**端点 `/v1/forge/ws/events`（M0 起可用；单操作员默认无鉴权，决策 v3③）**：
- 一条 WS 承载「订阅控制 + 事件推送」，客户端帧与服务端帧按顶层 `action`/`type` 判别：
  - 客户端 → 服务端：`{action:"attach", agg_id, since_seq?}` / `{action:"detach", agg_id}` /
    `{action:"ping"}`；可同时 attach 多个聚合（会话/运行）。
  - 服务端 → 客户端：`{type:"snapshot", agg_id, event_seq, state:{...}}`（冷挂载整包终态视图）/
    `{type:"event", ...事件帧}` / `{type:"detached", reason}` / `{type:"pong"}`。
- **快照/重放二分**（对齐 codeg attach 协议，§5.2）：`since_seq` 为空或落后于缓冲淘汰点 → 回
  `snapshot`；缺口小（仍在 per-aggregate 环形缓冲内）→ 回放事件批 + 高水位 seq；服务端持锁原子
  「决策 + 注册订阅」，杜绝快照与首事件竞态。
- 心跳：WS 协议层 ping/pong（30s）；应用层编排帧 `server.connected`（连接建立）/`server.instance.disposed`
  （daemon 关闭）各推一次，供客户端感知。
- 鉴权：默认无（单操作员，本机监听）；若确需对外暴露，再按「单操作员 token」最小实现评估——token 经 WS
  子协议（`forge-token.{b64url}`，浏览器可用）或首帧 auth，REST 走 header（协议位仅为该场景预留）。
- **刻意不做**（相对 codeg 传输层）：多路复用/逐订阅 forwarder、多通道并存——一条 WS 即一个面板实例的
  全部实时面；事件按 `agg_id` 路由到会话视图、按 `seq` 幂等去重（前端只前进不回退）。

**事件载荷契约（决策 v5 校准；可选字段缺省不出现在载荷/transcript 行中）**：

| type | payload（稳定键） | 可选键（决策 v5） |
|---|---|---|
| `session.created` | session_id, agent_id, runtime | — |
| `session.user_message` | session_id, agent_id, text | `msg_id` ① |
| `session.message` / `session.thought` | session_id, agent_id, text | `msg_id` ① |
| `session.tool_call` | session_id, agent_id, tool_call_id, title, status | `kind`/`input`/`output` ② |
| `session.tool_update` | session_id, agent_id, tool_call_id, title, status | `kind`/`input`/`output` ② |
| `session.usage` | session_id, agent_id | ③：input_tokens/output_tokens/thought_tokens/cached_read_tokens/cached_write_tokens/total_tokens/cost（随 turn 结束的 PromptResponse.usage 填实；shape 即 M3 `runs.usage_json`） |
| `session.permission` | session_id, title, allowed（acl.PermissionInfo，决策后留痕） | — |
| `turn.ended` | session_id, agent_id, stop_reason | — |
| `turn.error` | session_id, code, class, details | — |
| `run.cancelled` | session_id, stop_reason | — |

> 说明：`msg_id` 为 ACP unstable `messageId`（acp-go-sdk v0.13.5：user/agent/thought chunk 均携带；
> tool 更新仅 toolCallId，无 msg_id）——透传字段，runtime 不实现则为空，前端据此回退拼接（决策 v5①）。
> 工具 `status` 沿用 ACP 四态 `pending/in_progress/completed/failed`；`tool_call`（发起）与
> `tool_update`（更新/终态）共用载荷形状，终端状态看最后一次 update（决策 v5②）。

**权限/确认位（决策 v5④，M2 发布、契约先行）**：当前（M0/M1）权限为同步布尔决策
（`acl.AllowPermission`，默认拒绝），仅以 `session.permission` 留痕。M2 起权限请求 = **挂起态事件**
`session.permission_request`：payload `{session_id, call_id, title, options:[{label,value}]}`（call_id
关联 ACP 工具调用）；操作员经 REST 回执 confirm（allow/reject × once/always，决策 v4① 策略位）后，由
`session.permission` 落决策留痕、pending 撤下。会话连接态由 `session.runtime`（payload
`{session_id, phase, pending_confirmations, can_send}`；phase: starting/running/waiting_confirmation/
cancelling/idle）承载——供侧栏/徽标与「等确认」指示，UI 不持进程（决策 v4③，镜像 AionUi
`turn.completed`/`runtime.statusChanged` 语义，§5.8）。

### 4.6 实时通道决策：仅 WebSocket（相对架构文档 §2 的更新）

架构文档 §2 原定「SSE 首选、WS 仅出现双向需求时引入」，历经两轮收敛：先评估为「WS 主 + SSE 备」，最终
**决策 v2① 定为仅 WebSocket，不设 SSE 出口**。理由（对照 codeg 服务端形态 `codeg_server.rs`/
`remote_proxy.rs` 的实践与 ark-forge 实际需求）：

| 考量 | WebSocket（唯一实时通道） | SSE（已否决，作为对照） |
|---|---|---|
| 订阅控制（attach 带 since_seq / detach / 重连续传） | attach 帧是协议一等公民，快照/重放二分 first-class | 得借 REST query 表达，控制面别扭；多会话多 tab 只能全局流（前端去重噩梦）或每会话一连接（连接爆炸） |
| 鉴权（决策 v3③ 下无多用户；仅「确需对外暴露时的单操作员 token」与未来非浏览器消费者） | 子协议携带 token，浏览器与非浏览器一致 | EventSource 不能带自定义头 → cookie/query，泄漏/CSRF 面差 |
| 双向实时（控制信号、断连感知、未来 permission 应答/PTY） | 原生全双工 | 需另开通道 |
| 反代/网关 | upgrade 后隧道式转发，配置成熟 | 需关 proxy_buffering、防空闲超时 |
| 断线重连 | 显式 attach 协议承载（重连后按 lastAppliedSeq 补发） | 浏览器自带重连但与快照/重放语义叠加反而绕 |
| 纯「服务器→客户端」单向简单性 | 略重 | 最优——但 ark-forge 的实时面不止单向流（见上） |
| 维护面 | 单一通道，一种契约 | 双通道双契约，无人使用时是纯负担 |

**结论**：`server` 的「事件订阅抽象」seam 不变，**WS 是唯一输出适配器**；脚手架期遗留的 SSE 端点与
适配器代码随 M0 清理移除，不留「第二出口」。若未来出现「必须由事件通道单向喂给极简 EventSource 消费者」
的真实需求，再按当时的成本/收益评估是否恢复适配器，域层无感——但这不是默认路线。

> **佐证（codeg 自身未用 SSE 作实时通道）**：codeg 源码中 `text/event-stream`/`EventSource` 仅出现在
> office-watch **反向代理**对第三方 Office 服务的透传与压缩豁免处（`src-tauri/src/web/compression.rs`、
> `web/handlers/office_watch_proxy.rs`：重写 `window.EventSource` 指向代理、放行 `text/event-stream` 不压缩）；
> 其控制面→UI 事件通道是 **WS（server/浏览器形态）与 Tauri 事件（桌面形态）**，前端 transport 无
> EventSource client（`web-event-stream.ts` 实为 WS attach 封装）。即：SSE 只作为被代理外部服务的上游协议
> 出现，从未作为 codeg 自身实时推送载体——与 ark-forge 仅保留 WS 的取舍一致。

### 4.7 存储设计（M2 引入 SQLite，`internal/infra/store`）

沿用架构文档「database/sql + SQLite、无外部中间件」。首版 schema（**表名与字段即领域模型直译**）：

```sql
runtimes(id TEXT PK, name TEXT UNIQUE, kind TEXT, definition_json TEXT, created_at)
agents(id TEXT PK, name TEXT, runtime_id TEXT REFERENCES runtimes, config_json TEXT,
       state TEXT, is_released INTEGER, created_at, updated_at)
sessions(id TEXT PK, agent_id TEXT REFERENCES agents, runtime_id TEXT,
         remote_session_id TEXT, status TEXT, capabilities_json TEXT,
         created_at, updated_at, last_run_id TEXT)
runs(id TEXT PK, session_id TEXT REFERENCES sessions, status TEXT,
     error_kind TEXT, usage_json TEXT, created_at, finished_at)
events(id INTEGER PK AUTOINCREMENT, seq INTEGER, agg_id TEXT, type TEXT,
       version INTEGER, payload_json TEXT, created_at)   -- 终态事件落库（增量不入）
providers(id TEXT PK, name TEXT, kind TEXT, base_url TEXT, model TEXT,
          api_key_enc TEXT, extra_json TEXT, created_at)
```

要点：
- **只有 durable 终态事件落 `events`**（与 §2「live-only 增量 与 durable 终态 分离」一致）；
  `events.seq` 与 eventbus 全局 seq 同源（或落库后回填），供 `/sessions/:id/history` 与 WS 实时通道的
  after/快照续传共用。
- **密钥加密**：`providers.api_key_enc` 首版可先用环境变量/配置注入 + OS keyring 的简化等价物（如
  本地 master key 文件）；单操作员定位下以「自托管 daemon 密钥文件 + 文件权限」为常态，无需多用户密钥
  分发/secret 管理位（决策 v3③；codeg 桌面用系统 keyring、服务端用 token 鉴权），避免首版引入外部依赖。
- events/runs 增量为零，历史查询走 `events` 表回放（可分页）。

### 4.8 前端架构（M1 冻结面，`frontend/apps/panel`）

```
apps/panel/src/
├── main.tsx / app.tsx        # 入口 + 路由骨架（react-router：/agents /sessions /runtimes /settings）
├── api/                      # REST client（fetch 封装，VITE_API_BASE）＋类型（与 §4.1/§4.4 对齐）
├── transport/                # 实时通道 client（§4.5）：ws.ts（唯一，attach/snapshot/重连）
├── stores/                   # 会话运行时 store（zustand）：按 session_id 维护消息/增量/终态
├── pages/
│   ├── agents/  runtimes/    # 列表页（M1 只读，M2 增 CRUD/注册向导）
│   ├── sessions/             # 会话列表 + 详情（composer → POST prompt → WS 流式渲染）
│   └── settings/             # （M2）自定义 agent / provider 配置
└── components/
    ├── message/              # 渲染 text/thought/tool_call/error 各 type 的卡片（对照 codeg message/* 组件族，M1 只做 text+thought+tool+error 四类）
    └── layout/
```

**会话运行时 store 关键语义**（对齐 codeg `conversation-runtime-store.ts` 的职责，但只保留 Web 单面板子集）：
- 面板经 WS 主通道 attach 会话：连接即订阅（`since_seq` 缺省 → 收 `snapshot` 冷挂载），随后收增量事件；
  消费游标（`seq`）从 `/sessions/:id/history` 尾部或 snapshot 的 `event_seq` 初始化；
- live 增量按文本段就地追加：带 `msg_id`（决策 v5①）并入同 kind/同段，否则同 kind 尾拼接；整轮终态由
  `turn.ended / turn.error / run.cancelled` 收口（片段随之定稿为不可变，可折叠/重放）；事件按 `seq`
  幂等去重（`applyEvent` 前置 `seq ≤ lastSeq` 丢弃，只前进不回退，决策 v5⑤）；
- 断线自动重连（指数退避，重连后按上次已应用 seq 重新 attach，服务端按快照/重放二分补发），重连期间
  UI 显示「reconnecting」条；终态缺失时从 history REST 补齐；
- `turn.error` 渲染错误横幅（重试 = 重新 POST prompt；分类为 auth 时引导去 settings）。
- prompt 发送前**乐观置忙**（`streaming:true`，POST 返回前即禁用 Composer）并做防重入守卫，消除
  「请求在途仍可再发」打到后端 `turn_in_progress` 的竞态；失败时回退非忙以便重试。
- **服务端忙水位**：刷新/重启后本地 `streaming` 会重置，此时以 REST 会话列表 `status == in_progress`
  兜底判定会话忙（本地视图无终态条目时），照常展示「停止」按钮——后端 `cancel` 幂等且能解开
  卡住的 turn（agent 无响应时有界等待后本地解除），避免「看不到停止按钮 → 只能重启」的死胡同；
  收到终态事件后自动刷新列表使状态收敛。
- 前端 **不做**：编辑器/终端/文件树/Split View（对照 codeg workspace/layout 组件族，见 §5.4）。

### 4.9 M0/M1 之外的演进位（M2/M3 骨架预留，详 §5.4/§6）

- 自定义 agent 注册向导（definitions 目录 + 校验可启动 + capabilities 探测——对齐 codeg `custom_registry.rs`，
  细节与优先级见 §5.3）；
- provider 配置注入与 cascade（per-agent env key 映射表展开注入 agent 子进程 + fingerprint stale 提示重启——
  对齐 codeg model_provider，M2 只做「注入面」，细节见 §5.3）；
- Agent 发布/版本（draft→release，架构文档 §6.3）；
- 会话导入（读取 runtime 原生会话存储：dsh `~/.dsh/sessions/<munged-cwd>/<uuid>/` jsonl、opencode 单文件
  SQLite `opencode.db`——对齐 codeg `src/parsers/` 每 agent 一 parser 的思路，M3 后评估；wire transcript
  是通用兜底，见 M1 #5 与 §5.3）；
- MCP **数据管理**位（决策 v3①②/v4④）：平台管理 MCP 服务器定义/注册与注入面——统一 canonical spec 采用
  判别联合：stdio(`command/args/env`)、sse/http/streamable_http(`url/headers`)，含 enabled/toggle/
  test-connection/OAuth 状态（login/logout/check-status）/original_json 保真 + per-agent 子集 +
  会话级启用集（「仅建会话时注入」）；harness 原生不读 MCP 配置，须经 ACP `session/new` wire 送达
  mcpServers、拒 sse（对齐 §5.3，传输支持随 runtime 能力裁剪）。工具执行由 runtime 完成，
  ark-forge 不实现 MCP 客户端。→ P1 评估；
- skills **数据管理**位（决策 v3①②/v4④）：技能包的存储/分发/启用数据面（集中仓 + symlink 启用 + 声明式
  SkillStorageSpec）；skill 解析与执行归 runtime。最小闭环 = **注册与目录管理**（import/scan/路径检测，
  与「物化下发」分离）+ **会话级技能快照 + 聊天头指示器**（面板只读快照、不复算执行态）。→ P2 评估；
- automation **cron 数据契约**（决策 v4④，P1）：先冻结数据实体——`schedule`（kind at|every|cron，5 字段
  expr + tz）× `target.payload.message` × `execution_mode: existing|new_conversation` ×
  `state{next_run,retry}` + runNow；调度执行归 runtime（single-run 约束下「续会话 vs 新会话」两种执行
  语义天然成立），平台只做建/停/查。
- 会话数据管理 UI 面（决策 v4②，M3）：侧栏分组读模型 + 游标（置顶/项目目录/时间线）、软归档切面、
  全局消息搜索 + 锚点、批量导出——wire transcript 为导出事实源，不另造导出存储。
- 用量/日志统计（决策 v4②粒度）：`runs.usage_json` 按 per-turn 明细（input/output/thought/cached）+
  会话快照落库 + stderr 日志尾巴（对齐 codeg `stderr_tail.rs` 诊断思路）。

## 5. codeg 能力对照与取舍矩阵

> 本节的 codeg 结论来自对其源码的逐文件精读（标注文件），作为「平台 vs 桌面应用」取舍的证据。

### 5.1 三条可整体移植的工程纪律（codeg 精读最大收获）

1. **状态迁移 = 带预期状态的 CAS；事件与迁移同事务写**（证据：`db/entities/work_task.rs` 状态机注释、
   `work_task_event` append-only 与迁移同事务；delegation/automation 同款纪律）。→ ark-forge 的
   Session/Run/Agent 状态机（§4.1）从 M0 起就按「CAS 迁移 + 事件同事务」实现，这是未来多进程/多租户安全的前提。
2. **一切长时间行为 = 可跟踪条目 + 事件索引 + reconcile 兜底 + boot 恢复**（证据：work_task 引擎 30s
   reconcile、interrupted→failed 幂等恢复、merging 按 git truth 恢复；automation claim_due 同事务原子触发）。
   → ark-forge 的 Run 执行态需要「进程内注册表 + 超时 reconcile + daemon 重启恢复」，M2 起显式建模。
3. **业务核心与 transport 解耦（`_core` 函数 + 双壳）**（证据：AGENTS.md/CLAUDE.md 架构自述：
   `_core` 后缀函数接收普通引用参数供 Tauri command 与 Axum handler 共用）。→ ark-forge 已天然是
   「daemon 核心 + REST/WS 壳」；保持 `handler.Service` 接口与 manager 分离即可持续获得该属性。

### 5.2 服务端形态、事件流与前端同步（codeg WS 形态 → ark-forge 采纳要点）

来源：`bin/codeg_server.rs`、`commands/remote_proxy.rs`、`web/ws_attach.rs`、`acp/event_stream.rs`、
前端 `acp-connections-context.tsx` / `stores/conversation-runtime-store.ts` / `stores/canvas-store.ts`。

**服务端形态（命令与推送分路）**
- codeg 桌面与独立 server **跑同一个 axum router + WS + AppState**，差异仅是事件发射方式（Tauri app.emit
  vs 直接持 broadcaster/bus）——印证 §5.1③「核心与 transport 解耦」。
- **命令走 REST `POST /api/{command}`**（Bearer token，无信封：命令名即 URL、入参即 body），**推送走单一
  WebSocket `/ws/events`**；浏览器握手不能自定义头，token 经子协议 `codeg-token.{base64url}`。
  → ark-forge **采纳同一形态**：REST 命令 + WS 事件推送，WS 为唯一实时通道（决策 v2①，§4.5/§4.6）。
  子协议方案在 ark-forge 若需对外暴露（单操作员 token，决策 v3③）时复用
  （`forge-token.{b64url}`，浏览器与非浏览器一致）。
- codeg 教训一：早期全局 JSON 广播迫使前端做 receiver 去重与「未映射事件缓冲」，后来改为
  **per-connection attach**。→ ark-forge 的事件必须携带 `session_id/agent_id` 供前端按会话过滤，并把
  「续传口径」做成 first-class（快照/重放二分，见下），否则会重演去重噩梦。

**最值得抄：快照/重放二分的事件订阅协议（`web/ws_attach.rs` + `event_stream.rs`）**
- 每会话单调 `event_seq`；客户端带 `since_seq` 订阅：无游标或落后过多 → 服务端回整包 **snapshot**
  （`LiveSessionSnapshot` + 当前 seq）；缺口小（≤32 条且在环形缓冲内）→ **replay** 事件批 + 高水位；
  服务端**持锁原子「决策 + 注册订阅」**，杜绝快照与首事件之间的竞态。
- 环形缓冲双上限（条数/字节，超限清空 → 下次 attach 自动走 snapshot）；前端 apply 幂等：`seq <=
  lastAppliedSeq` 直接丢，游标只前进不回退。
- → ark-forge：一次 WS 连接上的 attach 即一次「带 since_seq 的订阅」。`since_seq` 落后于缓冲淘汰点或
  daemon 重启时，先推整包 snapshot 再推增量；否则按 eventbus history 回放 + live。eventbus 的 history
  需补 **per-aggregate 容量上限 + 淘汰即快照回退** 语义（M1/M2 落地，见 §5.7）。

**seq 纪律与前端消费**
- 事件信封扁平带类型标签 `{seq, session_id, type, ...}`，seq 幂等去重；apply 只前进。
- 高频 delta（ContentDelta）先入队，**16ms 或攒批后一次 flush** 进 reducer（按会话分组、相邻同型同父先
  拼接）——面板逐 token setState 必卡，照抄该批量纪律（量级可更小）。
- 列表/状态类（会话列表、agent 状态）与「会话流」分路：单独列表级订阅或轻量轮询，避免每个列表页消费
  全量流（对应 codeg `conversation://changed` 侧信道思路）。
- 桌面特有可省略：attach/detach 多路复用与逐订阅 forwarder + epoch 清理、lagged 重连风暴处理（ark-forge
  单面板单订阅连接自然规避）、viewer 跨客户端协作、Tauri app.emit 双通道。
- codeg 对「out-of-turn 活动」用 `acp/background_watch.rs` 反代（tail Claude Code transcript 补 ACP 缺陷）；
  ark-forge 首版**不做 transcript 逆向**——把这类活动当协议问题：需要时要求 runtime 走 ACP notification
  或显式上报，否则如实展示「该 runtime 不提供此事件」。

### 5.3 可扩展面：自定义 agent / 模型提供商 / MCP / skills / 会话导入（精读证据）

来源：`commands/custom_agents.rs`、`acp/custom_registry.rs`、`acp/remote_registry.rs`、
`commands/model_provider.rs`、`db/entities/custom_agent.rs`、`commands/mcp.rs`、`commands/custom_skills.rs`、
`src/parsers/`（前端 parser 目录）、`acp_transcript.rs`。

**自定义 agent（注册 → 校验 → 纳入同等待遇）**
- 统一结构 `AcpAgentMeta{agent_type, supports_mcp, name, description, distribution}`；内置 = 编译期手写
  常量，自定义 = DB 行，**两者共用同一 distribution 结构**（npx / uvx / binary{cmd,args,env,platforms,
  url,sha256,dir_entry}），自定义的 spec 与公开 ACP registry 的 distribution 对象**逐字兼容**（零翻译层）。
- hydrate 流程：`validate`（id 字符集、不撞内置、version 可作目录名防缓存逃逸、防路径穿越、本机平台有发布、
  渠道非空；**坏行只跳过 + 告警，绝不拖垮整批/启动**）→ `build_meta` → fingerprint（不变则复用，零分配）
  → 发布进进程级注册表 → **无需重启即可启动**。
- 内置模型目录（opencode_catalog / codex 目录）是**离线快照**：解析链恒为 live → 磁盘 cache（TTL 24h）→
  编译期快照——ark-forge 若要「provider/模型选择」位，用同一三段式降级，快照经 go:embed 进二进制。

**模型提供商（provider → env 注入 cascade）**
- provider 行 `{id, name, api_url, api_key, agent_type(唯一归属，不可变), model}`；codeg 明文存 SQLite 仅
  展示层掩码——ark-forge **不照抄明文**，改加密密钥文件/OS keyring 等价物，行内只留引用（§4.7 已定）。
- 更新 cascade：改 url/key/model → 按 **per-agent env key 映射表**展开注入 spawn env（dsh→
  `DEEPSEEK_ACP_MODEL`、codex→`OPENAI_MODEL`…），并按 adapter 需要同步 agent 原生配置文件；改后计算
  fingerprint，**给运行中会话标 stale**，前端提示「N 会话需重启」。→ ark-forge M2 只做
  「env 注入面 + stale 标记」，原生文件双写按 adapter 需要再补。

**MCP 与 skills**
- codeg 无中心 MCP 库，以各 agent 原生配置文件为真源 + 统一 canonical spec `{type: stdio|http|sse, command,
  args, env, url, headers}`；每 agent 一对 read/upsert/canonicalize 翻译器。
- **对 ark-forge 的直接相关**：harness/deepseek-acp 原生**不读** MCP 配置——codeg 自记 `$DSH_HOME/mcp.json`，
  建会话时经 ACP `session/new` 的 `mcpServers` **线上送达**（传输仅 stdio/streamable-http，拒 sse）。
  → ark-forge 若做 MCP 位，对 harness 必须走 ACP wire 而非本地配置文件。
- skills：集中仓 `~/.codeg/skills/<id>/SKILL.md`，「启用」= symlink/junction 链到各 agent 技能目录；
  声明式 `SkillStorageSpec{global_dirs, project_rel_dirs, kind}`；dsh 技能位 `$DSH_HOME/skills` + 项目
  `.dsh/skills` / `.agents/skills`（深挖至最近 .git）。
- **ark-forge 立场（决策 v3①）**：以上 MCP/skills 机制只取「**数据/配置管理层**」——canonical spec 定义、
  服务器/技能包注册、per-adapter 翻译与注入/送达；**执行**（MCP 工具调用、skill 解析与运行）一律由
  runtime 原生完成，ark-forge 不实现运行时能力。

**会话聚合/导入（src/parsers/）**
- 每 agent 一个 parser（统一 `AgentParser{list_conversations, get_conversation}`）发现/解析**原生存储**：
  dsh = `~/.dsh/sessions/<munged-cwd>/<uuid>/`（消息 jsonl；图片内容寻址存 `~/.dsh/attachments/v1/objects`）；
  opencode = 单文件 SQLite `~/.config/opencode/opencode.db`（含 task 子 agent 关联）。
- 导入管线：全机扫描 → 按 (agent_type, external_id) reconcile（去重/原地刷新/soft-deleted 不复活）→
  落 `status=PendingReview("待审查")` 隔离「外来会话」与自建会话。
- 自定义/无 parser agent：codeg 自录 **ACP wire transcript**（`acp_transcript.rs`：每 session 一个 JSONL，
  header + prompt/update 行，schema v1）即可恢复任意 ACP agent 历史——ark-forge 若从 M0 起把每 session 的
  prompt/update 原语落盘，天然获得同能力（会话导入的通用兜底，见 §5.7）。

**Go 侧分层优先级建议（映射 M0–M3/P1）**：① registry（AgentSpec + 自定义 CRUD + hydrate + preflight）→ M2；
② runtime adapters（spawn env / ACP 会话管理 + env key 注入 + fingerprint）→ M0/M2；③ model-provider
（cascade + stale）→ M2；④ mcp 数据管理（canonical spec + 翻译器；harness 走 wire；执行归 runtime，
决策 v3①）→ P1；⑤ sessions/import（dsh/opencode 两 parser + reconcile）→ P1–P2；⑥ skills 数据管理
（集中仓 + symlink + SkillStorageSpec；执行归 runtime）→ P2；
⑦ 远端 ACP registry 浏览 → P3。②③④⑤⑥ 全部挂在 **adapter 接口**之后、daemon 只懂统一结构——这正是
「可插拔 runtime」的平台价值所在。

### 5.4 能力取舍矩阵（ark-forge 语境）

| codeg 能力 | ark-forge 落法 | 阶段 | 理由 |
|---|---|---|---|
| 双模式同核（`_core` + EventEmitter） | **直接采用**：daemon 核心 + REST/WS 壳 | M0 起 | ark-forge 即 codeg server 模式的子集 |
| WS 事件订阅（attach / snapshot / replay，`web/ws_attach.rs`） | **采用为唯一实时通道**（§4.5/§4.6），不设 SSE 出口 | M0（端点）→ M1（面板） | 双向 + 鉴权（子协议）+ 快照/重放 first-class；codeg 自身亦无 SSE 实时通道 |
| ACP 连接管理（registry/manager/connection/session_state） | 移植思路到 acl/runtime/manager（§4.2/§4.3） | M0 | 同命题 |
| 事件状态机纪律（CAS+同事务事件+单例引擎+文件锁） | 采用 | M0 起 | 多租户安全前提 |
| 错误分类与可操作提示（feedback.rs） | 采用（turn.error + 错误码） | M0/M1 | 前端横幅/重试依赖它 |
| 自定义 agent 注册（custom_registry + 预检 initialize + 能力冻结） | 采用（Agent 定义 CRUD + 校验向导） | M2 | 「不写代码接入新 runtime」是平台定位核心卖点 |
| model_provider 配置 | 采用注入面（env 模板替身注入 agent 子进程） | M2 | harness 的 DEEPSEEK_API_KEY 即用例 |
| @协作 delegation（broker/spawner/独立子 ACP 会话 + one-shot/resume + 级联 cancel + 深度限制） | **不做（决策 v2②）**：当前定位无跨 agent 协作，一次运行只指定一个 runtime；该机制在定位扩展前不进路线。其在「CAS/事件同事务/异步跟踪」上的纪律已由 §5.1 吸收 | — | 定位约束；避免为「平台卖点」提前堆协作复杂度 |
| 任务板 todo→…→review→merge（worktree 隔离 + git truth 校验） | P1 候选：服务端 repo 副本/分支隔离 + 产物 diff 展示 + 人工采纳（review 门保留）；每次运行仍单 runtime 指定；merge 自动合入延后 | P1 | worktree 模型天然适配服务端沙箱；远端 forge 集成成本高 |
| automation（composer 快照重放 + cron/on-demand + LaunchSession/EnqueueTask） | P1 候选，与任务板同期；动作只留 LaunchSession（单 runtime 会话） | P1 | 纯服务端逻辑可搬 |
| chat channel / 远程驱动（webhook/IM bridge） | **不做（决策 v3③）**：多人/远程协作语义不在当前定位，接口不留排期位 | — | 无多用户场景则无远程驱动价值；定位扩展前不进路线 |
| 编辑器/终端/文件树/Split View/canvas 桌面体验 | **不做**；Web 只做只读文件树 + 变更 diff 页帮用户验收 | — | 桌面专属；编辑归 agent/外部 IDE |
| background_watch（transcript 反代） | 不做；当成协议问题 | — | 避免逆向耦合 runtime 私有存储 |
| MCP/skills **数据管理**（定义/注册、注入/送达、技能包分发；执行归 runtime，决策 v3①） | M3 后评估（MCP 能力位已在 acl.caps 预留） | P1+ | 数据管理层，不实现运行时能力；优先级低于 M0–M3 主线 |

### 5.5 定位差异一句话

codeg 是「**个人/团队的 agent IDE**」——价值在桌面交互深度，甚至多 agent 同场协作；ark-forge 是
「**可自托管的 agent 底座**」——价值在**统一拉起、管理、切换指定**多 runtime（一次运行一个 runtime，
无跨 agent 协作），并通过 REST + WS 把能力开放给任意前端。凡 codeg 中「依赖本地桌面形态」或
「依赖多 agent 协作」的能力，ark-forge 一律降级或不做；凡「服务端/协议层」的纪律与机制，直接吸收并可能提前。

在此基础上，定位边界由**决策 v3** 明确为三点：
- **能力不重造**：skills/MCP 等运行时能力复用底层 runtime 原生实现，ark-forge 只管理其数据与配置；
- **数据管理是平台职责**：会话、skill、MCP 三类数据由平台统一管理（管理面，非运行时面）；
- **无多人协作**：单机单操作员，多用户/鉴权登录/聊天远程通道不在路线——与决策 v2② 的「无跨 agent
  协作」叠加为「一个操作员 × 一次运行一个 runtime」。

### 5.6 codeg ACP 内核 → ark-forge acl/runtime/manager 迁移清单（精读证据）

来源：`codeg/src-tauri/src/acp/`（registry.rs / manager.rs / connection.rs / session_state.rs / types.rs /
error.rs / binary_cache.rs / preflight.rs / idle_sweep.rs），要点与 ark-forge Go 对应：

**注册表（registry.rs / binary_cache.rs）**
- codeg 以**静态注册表**描述每个内置 agent：分发三通道 `Npx{package@ver,cmd,args,env} /
  Binary{url,sha256,platforms} / Uvx`；平台 URL 矩阵；pinned 版本是编译期常量，靠断言测试兜底、无 CI 自动升级。
- 缓存布局 `<cache>/<registry_id>/<version>/<platform>/<cmd>`，按语义化版本取最高已缓存；下载后 sha256 校验、
  临时文件+rename 原子落盘。
- 启动前 `preflight`（node/uv/binary/PATH 探测）给出结构化 `PreflightResult{checks,fixes}`。
- **ark-forge 对应**：opencode 与 deepseek-harness 均**原生 ACP**（无需 codeg 为 claude/codex 引入的
  adapter npm 包），注册表退化为极简静态表：`{name, launch{cmd,args,envTemplate}, capabilityBaseline}`；
  但「静态表 + pinned 版本 + 结构化 preflight + 原子安装」的**形状直接采用**，作为 M2 自定义注册的底座。

**连接驱动（manager.rs / connection.rs）——每连接一个 driver + 命令通道**
- `spawn_agent`：以 `(agent, working_dir, session_id)` 为 key 去重（同一外部会话已有存活连接则复用——
  应对浏览器刷新重挂载），新建时用 `SessionStarted` oneshot 握手 + 超时。
- 每个连接 = `{ cmd_rx: mpsc<ConnectionCommand>, state: Arc<RwLock<SessionState>>, prompt_lock,
  child_pid }`；driver 是 `tokio::select!` 轮询 `cmd_rx` 与 update 流；prompt 先置 `turn_in_flight` 再入队，
  并发第二个 prompt 返回 `TurnInProgress`。
- initialize：60s 超时；**能力在 init 时刻钉死**（load/fork/resume、prompt_capabilities、mcpCapabilities）。
- session 建立按能力**阶梯**：session_id 存在且支持 → resume（不重放历史）；否则 → load（重放历史）；
  load 失败再分类（不可恢复 → 报错；可恢复 → 静默 fallback new + 历史链接）。
- cancel：发 CancelNotification → 释放本会话终端 → drain 权限队列 → **立即发 TurnComplete{cancelled}**
  （不等 agent）→ 把 in-flight prompt future 放后台 drain（防 SDK dropped-receiver/goroutine 泄漏）→
  级联取消子任务；连接本身存活。**Go 难点即此处的取消竞态**（drain 与 TurnComplete 需同一临界区）。
- fork：同进程内把 ForkSessionResponse 转挂为 NewSessionResponse（`attach_session`），换 transcript 继续跑，
  不重启进程；fork 支持与否来自 init 时钉死的能力。
- 「重连」= 崩溃/刷新后由管理器按 (agent,dir,session_id) **重开进程**走 resume/load/new 阶梯，无进程内自动重连。
- 进程树清理：优雅窗口 → `kill(-pgid)`（Go `SysProcAttr{Setpgid:true}`；Windows taskkill /T）；pid 只在真
  reap 后归零，防误杀已被 OS 复用的 pid。
- 空闲回收（idle_sweep.rs）：周期 sweep 只回收 `Connected` 且无 pending_permission 且无后台任务、
  `last_activity_at` 超时的连接；桌面靠前端 touch 保活，daemon 形态可用「活跃订阅/turn 活动」替代。

**状态与事件（session_state.rs / types.rs / event_stream.rs / lifecycle.rs）**
- 每连接一个**单写者权威状态**（SessionState reducer，`apply_event` 是唯一写入路径），文本/思考增量追加进
  live message，tool_call 按 id upsert，TurnComplete 沉淀末尾文本并清 live 区。
- 统一事件 envelope `{seq, connection_id, …}`，seq 全局全序；`to_snapshot()` 供冷挂载、
  `recent_events_after(seq)` 环形缓冲供带游标热挂载——**多客户端控制面最值得抄的机制**，与 ark-forge
  eventbus 的 history/after 同构，可加「快照式冷挂载」（连接建立时先给 snapshot 事件再给增量）。
- 持久化与高频事件解耦：bus → 过滤出 lifecycle 相关 6 类 → per-connection worker（重试退避）写 DB；
  ContentDelta 等高頻事件不过 worker。
- 错误三件套：stable `code`（机器可读）+ **净化后**的消息（剥 spawn 路径/本地路径/悬空逗号）+ `terminal`
  布尔（连接级死亡 vs turn 级可继续）；另有 AIR 词汇表 `category{connection,access,limit,request,service,
  unknown}` + `actions{retry,login,new_session}`。→ ark-forge 直接照搬此词汇（§4.2 错误分类）。

**ark-forge 省略项**：桌面窗口 touch 保活、Tauri/WS 事件桥、反向 `fs/* terminal/*` host 通道（permission
通道仍要）、Claude background_watch transcript 尾随、Cursor/pi/grok 等大量形状 hack（ark-forge 只有
opencode/harness 两形状）。

### 5.7 对齐修正（写文档时对 §4 的增补）

- §4.2 `Client` 增加 **snapshot 语义**：连接建立时先产出 `session.snapshot`（冷挂载）再进增量——事件契约补
  `session.snapshot` type（M1 若不做整流持久化，可用它做「刷新即续看」的廉价方案：history 由 manager 内存
  快照承载）。
- §4.3 `SessionOrchestrator` 显式实现 **resume/load/new 阶梯**与**单飞 turn 门**（并发 prompt 返回
  `turn_in_progress`），而非简单「fresh session」直通——即便首版 harness 只支持 fresh，opencode 侧阶梯
  从 M0 就按此实现，避免二次返工。
- §4.4 REST/错误契约：失败响应与 `turn.error` 事件统一携带 **稳定机器码 `code` + 脱敏 `details` +
  `actions{retry|login|new_session}`**（AIR 词汇表，§5.6）；i18n 文案留前端（对照 codeg 错误三件套）。
- §4.5 实时通道（仅 WS，决策 v2①）：`after`/`since_seq` 续传为**快照/重放二分**——落后于缓冲淘汰点
  （或 daemon 重启）先推整包 `snapshot` 再推增量，否则回放 + live（对齐 §5.2 的 attach 协议）；eventbus
  history 增加 per-aggregate 条数/字节双上限，超限淘汰并把「该聚合已不可增量续传」记为快照回退信号。
  事件必须携带 `session_id/agent_id`（现 agg_id 已含），前端按会话过滤而非全量消费。鉴权：默认无
  （决策 v3③，单操作员）；若对外暴露，WS 子协议 `forge-token.{b64url}`、REST 走 header（单操作员 token）。
- §4.8 前端：会话运行时采用**双层结构**（连接/活状态层 + 时间线渲染层分离，按最小 slice 订阅）与
  **批量 flush**（高频 delta 16ms/攒批一次进 reducer）、seq 幂等去重（`seq <= lastAppliedSeq` 丢弃、
  游标只前进）——面板流式渲染不卡的纪律，见 §5.2。
- §4.9/§6 M1：**每 session 的 prompt/update 原语落盘**（ACP wire transcript，JSONL），作为跨 agent
  会话导入/恢复的通用兜底（对齐 codeg `acp_transcript.rs`），避免日后为每个 runtime 写原生存储解析器。
- §4.3/M2 连接复用：spawn 以 `(agent, workspace, session_id)` 去重 + `SessionStarted` 握手（对齐 §5.6），
  防浏览器刷新/多标签双连；空闲回收以「活跃订阅/活动 turn」替代桌面 touch 保活。
- §4.2 错误分类命名对齐 codeg `feedback.rs`/AIR：分类词汇（connection/access/limit/request/service/unknown）
  在 acl 层产出、manager 语义化、实时通道原样透传，前端只做展示与动作分发。
- §4.1/§4.3 运行时语义（决策 v2②）：Session/Run 创建时即绑定唯一 Agent/Runtime，会话期内不换 runtime；
  删除一切「跨 runtime 委托/协作」相关接口与事件的演进位（delegation、子代理卡片、级联 cancel 的子任务面）。

### 5.8 参照 AionUi（应用型对照物，只读）——管理面蓝本与不借鉴清单

- 参照仓库：`/Users/morehao/Documents/study/ai-agent/AionUi`（iOfficeAI/AionUi，Apache-2.0，v2.2.1 快照；
  2026-09-04 只读调研，本仓库未动）。下文行号为该仓库相对路径，读于当日。
- **一句话分层**：AionUi = Electron/Web 壳 + React UI，经 REST/WS 调**外部 aioncore 引擎**
  （`packages/desktop/src/common/adapter/ipcBridge.ts` 自述「replaces the original IPC bridge calls with
  HTTP REST and WebSocket calls routed to aioncore」）；外部 CLI agent（Claude Code/Codex/…）经 **ACP**
  接入（readme.md:186），协议形状镜像于 `common/types/platform/acpTypes.ts`；会话/引擎/skills/MCP/cron
  的**执行实现均在外部 AionCore 仓库**（readme.md:713），本仓库只见数据模型、API 面与 UI。
  即：AionUi 与 ark-forge 同为「薄控制面 + 外部宿主内核」，但它是「自带引擎的应用型工作台」——
  其**数据/配置管理层**可当蓝本，引擎/协作/远程层不借鉴。

| AionUi 数据管理面形态（证据） | ark-forge 落点 |
|---|---|
| 权限 = 独立 confirm 通道：permission 消息（call_id/options）+ confirm REST（always_allow）+ approvals/check 策略记忆 + allow/reject×once/always 语义（`ipcBridge.ts:513-529`、`chatLib.ts` IConfirmation、`chat/approval/ApprovalStore.ts`） | **已落地（轻量化）**：ACP `session/request_permission` → `session.permission_request` 事件（options 透传）+ `POST /sessions/:id/permission` 决策 + 会话级「始终允许」记忆 + 超时自动拒绝（默认 30min，`FORGE_PERMISSION_TIMEOUT`）。详见 §5.8.1 |
| 会话 = 强类型定列 + extra JSON 快照；侧栏服务端分组读模型（pinned/项目/时间线）+ 软归档（archived_at 切片）+ 全局消息搜索 + 批量导出（`sidebar.ts`、`ipcBridge.ts:2423-2497`） | M3 会话数据管理面（v4②） |
| 会话 runtime 摘要状态机：ensure/restart/cancel/lease + pending_confirmations + can_send，UI 不持进程（`ipcBridge.ts:341-361`、`runtime/agent-process-registry.ts`） | M3 会话态/连接态分离（v4③） |
| MCP：IMcpServer 判别联合传输（stdio/sse/http/streamable_http）+ toggle/test-connection/OAuth/original_json 保真 + per-agent 子集 + 会话启用集「仅建会话时注入」（`common/config/storage.ts:595-640`、`ipcBridge.ts:1205-1269`） | P1 MCP 数据模型（v4④） |
| skills：注册与目录管理（import/scan，与物化下发分离）+ 会话级快照 + 聊天头指示器（`ipcBridge.ts:918`、`ConversationSkillsIndicator.tsx`） | P2 skills 最小闭环（v4④） |
| cron：ICronJob schedule at/every/cron+tz × target.message × execution_mode existing/new_conversation × state（`ipcBridge.ts:1629-1705`） | P1 automation cron 数据契约（v4④） |
| usage：TokenUsageData 明细+成本随会话快照 + usage_update 事件（`storage.ts` last_token_usage） | M3 runs.usage_json 粒度（v4②） |
| 消息 = msg_id 幂等 + 类型化合并（append 默认、`replace:true` 才替换；tool 按 call_id 原位合并；
  `hooks.ts:293-323`、`chatLib.ts:369`、`acpTypes.ts:143-156`） | WS 载荷透传 `msg_id` + 前端同段归并
  （决策 v5①）；其「无游标 + DB 权威补拉」传输方式不借鉴 |

#### 5.8.1 已落地「权限确认通道 + 轻量管理面」（轻量化决策，2026-09-07）

用户明确「不想做得太重」，scope = 单二进制最小版 + 权限确认通道 + 轻量管理面（前端技术栈不变：
保留 React + antd，不做零构建重写）。据此从 AionUi 蓝本中**优先引下两件事**，其余仍按 §6 M2/P1/P2 规划：

**① 权限确认通道（ACP 一致）**——链路与映射：
- agent 在 Prompt 中途发 `session/request_permission`（SDK 在其读循环派发的 goroutine 中处理，阻塞不卡读循环，
  已核实 acp-go-sdk `connection.go` 的 `go func` 分发 + acp_test.go「Prompt 内调 RequestPermission 不 deadlock」）。
- `hostClient.RequestPermission` 归一化为 `acl.PermissionRequest`（tool 信息 + options），调用方注入的
  `RequestPermission` 回调**阻塞等待宿主决策**；`acl.PermissionDecision{Allowed, OptionID, Always}` 折叠回
  ACP `Selected`/`Cancelled`。
- `internal/manager.PermissionManager`：按 `call_id` 注册挂起 → `session.permission_request` 事件（含 options）→
  阻塞等 `ResolveAllow/ResolveReject`（经 `POST /sessions/:id/permission`）→ 超时（默认 30min）自动拒绝；
  `allow_always` 记录「action+identifier」会话级记忆，同键后续请求走快速放行；`session/cancel` 按 ACP 规范
  回 Cancelled 全部挂起。
- 前端：流式正文渲染**权限确认卡**（选项 allow/reject × once/always 按钮 / 无选项时的 允许本次·始终·拒绝 兜底），
  点选经 REST 回传并置灰；聊天头显示待决权限角标。

**② 轻量会话管理面**：
- 会话元数据：`title`（首条用户消息自动推导，可 `PATCH` 覆盖）、`pinned`、`archived`、`created_at/updated_at`，
  经 `GET /sessions` / `GET /sessions/:id` 暴露；`PATCH /sessions/:id` 做元数据子集更新。
- runtime 能力基线（capabilities）只读暴露在 `GET /runtimes`；前端侧栏按「置顶 / 今天 / 昨天 / 更早」分组 + 置顶。
- **会话重启存活（轻量，2026-09-07 增补）**：daemon 启动回读 transcript 目录，重建会话列表与元数据
  （标题/状态/时间，状态由最后一条终态事件推导），并把历史事件注入 eventbus——管理面在重启后仍有会话可见、
  旧会话可经 WS/history 回看。**仍不引入 DB**（落库属 M2 的 events 表/快照兜底）；transcript header 仅在文件为空时
  写入（重启 resume 不重写 header，保持「首行=header」不变式）。
- 权限超时自动拒绝的前端收敛：请求事件携带 `timeout_at`，前端按该时刻定时把未决卡标记收敛（不发 API）；
  会话终态/取消事件也会就地收起未决卡。
- 不做：skills / MCP / cron 的**数据 CRUD**（当前 backend 无此类数据源，虚构会失真；仍按 §6 P1/P2 规划），
  **不引入 DB 的会话/事件持久化**（transcript 落盘 + 重启回读 = 轻量存活；events 表落库属 M2）。

**验收**：`make all` 单二进制 33MB 构建通过；`go test ./...` 全绿（含新增 `internal/acl` 端到端权限集成测试、
`internal/manager` permission 单元测试）；真实 opencode ACP 会话跑通（tool_call→tool_update→thought/message→
turn.ended），title/置顶等管理端点实测生效。

**发起问答前选择底座（面板交互校准，决策 v6）**：AionUi 的「新建会话」= **/guid 整页**（index 恒重定向：
`Router.tsx:75-79`；侧栏新建 → `/guid resetAssistant`：`Sider/index.tsx:50`）+ 顶部**助手胶囊栏**
（头像+名+底座 key `data-assistant-backend`、溢出 More + 搜索：`AssistantSelectionArea.tsx:224-324`）→
选中后按该 runtime 能力把 model/权限 mode/thought level 装配进**输入卡**（`GuidPage.tsx:592-604`、
`GuidActionRow.tsx:632-650`；选项来自握手目录快照 `agentRuntimeCatalog.ts:155-232`，非前端硬编码）→
create 仅带 `assistant.id + overrides`（`useGuidSend.ts:162-195,233-249`），**CLI(ACP) 底座未显式选模型则
不传 model**、交 CLI 自身默认（`useGuidSend.ts:140-171`）；会话**不可中途换底座**（会话 = 按 runtime
判别联合 + assistant 快照：`storage.ts:154-160,237-296`），与 ark-forge「一次会话绑定一个 agent runtime」
（v2②）一致。
ark-forge 落点（决策 v6②③，面板已落地）：① 新建会话 = **整页 `/new`**（无弹窗；hero + 助手胶囊条 +
内嵌输入卡[模型 pill 只读 + 圆形发送] + 示例指令，对照 AionUi /guid 形态）；② 默认选中**上次底座**
（localStorage `forge.panel.lastAgentId`；AionUi 持久化 lastAssistantId：`useGuidAssistantSelection.ts:92-111`）；
③ 底座选择呈**胶囊卡片**（`StartPage.tsx`，图标 = runtime 缩写 + 状态点，点击即选 + 记忆）；④ 首轮前
标注「模型 = 底座自身默认，不在此选模型」（对应 AionUi「CLI 不传 model」语义 + opencode
ACP 忽略客户端模型选择的实测约束，issue #13644）。

**不借鉴清单**（保持定位边界；AionUi 的这些卖点恰好撞 v2②/v3③/能力不重造）：
1. **内置 agent 引擎**（embedded aionrs + AionCore 全套能力）——与 v3①「能力不重造」相反，ark-forge 能力委托 runtime；
2. **Team 多 agent 协作**（Leader/Teammate + mailbox + 共享任务板）——撞 v2②；
3. **远程与 IM 通道**（WebUI remote / Telegram / Lark / DingTalk / WeChat，users/teams 表、QR+JWT 多端）——撞 v3③；
4. **扩展 Hub 生态**（extension contributes + tarball+SHA-512 + permissions/risk 门禁）——单操作员自托管优先级低；
   仅未来若做「现成 agent/mcp 目录」再参考其 integrity 与版本门禁；
5. **Office/文档垂直应用**（PPT/Word/Excel 助手、多格式预览面板）——应用层，不在底座定位。
6. **assistant 人格/目录层**（头像人格包装、内置办公助手目录、生成式 assistant 与 CLI 1:1 管理
   ——AionCore-internal）——底座定位不引入「人格包装」层；agent 注册表条目只有 name/runtime/state。
7. **ACP 底座上的 model/permission override 交互照搬**——AionUi 会向后端传 `conversation_overrides.model`，
   但 opencode **忽略 ACP 客户端模型选择**（issue #13644，2026-09-04 实测）；会误导「选了没生效」。
   模型只经 runtime 自身配置/启动环境注入（见决策 v6③ 与 §4.3 env 透传）；权限 mode override 留 M2
   （v4①）按 runtime config_options 支持度再议。

## 6. Roadmap 详解（M0–M3）

> 每个里程碑给出：目标、要做的事（按 §4 落点）、不做的事、验收（DoD）、预计工作量级（以单人/周计，粗估）。

### M0 — 打通 ACP 直连（真实会话流）

**目标**：把 mock 数据源替换为真实 ACP 链路：daemon 拉起 runtime 子进程 → initialize → new session →
prompt → 事件流入 eventbus → **经 WS 端点出流**（attach/快照/事件，§4.5）。控制面第一次「说真话」。

**要做**：
1. `internal/acl`：`Transport`(StdioTransport) + `Client`（基于 acp-go-sdk v0.13.x，方法面已核实，§4.2）+
   notification 归一化回调 + 能力断言 + 错误分类骨架。
2. `internal/runtime`：`contracts.go` + `registry.go`（map 注册）+ `opencode/` 与 `deepseekharness/`
   两个 adapter（启动命令/参数/能力基线：`opencode acp`；harness 为 `dsh --profile acp`
   （@deepseek-ai/dsh，统一启动器 profile，旧 `dsh-acp-demo` 已废弃，核实见
   `docs/deepseekharness-acp-adaptation.md`）；DEEPSEEK_API_KEY 随子进程环境透传；
   harness 会话可持久化：list/resume/close、无 load/fork）。
3. `internal/manager`：最简 `SessionOrchestrator`（连接获取→session/new→prompt→cancel/close）实现
   `handler.Service`（替换 mock），把归一化事件 Publish 进 eventbus。
4. `internal/infra/config`：runtime 配置加载（CLI flag/env/配置文件三选一起步）；日志（golib glog）。
5. `server` WS 事件端点：升级连接（无鉴权）→ attach/detach/ping 帧 → 订阅 eventbus → snapshot/event 帧
   （§4.5 基本版）；**清理移除脚手架期遗留的 SSE 端点与适配器代码**（决策 v2①，不留第二出口）。
6. `cmd/forge/main.go` 重装配（去 mock feed），暴露 `--runtime` 开关供本地冒烟。

**不做**：SQLite、Agent CRUD、断线续传的持久化、前端改动。

**实现落点（M0 代码提交）**：1–6 全部落地。其中 acl 层对「agent 自托管鉴权」做直通
（Authenticate methodId 取 `AuthMethodAgent.Id`，opencode 即此形态；env_var/terminal 仍报
`access/auth_required`，M3 补全流程）；验证方式 `forge demo --runtime opencode --allow-all`
（真实 opencode 1.18.27 冒烟：session.created → thought/message 流 → turn.ended）。

**DoD（验收）**：`make server` 起服后，经 WS 客户端（wscat / 小型 Go client / 单测）attach 会话能看到
真实 opencode/harness 会话产生的 token/thought 事件流（快照 + 增量）；`GET /runtimes /agents /sessions`
返回真实数据；对 harness 的 resume/fork 返回「unsupported」；代码中已无 SSE 端点/适配器；
`make build && make vet && make test` 绿。
**风险**：acp-go-sdk 与两 runtime 的握手细节差异（§7 风险清单第 1、2 条）。
**量级**：2–3 周。

### M1 — 面板 MVP（REST+WS 全链路）

**目标**：浏览器完成「选一个 agent（会话固定其 runtime，决策 v2②）→ 发 prompt → 看流式回包 → 中断」
闭环；Sessions 页为主视图。

**要做**：
1. 前端骨架（§4.8）：路由 + `api/` client + `transport/`（`ws.ts` 唯一：attach/snapshot/心跳/重连） +
   `stores/` 会话运行时 store；
2. Sessions 流式页：会话列表 → 详情（composer、消息/思考/工具/错误四类卡片、cancel）；
   Agents/Runtimes 只读列表页；
3. REST 契约按 §4.4 冻结子集（sessions CRUD + prompt/cancel/history）；WS 事件契约 §4.5 冻结
   （与 M0 端点联调）；
4. WS 断线重连（指数退避 + 按 lastAppliedSeq 重新 attach → 服务端快照/重放补发）至少做到
   「浏览器刷新/断网后会话续看」；
5. 会话 wire transcript 落盘：每 session 一个 JSONL（header + prompt/update 原语，schema v1，写后不重写），
   作为续看/调试/未来跨 agent 导入的原始事实源（对齐 codeg `acp_transcript.rs`，§5.3）。

**不做**：Agent 创建编辑、provider 配置、多 tab/复杂 UI、任何跨 agent 协作形态（决策 v2②，codeg 类
shell 均不做，见 §5.4）。

**DoD**：`make web && make all` 产出 `./forge` 单二进制，浏览器直接可用——面板经 **WS** 收到
snapshot + 流式事件完成对话并可中断，刷新后会话续看；两 runtime 均可在面板跑通 fresh session 流式对话。
**量级**：3–4 周。

> **M1 前哨（已落，2026-09-03）**：按 M1 后端子集先行打通真实链路，交付面见提交
> `feat(manager|server|cmd)`（Orchestrator 持久会话多轮 / REST sessions create/prompt/cancel /
> WS attach since=0 全量回放 / serve 装配编排器）：
> - REST：`POST /sessions`（agent_id+prompt，返回 `event_seq` 水位）、`POST /sessions/:id/prompt`、`POST /sessions/:id/cancel`；
>   turn 生命周期脱离 HTTP ctx（`context.WithoutCancel`），取消仅经 cancel 端点/agent 停止；
>   业务错误码 `restErr`（40401 未知 agent/session、40901 turn_in_progress、40101/40501/50201）。
> - WS attach 语义细化：`since_seq` 省略 → snapshot+live（冷挂载）；提供（含 0）→ `EventsAfter` 重放 + live
>   （UI 冷开启既有会话传 0 全量重建视图；断线重连传 lastSeq 增量续传）。
> - Orchestrator：ActiveSession（busy 单飞）句柄 + StartSession/Prompt/CancelTurn；终态分类
>   end_turn→completed，取消类（cancelReq/stop=cancelled/“Request cancelled”/ctx.Canceled）→ run.cancelled；
>   RunOnce（demo）改建于其上。实机冒烟：两 runtime 均经 REST 创建→WS 收流→多轮→cancel 全通。
> - 前端面板骨架随前哨交付（列表/新建/流式渲染/多轮/取消/断线重连），release 内嵌单二进制。
> M1 剩余项（wire transcript JSONL、能力标记裁剪入口、settings/agents 页等）维持 M1 范围。

> **M1 剩余项（已补，2026-09-03，提交见 feat(eventbus|server|manager|panel)）**：
> - **会话 wire transcript JSONL 落盘**（M1 #5，schema v1）：每 session 一个
>   `<sessionID>.jsonl`（首行 header + prompt/update 原语行，append-only 不重写）；目录
>   `FORGE_TRANSCRIPTS`/默认 `~/.forge/transcripts`，serve 与 demo 均落盘；写入失败仅告警
>   （尽力而为旁路事实源，对齐 codeg acp_transcript.rs，§5.3/§5.7）。
> - **REST `GET /sessions/:id/history`**（§4.4 M1 冻结面补缺）：eventbus 按聚合回放，
>   `after`/`limit` 分页（默认 200、上限 1000、`has_more` 翻页），未知会话 40401。
> - **per-agg 历史淘汰 + 快照回退**（§5.2/§5.7 M1 落地）：Bus 保留上限（默认 65536）超限
>   淘汰最旧并抬高该聚合 `EvictedThrough` 水位；WS attach `since_seq` 低于水位 → 回 snapshot
>   （快照/重放二分补齐；M2 落库后由 events 表兜底更久历史）。
> - **面板 Agents/Runtimes 只读列表**（M1 #2 子项）：左栏状态一瞥（agent state 点、
>   runtime version），与 sessions 列表同页。
> 至此 M1「要做」1–5 全部落地（settings/agents 页、能力标记裁剪入口属 M2 #2/#5，见下节）。

> **前端形态补记（2026-09-04，提交见 feat(panel)）**：§4.8 路由骨架补齐——react-router 多页
> `/sessions`（主视图，`/sessions/:id` 直达激活/刷新续看）、`/agents`、`/runtimes` 只读页、
> `/settings` M2 占位；store 提升为 context Provider（单实例 transport 跨页保活，且 transport
> 惰性实例化修复「子组件 effect 先于父组件导致冷加载丢 attach」）；reconnecting 横幅（WS 非 open
> 可见，重连按水位续传）。**浏览器验收（真实 Chrome headless 驱动 release 单二进制）**：新建会话
> dsh 流式文本→turn.ended、composer 多轮回显（完成标记 1→2）、刷新续看、cancel 触达终态、
> /agents·/runtimes·/settings 页、新 tab 冷启动直达 /sessions/:id——全过，无页面错误
> （脚本 /tmp/pbtest/panel-browser-check.mjs 与 focus-multiturn.mjs）。

### M2 — 可扩展底座（不写代码接入第三个 ACP agent）

**目标**：平台化第一步——Agent 定义成为一等可管理资源；runtime 接入成本从「写 Go adapter」降到「配置」。

**要做**：
1. `internal/infra/store`：SQLite schema（§4.7）落地；`events` 表承接 durable 终态，history 分页 API 实装；
2. Agent CRUD（§4.4 契约）+ 自定义 ACP agent 注册：注册向导后端（definitions 目录：命令/参数/env/
   分布来源）+「校验可启动」预检（initialize 探测 + 能力冻结）；前端 settings/agents 页（对照 codeg
   custom_registry 流程：注册→校验→纳入同等对待，§5.3）；
3. provider 配置 CRUD 与**注入面**：Agent config 的 env 模板可引用 provider（`{{provider.api_key}}`），
   拉起子进程时注入（harness 的 `DEEPSEEK_API_KEY` 走此通道）；
4. 连接池化与空闲回收（对照 codeg idle_sweep）、Agent 启停（lifecycle 状态机 running/paused/stopped）；
5. 会话能力标记（`capabilities_json`）驱动前端入口裁剪（harness 隐藏续谈/派生）；
6. 端到端测试基建：adapter 冒烟用「假 ACP server」测试夹具（echo/慢流 agent），与真实 runtime 冒烟分离。
7. **权限策略数据位**（决策 v4①）：permission 请求归一 allow/reject × once/always；confirm 通道（REST
   按 call_id 回执 + always_allow 回灌）+ 策略记忆查询/落库（默认策略 per-agent：deny/ask/allow）；
   前端 permission 卡片与侧栏待批计数——为自定义 agent 的真实工具会话解锁（替换当前默认一律拒绝的
   布尔回调）。

**不做**：多用户/鉴权、用量统计、会话导入、MCP/skills（位预留）。

**DoD**：通过面板注册第三个 ACP 兼容 agent（如某 registry 现成条目或自写 echo agent），不写 Go 代码即可
对话；重启 daemon 后会话终态可经 history 恢复；`make test` 含 adapter 夹具测试。
**量级**：4–6 周。

### M3 — 平台化一期（单操作员底座稳定冒烟）

**目标**：`./forge` 可作为单操作员底座稳定运行：多会话并发、重启恢复、可观测
（决策 v3③：无多用户/团队共享语义，无登录鉴权排期）。

**要做**：
1. 多会话并发与真实恢复：daemon 重启后按 `sessions`/`runs` 恢复终态视图；opencode 会话 load/resume
   贯通；harness（dsh）会话经同一 `$DSH_HOME` 持久化根 list/resume（断连后仍可续，无「过期横幅」
   一说，resume 入口按 caps 裁剪）；切换会话/新建会话时按需**指定 runtime**
   （决策 v2②：一次会话一个 runtime，切换 = 开新会话）；会话态与连接 runtime 态分离建模
   （决策 v4③：status vs ensure/restart/cancel/lease + pending_confirmations，连接可杀后重 spawn
   续会话；daemon 侧进程注册表兜底回收孤儿进程）；
2. 会话数据管理面（决策 v4②）：侧栏分组读模型 + 游标窗口（置顶/项目目录/时间线 Today~Earlier）、
   软归档切面（archived_at 切片/恢复/清空硬删）、全局消息搜索 + 锚点跳转、批量导出
   （wire transcript 已是事实源，导出直接基于它：JSON/纯文本）；
3. 用量/日志：runs.usage_json 按「per-turn 明细（input/output/thought/cached）+ 会话快照」粒度落库 +
   会话粒度 token 统计 API；stderr 诊断尾巴（对照 codeg stderr_tail）；
4. 审计与配置管理：`.forge/config` 目录化（runtimes/agents/providers 的可导出配置，备份友好）；
5. 可选① 远程 runtime（Streamable HTTP/WS 传输）评估——`acl.Transport` 第二实现验证 seam；
6. 可选② 对外暴露评估（决策 v3③）：默认仅监听本机（127.0.0.1）；若确需监听非本机地址，按
   「单操作员 token」最小实现（REST header + WS 子协议 `forge-token.{b64url}`），不做多用户/RBAC。

**不做**：多租户隔离与 RBAC、计费、登录/多用户鉴权（决策 v3③，单操作员定位外）、
跨 agent 协作（决策 v2②，定位外）。

**DoD**：多会话并发互不阻塞（单操作员开多个会话）；daemon 重启后历史会话可查、opencode 会话可续谈；
会话可分组/归档/搜索/导出；关键操作有日志。产出部署文档（systemd/container 二选一）。
**量级**：4–6 周。

### P1+ — 单 runtime 自动化与生态（方向性路线，能力位保留，不排期承诺）

详见 §5.4 取舍矩阵；按依赖排序的候选队列：任务板（worktree 隔离 + review 合入）→ automation
（定时/触发跑已配置的单 runtime 会话；**先定 cron 数据契约**——schedule at/every/cron+tz × target/
message × execution_mode existing|new_conversation × state{next_run,retry}，决策 v4④，执行归 runtime
后随 P1 排期）→ MCP 数据管理 → skills 数据管理（决策 v3①②/v4④：执行复用 runtime 能力，平台只做
数据/配置层）。全部遵守决策 v2②
（每次运行指定且仅指定一个 runtime，无跨 agent 协作）。每一项都先回答
「谁是用户、和 codeg 桌面场景差在哪、ark-forge 的 Web/平台等价物长什么样」再立项。

## 7. 风险清单与验证方式

### 7.1 风险（持续更新）

| # | 风险 | 影响 | 缓解 |
|---|---|---|---|
| 1 | acp-go-sdk 与两 runtime 的 ACP 握手/通知细节差异（版本协商、能力字段、cancel 语义） | M0 卡点 | 每个 adapter 先跑「手工 dial + initialize」冒烟再上层开发；SDK 方法面已核实（§4.2） |
| 2 | deepseek-harness 迭代快、接口可能 breaking change（README 明示） | M0–M3 持续 | adapter 运行时探测 + 降级；能力断言冻结在 Client.caps（§4.2）；以 harness 自身仓库最新文档为准 |
| 3 | opencode `acp` 子命令/NDJSON 形态演进 | M0–M3 | 同上；写 adapter 时固定并记录探测到的版本 |
| 4 | WS 长连接在反代/网关下的保活与续传（空闲超时、升级路径、缓冲） | M0/M1 体验 | WS 协议层 ping/pong + 应用层 attach 重试（指数退避）；文档给出 nginx/反代配置建议（WS upgrade、read 超时） |
| 5 | 事件体积膨胀（harness 高频 thought/token） | M2 前 | 增量 live-only、终态落库（§2 范式已定）；必要时增量节流/批量合并；前端批量 flush（§4.8） |
| 6 | 断连续传依赖内存缓冲，daemon 重启即丢 | M2 边界内 | M2 起 durable 终态落 `events` 表，实时通道 `after`/快照从库补历史；内存缓冲只兜 live 段（快照/重放二分，§4.5） |
| 7 | 单操作员 daemon 被误暴露（默认 127.0.0.1 被改成公网监听却无鉴权） | M3 | 默认仅监听本机；确需对外暴露先过「单操作员 token」最小实现（§6 M3 #6）；部署文档给出反代/防火墙建议 |
| 8 | 平台定位漂移成「又一个桌面 IDE」「偷偷长出多 agent 协作」或「长出多人协作/多用户」 | 全程 | 以 §2 七条总则为评审红线；P1+ 立项先过 §5.4 矩阵；新增「协作类」需求（含多人/远程）必须显式走定位评审（决策 v2②/v3③） |

### 7.2 验证方式（每里程碑重复执行）

- 静态：`make build && make vet && make test`；CI 挂 golangci-lint（含 depguard 约束跨层 import 方向，
  见架构文档 §5）+ `pnpm typecheck`。
- 集成：adapter 夹具测试（假 ACP server：echo / 慢流 / cancel 语义）；真实 runtime 冒烟仅手工/标记跳过。
- 端到端：`make web && make all` 产出 `./forge`；按 §6 各 DoD 逐条手工验收（WS 客户端 + 浏览器双路径）。
- 回归对照：每次里程碑完成时，用**同一 prompt** 在 opencode 与 harness 上各跑 fresh session，
  验证「切换指定 runtime」后的能力不对称 UI 裁剪（harness 隐藏续谈/派生）仍正确。

