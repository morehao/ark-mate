# 一个东西一个词：`session`（外加一个同名不同物的 `Session`）

> 从仓库根 `AGENTS.md` 拆出（2026-10-18）——本文件是分册后的唯一事实源；`AGENTS.md` 只保留入口与硬约束。
> 词规都是机器判据（见 [`guards.md`](guards.md)）；改表名/列名/DTO/前端类型前先读这一册。

**v15 起平台聚合的概念只有一个词：`session`。** 表名 `sessions`、REST 路径 `/sessions/:id`、Go 类型
`SessionEntity`/`ActiveSession`/`SessionRec`、包名 `dtosession`/`svcsession`/`ctrsession`、前端
`packages/types/src/session.ts` —— 全部对齐。wire 上本来就是 `session`，改名只是把后者追上来。

**这次是推翻旧决策**（2026-10-11，v15）：此前「wire 叫 session、平台领域词叫 conversation」被登记为
**冻结的历史词汇**、"改名零功能收益"。推翻的理由是收益并不为零——同一个东西四个词（表名/REST/Go 类型/wire）
让每个新人都要过一次「哪个词指哪个」，而 `grep -ri conversation` 的**零命中**是可以机器判据的
（`TestNoConversationVocabulary`）。代价与一次性口径见 `docs/decisions.md`「决策 v12」。

**消歧规则（v15 起生效，这是本节唯一要记的事）**：

| 词 | 指 | 允许出现的位置 | 禁止 |
|---|---|---|---|
| `Session` / `session`（无限定） | **平台聚合** | `sessions` 表、`model.SessionEntity`、`/sessions`、`dtosession`、前端、事件 `session.*`、`session_id` | — |
| `RuntimeSessionID` / `runtime session`（v17 之前叫 `ProviderSessionID`） | **runtime 侧的 ACP 会话**（原生底座会话 id） | `runtime_session_id` 列与字段、`internal/infra/protocol/acp.Session`、`NewSession`/`ResumeSession`/`SessionOptions`/`SessionCapabilities`/`SessionInfo`/`LoadSession`、health key `sessions_persistent`/`multi_session_concurrent` | **不得**出现在 URL 路径；**不得**命名为 `session_id`；**不得**出现在对外的 `id` 字段 |
| 局部变量（底座句柄） | 同上 | 一律 `acpSess`（v15 之前叫 `sess`） | 不得再出现裸 `sess`/`session` 变量名 |

**最容易踩的一处**：DTO / 前端里的 `SessionID` 指的是**平台会话 id**（= `sessions.id`），而
`RuntimeSessionID`（v17 之前叫 `ProviderSessionID`）才是 runtime 的。两者在 `SessionResp` 上并存（`service.go`），且**形状可分**：
平台 id 是 `ses_<26 位 ULID>`，底座 id 一律带 `rt_` 壳（`rt_<底座原文>`，见 `internal/core/object/objid`）。

**底座 id 的 `rt_` 壳（v15 / 选项 D）**：平台不改底座、只加壳，而且**只加壳不解析主体**——
`rt_9f3c2b8e-…`（dsh，裸 uuid）/ `rt_ses_01H…`（原生就发 `ses_` 前缀的底座）。包装 4 处（建会话落 rec ×2、探测列出会话 ×1、
memory 模式写入断言 ×1）、解包 3 处（原生恢复 ×2、探测 resume ×1），**只有这些点**允许调
`WrapRuntime`/`UnwrapRuntime`（守卫② 钉调用点白名单）。两条函数都**严格**：`UnwrapRuntime`
拒绝无壳、拒绝双包——因为"忘了解包"的后果不响（底座找不到会话 → 打一条 WARN 后走 handoff memo，
用户只看到"上下文丢了"）。

事件契约的三条边由 `TestEventTypeContractFrozen`（常量 ↔ 冻结清单）与 `TestFrontendEventTypesMatchRegistry`（清单 ↔ 前端 `Ev`）共同钉住；错误码同理见下一节。

**v16 起另有两条词规**（2026-10-11，同一次收口；设计 `.dsh/docs/specs/2026-10-11-data-model-v16-design.md`）：

| 词 | 指 | 落点 | 机器判据 |
|---|---|---|---|
| `definition` | **定义变更审计**：覆盖 skill / mcp / automation / provider / model **五种定义**（v19 起 agent 不再是一种定义） | 表 `definition_revisions`、列 `definition_id`、Go 类型 `DefinitionRevisionEntity`/`DefinitionRevisionDao`、常量 `DefinitionKind*`/`DefinitionAction*`、索引 `idx_definition_revisions_history` | `TestNoCapabilityRevisionVocabulary`：旧词 `capability_revisions`/`capability_id`/`CapabilityRevision` 零命中 |
| `run` | **平台聚合的一轮**（用户发一次 prompt → 终态） | 表 `runs`（v1 起就叫 runs）、`messages.run_id`、`inputs.target_run_id`、Go 类型 `RunEntity`/`RunRec`、`domain/session/runner.go`、事件 `run.started`/`run.ended`/`run.error`、配置键 `run_timeout`/`MATE_RUN_TIMEOUT` | 全仓 `turn` 只剩底座原文（见下） |

**`turn` 只允许留在底座原文里**（改了就等于改协议常量，不是改我们的词汇）：ACP 的 `StopReasonEndTurn` 与逐字取值 `"end_turn"`（`internal/infra/protocol/acp`/`internal/runtime` 原样比对，ACP 回的就是这个串）。

**v19 起还有一条词规：被撤掉的 agent 面**（2026-10-14）。agent 整个并入 runtime（底座构建期固定、用户不可自定义），于是表 `agents`、`/agents/*`、`agent_id` 身份、`dtoagent`/`ctragent`/`svcagent`、`DefinitionKindAgent`、`default_agent`/`imported_agents` 全部删除。机器判据两条：`TestNoAgentVocabulary`（**被撤掉的那批标识符**零命中——注意判据不是「grep 到英文词 agent 就红」：ACP 自己的词表里 `agent` 指的就是**底座进程**（SDK 的 `Agent` 接口、`AuthMethods` 的 `agent` 变体），`internal/infra/protocol/acp` 必须能照原文写）；`TestSessionIdentityIsRuntimeOnly`（会话身份只剩 `runtime_id`——实体 / DAO / 迁移 / 用量聚合四处）。白名单只放历史与**别人的原文**（`migrate.go` 的历史 DDL、停在旧版本的测试夹具、ACP 原文），判据与 v15 的 `conversation` 同形：每一处残留都要能回答「这是谁的原文」。决策全文见 `docs/decisions.md`「决策 v19」。

**v20 起还有一条词规：凭据去 vault 化**（2026-10-14）。密钥的权威从"文件 + AES-GCM"搬进库——`providers.key` 一列与 `mcp_secrets` 一张表；`internal/vault` 整个删除、`MATE_VAULT_KEY` 退役、引用词法 `vault:<ns>/<name>` → `store:<ns>/<name>`（旧前缀只在**导入档案文件**时被显式翻译并 WARN：档案不在库里，迁移改不到它们）。盘上只剩一份**可重建**的托管文档 `<MATE_HOME>/credentials.json`（严格 `引用 → 值` 映射，0600，权限不合即失败），四层回落（继承环境 > 托管文档 > `<cwd>/.env` > `<MATE_HOME>/.env`）的唯一实现是 `internal/infra/credentials`。机器判据三条：`TestNoVaultVocabulary`（剥注释后 `vault` 零命中；白名单只放一次性搬运、历史迁移与停在那一版的夹具）、`TestCredentialLayeringOrderIsSingleSource`（层序只在 `Layered.Resolve` 里判：实现体内的先后 + 别处不得自己读 `.env` + 词表只声明一次）、以及动态那半（设一把密钥跑一轮，断言 transcript/事件/日志零命中明文）。代价如实写：**库与它的备份现在含明文**，`backups/` 0700 与备份文件 0600 从"整洁"变成**硬要求**，共享机器不再安全。旧密文的搬运**只读不删**（`mate doctor` 报「已迁移，可删」，用户确认后自己删）；决策全文见 `docs/decisions.md`「决策 v20」。

**v24–v26 起还有一条词规：`models` 那一列的 `model` 与会话的 `model_id`**（2026-10-15，provider/model 身份收口；设计 `.dsh/docs/specs/2026-10-15-provider-model-identity-design.md`，计划 `.dsh/docs/plans/2026-10-15-provider-model-identity-plan.md`）。用户原话是「provider 的主键 id 使用底层 runtime 的值，db 层不该有自己的主键 id 吗」「`models` 中 `id` 和 `model_id` 两个字段也很有歧义」——两点都对，但根因不是"缺代理键"（`providers.id` 本来就是**用户自己起的 slug**，不是底座的值；`internal/core/object/objid` 早把"provider 刻意不铸造 id"登记成判据），而是**同一个词被用在底座命名空间里**。于是三步：

| 词 | 指 | 落点 | 机器判据 |
|---|---|---|---|
| `models.model`（v24 从 `model_id` 改名） | **发给上游的模型名**（外部世界的名字；不是任何一行的 id） | `models.model` 列、wire `model`、`resource.Model.Model`、`ModelValueAdapter.Value(providerID, model)` | `TestModelsVocabularyIsModelNotModelID`：真 schema 有 `model` 无 `model_id` + DTO（`dtomodel/model_list.go`）+ 前端类型（`ModelRow`/`ModelCreateReq`）+ 实体 gorm 标签四面，**反向**断言 `sessions.model_id` 仍在 |
| `sessions.model_id`（v26 改名保留、**语义改**） | **平台模型行 id**（`mdl_…`；指向 `models.id`）。运行时的不透明值**不落库、不过 wire**，由 adapter 现算 | `sessions.model_id` 列、wire `model`（`dtosession` 三处）、`SessionRec.ModelID` | `TestSessionModelRefIsPlatformID`（写入点必须先经 `resolveSessionModel`）+ `TestModelSelectionIsLimitedToPlatformList`（对不上平台清单必须**显式拒**，复用 `40401 model_not_found`） |
| `ConfigOptionValueResp.model_id`（v26 加） | **注解**：这条运行时候选对应平台清单里的哪一行（空 = 平台未声明 ⇒ 客户端选不了） | `GET /models/catalog` 的 `values[].model_id`、`domain/provider/catalog_read.go` 的 `ModelIDByValue`（唯一产出点，多命中 fail-closed） | 同守卫⑧ + `internal/sessionruntime/session_model_ref_test.go` 的三条链一致性判据 |

**消歧规则**（这是本节唯一要记的）：`_id` 一律指"指向某表 `id`"（`models.provider_id` → `providers.id`、`sessions.model_id` → `models.id`），而 `model` 是"发给上游的那个名字"。所以判据**按表/按面**限定，不是全局 grep：`sessions` 侧与目录注解的 `model_id` 是**正确**的名字，历史迁移里的 `model_id` 是**当时那一版**的名字。

**连带两条**（同一批，别漏）：① **路由键隔离**——交给 dsh 的每个路由键都带平台保留前缀 `mate-`（`internal/infra/dsh/route_key.go` 是唯一派生点；`config.go` 两处 + `model_value.go` 一处必须同源，否则所有选了模型的会话起不来），`displayName` 交付**供应商展示名**（空则回落路由键）；② **容量由平台声明**——`models.max_input_tokens`/`max_output_tokens`（**可空、无 DEFAULT**：0 当不了"未声明"的哨兵，底座 schema 是 `min(1)`）；路由键隔离之后"命中底座内置目录拿容量"这条退路消失，未声明的容量就是底座那句自注 "a guess by construction" 的默认值，界面必须如实标"容量由环境兜底"。判据是守卫①②③④（`dsh_route_key_test.go`）+ 守卫③的动态那半（`internal/infra/dsh` 的 `TestDshRouteKeyAgreesBetweenPatchAndModelValue`）。

**代价（如实登记）**：升级后三类变化——(a) `/models` 的键名 `model_id`→`model`（v24，纯改名）；(b) 交付给底座的配置字节变了（v25：路由键带前缀、展示名是真名、**原先靠路由名恰好命中内置目录拿到的容量不再生效**）；(c) 会话的 `model` 变成 `mdl_…`，**模型从未被平台声明过的会话升级后失去模型选择**（v26：存量回填只认"唯一命中"，零命中/多命中置空并计数）。三条都不写清楚就会被当成 bug。

**反过来说，凡是「我们自己起的名字」都要收口**：`internal/core/resource` 与 `internal/core/object/objdelivery` 的生效时刻枚举 `next_turn` 与它是**我们的**词（不是哪家底座的），已随 D4 改成 `next_run` —— 连带 `PATCH /providers/:id` 回执里的 `effect=next_run` 这个 **wire 值**（README 与前端注释同步改）。所以判据不是「grep 到 turn 就红」，而是「`turn` 的每一处残留都能回答『这是谁的原文』」：今天只剩 ACP 的 `StopReasonEndTurn`/`"end_turn"` 两处，外加历史迁移 DDL 与冻结的历史文档。

**为什么这张审计表不再叫 `capability`**：它在仓里是**重载词**——`capability_json`/`CapabilityScope`/`internal/core/capability` 指**运行期能力声明**（只有 skill + mcp），而这张表记的是**五种定义**的变更（后三种与"运行期能力"无关）。`definition` 是仓里**已有的词**（`store.DefinitionImportItem`、`internal/core/capability/definition.go`、`validDefinitionID`），收口到它不引入新词。

**`kind` 列刻意不加表名前缀**（`definition_revisions.kind`，不是 `definition_kind`）：列在**表的命名空间**里（`sessions.provider_id` 一眼看得出属于谁），只有**索引名**在库的全局命名空间里（`idx_memories_session` 属于 `memories` 还是 `permission_memories` 只能翻 DDL）。索引名带表名段（`idx_<表>_<用途>`）、列名不带——这是同一条规则的两面（守卫 A2）。

**标识符形态**：表名 / 列名 / 索引名一律 `^[a-z][a-z0-9_]*$`（守卫 A1，与 DSH 的 `UNIT_NAME_RE` 同形）。SQLite 的标识符**大小写不敏感**，混合大小写会让「这条 SQL 查的是哪一列」只能靠人脑做不敏感匹配，而 Go 侧字段名是敏感的。

**表名/列名改动的落地点是四处，改一处就得改四处**（v16 的实测教训）：`internal/persistence/store/migrate.go`（**只追加**新迁移，历史语句绝不回改）、`internal/persistence/model`（实体 + gorm `column:` 标签，**它写错编译期无感、运行期才报 no such column**）、`internal/persistence/dao`（upsert/update 的列名映射）、以及**停在那一版之前的测试夹具**（它们必须按**当时那一版**的表名走裸 SQL，同 `readLegacyConversation`/`insertLegacyDefinition`）。v24（`models.model_id` → `model`）是这条纪律的最新实例：`migrate_v13_test.go` 上界停在 v13，它读 `models` 便从 DAO 换成**裸 SQL + 当时那一版的列名**——DAO 认的是今天那一版（`ORDER BY provider_id, model`），拿它读 v13 库会当场 `no such column: model`，而那会红在**查询**上、不是红在被测的迁移上。
