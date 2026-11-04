# 未实现能力与降级清单（唯一汇总口径）

> **为什么有这份文档**：设计切片末尾都会写当次的「遗留/降级」，但分散在二十多处，且部分条目已被后续
> 工作**关闭**——读者容易按旧结论判断能力边界。本文件是**唯一汇总口径**：与任何别处（历史设计文档、
> 注释、README 的特性段）不一致时，**以本文件为准**。
>
> **它怎么来的**：2026-10-16 从 `docs/agent-runtime-refactor-design-20260908.md` 的最后一篇汇总节
> **原样迁移 + 逐条按代码复核**而来（那篇文档已随本次文档重建删除，历史留在 git 里）。迁移时做了一件
> 原文档做不到的事：**逐条去代码里核对**，把已经被后续工作关闭的条目移进 §3，并改掉那些指向已下线
> 底座（`opencode` 适配、`echo` 演示底座）或已撤销概念（`agent`）的措辞。

**状态口径**

| 状态 | 含义 |
|---|---|
| `DEGRADED` | **底座/SDK 不提供**，平台按明确降级运行（不臆造、不假装能做） |
| `OPEN` | **平台自身未做**，但可做；每条都给出「触发条件」= 什么时候值得做 |
| `CLOSED` | 后续工作已关闭，保留以便追溯 |

**维护约定**：新完成一项时**同步本节**——新条目进 §1/§2 并给出触发条件，被关闭的条目移入 §3 并注明
关闭依据（提交号 / 决策号 / 文件）。**不要只写"已完成"**：关闭依据要能被下一个人复核。

> **归档文档指针**：本文件的 `§19.x`（以及 `§17` 等旧章节号）指 2026-10-16 文档重建期间删除的
> `docs/agent-runtime-refactor-design-20260908.md`（它仍在 git 里，删除提交 `1e9e039`）。
> 复核命令：`git show 1e9e039^:docs/agent-runtime-refactor-design-20260908.md`。
> 新条目**不再**使用 `§19.x`：记录列必须给「现行文件路径」（可附决策版本）。

> **编号不重用**：移入 §3 的 `O`/`D` 号不再分配给新条目；§3 一律 `C` 前缀。

---

## 1. 底座能力缺口（DEGRADED）

| # | 内容与现状降级 | 触发条件（何时该做） | 记录 |
|---|---|---|---|
| D1 | **elicitation 会话归属只能推断**：Go SDK v0.13.5 丢弃顶层 `sessionId`，宿主先读 `_meta` 里的 `sessionId`/`conversationId`（命中则 `scope_source=wire`）；未命中才按「连接上唯一在途轮次」归属（`inferred_inflight`）；两者都不成立时置 `ambiguous`——**不猜测**、按取消回应，并把事实与建议写进 `GET /runtime/diagnostics` 的 `remediation`。同连接多会话并发在途时**无法唯一归属 → 按取消处理**（WARN 留痕） | SDK 补规范 scope 字段（届时直接置 `wire`）；或改连接级串行执行（牺牲并发）。**触发式能力位**：出现第一个真实 elicitation wire 帧时启用规范 scope 解析预案 | §19.23 |
| D2 | **skill 投影根不可按会话指定**：底座在 provider 构造时读自己的 agents 家目录（dsh 是 `DSH_AGENTS_HOME` / `DSH_BUNDLED_SKILL_DIR`），而该读根**不允许**按会话覆盖——它同时是底座的通用 agents 家目录，覆盖它会越界，也会让「平台写根」与「底座读根」悄悄分叉（skill 静默不投放而平台侧仍自报 `in_sync`）。于是一条会话只能拿到投影根下**全部**候选 skill，不能按会话裁剪 | 出现「同一部署内两条会话需要不同 skill 集合」的真实需求 | §19.26 |
| D3 | **原生权限只映射已知键，且原生沙箱不构成对平台放行的否决**：内核只做「模式 → 取相应端口表（见 `internal/runtime` 根包的登记表）→ 转发」，adapter 只映射自己认识的原生权限键、未知键跳过（仍由平台拦截面裁决）；dsh 在 `read-only` 下会发起沙箱升级请求，该请求走**平台权限通道**——平台策略 `allow` 时会被放行 | 需要「原生沙箱能否决平台放行」时（要底座提供不可绕过的策略位） | §19.20 |
| D4 | **用量成本依赖底座上报**：`usage.cost` 需底座主动上报，当前内置底座不上报 → 界面显示 `—`，平台**不做估算**；逐轮 token/费用明细受 ACP 通道限制（只有上下文快照 + 可选累计费用）；快照口径 / 上报率 / 多币种已显式化 | 底座开始上报 `cost` 或提供逐轮用量 | §19.7 · §19.7.1 · §19.21 |
| D5 | **dsh 的 `.system` 子目录与可选键未映射**（`user-invocable`/`disable-model-invocation` 等）；ACP 的 `resources/subscribe`、`prompts` 的 `completion/complete` 未支持；MCP 服务端反向请求（sampling/roots）只回 `method_not_found` | 有真实使用场景再评估（当前不在平台范围） | §19.4 · §19.17 · §19.18 |
| D6 | **网络与进程可见性拿不到**：ACP 面没有「这一轮访问了哪些主机/起了哪些进程」的通道。平台的工具卡只能显示工具**自报**的 title/status，以及 `tool_call_update.content` 的正文文本；界面**不做**网络/进程维度的汇总，也不从 title 里猜（`bash` 的 title 是模型写的自由文本） | 底座提供结构化的网络/进程审计事件 | 2026-10-16 探针（`dsh 0.2.0-rc.2`；2026-10-18 于 0.2.1-rc.2 复测仍成立）· `backend/internal/core/protocol/event.go` |
| D7 | **压缩不可观测**：底座**不通告**上下文压缩。实测把阈值压到几乎必触发、会话日志里两次完整压缩（`compaction/start→summary→end`），同一时段 ACP 面 **0 帧**、帧里 `compact` 命中 0。所以 `session.compact` 事件**没有外部事实来源**——平台**不发**这个事件，界面也不显示"正在压缩" | 底座在 ACP 面通告压缩（届时接 `session.compact`） | 2026-10-16 探针（原始记录未入库）· `backend/internal/domain/session/events.go`（事件目录里没有 `session.compact`——这正是"平台不发"的机器判据） |
| D8 | **阶段/进度不可观测**：ACP 服务端只发 6 种 `sessionUpdate`（thought/message chunk、`tool_call`、`tool_call_update`、`usage_update`、`config_option_update`），`phase`/`progress`/`_meta` 全包零命中。所以 `run.status` 的 `phase` 段**恒空**，`retrying`/`compacting` 两档**永不出现**（P0-2 当时"没找到观察点"的选择，现在有了实测依据） | 底座补 `agent_phase` / `progress` 类元数据 | 2026-10-16 探针 · P0-2 · `backend/internal/domain/session/runstatus.go` |
| D9 | **终端与 plan 不可见**：ACP 的 `terminal/*`（内嵌终端）与 `plan`（`sessionUpdate` 的 plan 变体）平台**不建模**——`normalizeUpdate` 的 `default` 分支丢弃它们。界面上没有"这一轮的计划清单"，也没有内嵌终端 | 底座把 plan 当常规更新推送、或出现内嵌终端的真实需求 | `internal/infra/protocol/acp/client.go` 的 `default:` 分支 |
| D10 | **沙箱形态读不到（两半）**：① ACP 面**没有** `SandboxEnforcement`（`full`/`partial`）——`packages/acp/**` 搜 `enforcement`/`permissionMode`/`sandboxMode` 零命中，`session/new` 只回 `{sessionId, configOptions}`。② 沙箱失败的结构化码（`SANDBOX_UNAVAILABLE`）在 `tool/result.data.error.code` 里，而 ACP 只转发 `message.content`，**那串字连文本里都不出现** ⇒ 平台只能按**文本签名**归类（`sandbox_denied` / `sandbox_unavailable`，见 `internal/infra/dsh/tool_failure.go`），**不能**用 `status` 判（被拒绝的那一帧 `status` 是 `completed`）。**因此界面不得声称「文件修改都被限制在工作空间内」**——平台没有能支撑这句承诺的事实 | 底座在 ACP 面透出 enforcement 与结构化错误码 | 2026-10-16 探针 · P1-b · `backend/internal/infra/dsh/tool_failure.go` |
| D11 | **会话级权限模式只能在进程边界上区分**（原 O14 改判为 DEGRADED）：`/permission` 命令面在 ACP 上**不存在**——底座从不发 `available_commands_update`（见 D10），所以模式只能以进程 env（`DSH_PERMISSION_MODE`）下发。数据模型 v29 起模式进 `ConnKey` 基础键 ⇒ **不同模式各起一个暖进程**；**同一进程内按会话切模式不可达**（不是平台没做，是底座没有运行时切换面）。代价：进程数随模式数增加 | 底座补命令面或运行时切换面（届时才谈同进程切换） | `backend/internal/drivers/runtimeconn/pool.go` · 决策 v29 |
| D12 | **工具结果的结构化字段拿不到**（2026-10-18 会话流第二轮）：dsh 0.2.0-rc.2 **从不下发 `rawOutput`**（0.2.1-rc.2 复测仍不下发）——结果正文只在 `tool_call_update.content` 的 text 块里（`backend/internal/infra/protocol/acp/client.go` 的 `toolContentText`）。平台已把它读进进程内的 `ContentText` 并用于失败归类，但它带 `json:"-"`、**刻意不进 wire**；事件载荷的 `output` 仍只取 `rawOutput`（`backend/internal/domain/session/eventsink.go` 的 `toolPayload`）。于是 dsh 上工具详情通常只能显示「结果不可用（底座未上报）」：read/list 无内容预览、execute 无 stdout/stderr/退出码、edit/write 无工具官方 diff（界面里的 diff 来自调用参数的 `old_string/new_string` 或写入内容，已标「来自调用参数」）。命令输出的 `[stdout]` / `[stderr]` / `[exit code: N]` 识别是**文本启发式**，不是结构化字段 | 底座在 ACP 面补 `rawOutput` 或结构化结果；**或**产品确认工具详情必须显示结果正文时，由平台把已拿到的 `ContentText` 作为**新的对外结果键**上线（会同时动 `ToolPayloadKeys`、`store.ts`、`TestToolPayloadKeysAreClosed`）——在此之前界面只渲染已有字段并如实标「不可用」 | `backend/internal/infra/protocol/acp/client.go` · `backend/internal/domain/session/eventsink.go` · `frontend/packages/console/src/console/transcriptView.ts` |

## 2. 平台自身未做（OPEN）

> **2026-10-17 L2 决策**：本节原有的 14 条已**逐条决策**（决定与理由见 `docs/decisions.md` v29）：
> 11 条移入 §3（其中 O14 改判为 D11、O10 的第一条并入 D2），3 条**保留**——O3 排期 L3 第 6 项，
> O4 / O5 归属云端 B 段（方向阶段，不在本地排期）。只有「决定做、但还没做」的条目留在这里：
> §2 的语义是**已决定待实现**，不再是"待评估"。

| # | 内容（决定） | 触发条件 | 记录 |
|---|---|---|---|
| O3 | **MCP 侧的工程化缺口**（2026-10-17 复核）：连接池**已有**（`internal/infra/protocol/mcp/pool.go`：按 server 池化、同会话串行、有界 + 空闲回收、定义/密钥变更即作废重建）。**决定：做**，拆两批——① **全局并发限流/排队**（今天是每会话串行，N 条会话可同时打到同一个远端 server）；② **调用结果进事件流与审计表**（网关现在只在 stderr 留一行 `(server, tool, outcome, latency_ms)`）。**不做**定时/变更监听探测：触发条件未到，且常驻探测会给远端 server 增加无谓请求。同名工具冲突已关闭（C18） | 生产化 MCP 接入（并发量 / 审计需求） | `backend/internal/infra/protocol/mcp/pool.go` · 决策 v29 |
| O4 | **平台不做鉴权（单用户场景，by design）**：无 `MATE_TOKEN`、无分面凭据、无用户/角色/审计、无速率限制——面向单用户、只绑回环；对外暴露请在反向代理层自行加鉴权。**决定：不做**；真正要多身份/公网时另立项（原云端阶段的触发器不变） | 多租户或公网暴露——云端阶段另立项 | `backend/internal/middleware/origin.go` · 决策 v29 |
| O5 | **远程 runtime 无鉴权面**：`headers` 可带 token，平台不做 OAuth/mTLS；HTTP 形态的 GET 长流在建会话前会被 404（降级记进诊断）；服务端半场的会话只在内存。**决定：做**，随**云端远端接入**（B 段）一起——跨机/跨组织接入之前，本地形态不需要这层 | 跨机 / 跨组织接入 | `backend/internal/runtime/connect.go` · 决策 v29 |
| O6 | **身份交付不做启动期底座级校验**：身份 overlay 只有两个键（`includeHarnessIdentity` + `personaPrefix`）、目标条目在载荷 pin 版本上已验证，所以启动期只做静态校验 + golden + 版本 pin，**不跑** `--dump-config-schema`（用户已拍板：不为一次性校验吃掉启动预算）。真实底座校验只在一次性 probe/CI 里跑。代价如实记：一份被静态校验放过、而底座实际不认的 patch 会让底座**拒启**，那时只能靠诊断 + `MATE_IDENTITY_MODE=user` 回退 | 线上出现一次身份 patch 导致的拒启 → 升级为启动期校验（那时再评估"只校验身份这份候选"的秒级开销） | `backend/internal/infra/dsh/identity.go` · 设计 §3.7/§3.17 |
| O7 | **不做会话级身份快照 / 用户改名**：身份是部署级常量（`identity.Version` 跟构建走），`sessions` 表**刻意没有** `identity_version` 列——原生 resume 用历史、handoff 用当前身份，两者都不需要按会话回放旧模板。用户自定义人格/昵称/onboarding（WorkBuddy 的 SOUL/BOOTSTRAP 那一路）是另一条产品线，本期只固定产品身份为 ark-mate | 要做"产品名 vs 昵称"的优先级、或需要回答"这段历史是哪一代身份生成的"时 | `backend/internal/domain/identity/identity.go` · 设计 §3.11/§3.17 |
| O8 | **身份不做运行期热更新**：改 `MATE_IDENTITY_MODE` 或改身份文案都需要重启应用（与底座"启动期 `--patch`"的语义一致）。运行期切模式还会让同一份 transcript 里的自述随打开顺序变化——那不是产品行为。另：**不改写历史 transcript**（旧会话里的旧自述保留原样，只有新的执行上下文才重新声明） | 出现"不重启就要换身份"的真实需求（届时要先回答 transcript 里两代自述并存的读面口径） | `backend/internal/bootstrap/app/assembly.go` · 设计 §3.12/§3.17 |

## 3. 已关闭（CLOSED，保留追溯）

| # | 原遗留（记录处） | 关闭依据 |
|---|---|---|
| C1 | MCP 工具执行（`tools/call`）「不在平台范围」 | §19.16 B1 |
| C2 | MCP `resources`/`prompts` 目录化未做 | §19.17 B2 |
| C3 | MCP `sse` 传输未支持 | §19.18 B3 |
| C4 | MCP 探测为手动触发，无自动探测 | §19.19 B4 |
| C5 | 原生权限映射待后续切片 | §19.20 B5 |
| C6 | 前端尚无用量视图 | §19.21 C1 |
| C7 | 前端未展示 `permission_policy` | §19.21 C1 |
| C8 | `/history` 只有 `after`、无 `before` 游标 | §19.22 C2 |
| C9 | 默认监听所有网卡且无鉴权 | §19.9 H2（收窄）+ §19.15 A6（令牌） |
| C10 | dsh skill 物化未实证（只列不物化） | §19.4 G4 |
| C11 | 单 root 投影下多 runtime 共用只取首个 format | §19.24 C4 |
| C12 | 共享 root 下目录可能残留 | §19.26 C6 |
| C13 | `POST /conversations/:id/permission` 回显请求 `allowed` 而非有效决策 | §19.25 C5（当时的路径词 `conversations` 已在决策 v12/数据模型 v15 统一为 `sessions`） |
| C14 | §16.3「backend-wide Runtime 多会话重构」评审点 | §17（E1/E2 完成 + 采用收口；其开放项分别落在 D1、D11、C34） |
| C15 | **浏览器渲染自动化/视觉走查缺失**（原 O1：仓库无 Playwright/Puppeteer，页面仅由 typecheck/build/bundle 断言覆盖） | `frontend/package.json` 的 `ui:accept`（CDP 驱动真 Chrome + 真服务，自造自清夹具）与 `ui:accept:desktop`（真 Electron + 真核心）已落地且**零新依赖**；另有三条桌面专项验收（skill 投影 / 无宿主机依赖 / 打包态）。**仍未做的是像素级对比**（那是另一种判据，不属本条） |
| C16 | **Resume 未接入平台恢复流程**（原 O14） | `backend/internal/domain/session/lifecycle.go` 已按 `acl.Client.ResumeSession(resume=true)` 恢复**同一底座会话**（`session.resumed` 带 `reason=restart`）；未声明原生恢复能力的底座走 handoff memo。模型目录取样也复用同一条 resume 通道 |
| C17 | **会话的底座会话 id 未持久化**（原 O15，当时叫 `provider_session_id`） | 数据模型 v17 起该列持久化为 `sessions.runtime_session_id`（实体 / DAO / 迁移 / 恢复四处同源，`backend/internal/persistence/model/session.go`），决策 v13④ 同时把词汇从 `provider_*` 收敛为 `runtime_*` |
| C18 | **MCP 同名工具冲突「后探测者覆盖前者」**（原 O3 的一部分） | `backend/internal/core/capability/mcp_tool_name.go` 的 `NamespaceTools` 做 fail-closed 裁决：工具暴露名 `<serverID>__<toolName>`，跨 server 撞名（`a`+`b__x` vs `a__b`+`x`）**整组丢弃并 ERROR 留痕**。**边界**：这只覆盖走平台网关的远程 MCP；stdio server 由底座直连，不在此列 |
| C19 | **权限记忆只有一种形态**（原 O16） | 数据模型 v28（`backend/internal/persistence/store/migrate_v28.go`）：`permission_memories` 补两列 `scope`（授权形态，今天恒 `session`）与 `grant_input_digest`（当初那条工具入参的 sha256 前 16 位），`Upsert` 写入、启动装载 `WithGrantShape` 复原。**唯一键保持三元组**——把形态或入参并进去会让"这类工具始终允许"退化成"这一个参数组合始终允许"。原条目里"一次性的允许被永久记住"这一条**前提不成立**：`Remember` 只被 `always` 决策调用，`once` 从不落库 |
| C20 | **工作空间不能改登记路径**（原 O17） | `9e35fe8` 起 `PATCH /workspaces/:id` 的 `path` 走 `RelocateWorkspace`（`backend/internal/persistence/store/workspace.go`）：id 不变、会话归属与 `cwd` 零写、冲突 `ErrWorkspacePathTaken`、默认工作空间仍不可改（`workspace_default_immutable`）。守卫 `TestWorkspacePathUpdateKeepsIdentity` 钉住四段 |
| C21 | **权限策略增强**（原 O11） | 改判依据（2026-10-17 复核）：当前**不存在**权限策略面——`routes_*.go` 无 policy 路由、`dto` 无 Policy 类型、设置分节只有 通用 / 模型 / 命令规则 / 关于（`frontend/packages/console/src/console/settingsView.tsx`），原句描述的「JSON 文本域」与「运行中 PATCH」都已没有对象；命令粒度并入 O20（规则表已落地）。若策略编辑面重新立项，新开一条 O 条目，不复用本号 |
| C22 | **会话行仍是单行式**（原 O21） | 已实现两行式（`frontend/packages/console/src/components/SessionPane.tsx`）：第 1 行 = 标题单行省略 + 置顶标记，第 2 行 = 状态点 + 阶段文案 + 来源 + 时间；行尾「更多」绝对定位在第 1 行右端（不嵌套 button）。守卫 `TestSessionRowIsTwoLine` 用 `data-sess-line` 两个锚点钉住；反证（实跑）：删 meta 锚点 / 把 `WebkitLineClamp: 2` 加回标题行，两者皆红 |
| C23 | **命令门禁没有编辑面**（原 O20） | 规则文件 + 裁决点 + 设置「命令规则」编辑面均已落地：`GET/PUT /command-policy`（`backend/internal/controller/ctrcommandpolicy` + `backend/internal/service/svccommandpolicy`）、`frontend/packages/console/src/components/settings/CommandPolicyPanel.tsx`；保存**先校验再原子落盘**并热生效（`Apply` 回调）。**边界**：`match_samples`/`not_match_samples` 仍只能手写文件（面板原样保留、不提供编辑）；只对简单命令自动裁决，复杂命令一律 prompt |
| C24 | **既有会话归属自愈**（原 O18） | 判定为**不做（by design）**：`sessions.workspace_id` 指向登记本身没有坏；坏的是登记指向的目录不存在了（`dir_status: missing`）。用户的「确认过的新位置」= `PATCH /workspaces/:id {path}`（设置里『选择新目录…』，`frontend/packages/console/src/components/WorkspaceGroupPane.tsx`）；平台**不**把「目录没了」猜成「搬到哪」——猜错会把会话归属悄悄改到另一个项目。**会话的 `cwd` 是执行事实，刻意不批量改写**：旧 transcript / 上下文仍指当时的工作目录，改写等于让历史会话在另一个目录里续跑。读面如实上报 + 显式改路径就是完整出路；将来若要批量重挂，应作为**独立的显式动作**（逐会话确认），不是自愈 |
| C25 | **无配置热重载**（原 O2） | 判定**不做（本阶段）**：热重载的正确形态是「整进程重建」（`runtimes.json` 决定 adapter、能力定义决定 MCP 客户端集合、`config` 决定 store/addr），半热只会制造「配置说 A、进程跑 B」的静默分叉；本地形态有显式重启这条等价出路（秒级）。触发条件（运维要求不重启变更）出现时按整进程重建做，新开 O 条目，不复用本号。决策 v29 ① · `backend/internal/bootstrap/app/app.go` |
| C26 | **进程回收边界**（原 O6） | 判定**不做（本阶段）**：单实例是架构前提（跨实例锁没有对象）；`setsid` 孙进程是底座自主 detach，追它等于越过进程边界；非 unix（Windows）当前**不在交付矩阵**（`Makefile` 的桌面 recipe 只出 macOS），M4-3 接 Windows 出包时用 Job Object 重做回收——不在本阶段做半套。决策 v29 ① · `backend/internal/infra/procreg/procreg_other.go` |
| C27 | **导出与检索的增强面**（原 O7） | 判定**不做**：三条都是规模触发——zip 打包与「导出全部会话」的服务端游标在本地形态没有对象（`ids` 上限 50 已覆盖一次导出）；分词与相关性排序要引入索引与词典，而子串匹配在单机数据量下是毫秒级。决策 v29 ① · `backend/internal/controller/ctrexport/export.go` |
| C28 | **用量视图增强**（原 O8） | 判定**部分做，其余不做**：**时区已对齐**（`?tz_offset=`：日期参数与 day 桶按客户端时区解释，缺省 UTC 向后兼容）——此前 UTC+8 用户每天 00:00–08:00 的轮次会被算进前一天，而界面上看不出来。**边界**：偏移按请求时刻取一个固定值，区间跨夏令时切换时那一天的日界仍会差 1 小时（要精确得传 IANA 时区名，而界面只需要「我看到的日期」）；**不做**：图表（不引图表库）、逐轮输入/输出 token 明细（受 ACP 通道限制，见 D4）。决策 v29 ③ · `backend/internal/domain/session/usage.go` |
| C29 | **历史分页增强**（原 O9） | 判定**不做**：虚拟滚动是长会话性能项（`TRANSCRIPT_WINDOW` 尾部窗口 + 「显示更早」已覆盖可读性）；`after`/`before` 组合区间没有查询消费者（服务端两者互斥）；50 条/页在真实会话长度下够用；「空视图点加载更早等价取末尾一页」在界面上**不可达**——按钮只在有内容时渲染。决策 v29 ① · `frontend/packages/console/src/pages/ChatPage.tsx` |
| C30 | **skill 投影增强**（原 O10） | 改判并关闭：① 「按会话/cwd 动态 root」**就是 D2 的事实**（底座读根不可按会话覆盖，平台侧写根跟着分叉只会静默不投放），已并入 D2，不单列；② 「skill 文件变更热重载」**不做**，与 C25 同口径（变更走重启）。决策 v29 ① · `backend/internal/domain/skill/projections.go` |
| C31 | **排障可见性**（原 O12） | 判定**不做（by design）**：跨会话决策与「未知 id」同码同文案是**会话归属校验**的防泄露口径（`domain/interaction` 的 resolve scope 判据：决策会话必须与挂起项归属一致）（两者都回 `elicitation_not_found`，细节只在服务端 WARN）——区分它们等于告诉调用方「那个 id 在别的会话里存在」。`always` 回执是**请求意图**，记忆是否真的写入以 `GET /sessions/:id/permission-memories` 读面为准；回执里再补一个「已记住」会让同一件事有两处事实。决策 v29 ① · `backend/internal/service/svcsession/interaction.go` |
| C32 | **事件持久化的边角**（原 O13） | 判定**不做**：事件写库失败不阻断会话（总线侧仍有 + WARN，位次退回进程内分配）是刻意的取舍——一轮跑到一半因为 append 失败而中断，比「这条事件不在 REST 历史里」更糟。要让两侧完全一致需要事务性 outbox（发布路径改两阶段），代价大于收益。决策 v29 ① · `backend/internal/domain/session/publish.go` |
| C33 | **会话级权限模式只能在进程边界上区分**（原 O14） | 改判为 **D11**（底座能力缺口，不是平台未做）：同一进程内按会话切模式需要底座提供运行时切换面，而 `/permission` 命令面在 ACP 上不存在；平台已用「每模式一个暖进程」做到不同会话不同沙箱边界。决策 v29 ① · `backend/internal/drivers/runtimeconn/pool.go` |
| C34 | **队列项不能原子替换正文**（原 O15） | 判定**不做**，与决策 v28 ④ 同一条：取出编辑（pop）已覆盖用户意图，就地替换会引入「身份不变、正文变了」的状态，代价是位次变化（改完排到队尾）——这个代价我们接受。决策 v28 ④ · `backend/internal/domain/session/inputqueue.go` |
| C35 | **确认区一次只显示一张卡**（原 O19） | 已实现：确认区**并列显示全部未结算卡**（权限在前、同优先级按入流顺序），标题吸顶、整块限高 46vh 自行滚动（不把输入框挤出屏幕）。守卫 `TestPendingCardsLiveOutsideTranscript` 加两段：必须 `ordered.map(...)` 且每张带 `key={en.id}`，并禁止 `pending[0]` / `pending.length - 1`（单卡形态的痕迹）。决策 v29 ④ · `frontend/packages/console/src/components/ConfirmArea.tsx` |
| C36 | **工具详情里的路径不能点击打开**（原 `open-path` 能力；2026-10-18 会话流第二轮只做预览） | 判定**不做**：2026-10-11 的能力面收窄已移除 `open-path` 声明（`backend/internal/controller/ctrsystem/system.go` 的注释记录了理由：声明了但界面零调用）。桌面的 `native.openPath` 已无声明可走；浏览器入口没有等价的「打开本地文件」出路（`file://` 被宿主策略挡住），新造一座桌面宿主桥会违反「桌面多给的那半必须有服务端等价路径」的纪律。本轮只做路径/内容预览。重开条件：产品明确要求点击路径打开文件，且先定浏览器态的等价出路（例如只复制路径）· `backend/internal/controller/ctrsystem/system.go` · `frontend/packages/console/src/components/Transcript.tsx` |
| C37 | **知识库（语义检索 / RAG 问答）**（2026-11-03 资料库范围拍板） | 判定**本期不做（另立项）**：本次只做「资料库」（目录树存储 + 结构浏览 + 关键词子串匹配 + 回流），**不含**语义向量检索 / embedding / RAG 问答。理由：知识库是在资料库之上再加一层语义引擎，两层可独立演进；本期先把下层收干净、零模型依赖。**守卫 `TestMaterialHasNoSemanticSearch` 把这条拍板变成机器判据**（资料库域/服务/infra 出现 `embedding`/`vector`/`semantic`/`rag`/`chunks` 即红）——要加知识库必须删改该守卫并走范围评审，不能悄悄塞回来。重开条件：产品明确要「问一句从我的资料里找答案」，且按定位边界①「能力不重造」重做评估（embedding 走已配置 provider，不内置模型）· 设计 `.dsh/docs/specs/2026-11-03-material-library-design.md` §0/§7。**另**：资料库的 MCP 工具面（`mate mcp material`）已交付且可被任意 MCP 客户端拉起，但「默认内置进 dsh 注入面」是 config-plane 决策（注入面与能力指纹/代际同源），本期不接（详见计划 `.dsh/docs/plans/2026-11-03-material-library-plan.md` §6.3） |
| C38 | **产物自动抽取 + 收录闭环（P2/P3）**（2026-11-04；上一轮盘点发现的记录缺口，此前未登记即落地） | **已实现**：真会话里 dsh 的 write/edit 工具写出/改动工作区文件后，平台**旁路**观察工具终态事件，把该文件快照进 filestore 并登记一条 `artifacts` 行（`origin=tool_write/tool_edit`、带 `path` 工作区相对路径）；`GET /sessions/:id/artifacts` 可查、`GET /artifacts/:id` 惰性算 `dirty`（工作区文件改动/被删→1，列表页不算）。**2026-11-04 真机修正（P2 上线后零产物）**：模型几乎总给**绝对路径**（真会话 10/10 次），旧判据 `filepath.IsAbs → 跳过` 让产物静默消失；现相对/绝对都认，绝对路径必须落在 `sessions.cwd` 内（越界只 WARN 跳过，不再静默），回归用例 `TestArtifactExtractor_AbsolutePathInsideCwd` 用真形状（绝对路径 + 中文名）钉住。**收录闭环（P3 补齐）**：会话详情「产物」区列 artifacts 并提供「存进资料库」（`POST /materials/import-artifact`，幂等）；资料库 `?category=artifact` 分栏可直达、产物节点可回跳来源会话；MCP 工具面补 `library_import_artifact`。**边界不变**：产物默认只进 `artifacts`，进资料库仍是显式动作、不自动全量入库。**零侵入铁律**：不改 dsh 任何行为——只消费已上报的 ACP 工具事件、只读工作区文件、只写平台 filestore/artifacts；守卫 `TestArtifactExtractNoDshTouch`（E-3，archtest）钉死不 import 底座/不 spawn/不写工作区；行为测试 `TestArtifactSnapshotBeforeAsync`（E-4）钉住「同步捕获字节、异步落盘」。**其余已拍板边界**：超 32 MiB 文件跳过不登记（不造 `upload_id` 空行）；同一 path 多次写每次一行（UI 按 path 聚簇）。实验证 R1 探针 `.dsh/docs/notes/2026-11-04-r1-tool-rawinput-probe-notes.md`；计划 `.dsh/docs/plans/2026-11-04-artifact-auto-extraction-plan.md` · `backend/internal/infra/workartifact` + `backend/internal/bootstrap/assembly/artifactextract.go` + v35 迁移 |
