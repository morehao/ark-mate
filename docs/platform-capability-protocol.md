# 平台能力协议（Platform Capability Protocol）

> **这份文档是什么**：`mate mcp platform` 这条 **MCP 工具面**的对外契约——工具集、语义、错误码、
> 幂等/CAS、以及"什么算符合本协议"。它是给**接入新 runtime 的人**、以及**给平台加新能力域的人**
> 看的唯一口径。
>
> **事实源是代码，不是本文**：工具 schema 在 `backend/internal/infra/{automationmcp,capabilitymcp}`，
> 聚合与分派在 `backend/internal/infra/platformmcp`，装配在
> `backend/internal/bootstrap/assembly/platform_mcp.go`，注入在
> `backend/internal/sessionruntime/platform_mcp.go`。**本文与代码冲突时以代码为准。**
>
> 设计依据：[`.dsh/docs/specs/2026-11-07-nl-create-expert-and-schedule-design.md`]（§8 两层协议）；
> 执行计划：`.dsh/docs/plans/2026-11-07-platform-mcp-execution-plan.md`（内部文档，不入库）。

## 1. 这份协议解决什么

用户用**自然语言**在会话里说"帮我建个代码审查员专家""每天 9 点提醒我喝水"，模型把它翻译成
对 `mate mcp platform` 的**工具调用**。平台不把自然语言交给某个底座去解释，而是：

1. 平台以 **MCP server**（stdio）形式提供结构化工具；
2. 经 ACP 的 `mcpServers` 把这条 server 注入会话；
3. 模型填**语义字段**（不是规则串/JSON），平台服务端做**唯一闸门**校验与落库。

## 2. 什么算"符合本协议"（A 级硬约定）

> **任何 runtime，只要 (a) 说 ACP、(b) 支持 stdio MCP server 注入，就自动获得本协议的全部工具。**

这条是"能力协议中立"的可操作定义。判定依据是 `runtime.Spec.McpTransports`（空 = 仅 stdio，
ACP 全 agent 必支持）。平台在 `sessionruntime/platform_mcp.go` 的 `appendPlatformMcpServers` 里
按这条判据注入，**不认任何具体底座 id**。

因此：**给本协议加一个能力域（如技能 NL 创建），不需要改任何 adapter**——只往聚合器的
`domains` map 加一项（域前缀 + 该域 `mcp.Handler`）。

## 3. 工具集

工具名 = **域前缀 + `_` + 动作**。聚合器按**首个下划线前的段**分派（`expert_set_enabled` 的域是
`expert`，不是 `expert_set`）。新增域必须选一个**不含下划线**的前缀。

### 3.1 `automation_*`（定时任务）

| 工具 | 语义 |
|---|---|
| `automation_create` | 新建：`name` + `prompt` + 一个 `schedule` 选择器 + `target` |
| `automation_update` | 增量改（只改传入键） |
| `automation_list` | 列表（含装载失败带 `error` 的） |
| `automation_get` | 详情 + 最近运行史摘要 |
| `automation_delete` | 删除（幂等） |
| `automation_preview` | 只算未来 N 次到期时刻，**不落盘、不建会话** |

`schedule` 是**五选一**（`once` / `interval` / `daily` / `weekly` / `monthly`），**没有 cron**——
模型永远只填语义字段，不产语法串。详见 `docs/` 的定时任务设计口径与
`backend/internal/core/automation`（时间契约的事实源）。

### 3.2 `expert_*`（专家定义）

| 工具 | 语义 |
|---|---|
| `expert_create` | 新建专家（会话级能力预设：推荐技能/MCP/模型 + 角色设定） |
| `expert_update` | 增量改（`id` 不可改：要换 id 就删掉重建） |
| `expert_list` | 列表（**不含 `instructions` 正文**，只有 `has_instructions` 标记） |
| `expert_get` | 详情（含 `instructions` 全文） |
| `expert_delete` | 删除（幂等） |
| `expert_set_enabled` | 启用/禁用（禁用只影响新会话，不删定义、不影响历史会话） |

专家字段与 `capability.ValidateExpert`（`backend/internal/core/capability/expert.go`）**同口径**：
`id` 正则 `^[a-z0-9]+(-[a-z0-9]+)*$` ≤64、`name` 1–40、`description` ≤120、`avatar` 枚举
`spark|chart|code|pen|search|briefcase`、`model` 前缀 `mdl_`、`skills`/`mcps` ≤50、`tags` ≤8 每个 ≤20、
`instructions` ≤4096 **字节**且**禁止危险标记**（`{{` `}}` `<!--` `-->` `</system-reminder>`）。

> **专家 ≠ 运行时 agent**：专家是**数据**（命名的会话级能力预设），不是 agent 实体。它真正的
> 作用在建会话时被解析成技能/MCP/模型 + 角色设定。因此 `expert_create` 成功**不会**让当前会话
> 变成该专家——工具 description 要求模型明确告知用户"下次新建会话可选它"。

## 4. 错误码

服务端是唯一闸门，工具把服务端错误**透传成 `isError` 结果**（模型看得到原因，会话不炸），
错误码词表：

| 码 | 含义 | 典型触发 |
|---|---|---|
| `40001 definition_invalid` | 定义被拒 | id 非法 / name 超长 / `instructions` 超字节或含危险标记 |
| `40001 invalid_param` | 参数非法 | 写面 body 结构错、id 与路径不一致 |
| `40901 expert_exists` | 新建时 id 已存在 | `expert_create` 撞已有 id（**绝不覆盖**） |
| `40901 definition_changed` | CAS 失败 | 并发写抢先（绝不 last-write-wins） |
| `40401` | 目标不存在 | `expert_update` 未命中（平台不顺便新建） |
| `automation_exists` / `delivery_*` | 定时任务侧 | 见 `docs/agents/error-codes.md` 与自动化设计 |

协议层错误（工具名不存在 / 已停用）走 MCP 的 `-32602`（`mcp.ErrToolNotFound`），与"工具跑了但
失败"（`isError` 结果）**分线**——这条分线决定前端看到"参数错误"还是"工具报错"。

## 5. 幂等与 CAS

- **删除幂等**：不存在 → `deleted:false` 且无错。
- **写面 CAS**：专家与定时任务的覆盖写都带 revision 期望；不中即 `40901 definition_changed`，
  **绝不静默覆盖**。
- **写面 patch 语义**（与 REST 逐字一致）：**未出现的键保留原值、`null` 删键、`""`/`[]` 显式清空**。
  因此"只改一个字段"不会冲掉用户手写的未知键。

## 6. 边界

- 本协议只覆盖**数据面 CRUD**（专家定义、定时任务定义）。**不实现任何 agent 运行时能力**
  （推理、工具循环、技能执行归底座）。
- 定时任务的**调度执行**由平台核负责（单一会话 owner）；`mate mcp platform` 子进程**只碰库**，
  触发执行由 daemon 侧的调度循环每 tick 重新装载定义。
- 专家 `instructions` 的人格**注入通道**是 runtime 相关的（当前 dsh 走首轮隐藏 user-context）。
  若未来接第二个底座，按设计 §8.5 预留的 `PersonaInjector` 端口扩口——**本协议不定义注入通道**，
  只定义"专家有这段人格"。

## 7. 给"加一个能力域"的人

1. 新建 `backend/internal/infra/<域>mcp/`，实现 `mcp.Handler`（`ListTools` + `CallTool`），
   **自声明窄端口 Service，不 import `service/svc*`**（层规），工具名以 `<域>_` 开头；
2. 在 `backend/internal/bootstrap/assembly/platform_mcp.go` 的 `domains` map 加一项；
3. 在 `backend/internal/archtest/layers_v32_test.go` 登记新包（L5）；
4. 在本文 §3 补一节工具表 + 错误码；
5. 在 `backend/internal/bootstrap/cliops/mcp_platform_test.go` 的工具名集合里加新工具。

**不需要**改 `sessionruntime/platform_mcp.go`（注入表不动），**不需要**改任何 adapter。
