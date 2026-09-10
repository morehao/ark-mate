# ark-forge

面向 Agent 应用开发的通用底座/平台：创建、运行、扩展与统一管理 Agent，可接入 OpenCode、DeepSeek Harness 等 ACP 兼容的多种 Runtime。

> A self-hostable forge for building, running, extending, and managing AI agents. Hosts multiple agent runtimes — OpenCode, DeepSeek Harness, and ACP-compatible executors — under one unified control plane.

**定位边界**（详见 [docs/design-and-roadmap.md](docs/design-and-roadmap.md)「决策变更 v3」）：
- **能力不重造**：skills / MCP / 推理等执行能力直接复用底层 runtime 原生实现，本项目不做运行时功能；
- **数据管理是平台职责**：统一管理会话数据（wire transcript / 历史回放）、skill 数据、MCP 服务器数据；
- **单机单操作员**：不做多人协作 / 多用户鉴权 / 远程聊天通道（跨 agent 协作亦不在定位内）。

## 特性（当前形态）

- **ACP 直连运行时**：内置 `opencode`（`opencode acp`）、`deepseekharness`（`dsh --profile acp`）与本地自包含 `echo`（`forge acp-demo`，权限演示）三个 runtime adapter；子进程 stdio 拉起、能力探测、权限请求归一。
- **远程 runtime（A5）**：除本地子进程外，同一套 ACP 客户端还可接**远程端点**——`acl.HTTPTransport`（Streamable HTTP：POST 每条消息 + SSE 响应流承载通知/权限请求，另开 GET 长流收无在途请求时的服务端消息，响应头 `Acp-Session-Id` 回带）与 `acl.WSTransport`（一条 WebSocket，一帧一条消息）。远程 runtime 在 `.forge/config/runtimes.json` 声明（`id/name/transport/endpoint/headers`），启动即注册进同一 runtime 注册表，agent 的 `runtimes` 字段直接引用；传输层不新增依赖、不加第二套客户端（把网络连接伪装成 io 流，握手/权限/elicitation 逻辑与 stdio 完全同构）。`forge acp-serve --transport ws|http` 可把内置 echo 底座暴露成远程端点，用于免模型 key 的跨进程/跨机演示。远程 runtime 无 pid（进程注册表/孤儿回收不适用），连接级诊断（建连失败、HTTP 状态、被丢弃的非 JSON 消息）进 `/agents/:id/diagnostics` 的 `stderr_tail`。
- **结构化提问通道（ACP `elicitation/*`，unstable 契约）**：agent 发起 `elicitation/create`——form 模式携带 JSON Schema 表单（单选/多选/文本/数字/布尔等字段 + required/default/枚举约束）、url 模式携带引导链接——服务端发布 `session.elicitation_request` 事件 → UI 依据 schema 渲染提问卡 → 用户提交（content）/拒绝/取消经 `POST /conversations/:id/elicitation` 决策回流给 agent；超时自动取消（默认 30min，`FORGE_ELICITATION_TIMEOUT` 可覆盖）；宿主在 Initialize 中声明 `ClientCapabilities.Elicitation{Form,Url}`。**会话归属（过渡实现）**：Go SDK（coder/acp-go-sdk v0.13.5）未建模规范 scope 字段、顶层 `sessionId` 在解码时被丢弃，宿主改按「连接上唯一在途 turn」归属会话（`orchestrator.busyConn`，多会话并发在途不猜测、按取消），不再使用 `_meta`；归属来源随载荷回带（`scope_source`: `wire` | `inferred_inflight`），REST/WS 均可观测。**决策带 scope 校验**：只有登记该提问的会话能决策（`POST /conversations/:id/elicitation` 的 `:id` 与归属会话不符时，与「未知 elicitation_id」同码同文案 40001，不泄露他会话挂起项，挂起项保持不动）。规范 scope 解析列为触发式能力位（首个真实 elicitation wire 出现时启用，预案见 `docs/agent-interactions-acp-v1-design-20260907.md` §0/§2.4A）。
- **权限确认通道（ACP 一致）**：agent 发起 `session/request_permission` → 服务端发布 `session.permission_request` 事件（含 options：allow/reject × once/always）→ 用户经 `POST /conversations/:id/permission` 决策 → 决策回流给 agent（**显式拒绝带 deny 选项时回 Selected{该选项}，agent 可区分拒绝/取消/超时**；无可选 deny 选项时兜底 Cancelled）。**决策回执如实描述实际投递结果**（`allowed/rejected/always` + 请求 `option_id` + ACP 形态 `outcome`/`selected_option_id`）——`option_id` 缺省或无效时回执给出兜底选中的第一个选项，显式拒绝无 deny 选项时回执 `outcome=cancelled`，不回显请求参数、不猜测；支持会话级权限记忆——「始终允许」与「始终拒绝」**双侧对称、同键互斥覆盖**（决策 `{title, kind, decision: allow|reject}`，可见可撤销：`GET /conversations/:id/permissions/memories` 列出、`POST …/memories/revoke` 单条或一键撤销，旧 `/permissions/always` 路径兼容别名）；挂起超时自动拒绝/取消（默认 30min，`FORGE_PERMISSION_TIMEOUT`/`FORGE_ELICITATION_TIMEOUT` 可覆盖）；`session/cancel` 按 ACP 规范回 Cancelled 全部挂起。**任何挂起（权限/提问）一旦终局即发布收敛事件**（`session.permission_resolved` / `session.elicitation_resolved`），前端以服务端事件为唯一事实收敛卡片（`timeout_at` 仅作展示，无前端自设时钟）。
- **权限真实链路演示（echo 演示底座）**：内置 `echo` runtime——daemon 自派生 `forge acp-demo` 子进程充当 ACP server（与 opencode/dsh 同构的 stdio NDJSON 链路），每轮 prompt 发起一次真实 `session/request_permission`（allow/reject × once/always），按决策回显授权/拒绝与模拟工具卡；**无需安装 opencode/dsh、无需模型 key**，即可在 Web 面板全链路点选权限确认卡验证通道。除权限外，echo 还可按 prompt 关键词触发**结构化提问**（单选/多选/复合表单/URL 引导，见下）与**失败轮重试**（`boom`），覆盖 ACP 三种主要「agent↔用户」交互。
- **控制面 REST**（`/v1/forge`）：agent/runtime/会话列表 + 会话动作——`POST /conversations`（指定 agent + prompt 后台起真实会话，可带 `model` 会话级模型）、`GET /conversations/:id`（详情 + 挂起权限）、`GET /conversations/:id/history`（事件回放分页：`after` 向后 / `before` 向前翻页）、`GET /conversations/:id/runs`（轮次明细：失败轮输入/统计回溯）、会话状态含 `interrupted`（重启前中断残留，可续聊重发）、`POST /conversations/:id/prompt`（同会话多轮）、`POST /conversations/:id/cancel`、`PATCH /conversations/:id`（title/pinned/archived/model 元数据）、`GET /conversations/:id/permissions`、`POST /conversations/:id/permission`（决策）、`GET /conversations/:id/elicitations`、`POST /conversations/:id/elicitation`（结构化提问决策）。
- **轻量会话管理面**：会话元数据（title 由首条用户消息自动推导、pinned/archived、created_at/updated_at）经列表/详情暴露；runtime 能力基线（capabilities）只读暴露；前端侧栏按「置顶 / 今天 / 昨天 / 更早」分组并支持置顶。
- **实时事件流 WS**（`/v1/forge/ws/events`）：attach/detach 订阅（按会话聚合过滤），`since_seq` 游标续传/全量回放，历史缓冲超限自动淘汰并「快照回退」，心跳与断线重连；**慢订阅丢帧发 `gap` 帧**（客户端按水位立即重挂续传）；传输层 `github.com/coder/websocket`。
- **Agent 管理面（P2：注册 + 启停 + 热更新 + 切换当前）**：REST `POST /agents`（注册，校验 runtime 已注册即「可启动预检」）、`PATCH /agents/:id`（name/provider/runtime 热更新）、`POST /agents/:id/default`（切换**当前 Agent**：全局默认单例，新建会话与 `GET /models` 缺省都取它）、`DELETE /agents/:id`、`POST /agents/:id/start|stop`（真实进程级启停；有在途 turn 时 stop 报 `agent_busy`）；definitions 目录 `FORGE_AGENTS`（默认 `~/.forge/agents`，`<id>.json`）持久化并在 serve 启动时热加载——**不写代码即可注册第三个 ACP agent**（复用既有 runtime + provider 档案）；内置播种 agent `dsh`/`opencode` 与保留前缀 `agent-*` 只读（runtime 不可改、不可删；`AgentResp.seed` 为服务端权威标记，历史上前端按 id 前缀猜测，播种 id 改名后失效）。「当前 Agent」不再是硬编码常量（历史实现恒为 `dsh`）：显式选择落 `<AgentDir>/default.json`（`{"default_agent":"<id>"}`，与 `providers.json` 同属**目录级配置**，不是 agent 定义），回落链「显式选择（agent 仍存在）> 内置种子 `dsh` > id 升序首个」，被切换的 agent 被删除时自动清除选择；`AgentResp.is_default` 即由此计算。修复一个潜伏缺陷：runtime 子进程此前以 `exec.CommandContext(请求 ctx)` 启动，请求返回即被杀（StartAgent 暖启动断连）——现拨号用脱离请求的上下文，连接生命周期归编排器（watchConn/Stop/Close）。**删除面与错误分类（P2 收口）**：`DELETE /agents/:id` 回带影响面（绑定会话数 / 摘除的活跃会话数 / 是否当前 Agent），有在途 turn 报 `agent_busy`；**归属不可变**——存量会话的 agent 归属不被改写、也不静默改绑（goose 在 provider 不可用时会兜底全局默认并写回会话行，本平台不学这条：我们的 agent 携带权限策略/MCP 白名单/原生上下文，静默改写等于静默换掉权限边界），但内存里仍活跃的会话在删除那一刻即被摘除，修掉此前「删了还能跑、重启后才失效」的半死态。会话侧错误按层拆开：`agent_not_found`（会话绑定的 agent 不存在，文案给出「同 id 重新注册即可复活绑定 / 导出历史另开新会话」两条出路）、`runtime_not_found` / `runtime_unavailable`（底座未注册 / 命令不在 PATH，文案指向 diagnostics 的 `remediation`）、`credential_missing`（agent 绑定的 provider 档案悬空或缺 key → 拨号前显式失败，不静默以「无密钥」姿态起进程）。诊断面把「怎么修」一并外露：`remediation` 由事实推导（runtime 未注册 / 启动命令不可执行 / 档案悬空 / 底座自报的鉴权方式 `caps.AuthGuidance` / runtime 声明的 `SetupSteps`——opencode 与 dsh 各自带安装与登录步骤，echo 自证无需安装），健康快照不掺安装说明。
- **P2 页面（Agents 管理页）**：原只读列表升级为管理面——注册向导（ID/名称/Runtime/Provider 档案，`agent-` 保留前缀与格式校验）、**「设为当前」切换当前 Agent**（`is_default` 行标出当前，切换后新建会话面板的 Agent 徽标即时跟随；当前行不再显示该按钮）、启动/停止按钮、编辑（热更新 name/provider；种子 runtime 只读）、删除（仅自定义，Popconfirm 里如实列出影响面：多少条会话将失去执行者、是否当前 Agent；删除回执再报实际摘除数）；下方 Provider 档案区（新建/编辑：type/model/密钥输入，`has_key` 标签只写不读回、留空不变更）；诊断弹窗把 `remediation` 作为「建议处理步骤」列出（怎么装底座、怎么补密钥）。Settings 页文案同步指向该页。
- **P2 provider 注入面**：`GET /providers` + `PATCH /providers/:id`（档案 upsert；密钥**只写不读回**——GET 只给 `has_key`；`key` 空串=清除）；agent 引用 provider 档案时启动拨号注入 `<TYPE>_API_KEY`（type 未知取大写 type + `_API_KEY`），档案落 `<AgentDir>/providers.json`。
- **模型目录（运行时派生，无内置清单）**：`GET /models`（`?agent=<id>`，缺省取默认 agent）返回**由运行时自报**的会话配置项（ACP session config options）——模型目录（`category=model`）为主，运行时给出的其它选项（如 `mode`/`thought_level`）一并原样透出但只作展示，平台**不提供思考档位选择面**（少一个「取值随模型变化」的耦合点），也**不内置任何模型清单**（原 `internal/modelcatalog` 的 `go:embed` 快照 + 上游 live + 24h 磁盘缓存三段式已整体删除；provider 档案维度的模型目录路由（`GET /models` 外的旧路径）也已下线——provider 档案只承担密钥/启动 env 注入）。取样来源由响应 `source` 标注：`session`（直接读该 agent 的活跃会话）→ `cache`（内存 TTL 5min）→ `resume`（恢复该 agent 最近一条历史会话读取，读完即 `session/close`）→ `none`（空目录 + `note` + 运行时声明的鉴权引导 `auth_guidance`，面板据此提示登录）。**绝不新建探测会话**：dsh 的 `session/new` 经 `persistence.ensureMaterialized` 落持久记录、opencode 同样落库，而两家都不广告 `session/delete`，探测会留下永久垃圾。取值一律是**不透明字符串**（dsh 为 `JSON.stringify([provider, model])`、opencode 为 `provider/model`），前端只按 `category` 渲染并原样回传，绝不解析或拼接；可用性以运行时 `session/set_config_option` 的成败为唯一判据（平台不做白名单预判，设置失败即建会话失败、绝不静默回落默认模型），被运行时拒绝过的取值在目录里带 `unavailable`/`unavailable_reason` 标注。dsh 在 provider 拓扑变化时主动推送 `session/config_option_update`，平台据此刷新缓存并向 WS 转发 `session.config_options` 事件，模型下拉与「当前模型」无需轮询保持一致。
- **进程注册表 + 孤儿回收（M3 v4③）**：daemon 把拉起的 runtime 子进程（pid/pgid/命令/runtime/agent/实例 id）登记到 `<数据目录>/processes.json`（`FORGE_PROC_REGISTRY` 覆盖，`off` 关闭），正常停止/断连时摘除；启动时回收**上一实例遗留**的孤儿进程（SIGTERM → 宽限期 → SIGKILL 整个进程组），并以「pgid 与登记值一致」防 PID 复用误杀（不一致只清账并留日志）。实测：echo/opencode 子进程在 daemon 被 `kill -9` 后随 stdin EOF 自行退出，故孤儿多出现在「runtime 忽略 EOF / 派生孙进程」场景，注册表为兜底防线。
- **P2 空闲回收 + 按需拉起**：reapLoop（`FORGE_REAP_INTERVAL`，默认 5m）扫「无在途 turn、无会话引用、闲置 > `FORGE_IDLE_TIMEOUT`（默认 5m；0=关闭）」的暖进程 → 原子摘除 + 停进程；之后会话/StartAgent 自动按需重拨；旧连接收尾不覆盖新连接 running（ownerBefore 保护）。
- **会话 wire transcript（重启存活）**：每 session 一个 JSONL（header + prompt/update 原语，schema v1，append-only 不重写）；daemon **启动时回读 transcript 重建会话列表与元数据（标题/状态/时间），并把历史事件注入 eventbus**——管理面在重启后仍有会话可见、旧会话可经 WS/history 回看，会话列表不随进程失联（轻量「重启存活」，不引入 DB）；目录 `FORGE_TRANSCRIPTS` 或默认 `~/.forge/transcripts`；P1 起提供 `forge migrate` 把既有 transcript 幂等导入 SQLite、`forge transcripts prune --older-than <duration>` 显式清理（旁路停写/只读保留/不设自动删除见设计文档 ADR-5）。
- **Web 问答控制台**（浅色 modern-minimal 应用壳，视觉基准 ark-forge-console.html；frontend 为 `apps/*` + `packages/*` 多包 workspace，React 19 + antd 6 + zustand + axios，结构与脚本对齐 ark-iam/frontend）：主视图即会话问答——会话侧栏（**会话/归档双视图** · 按日分组 · 置顶 · 行内更多操作=重命名/归档/恢复 · 新建会话 · 选 Agent 底座）、流式正文（思考 / 工具卡 / 权限确认卡 / **结构化提问卡**——按 schema 渲染单选/多选/文本/数字/布尔控件并本地校验必填与范围 / meta 行（终态轮后附本轮用量行：上下文占用 · 较上轮 Δ · 成本；hover 弹出 goose 风格明细——上下文占用(快照)/首令牌/总耗时/成本，composer 下方常驻最近一轮汇总行） / **失败轮就地「重试该轮」**；消息可一键复制全文，正文内 ``` 围栏代码段渲染为带语言/行数、可复制、长块可折叠的代码卡片）、composer 追加、停止、「待确认」角标（权限 + 提问合并计数，含超时/取消自动收敛）、**会话详情面板**（元数据 + **运行统计**：轮次/失败/取消/工具数 + 失败轮列表一键重试 + 挂起权限就地决策 + 会话级权限记忆——始终允许/始终拒绝——逐条/一键撤销）、断线自动重连 + reconnecting 横幅（重连按水位续传，浏览器原生 WebSocket）；会话头按绑定 agent 如实标注，agent 已不存在时就地给出路（用同一 id 重新注册 / 导出历史另开新会话），不再只显示一个悬空 id；`/conversations/:id` 直达激活，刷新/冷启动即续看；`/agents`、`/runtimes`、`/capabilities`、**用量页（顶栏「用量」，数据面 `GET /usage/summary`：按 agent/日期聚合轮次·时长·上下文·成本，支持近 7/30 天与自定义区间；响应回显口径——上下文/成本为区间末快照、day 桶不可加、多币种不混加，上下文与费用上报率分列）**、`/settings` 为管理次要页；Agents 页每行展示 `permission_policy` 摘要（默认动作 + 工具项数 + 原生模式，hover 展开明细），编辑弹窗可直接改 `default`/`native_mode`/工具项 JSON。经真实 Chrome 驱动的 DoD 验收（新建→多轮→刷新续看→cancel 全过）。
- **单二进制交付**：`make all` 把面板产物内嵌进 `./forge`（release tag + `go:embed`），一个文件起全部（REST + WS + 静态页面）。
- **P3 收口**：传输保险丝/resync、hydration 四态均完成；会话侧栏（置顶 + 按日分组 + 近底跟随滚动）与工具/思考内联卡片为 M1 既有覆盖项，无需改动。
- **P3 加载四态（前端）**：会话视图加 hydration 态——冷挂载/切回置 `hydrating`，服务端 snapshot 或首个事件置回就绪，空转 12s（保险丝重连前）自动回空态不再永久 loading；聊天页在 hydration 中显示「连接会话…」而非空态问候。
- **P3 传输可靠性（前端）**：WS 传输增 **stalled 保险丝**（OPEN 后 70s 无任何服务端帧——半开/黑洞连接——主动断开并重连，健康连接每 25s ping→pong 计入活跃度不会误熔断）与**重连 resync 裁决**（重连后按各 agg 已确认水位重新 attach，服务端 EventsAfter 开区间 → 增量续传不重复、权威收尾）。
- **无外部依赖进程**：实时会话/事件为进程内内存态；**P1 起持久化落 SQLite**（events 事实源 + sessions/runs/messages/permission_memories 读模型投影 + `schema_migrations` 自管迁移），`forge migrate` 把现有 transcript 幂等导入（单事务 + 数量校验 + 失败整体回滚，原 transcript 目录不动）；serve 运行态接线已完成（发布持久化、重启恢复、`FORGE_STORE=sqlite|memory|auto`）；messages 逐轮物化（user 原文 + assistant 文本片段按 msg_id 拼接 + 交错时间线 + 工具最终态），会话搜索在 sqlite 模式改走 messages 表（无行时回扫 transcript）；跨会话**消息级检索**返回 `seq` 锚点（前端点击命中即跳转定位到该条消息），会话**批量导出**（markdown/text/json）以事件序列为事实源，两种存储模式产物一致；重启后对既有会话 prompt 自动**按需重建**——按记录的 agent/runtime/cwd 重建 ACP 会话（dsh/opencode 走原生 resume，echo 等走 handoff memo），事件/权限/提问继续聚合到**同一个平台会话**；会话可续聊性只取决于会话是否存在，**任何已存在的会话（含上一轮 `completed`/`failed`）重启后都可继续提问**，停机等主动拆除不改写会话状态。

里程碑与后续规划见 [docs/design-and-roadmap.md](docs/design-and-roadmap.md)；**未实现能力与降级清单（唯一汇总口径）**见 [docs/agent-runtime-refactor-design-20260908.md](docs/agent-runtime-refactor-design-20260908.md) §20；harness adapter 的源码级适配核实记录见 [docs/deepseekharness-acp-adaptation.md](docs/deepseekharness-acp-adaptation.md)。

## 快速开始

### 1. 前置

| 依赖 | 说明 |
| --- | --- |
| Go ≥ 1.26 | `go.mod` 声明 `go 1.26.1` |
| Node 24.x（LTS） | 见 `frontend/.nvmrc`（精确版本，唯一事实源） |
| pnpm 11.x | 见 `frontend/package.json` 的 `packageManager` 字段 |
| opencode | 本机安装且支持 `opencode acp` 子命令 |
| dsh（deepseek-harness） | npm 包 `@deepseek-ai/dsh`，见下方版本注意 |
| echo（演示底座） | 无需安装/模型 key；`acp-demo` 子命令内建于单二进制（本 runtime 自派生） |

**前端工具链约定（Node / pnpm）**：版本声明全部收在 `frontend/` 内、仓库根不持有——
Node 看 `frontend/.nvmrc`（现为 `24.20.0`），pnpm 看 `frontend/package.json` 的 `packageManager`（现为 `pnpm@11.24.0`）。

- 团队统一用 **fnm** 管理 Node（`fnm env --use-on-cd` 后进目录自动按 `.nvmrc` 切换）；用其它管理器（nvm/Volta/mise）不破坏仓库，只要 node 落在 24 主版本即可。
- 选 Node 24 线的原因：Node 24 是 **LTS** 线（2025-10 起），团队/生产默认的稳定选择。注意 Node 24 仍捆绑 corepack（Node ≥ 25 才移除）：fnm + Node 24 下 `pnpm` 由 corepack shim 提供，首次运行会打印一次 `! Corepack is about to download …` 并把 `packageManager` 锁定的 pnpm 下载入缓存，之后静默；想彻底消除这行可机器级二选一：`corepack disable`（改走独立安装的 pnpm）或 `corepack prepare pnpm@11.24.0 --activate`（预热）。pnpm ≥ 11 也会在本机版本与 `packageManager` 不一致时自动按其下载运行。
- 兜底两层：`engines.node`（`>=24 <25`）保证 node 不符时 `pnpm install` 直接失败；`make dev` / `make web` 在启动前端前校验 node 主版本并给出切换提示。CI（`setup-node` 的 `node-version-file: frontend/.nvmrc`）与本地读同一份声明，杜绝漂移。

**dsh 版本注意**：统一启动器 `dsh` 的 npm `latest`（如 0.1.1-rc.2）**缺 `acp` profile 模板**，需 `@next`（≥ 0.1.2-rc.1）。安装：

```bash
npm i -g @deepseek-ai/dsh@next     # 或不经安装：npx --yes @deepseek-ai/dsh@next --profile acp ...
dsh --profile acp --help           # 验证可用（首用会自动初始化 profile）
```

**版本混装排查**：若 forge 日志里 dsh 子进程启动即崩、ACP 连接随即断开（`connection closed … cause="peer connection closed"`），且 stderr 出现
`stdio app: the launcher must provide ctx.appExit and ctx.appReady before the tree mounts`
（堆栈同时含 `…/installation/lib/node_modules/@deepseek-ai/dsh` 与 `~/.npm/_npx/…/@deepseek-ai/dsh-acp-app`），说明 **dsh 启动器与 acp 插件来自两棵不同版本树**（典型：全局 `latest` 0.1.1-rc.2 启动器 + npx 缓存里 @next 的 acp 插件）。修法：`npm i -g @deepseek-ai/dsh@next` 让全局树自洽后 `dsh --version` 应为 ≥ 0.1.2-rc.1（forge 按 PATH spawn dsh，升级后无需重启 serve）；仍报错则备份重建 `~/.dsh/profiles/acp`（曾被混装写坏时）。

模型凭证：harness 读取进程环境 `DEEPSEEK_API_KEY`；若你本机 `dsh` 已配好 profile/home 设置，无 shell 环境变量也能跑。

### 2. 构建单二进制

```bash
make all          # = 前端 pnpm build → 拷贝到 backend/pkg/web/dist → go build -tags release
./forge           # 产物在仓库根
```

### 3. 启动并打开面板

```bash
./forge serve --addr 127.0.0.1:10001
```

> **默认监听地址（安全相关）**：`forge serve` 不带 `--addr` 时读 `FORGE_ADDR`，默认
> `127.0.0.1:10001`——**仅回环，不对外**（默认无鉴权，故默认收窄；`lsof` 可见
> `127.0.0.1:10001`）。确需跨机访问时**先设 `FORGE_TOKEN`**（A6 单操作员令牌，见下）再显式改
> `--addr 0.0.0.0:10001`，并置于反向代理/防火墙之后，见 `docs/deployment.md`「安全边界」。
> 生产建议同时 `export GIN_MODE=release`（消除 debug 模式告警）。

**单操作员令牌（A6）**：设 `FORGE_TOKEN=<随机串>`（或 `.forge/config` 的 `token`）后，除
`/health` 外的**全部** `/v1/forge` 路由都要求 `Authorization: Bearer <token>`；WS 因浏览器无法
设置握手头，接受 `?token=<token>`。面板顶栏「令牌」按钮可填写/清除（只存本浏览器 localStorage），
收到 401 会自动弹出。令牌比较用恒定时间实现，`forge config` 只显示掩码（`cf…3a（14 字符）`）。
未设置令牌 = 历史行为（不鉴权，仅建议回环使用）。

浏览器打开 <http://127.0.0.1:10001>：

1. 左侧「新建会话」选择 agent——内置播种 agent 为 `dsh`（默认）与 `opencode`；要跑**零依赖演示**
   （无需模型 key），先建一个指向 `echo` runtime 的 agent（面板「Agents」页新建，或
   `curl -s localhost:10001/v1/forge/agents -H 'content-type: application/json' -d '{"id":"echo","name":"Echo","runtimes":["echo"]}'`），
   再在「新建会话」里选它；
2. 点击「创建并发送」→ 右侧即时流式渲染 assistant 消息/思考/工具卡；
3. 回复期间可点「⏹ 取消」；结束后可在 composer 输入下一条（多轮同会话）；
4. 断开/刷新后重新点选会话，会按游标全量/增量续传补齐会话视图。

监听地址也可用环境变量覆盖：`FORGE_ADDR=:9999 ./forge serve`。会话 wire transcript 默认落
`~/.forge/transcripts/`，可用 `FORGE_TRANSCRIPTS=/path/to/dir` 改目录（每会话一个
`<会话ID>.jsonl`，schema v1，可直接查看/留档）；SQLite 库默认 `~/.forge/forge.db`
（`FORGE_DB` 覆盖，`FORGE_STORE=sqlite|memory|auto` 控制持久层）。完整环境变量与部署形态见
`docs/deployment.md`。

**配置目录（`.forge/config` 目录化，M3 A4）**：不想导 15 个环境变量时，把配置写在
`~/.forge/config/`（`FORGE_CONFIG_DIR` 覆盖）：

```
~/.forge/config/config.json   # {"addr":"127.0.0.1:10001","store":"sqlite","db":"/data/forge.db","idle_timeout":"5m"}
~/.forge/config/env           # dotenv：FORGE_TRANSCRIPTS=/data/transcripts（# 注释、可 export 前缀）
~/.forge/config/runtimes.json # 远程 runtime 声明（A5）：[{"id":"remote-echo","transport":"ws","endpoint":"ws://127.0.0.1:10020/acp"}]
```

优先级：默认值 < `config.json` < `env` < 进程环境变量 `FORGE_*` < CLI flag。目录/文件不存在
= 完全维持历史行为；**键名或取值非法则启动即报错**（不静默回落）。`forge config`（`--json` 可
带来源）打印最终配置与每项来源，上线前可直接核对。

### 4. 命令行冒烟（不经面板）

```bash
# 完整跑一次 fresh 会话并打印事件（超时 5min 默认）
./forge demo --runtime opencode --prompt "你好，请用一句话介绍你自己"
./forge demo --runtime deepseekharness --prompt "你好，请用一句话介绍你自己"

# echo 演示底座：无需外部 runtime/模型 key（--allow-all 即时放行权限请求）
./forge demo --runtime echo --allow-all --prompt "跑个演示"

# REST 触发 + WS 收流（可用任意 WS 客户端，如 wscat / 浏览器控制台）
curl -s localhost:10001/v1/forge/conversations \
  -H 'content-type: application/json' \
  -d '{"agent_id":"opencode","prompt":"请只回复两个字：明白"}'
# → {"code":0,...,"data":{"id":"...","event_seq":7,...}}，随后 WS attach 该会话即可收增量
```

> `demo` 默认拒绝一切工具权限请求（仅回复型 prompt 可直通）；`--allow-all` 仅调试放行。控制面 REST
> （serve）的权限语义是「**征询**」：默认 `ask` → 发布 `session.permission_request` 挂起等用户决策，
> 超时自动拒绝；可按 agent 配 `permission_policy`（`allow`/`deny` 直接决策，见「控制面接口摘要」），
> 或由用户在会话内决策并形成会话级记忆。
>
> **原生权限映射（B5）**：`permission_policy` 同时投影到底座**原生**权限开关（双层防护，策略仍是拦截面）——
> dsh 注入 `DSH_PERMISSION_MODE`（`default:deny` → `read-only`；显式 `native_mode: workspace-write|danger-full-access`
> 原样使用；其余不注入以免覆盖环境值），opencode 注入 `OPENCODE_CONFIG_CONTENT` 的 `permission` 表
> （`tools` 的 bash/edit/read/write/fetch 等键按别名映射，全 ask 时不注入）。`native_mode: off`
> 或 `FORGE_NATIVE_PERMISSION=off` 可整体关闭。未声明策略的 agent 行为不变。

> **零依赖演示权限通道（echo）**：先建指向 `echo` runtime 的 agent（见上「启动并打开面板」第 1 步），
> 面板左侧「新建会话」选它并发送任意消息 → 右侧即时出现权限确认卡（工具「运行演示命令 echo hello」
> + 允许一次/始终允许/拒绝一次/始终拒绝）→ 点选后看到授权消息 + 模拟工具卡（允许）或「未获授权」
> （拒绝）；「始终允许」记为会话级记忆，后续轮次同键自动放行不再弹卡。对应 REST：
> `GET/POST /conversations/:id/permissions|permission`。
>
> **零依赖演示失败轮重试**：同一 `echo` agent 会话中，输入首次含 `boom` 的那一轮会以 `turn.error`
> 失败（同一会话后续恢复正常）；此时聊天流错误条与详情面板「运行统计」均提供「重试该轮」——重发同一
> 输入即可看到该轮成功完成。
>
> **零依赖演示结构化提问（echo）**：同样 `echo` agent 会话，按输入关键词触发不同交互——含「单选 /
> single」→ 单选表单（如部署环境 dev/staging/prod）；「多选 / multi」→ 多选表单（1~3 项）；「表单 /
> form / 复合」→ 复合表单（名称/评分/是否公开/紧急度，含必填与默认值）；「链接 / url」→ URL 引导
> （模拟外部 OAuth 授权跳转）。提问卡支持**提交**（本地校验必填/范围/个数，提交内容以摘要回显）、
> **拒绝提供**、**取消**；超时自动收敛为「超时自动取消」。CLI 侧 `demo --allow-all` 会以空 content
> 自动 accept，保持非阻塞。对应 REST：`GET /conversations/:id/elicitations`、
> `POST /conversations/:id/elicitation`（body `{elicitation_id, action: accept|decline|cancel, content?}`）。

### 5. 前后端分离开发

```bash
# 方式 A：一条命令同时起前后端（Ctrl-C 一起收掉）
make dev

# 方式 B：分开两个终端（日志可分别看）
# 终端 A：后端（默认 :10001）
make server            # = cd backend && go run ./cmd/forge serve
# 终端 B：前端（vite dev :5173，/v1 代理到 127.0.0.1:10001，含 ws）
cd frontend && pnpm dev
```

> **用量/会话统计需要 sqlite 持久层**：`make dev` / `make server` 默认 `FORGE_STORE=sqlite`
> （自动创建 `~/.forge/forge.db`），这样才有 `runs` 读模型与每轮用量。若想轻量纯内存，
> 用 `FORGE_STORE=memory make dev` 覆盖（此时 `/usage/summary` 返回 40501、每轮用量不显示）。
> 已有 transcript（内存模式积累的旧会话）需先一次性 `cd backend && go run ./cmd/forge migrate`
> 导入 SQLite，否则 sqlite 模式下旧会话不在面板列表（serve 会提示你 migrate）。
> 注意：`runs` 在**轮次收口时**物化，**开启 sqlite 之后新增轮次**才有单轮用量；历史轮次只有
> runs 计数、无用量（usage 来自实时 ACP 上报，回放不重算）。

## 控制面接口摘要

REST 前缀 `/v1/forge`；响应统一信封 `{code, requestID, msg, data}`，`code=0` 成功，非 0 为业务错误（40401 未知 agent/会话、40901 会话正忙、40501 能力不支持、50201 runtime/连接故障等）。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/health` | 健康检查（`{code:0,msg:"ok"}`） |
| GET | `/agents` | Agent 列表（含 `permission_policy` 只读面，含 `native_mode`；`is_default` 标出「当前 Agent」；`conversation_count` = 绑定它的会话数，即删除影响面） |
| POST | `/agents` | 注册 Agent `{id, name?, runtimes[], provider?, permission_policy?}`（落 definitions 文件） |
| PATCH | `/agents/:id` | 热更新 `{name?, runtimes?, provider?, permission_policy?}`（指针字段，nil 不更新） |
| POST | `/agents/:id/default` | 切换「当前 Agent」`{is_default}`（全局默认单例：新建会话与 `/models` 缺省取它；幂等，落 `<AgentDir>/default.json`，未知 agent 报 40401） |
| DELETE | `/agents/:id` | 删除 Agent（种子 agent 只读，报 `seed_agent_readonly`）：返回影响面 `{deleted, affected_conversations, detached_conversations, was_default}`；有在途 turn 报 `agent_busy`。存量会话**归属不被改写**（历史保留可读，继续提问报 40401 `agent_not_found` 且文案给出「同 id 重新注册 / 导出历史」出路），内存中仍活跃的会话此刻被摘除（不再「删了还能跑」） |
| POST | `/agents/:id/start` · `/agents/:id/stop` | 启停底座进程（无编排器装配时报明确错误） |
| GET | `/agents/:id/capabilities` | 该 Agent 视角的统一能力目录（按候选 runtime 聚合 skill/MCP/tool + runtimes） |
| GET | `/agents/:id/diagnostics` | agent 诊断快照（连接态 + `pid` + `stderr_tail` + 最近失败；`stderr_source` 标来源 live/last_error/none；`remediation` = 按事实推导的可执行修复步骤：runtime 未注册 / 启动命令不可执行 / provider 档案悬空或无密钥 / 底座自报鉴权方式 / 底座安装步骤） |
| GET | `/runtimes` | 底座列表（ACP adapter 注册表） |
| GET | `/providers` · PATCH `/providers/:id` | provider 档案列表 / 更新 `{type?, model?, key?}`（密钥只写不读回，仅 `has_key`） |
| GET | `/models` | 运行时派生的模型目录（`?agent=<id>`，缺省默认 agent）：ACP 会话配置项直投影 `{source, options[{category,id,name,type,current_value,values[{value,name,group,…}]}], auth_guidance}`；`source`=`session\|resume\|cache\|none`，平台无内置清单 |
| GET | `/conversations` | 会话列表（`?archived=true` 过滤归档；`?cwd=<绝对路径>` 只返回工作空间**精确等于**该路径的会话，用于「按工作空间查看/分组」）。每条会话回读 `cwd` 与 `workspace_temporary` |
| GET | `/workspaces/browse` | 目录浏览（`?path=<绝对路径>`（缺省 = 首个工作区根，未配白名单时= 服务进程家目录）、`?hidden=true` 含隐藏目录）：**只列目录** → `{path, parent, home, roots, entries:[{name, path}]}`；`entries[].path` 与建会话 cwd 同形态（已符号链接规范化），可直接提交。配了 `FORGE_WORKSPACE_ROOTS` 时被收窄：越界 `path` → `workspace_outside_roots`，`parent` 到根为止（越不过去），根外的符号链接子项不列出，`home` 不在根内则不返回 |
| GET | `/workspaces/libraries` | **资料库**列表 = `FORGE_WORKSPACE_ROOTS` 下的一级子目录（`{name, path, root, last_used_at, conversations}`，按名排序）：**零新状态**（不存登记表，目录名即资料库名），未配白名单时为空数组。用途是资料/文档场景"选名字而不是选绝对路径" |
| GET | `/workspaces/files` | 目录**内容**清单（`?path=<绝对目录>` 必填、`?hidden=true` 含隐藏项）：`{path, roots, entries:[{name, path, is_dir, size, modified_at}]}`（目录在前、文件在后）。与 `browse` 分工：**browse 用于选工作空间**（只列目录，条目可直接当 cwd），**files 用于看资料**（列文件，条目**不可**当 cwd）。边界与 browse 一致（越界拒绝、根外真身的符号链接不列） |
| GET | `/workspaces/recent` | 最近使用的工作空间（`?limit=` 默认 10 上限 50）：**从既有会话派生**（`{path, name, last_used_at, conversations}`，按最近使用倒序、同目录聚合计数、排除平台代建的临时空间），不另存状态 |
| GET · PUT | `/settings/workspace` | 工作区根白名单的**设置面**：GET 回 `{configured[], roots[], restricted, provision_dir, provision_included, source, locked_by?, writable}`——`configured` 是面板可编辑的显式名单，`roots` 是**生效**名单（= `configured` ∪ 平台代建根；代建根**无需**手写进配置，它由平台隐式并入）；PUT `{roots[]}` **全量替换**（`{"roots":[]}` 或字段缺省 = 恢复"不限制"），逐项要求绝对路径 / 存在 / 是目录，落盘 `<配置目录>/config.json` 并对**新**会话与浏览面即时生效（已建会话 cwd 不变）。被进程 `FORGE_*` / env 文件 / flag 改动过该键时回 `settings_locked`（写入必被覆盖 → 面板转只读），无配置目录的进程回 `settings_readonly` |
| GET | `/conversations/search` | 会话搜索（`?q=` 必填、`?limit=` 默认 30 上限 50；含归档、`updated` 降序） |
| GET | `/search/messages` | 跨会话消息检索（`?q=` 必填、`?limit=` 默认 30 上限 200；返回 `seq` 锚点用于跳转定位，`source` 标数据面） |
| POST | `/conversations/export` | 批量导出 `{ids[], format?: markdown\|text\|json}`（ids 上限 50）→ `{files:[{filename, content, bytes, events, truncated?}], skipped[], total_bytes}` |
| POST | `/conversations` | `{agent_id, prompt?, cwd?, runtime_hint?, model?}` → `{id, event_seq, cwd, workspace_temporary, …}`（带 prompt 则后台起首轮；`model` 为 `GET /models` 的 `options[category=model]` 里的**不透明 `value`**，原样回传、经 `session/set_config_option` 会话级应用，运行时拒绝即建会话失败）。**工作空间**：`cwd` 非空时校验「绝对 / 存在 / 是目录」并按符号链接规范化，且（若配了 `FORGE_WORKSPACE_ROOTS`）必须落在某个允许的工作区根之内，否则报 `workspace_outside_roots`；为空时平台在 `FORGE_WORKSPACE_DIR`（默认 `~/.forge/workspaces`）下按**会话 id** 代建 `<id>/` 并回 `workspace_temporary=true`；代建关闭（`workspace_dir` 为空）且未指定则报 `workspace_required`——**不回落 serve 进程目录**（那会让所有会话共用一个沙箱边界）。工作空间建成即不可变（它是底座 ACP session 的 cwd，也是原生权限 `workspace-write` 的边界） |
| GET | `/conversations/:id` | 会话详情（元数据 + 挂起权限 + 权限记忆） |
| PATCH | `/conversations/:id` | 元数据更新 `{title?, pinned?, archived?, model?}`（`model` 会话级模型：opencode 仅创建时可选，dsh 会话内即时） |
| GET | `/conversations/:id/history` | 事件回放（`?after=` 向后 / `?before=` 向前（加载更早，`before=0`=末尾一页）互斥 + `limit`，默认 200/上限 1000；sqlite 模式读 events 表可翻到会话第一条） |
| GET | `/conversations/:id/runs` | 轮次明细（runs 读模型；失败轮输入/统计回溯） |
| POST | `/conversations/:id/prompt` | `{prompt}` → `{id, event_seq}`（多轮） |
| POST | `/conversations/:id/cancel` | 取消进行中的 turn（幂等） |
| GET | `/conversations/:id/permissions` | 挂起权限列表 |
| POST | `/conversations/:id/permission` | 权限决策 `{call_id, allowed, option_id?, always?}`（拒绝也透传 option_id/always） |
| GET | `/conversations/:id/permissions/memories` | 会话级权限记忆（始终允许+始终拒绝，`{title,kind,decision,since}`；旧 `/permissions/always` 为兼容别名） |
| POST | `/conversations/:id/permissions/memories/revoke` | 撤销权限记忆 `{title, kind}`；两者均空 = 一键撤销全部（旧 `/permissions/always/revoke` 为兼容别名） |
| GET | `/conversations/:id/elicitations` | 挂起结构化提问列表（表单 schema / URL） |
| POST | `/conversations/:id/elicitation` | 结构化提问决策 `{elicitation_id, action, content?}` |
| GET | `/skills` · `/mcps` · `/tools` | 能力数据面目录（Skill/MCP/Tool；MCP 含探测状态与 `tool_count`） |
| POST | `/mcps/:id/probe` | 对 MCP server 做一次 live `tools/list` 探测并写 Tool 目录（显式触发；stdio/http/streamable_http/sse 四种传输均支持） |
| POST | `/mcps/:id/tools/:tool/call` | 调用 MCP 工具（`tools/call`；body `{arguments,timeout_ms}`；工具级失败 → `is_error=true` 且 code=0；超时 40901） |
| GET | `/mcps/:id/resources` | 实时拉取 MCP 资源列表（`resources/list` + `resources/templates/list`；模板不支持 → `templates_error` 降级） |
| POST | `/mcps/:id/resources/read` | 读取 MCP 资源（`resources/read`；body `{uri}`；text/blob 原样保真） |
| GET | `/mcps/:id/prompts` | 实时拉取 MCP 提示词模板（`prompts/list`） |
| POST | `/mcps/:id/prompts/:name/get` | 取用 MCP 提示词（`prompts/get`；body `{arguments}`，值为字符串） |
| GET | `/usage/summary` | 用量统计（`?from=&to=&agent_id=&group_by=agent|day`；区间半开、日期按 UTC；无 store → 40501） |
| GET | `/ws/events` | 实时事件通道（见下） |

### 业务错误码

信封 `code` 字段的**唯一事实源是 `backend/pkg/code`**（本表由 `TestReadmeCodesMatchRegistry` 守卫，
改码必须两处同步）。语义错误一律 HTTP 200 + `code != 0`，只有鉴权失败同时给 HTTP 401。

| code | 含义 |
| --- | --- |
| `0` | 成功 |
| `40000` | 参数错误（缺失/类型或取值不合法） |
| `40001` | 请求语义非法（游标互斥、归属不符、状态机不允许） |
| `40101` | 未授权（单操作员令牌缺失或无效；HTTP 401） |
| `40401` | 资源不存在（会话/agent/runtime/provider/MCP server） |
| `40501` | 当前形态明确不支持（典型：memory 模式下的 `/usage/summary`） |
| `40901` | 与当前状态冲突或超出额度（在途轮次、上游超时） |
| `50000` | 服务内部错误（**唯一兜底码**） |
| `50001` | 上游执行失败（runtime / MCP server 报错） |
| `50201` | 上游连接失败（进程拉不起、连接断开、不可达） |

`50000` 是兜底码：任何未归类的 error 经 `code.FromError` 出口都会落成它。
信封里**不会出现 `code: -1`**（那是不带业务码的裸错误落到 gincontext 默认分支的产物）。

WS 帧契约（详见设计文档 §4.5）：客户端 `{action: attach|detach|ping}`；attach 带 `since_seq`（省略 → snapshot + live；数字含 0 → 按序重放 + live）。服务端 `{type: snapshot|event|detached|pong}`，event 载荷 `{type, seq, agg_id, version, payload}`，type 域见 `backend/internal/manager/events.go`（`session.message/thought/tool_call/tool_update/user_message`、`session.permission_request/permission_resolved`、`session.elicitation_request/elicitation_resolved`、`session.config_options`（运行时会话配置项全量更新，如模型）、`turn.ended/error`、`run.cancelled` 等）。会话动作的 POST 响应返回当前 `event_seq` 水位，作为 attach 续传起点。

## 开发

```bash
make all      # 单二进制（前端 pnpm build → 内嵌 → go build -tags release），产物 ./forge
make web      # 仅构建前端并刷新 embed 宿主目录
make build    # 不带 release 的内核编译（无需前端产物）
make vet / test   # go vet / go test ./...
# 前端：cd frontend && pnpm typecheck / pnpm build
```

容器：

```bash
make image                                    # 最小镜像：只含 forge 本体（echo 底座可自检）
make image/runtimes WITH_DSH=true WITH_OPENCODE=true   # 额外内置 dsh / opencode（可分别开关）
make image/run                                # 起容器（具名卷 /data，端口只绑回环）
docker compose up -d --build                  # 等价的 compose 路径（变量见 .env.example）
```

数据（SQLite / transcript / agents / capabilities / 配置 / 代建工作空间）全落在 `/data` 一个卷里；
容器内必须 `FORGE_ADDR=0.0.0.0:10001` 才映射得出去，对外部署先设 `FORGE_TOKEN`。完整参数、
底座开关语义与受限网络（镜像站 / Go 代理）覆盖方式见 `docs/deployment.md` §4。

CLI 子命令：`forge serve`（控制面 daemon）、`forge demo`（单轮真实会话冒烟）、
`forge migrate`（transcript → SQLite 幂等导入）、`forge transcripts prune --older-than <dur>`
（显式清理 transcript；不设自动删除）、`forge config`（打印解析后配置与每项来源）、
`forge acp-serve`（把 echo 底座暴露成远程 ACP 端点：`--transport ws|http`）、
`forge acp-demo`（作为 echo 底座被 daemon 派生的 ACP server 入口）。

部署形态（单二进制 / systemd / 容器 / 环境变量全表 / 备份升级 / 排障）见 **`docs/deployment.md`**。

仓库结构：

```
backend/                单 Go module（github.com/morehao/ark-forge）
  cmd/forge             进程入口：只做子命令分发（→ internal/app/command）
  pkg/                  可复用基础设施（不得 import internal/，由 archtest 守卫）
    acl                 ACP 客户端（stdio 传输、能力探测、权限、elicitation（unstable）、错误分类）
    runtime/            运行时注册表 + adapter（opencode/、deepseekharness/、echo/，纯数据）
    mcp                 自研最小 MCP 客户端（stdio / http / streamable_http / sse）
    capability          Skill/MCP/Tool 能力定义装载 + skill 物化
    routing             路由引擎 FirstAvailable 切片
    config              进程配置（flag + FORGE_* 环境变量 + .forge/config 目录化）
    procreg             进程注册表（孤儿 runtime 子进程回收）
    web                 release 内嵌前端产物宿主（go:embed dist）
    dbclient             gorm 句柄装配 + 自注册纯 Go SQLite dialector（无 CGO）
    object/objusage     跨层共享值对象（会话用量快照）
  internal/             应用专属逻辑
    app                 装配 + 启停生命周期（Init/Run/Close）
    app/command         CLI 子命令（serve / demo / migrate / transcripts / config / acp-*）
    router              唯一路由装配点（按领域分 routes_*.go）
    controller/ctr*     传输层控制器（接口 + 小写实现 + New 构造）
    middleware          HTTP 中间件（单操作员 token 鉴权）
    dto/dto*            请求/响应 DTO（按领域分包，双方共用）
    service/svc*        领域端口（控制器依赖的窄接口，manager 实现）
    manager             领域实现：Service + Orchestrator（会话编排、事件发布、权限策略、用量聚合）
    model               持久层实体 + 列与取值约定（gorm 映射；时间 TEXT/布尔 INTEGER 由 serializer 保证）
    dao                 单表数据访问（gormdao 泛型 Dao + 各领域 Cond）
    store               持久层门面（句柄 + 跨表事务：序号分配、导入 staging、读模型 API）
    eventbus            进程内事件总线（WS 订阅源）
    archtest            架构守卫测试（分层边界 + 事件 type 契约快照）
frontend/               pnpm workspace（apps/* + packages/*）
  apps/panel/           Web 问答控制台（React + antd；src/{store,console,components,pages,transport}）
  packages/tsconfig     @ark/tsconfig（base.json / app.json，同 ark-iam 编译器约定）
  packages/types        @ark/types（REST 信封 / 会话 / WS 帧 / 域事件契约）
  packages/api          @ark/api（axios 客户端 + /v1/forge resources）
  packages/ui           @ark/ui（tokens/themeConfig/AppShell/PageContainer/状态胶囊；DESIGN.md 事实源）
docs/                   设计/路线图 + runtime 适配核实 + 部署运维
  deployment.md         部署/运维（单二进制、systemd、容器、环境变量、备份、排障）
  design-and-roadmap.md 总体设计与 M0–M3 路线图（历史设计记录）
  agent-runtime-refactor-design-20260908.md  P0–P3 重构设计与逐切片证据（§19.x）；§20 未实现能力/降级清单
```

> `docs/` 下的设计文档是**历史设计记录**（含当时的路径命名——会话路径早期叫 sessions、P0 起为
> conversations）；对外契约以本
> README 的「控制面接口摘要」与实际路由（`backend/internal/router/routes_*.go`）为准，
> 二者由 `TestReadmeRoutesMatchRouter` 自动守卫。

## 协议 / License

[MIT](./LICENSE)
