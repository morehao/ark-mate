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

---

## 1. 底座能力缺口（DEGRADED）

| # | 内容与现状降级 | 触发条件（何时该做） | 记录 |
|---|---|---|---|
| D1 | **elicitation 会话归属只能推断**：Go SDK v0.13.5 丢弃顶层 `sessionId`，宿主先读 `_meta` 里的 `sessionId`/`conversationId`（命中则 `scope_source=wire`）；未命中才按「连接上唯一在途轮次」归属（`inferred_inflight`）；两者都不成立时置 `ambiguous`——**不猜测**、按取消回应，并把事实与建议写进 `GET /runtime/diagnostics` 的 `remediation`。同连接多会话并发在途时**无法唯一归属 → 按取消处理**（WARN 留痕） | SDK 补规范 scope 字段（届时直接置 `wire`）；或改连接级串行执行（牺牲并发）。**触发式能力位**：出现第一个真实 elicitation wire 帧时启用规范 scope 解析预案 | §19.23 |
| D2 | **skill 投影根不可按会话指定**：底座在 provider 构造时读自己的 agents 家目录（dsh 是 `DSH_AGENTS_HOME` / `DSH_BUNDLED_SKILL_DIR`），而该读根**不允许**按会话覆盖——它同时是底座的通用 agents 家目录，覆盖它会越界，也会让「平台写根」与「底座读根」悄悄分叉（skill 静默不投放而平台侧仍自报 `in_sync`）。于是一条会话只能拿到投影根下**全部**候选 skill，不能按会话裁剪 | 出现「同一部署内两条会话需要不同 skill 集合」的真实需求 | §19.26 |
| D3 | **原生权限只映射已知键，且原生沙箱不构成对平台放行的否决**：内核只做「模式 → 取三张端口表之一 → 转发」，adapter 只映射自己认识的原生权限键、未知键跳过（仍由平台拦截面裁决）；dsh 在 `read-only` 下会发起沙箱升级请求，该请求走**平台权限通道**——平台策略 `allow` 时会被放行 | 需要「原生沙箱能否决平台放行」时（要底座提供不可绕过的策略位） | §19.20 |
| D4 | **用量成本依赖底座上报**：`usage.cost` 需底座主动上报，当前内置底座不上报 → 界面显示 `—`，平台**不做估算**；逐轮 token/费用明细受 ACP 通道限制（只有上下文快照 + 可选累计费用）；快照口径 / 上报率 / 多币种已显式化 | 底座开始上报 `cost` 或提供逐轮用量 | §19.7 · §19.7.1 · §19.21 |
| D5 | **dsh 的 `.system` 子目录与可选键未映射**（`user-invocable`/`disable-model-invocation` 等）；ACP 的 `resources/subscribe`、`prompts` 的 `completion/complete` 未支持；MCP 服务端反向请求（sampling/roots）只回 `method_not_found` | 有真实使用场景再评估（当前不在平台范围） | §19.4 · §19.17 · §19.18 |
| D6 | **网络与进程可见性拿不到**：ACP 面没有「这一轮访问了哪些主机/起了哪些进程」的通道。平台的工具卡只能显示工具**自报**的 title/status，以及 `tool_call_update.content` 的正文文本；界面**不做**网络/进程维度的汇总，也不从 title 里猜（`bash` 的 title 是模型写的自由文本） | 底座提供结构化的网络/进程审计事件 | 2026-10-16 探针（`dsh 0.2.0-rc.2`） |
| D7 | **压缩不可观测**：底座**不通告**上下文压缩。实测把阈值压到几乎必触发、会话日志里两次完整压缩（`compaction/start→summary→end`），同一时段 ACP 面 **0 帧**、帧里 `compact` 命中 0。所以 `session.compact` 事件**没有外部事实来源**——平台**不发**这个事件，界面也不显示"正在压缩" | 底座在 ACP 面通告压缩（届时接 `session.compact`） | 2026-10-16 探针 · `.dsh/docs/notes/2026-10-16-workbuddy-alignment-execution-notes.md` P1-a ② |
| D8 | **阶段/进度不可观测**：ACP 服务端只发 6 种 `sessionUpdate`（thought/message chunk、`tool_call`、`tool_call_update`、`usage_update`、`config_option_update`），`phase`/`progress`/`_meta` 全包零命中。所以 `run.status` 的 `phase` 段**恒空**，`retrying`/`compacting` 两档**永不出现**（P0-2 当时"没找到观察点"的选择，现在有了实测依据） | 底座补 `agent_phase` / `progress` 类元数据 | 2026-10-16 探针 · P0-2 |
| D9 | **终端与 plan 不可见**：ACP 的 `terminal/*`（内嵌终端）与 `plan`（`sessionUpdate` 的 plan 变体）平台**不建模**——`normalizeUpdate` 的 `default` 分支丢弃它们。界面上没有"这一轮的计划清单"，也没有内嵌终端 | 底座把 plan 当常规更新推送、或出现内嵌终端的真实需求 | `pkg/acl/client.go` 的 `default:` 分支 |
| D10 | **沙箱形态读不到（两半）**：① ACP 面**没有** `SandboxEnforcement`（`full`/`partial`）——`packages/acp/**` 搜 `enforcement`/`permissionMode`/`sandboxMode` 零命中，`session/new` 只回 `{sessionId, configOptions}`。② 沙箱失败的结构化码（`SANDBOX_UNAVAILABLE`）在 `tool/result.data.error.code` 里，而 ACP 只转发 `message.content`，**那串字连文本里都不出现** ⇒ 平台只能按**文本签名**归类（`sandbox_denied` / `sandbox_unavailable`，见 `pkg/runtime/dsh/tool_failure.go`），**不能**用 `status` 判（被拒绝的那一帧 `status` 是 `completed`）。**因此界面不得声称「文件修改都被限制在工作空间内」**——平台没有能支撑这句承诺的事实 | 底座在 ACP 面透出 enforcement 与结构化错误码 | 2026-10-16 探针 · P1-b |

## 2. 平台自身未做（OPEN）

| # | 内容 | 触发条件 | 记录 |
|---|---|---|---|
| O2 | **无配置热重载**：`config.json` / 能力定义目录 / `runtimes.json` 变更需重启（与连接/进程生命周期强耦合）；新增 MCP server 也需重启。**唯一的 watcher 是开发态的资产重建通知**（`MATE_ASSET_WATCH`，默认关闭，交付形态下那条路由根本不存在），它不是配置热重载 | 运维要求不重启变更时 | §19.13 · §19.14 · §19.17 |
| O3 | **MCP 侧的工程化缺口**（2026-10-16 复核后收窄）：无连接池（每次调用一次 spawn/握手）、无并发限流/队列、调用结果不进事件流与审计表（网关只在 stderr 留一行 `(server, tool, outcome, latency_ms)`）、无断线重连、无定时/变更监听探测。**已关闭的那半见 §3 C18**（同名工具冲突） | 生产化 MCP 接入（并发量 / 审计 / 命名空间需求） | §19.16–§19.19 |
| O4 | **鉴权只有单操作员令牌**：`MATE_TOKEN` 一个静态串（默认空 = 不鉴权，仅回环可访问），无用户/角色/审计、无速率限制与防爆破、无令牌轮换与过期、静态资源不鉴权、`?token=` 可能进反代日志 | 多租户或公网暴露——**这是云端阶段的第一批工作** | §19.15 |
| O5 | **远程 runtime 无鉴权面**：`headers` 可带 token，平台不做 OAuth/mTLS；HTTP 形态的 GET 长流在建会话前会被 404（降级记进诊断）；服务端半场的会话只在内存 | 跨机 / 跨组织接入 | §19.14 |
| O6 | **进程回收边界**：非 unix 平台无进程组信号（降级不支持）；已 `setsid` 脱离进程组的孙进程不在回收范围；进程注册表无跨实例锁 | 非 unix 部署，或需要更严的回收 | §19.10 |
| O7 | **导出与检索的增强面**：导出无 zip 打包、无「导出全部会话」的服务端游标（`ids` 上限 50）；检索为子串匹配，无分词与相关性排序 | 大规模导出，或需要相关性排序 | §19.11 |
| O8 | **用量视图增强**：无图表（不引图表库）；前端时间区间按本地日期而后端按 UTC 半开区间（跨时区 ≤1 天偏差）；逐轮 token/费用明细仍受 ACP 通道限制（仅上下文快照 + 可选累计费用）。`buckets_additive` 已把「day 桶不可加」显式标注，`runs` 的每轮 `usage` 已暴露并在会话视图渲染 | 需要图表 / 时区精确 / 逐轮输入输出 token 明细 | §19.21 · §19.7 · §19.7.1 · §19.7.2 |
| O9 | **历史分页增强**：前端「加载更早」50 条/页、无虚拟滚动；`after` 与 `before` **互斥**，不支持组合区间；空视图点击「加载更早」等价取末尾一页 | 长会话性能，或区间查询需求 | §19.22 |
| O10 | **skill 投影增强**：无「按会话/cwd 动态 root」（见 D2 结论）、无 skill 文件变更热重载 | 会话级 skill 隔离 / 免重启更新 | §19.24 · §19.26 |
| O11 | **权限策略增强**：无「按工具参数模式匹配」（如 bash 命令白名单）；前端策略编辑的工具项是 JSON 文本域（无键值行编辑器）；原生配置在连接建立时固定，运行中 PATCH 只影响新连接 | 精细化策略 / 更好的编辑体验 | §19.5 · §19.20 · §19.21 |
| O12 | **排障可见性**：跨会话 elicitation 决策与「未知 id」**同码同文案**（细节只在服务端 WARN）；决策回执里的 `always` 是请求意图，不是「记忆是否真的写入」（记忆以 memories 读面为准） | 需要更强的审计/排障信息 | §19.23 · §19.25 |
| O13 | **事件持久化的边角**：`store.AppendEvent` 写库失败的事件不出现在 REST 历史（事件总线侧仍有，且打 WARN），位次退化为进程内分配 | 需要历史与事件总线完全一致 | §19.22 |
| O14 | **会话级权限模式不可达**：`/permission` 命令面在 ACP 上**不存在**（底座根本不发 `available_commands_update`，见 D10），所以"会话级模式下发"只能退化成「每模式一个暖进程 + `DSH_PERMISSION_MODE`」。今天是**每部署一个模式**：模式不同的两个会话共享同一个暖进程，各自拿不到自己的模式 | 底座补命令面，或愿意接受"进程池按模式分桶"的开销 | P1-a ③(b) · P1-f 偏离 ② |
| O15 | **队列项不能原子替换正文**：队列只补了三个动作（重排 / 立即发送 / 取出编辑），**没有** `surfaceOp: replace` 那类"就地改写一条已排队输入"。取出编辑会把它作废、让用户改完重新入队——语义上等价，但**位次会变**（改完排到队尾） | 出现"改一句话但保留排队位置"的真实需求 | ADR-10（刻意不做 17 个方法） |
| O17 | **工作空间不能改登记路径**：`PATCH /workspaces/:id` 收 `path` 但**显式拒绝**（`workspace_path_immutable`）。改路径要先定"既有会话的 `cwd` 怎么办"——它们的历史就在那个目录里，跟着搬等于把历史指到别处 | W4 落地时（归属自愈 + 改 path 同批） | P1-f `PatchWorkspace` 注释 |
| O18 | **既有会话的归属不自愈**：工作空间改名/搬迁/删除之后，`sessions.workspace_id` 仍指向那条登记，而登记指向的目录可能已经不存在（`dir_status: missing`）。今天只有读面如实上报，没有自愈路径（不自动重挂、也不批量改归属） | W4 落地时 | 计划 P1-g |
| O19 | **确认区一次只显示一张卡**：权限优先、其余以「还有 N 条」计数，**不能**并列处理。同时挂起多条时用户需要逐条处理 | 出现"一次要确认很多条"的真实场景 | P1-e `ConfirmArea.tsx` |
| O20 | **命令门禁未做**：斜杠命令目前是"底座自报清单 + 选中即填入输入框"，平台**不拦截**未在清单里的命令，也没有命令级权限 | A2 落地时 | 计划 P2-c |

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
| C14 | §16.3「backend-wide Runtime 多会话重构」评审点 | §17（E1/E2 完成 + 采用收口；其开放项分别落在 D1、O14、O15） |
| C15 | **浏览器渲染自动化/视觉走查缺失**（原 O1：仓库无 Playwright/Puppeteer，页面仅由 typecheck/build/bundle 断言覆盖） | 界面验收已落地且**零新依赖**：`pnpm ui:accept`（CDP 驱动真 Chrome + 真服务，自造自清夹具）与 `pnpm ui:accept:desktop`（真 Electron + 真核心）；另有三条桌面专项验收（skill 投影 / 无宿主机依赖 / 打包态）。**仍未做的是像素级对比**（那是另一种判据，不属本条） |
| C16 | **Resume 未接入平台恢复流程**（原 O14） | `internal/manager/session_start.go` 已按 `acl.Client.ResumeSession(resume=true)` 恢复**同一底座会话**（`session.resumed` 带 `reason=restart`）；未声明原生恢复能力的底座走 handoff memo。模型目录取样也复用同一条 resume 通道 |
| C17 | **会话的底座会话 id 未持久化**（原 O15，当时叫 `provider_session_id`） | 数据模型 v17 起该列持久化为 `sessions.runtime_session_id`（实体 / DAO / 迁移 / 恢复四处同源），决策 v13④ 同时把词汇从 `provider_*` 收敛为 `runtime_*` |
| C18 | **MCP 同名工具冲突「后探测者覆盖前者」**（原 O3 的一部分） | `backend/pkg/capability/mcp_tool_name.go` 的 `NamespaceTools` 做 fail-closed 裁决：工具暴露名 `<serverID>__<toolName>`，跨 server 撞名（`a`+`b__x` vs `a__b`+`x`）**整组丢弃并 ERROR 留痕**。**边界**：这只覆盖走平台网关的远程 MCP；stdio server 由底座直连，不在此列 |
| O16 | **权限记忆只有一种形态** → **已关闭（数据模型 v28）** | `permission_memories` 补两列 `scope`（授权形态，今天恒 `session`）与 `grant_input_digest`（当初那条工具入参的 sha256 前 16 位），`Upsert` 写入、启动装载 `WithGrantShape` 复原。**唯一键保持三元组**——把形态或入参并进去会让"这类工具始终允许"退化成"这一个参数组合始终允许"。原条目里"一次性的允许被永久记住"这一条**前提不成立**：`Remember` 只被 `always` 决策调用，`once` 从不落库 |
