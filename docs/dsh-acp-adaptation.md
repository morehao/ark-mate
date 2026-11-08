# 内置底座适配：dsh（deepseek-harness）

> **这份文档是什么**：平台与内置底座 `dsh` 之间的**接入事实**——启动形态、能力基线、配置隔离、
> 原生凭据、模型清单交付、权限投影、已知边界。
>
> **事实源是代码，不是本文**：声明汇入点是 `backend/internal/runtime/adapters.go` 的 `dshSpec()`，
> 实现住在 `backend/internal/infra/dsh/`（`dsh.go` 形态与能力、`config.go` 模型清单交付、
> `credentials.go` 原生凭据、`route_key.go` 路由键、`model_value.go` 模型取值、`native_permission.go`
> 权限投影）。**本文与代码冲突时以代码为准**——它是地图，不是契约。
>
> **沿革**：本文件取代了旧文档 `deepseekharness-acp-adaptation.md`（2026-08/09 对 dsh **v0.1.x**
> 快照的源码核实记录）。那份记录里的若干结论（独立 `dsh-acp-demo` bin、`--config <cordis.yml>` 形态、
> 只支持 fresh session）**早已不成立**，把它当现状读会做错决定；它的历史价值留在 git 提交里。
> README 与 `internal/infra/dsh/dsh.go` 此前指向的路径一直写成本文件名，本次让磁盘追上引用。

## 1. 启动形态

| 项 | 值 |
| --- | --- |
| 命令 | `dsh`（统一启动器，npm 分发；ACP server 是它的一个 **profile**，不是独立 bin） |
| 参数 | `--profile acp`（平台按需追加 `--patch <绝对路径>`，见 §5） |
| 分发 | 随包载荷 pin **`0.2.2-rc.1`**（`frontend/apps/desktop/release.json` 的 `payloads[]`）；未安装时可 `npx` |
| 版本要求 | 必须是**含 `acp` profile 模板**的版本——模板首用会从内置模板初始化到 `$DSH_HOME/profiles/` |
| 鉴权 | `authMethods` 为空 ⇒ authenticate 立即成功，平台不触发 agent 鉴权分支 |
| stdio 纪律 | stdout **只**承载 NDJSON ACP 帧，诊断走 stderr；stdin EOF / 连接关闭 / SIGINT / SIGTERM 都触发有界关闭 |

## 2. 能力基线：静态声明 vs 连接期事实

`dsh.Caps` 是**按源码声明的静态基线**，不是能力事实：

| 声明 | 值 | 说明 |
| --- | --- | --- |
| `SessionsPersistent` | `true` | `session/new` 即持久；disconnect/close 走同一 quiescent teardown，结束后仍可 list/resume（**「断连即失效」不成立**；断连时进行中的 run 被取消） |
| `MultiSessionConcurrent` | `true` | 同一连接可并行多会话 |
| `Images` | `false` | **保守基线，不是门禁**：真值取决于附件仓是否可用与**当前路由**的 `models[].input` 是否含 image。拿它当门禁，用户会在一个其实能收图的 dsh 上被告知「不支持」 |
| `EmbeddedContext` | `false` | 同上，以连接期探测为准 |

`Caps` 的**消费者是零**：唯一旧出口 `/runtimes` 已随决策 v18 删除，「能不能收图」一律走连接期事实
（`internal/infra/protocol/acp` 解析 `initialize` 得到的 `Capabilities.Images`）。由 `TestStaticCapsImagesHasNoConsumer`
钉住「没有新的读法」。

**不支持的面**：`session/load`、fork/delete、附加目录、终端、客户端 FS、SSE 与传输级 MCP。
`McpTransports` 只声明 `stdio` + `streamable_http`（dsh 拒 `sse`）。

**0.2.1 的结算语义**：`session/prompt` 要等**根 Agent + 受管后代 + 已提交输出 drain** 完才返回；
`session/cancel` 也等当前根活动与已排队输出完成（之后到达的子消息不延迟取消响应）。
所以平台不能按旧口径「发完 cancel 就本地解除」——那会把迟到的 `session/update` 甩到下一轮
（ACP 没有 run id）。平台的对应处置是 `AgentSettleTimeout`（10s 有界）：
`backend/internal/domain/session/runner.go`。

## 3. 配置隔离

隔离表只给**一个**变量：

| 变量 | 落点 | 启动器兜底 |
| --- | --- | --- |
| `DSH_HOME` | `<MATE_HOME>/runtime/dsh` | **是**（`LauncherFallback: true`） |

- **必须有启动器兜底**：dsh 连 `--version` 都会写自己的 home（实测：不隔离时往用户的 `~/.dsh` 写日志），
  而桌面壳直接跑随包载荷那条路径上没有核心注入。
- **刻意不注入 `DSH_AGENTS_HOME`**：dsh 的 skill provider 按 `config.agentsHome ?? $DSH_AGENTS_HOME ?? ~/.agents`
  解析 agents 根，而平台把 skill 投射到 `${HOME}/.agents/skills`。一旦注入它，**读根与写根分叉**、
  skill 静默不生效，而平台侧照旧自报 `in_sync`（2026-10-09 E5 实测）。这条根是**跨工具约定**
  （`~/.agents` 不属于任何单个底座），不是某个底座的私有配置。护栏：
  `TestIsolationKeepsSkillRootAlignedWithProjection`。
- 隔离表在仓库里有**三份**（核心 / 桌面私有启动器 / V9 探针），**改一处就得改三处**——
  详见 `architecture.md` §3.4。

**Skill 投影**：`~/.agents/skills/<id>/SKILL.md`（带 frontmatter），已端到端实测（`Tested: true`）。

> **资源初始化单独成册**：底座随包载荷（dsh 载荷 + `primary-runtime`）、`mate-acp` profile 覆写层（office 三件套 / present / deliverable-file-references）、4 个 office env 注入，以及「升 dsh 版本时要同步的锚点清单」见 [`agents/dsh-resources.md`](agents/dsh-resources.md)。本文件不重复这套资源口径。

## 4. 原生凭据

平台写**底座的原生凭据文档** `<MATE_HOME>/runtime/dsh/.credentials.yaml`，换来**热生效**。

| 项 | 值 |
| --- | --- |
| 引用名模板 | `MATE_%s_API_KEY`（`MATE_` 前缀是平台命名空间，读写都按它识别） |
| 环境回落 | **关闭**（`FallbackEnv: false`）——见下「env 赢」 |
| 实测版本 | `0.2.2-rc.1`（H6 探针 7/7 PASS，2026-10-18） |

官方文档写明的文档形态（平台只依赖它，不猜）：`version: 1` + `refs:`（按**环境变量名**存密钥值）
+ `records:`（各插件自己的凭据）。

**五条纪律**（都是实测换来的，不是设计洁癖）：

1. **权限 0600（父目录 0700）且落盘后读回校验**，不满足即中止——dsh 在启动与重载时都**拒绝**
   可被属主之外读取的文档，错误文案指向 `chmod 600`。所以权限不合是**拒启**，不是告警。
2. **读-改-写，绝不整份覆盖**：文档是共享面（还有 `records:` 与用户自己的 refs），整份覆盖会抹掉
   用户自己的密钥；dsh 也从不回写它（平台只替换 `refs` 分节，注释与其余分节逐字保留）。
3. **同一 runtime 的 env 与文档绝不能同时启用**：0.2.1 起实测「文档赢」（0.2.0 是 env 赢）——
   两者同时给时实际用的是文档，于是 env 变成**静默死值**；反过来若哪天底座改回 env 优先，
   被遮蔽的就是文档。`FallbackEnv: false` 就是这条的声明（只启用一条通道）。
4. **只写平台独占根**（`<MATE_HOME>/runtime/dsh/`），绝不写用户的 `~/.dsh`。
5. **这不是安全改进，是持久化换热生效**：官方文档逐字写着「agent 的工具进程以同一 OS 用户身份运行，
   因此该存储**无法向 agent 隔离机密**」。

## 5. 模型清单交付

平台写**自己的** overlay `mate-models.patch.yml`，落点是隔离表注入的 `DSH_HOME`，并经启动参数
`--patch <绝对路径>` 交给 dsh。

**为什么不用 `$DSH_HOME/cordis.patch.yml`（home 级 patch）**：那份是 dsh 自己的持久层，写它会挤掉
用户从底座设置界面里的权利，而且写坏就是**拒启**、只能靠修文件恢复。平台的 overlay + 启动参数坏了
把参数去掉即可（回到出厂配置），归属也干净（那个文件只有平台写），且文件名带 `mate-` 前缀表明归属。

**两级校验门**吃的是 `dsh --dump-config-schema`（默认超时 20s）：只有它输出 JSON 并带
`x-cordis.{complete,diagnostics}`；`--dump-config` 输出 YAML、诊断只写 stderr，判据不好机器化。
代价是 schema dump 会 import 各插件模块（可能初始化 profile），所以 staging home 是必需的。

**模型取值**走 ACP 会话配置项（`session/set_config_option`，会话级、可共享连接）：目录与取值全部由
dsh 自报（option id `model`，值是 `JSON.stringify([provider, model])`），平台只按 `category` 消费、
**不解析也不拼接**。交给 dsh 的每个**路由键**带平台保留前缀 `mate-`，派生点唯一
（`internal/infra/dsh/route_key.go`，`config.go` 两处 + `model_value.go` 一处必须同源）——不带前缀时
`providers` 的 dict 键会命中 dsh 内置目录，那一家的端点/协议/展示名会变成这条路由的逐字段默认值，
「平台配了 A、实际按 A 的目录解析出 B」会**静默发生**。

**0.2.1 的凭据门禁**：`llm-pi-ai` 的 `listModels` 在路由的 `apiKeyEnv` 解析不到时返回空目录——
于是「缺密钥」的表现是**模型不在广告列表里**，而 `set_config_option` 会拒绝一个不在列表里的值。
平台在 `applyModel` 前用 `Spec.ModelCatalogNeedsCredential` + 供应商密钥状态给出可执行的
`model_credential_missing`，不把底座的 `invalidParams` 原样甩给用户（见
`backend/internal/domain/session/lifecycle.go`）。

## 6. 原生权限投影

`DSH_PERMISSION_MODE` 同时决定 dsh 的 **sandbox 模式**与 **approval 策略**（仅 `danger-full-access`
时 approval=never）。平台把会话的权限策略投影到它，形成「原生层 + 平台拦截面」的双层。

它的取值在内核 wire 常量（`permission_policy.native_mode`，对外契约的事实源）与 adapter 侧
**必须逐字相同**（守卫 `TestNativePermissionModeLiteralsMatchWire` 钉住）。分叉的后果是**静默**的：
adapter 落进 `default` 只写进描述、不落日志，表现为「我设了没反应」。

**边界**：原生沙箱**不构成对平台放行的否决**——平台策略 `allow` 时，dsh 的沙箱升级请求也会被放行
（见 [`unimplemented.md`](./unimplemented.md) D3）。

## 7. 已知边界

本文件只描述**适配面**；能力缺口与平台自身未做项的唯一汇总口径在
[`unimplemented.md`](./unimplemented.md)。与 dsh 直接相关的几条：

- **D2**：skill 投影根不可按会话指定（一条会话只能拿到投影根下全部候选 skill）。
- **D3**：原生权限只映射已知键，且原生沙箱否决不了平台放行。
- **D4**：用量成本依赖底座上报——dsh 当前不上报 `usage.cost`，界面显示 `—`，平台不做估算。
- **D5**：dsh 的 `.system` 子目录与若干可选键未映射。
