# 控制面 P0 加固设计（2026-09-11）

> 参考实现：compozy（`/Users/morehao/Documents/study/ai-agent/compozy`，Go/MIT，与 ark-mate 同用
> `coder/acp-go-sdk v0.13.5`、同 gin、同纯 Go SQLite）。本文档的全部 compozy 侧引用均由四路只读深扫产出，
> 并**逐行回读核对**（其中 3 处错锚点已纠正：`internal/acp/start_process.go:146`、
> `internal/mcp/mcppolicy/headers.go` 路径笔误、`internal/vault/types.go:30`）。
>
> 本文档为**方案存档**，尚未改动任何代码；落地按 §8 顺序推进。
>
> 与既有文档的关系：§2（归属判定）是 `docs/agent-interactions-acp-v1-design-20260907.md`
> Rev2「触发式能力位预案」的**局部提前兑现**——只做零 fork 可做的部分（`_meta` 容错读取 + 归属一致性 + complete 收尾），
> **不改变 Rev2「不 fork SDK」的结论**；顶层规范 scope 仍留作触发式预案（§2.3 T4）。

---

## 1. 背景与判定口径

compozy 与 ark-mate 定位重叠度高（ACP runtime 的统一控制面），机制可直接互相参照；差异在于
compozy 的复杂度来自「无人值守 + 多控制面 + 多租户工作区」，那部分对单操作员本地优先的 ark-mate 是负担。

**判定口径**（本文档只收录满足全部三条的项）：

1. 有 **ark-mate 侧的真实缺口**（不是"也可以更好"，是现状确实缺）；
2. 有 **compozy 侧的可验证实现**（`文件:行号` 锚点，已回读）；
3. **不引入 ark-mate 定位外的能力**（多租户/沙箱/编排/网络/记忆/扩展面一律排除）。

按此口径收敛出 **5 条 P0**（§2–§6）与 **3 条顺带安全硬化**（§7）。

---

## 2. ① 归属判定：从「猜」升级为「可寻址 + 歧义即拒 + 收尾」

### 2.1 现状（ark-mate 侧证据）

| 事实 | 证据 |
| --- | --- |
| 权限请求**有** wire sessionId，但 `_meta` 零利用 | `pkg/acl/permission.go:40`（读 `p.SessionId`）；SDK 实际**建模了 `Meta map[string]any`**：`acp-go-sdk@v0.13.5/types_gen.go:4272` |
| **决策入口把 `:id` 丢了** | `internal/controller/ctrconversation/conversation.go:155` 传了 `c.Param("id")`，但 `internal/manager/interaction.go:29` 的 `id` 形参**全程未使用**，直接 `s.orch.ResolvePermission(callID, …)`（`:33`）；`internal/manager/permission_flow.go:79` 签名里也没有 conversationID |
| 而 elicitation 侧**有**归属校验 | `internal/manager/elicitation.go:206-223`（owner 不符 → 只回 mismatch，对外统一 40401） |
| elicitation scope 靠推断 | `pkg/acl/elicitation.go:19-25`；`internal/manager/connection.go:88-89` 置 `ScopeSource=inferred_inflight` |
| **`elicitation/complete` 是空实现** | `pkg/acl/elicitation.go:125-127` 直接 `return nil`；而 SDK **建模了 `ElicitationId`**：`types_gen.go:6716` |

即：**同一类"用户交互归属"问题，两条路的严格程度不一致**——elicitation 防跨会话决策，权限决策不防。

### 2.2 关键新发现（改变原方案）

- `UnstableCreateElicitationForm` / `UnstableCreateElicitationUrl` **都建模了 `Meta`**
  （`types_gen.go:6779` / `:6794`），且变体解码是 `json.Unmarshal(b, &v)`（`:6816-6832`）
  → **`_meta` 会被完整保留**；被丢弃的只有**顶层** `sessionId`。
- 因此：把 scope 放 `_meta` 的发送方**今天就能零 fork 读到**；规范顶层 scope 仍需补 SDK，
  与 `agent-interactions-acp-v1-design-20260907.md` §3.2「fork 改动量极小」的预案一致——**该预案维持触发式**。
- compozy 的可寻址做法（`internal/acp/permission_helpers.go:13-20` 的三键回退 `request_id`/`requestId`/`id`）
  可直接移植到权限路径（SDK 已建模 `Meta`），无需 fork。

### 2.3 改造任务（4 个可独立上线）

| # | 改动 | 文件 |
| --- | --- | --- |
| **T1** | **权限决策带会话校验**：`Orchestrator.ResolvePermission(conversationID, callID, …)`；`PermissionManager.ResolveDecision` 增加 owner 比对（`pendingPermission.conversationID` 已存在：`manager/permission.go:29-31`，决策入口 `:234`）；mismatch 与「不存在」**同码同文案**（对齐 `manager/elicitation.go:213-216`） | `manager/interaction.go`、`manager/permission_flow.go`、`manager/permission.go` |
| **T2** | **`_meta` 容错读取 + 第三种 ScopeSource**：新增 `scopeFromMeta()`，键序列 `sessionId`/`session_id`/`requestId`/`request_id`；权限侧读 `Meta` 取 `request_id` 作可寻址决策键；elicitation 侧 `Form.Meta`/`Url.Meta` 命中即直填 `SessionID` 并置 `ScopeSource=wire`（常量已存在：`pkg/acl/elicitation.go:39`）；**busyConn 歧义时不再静默 cancel**，置 `ambiguous` 并落一条诊断 | `pkg/acl/permission.go`、`pkg/acl/elicitation.go`、`manager/connection.go`、`manager/elicitation.go`、`manager/diagnostics.go` |
| **T3** | **`elicitation/complete` 收尾**：用已建模的 `ElicitationId` 反查挂起项 → 命中即收敛卡片，发 `session.elicitation_resolved`（`reason=complete`）；未命中仅 debug。现状：URL 模式下用户点开链接后卡片一直挂到超时 | `pkg/acl/elicitation.go`、`manager/elicitation.go` |
| **T4** | （**不在本批**）按 Rev1 §3.2 补 SDK fork 顶层 scope，全面切 `wire` | 触发式预案 |

### 2.4 契约影响

- ~~新增 `scope_source` 取值 `ambiguous` → DTO + 前端联合类型需扩~~ → **实施后修正**：歧义路径不注册
  挂起项（按取消回应），没有 `ElicitationResp` 承载它 → 常量只落 `pkg/acl`，可观测性走 agent 诊断
  `remediation`（见 §2.6）。
- T3 给 `session.elicitation_resolved` 的 `reason` 增 `complete` 取值 → **事件 type 不变，冻结快照不动**。
- T1 无对外形状变化（错误码复用 40401）。
- 顺带借 compozy 的「错误附可执行建议动作」（`internal/diagnostics/item.go:60`）——T2 的歧义诊断正好用得上。

### 2.5 守卫与测试

`TestEventTypeContractFrozen` / `TestFrontendEventTypesMatchRegistry` 不受影响；
`TestReadmeRoutesMatchRouter` 不动（无新路由）。新增测试：跨会话决策必须 40401、`_meta` 命中走 `wire`、
complete 收敛、歧义路径可观测。

**工作量：T1 S / T2 M / T3 S。优先级最高。**

### 2.6 实施记录（2026-09-11，分支 `feature/control-plane-hardening`）

T1/T2/T3 已实现，`go test ./...` 与前端 `pnpm typecheck` 全绿。与上文的**两处出入**（都是收敛，不是缩水）：

| 项 | 原方案 | 实施 | 理由 |
| --- | --- | --- | --- |
| T2 权限侧 | `_meta` 的 `request_id` 作「可寻址决策键」 | 只作**归属证据**（`PermissionRequest.RequestID` + 注册日志 `wire_request_id`），不改决策寻址 | ark-mate 的待决键是宿主自生成的 `call_id`（`manager/permission.go:149`），wire `request_id` 没有第二条寻址路径可用；compozy 需要它是因为它按 wire 请求寻址 |
| T2 歧义取值 | 新增 `scope_source=ambiguous` 且扩 DTO/前端 | 常量落 `pkg/acl`，**不进 DTO**：歧义路径不注册挂起项，没有载荷承载；改为写 agent 诊断 `remediation` | 不为塞一个取值而改变「不归属就不投卡」的语义 |
| T3 收敛 action | 只写「命中即收敛卡片」 | 收敛为 `accept`（无 content）+ `reason=complete` | agent 说流程已完成，`cancel` 会谎报「用户放弃」；URL 模式本就不由客户端收集 content |

实现清单（每项可独立回滚）：

- **T1**：`manager/permission.go` 的 `ResolveDecision(conversationID, callID, d)` 增加 owner 比对（mismatch 与「不存在」同形：`found=false` + Warn 日志）；`permission_flow.go` 的 `ResolvePermission` 透传归属；`interaction.go` 补 `hasConversation` 前置校验（未知会话 40401）。`ResolveAllow/ResolveReject` 保持「按 call_id 直投」语义并显式注明**不做归属校验**（仅内部/测试入口，REST 决策不经过它们）。
- **T2**：新增 `pkg/acl/scope.go`（`_meta` 容错读取：只认非空字符串、键按序回退、裸 `id` 不认）+ elicitation Form/Url 两分支读取（命中置 `ScopeWire`）+ `Orchestrator.resolveElicitationScope` 三分支（wire → inferred → ambiguous）+ `Service.noteScopeAmbiguous` 与 `AgentDiagnostics` 的 remediation 外露。
- **T3**：`acl.ClientOptions.CompleteElicitation` 钩子 + `ElicitationManager.CompleteByURLID`（按 ACP 侧 `urlID` 反查、只认 URL 模式）+ 终局原因贯通成常量（`user`/`timeout`/`cancel`/`complete`），随 `session.elicitation_resolved` 上报。

新增测试：`TestResolveDecisionRejectsCrossConversation`（跨会话决策不投递、不删挂起项，归属会话仍能决策）、
`TestElicitationCompleteByURLID`（含 form 模式与重放不收敛）、`TestResolveElicitationScope`（三分支）、
`TestAgentDiagnosticsShowsScopeAmbiguous`、`pkg/acl` 的 `TestSessionIDFromMeta`/`TestRequestIDFromMeta`/
`TestElicitationScopeFromMetaSDK`。

---

## 3. ② 统一脱敏闸门（单点收口）

### 3.1 现状

**全仓零脱敏**——`grep -i redact` 只命中两处注释（`pkg/acl/errors.go:25`、`pkg/code/acl.go:48`，
说的是"不泄露内部细节"，不是密钥清洗）。泄露面三条：

1. `stderr_tail` 进诊断载荷（`manager/event_sink.go:133-145`，采集在 `pkg/acl/client.go:120-165`）；
2. events 表 payload（`manager/publish.go:27` → `store.AppendEvent`）；
3. transcript 逐行落盘（`manager/publish.go:58` → `manager/transcript.go`）。

compozy 对照：脱敏是**落库/回放/推流前的必经路径**，且分两档强度（给人看的文本走 display 脱敏，
结构性标识走另一档）：`internal/transcript/redaction.go:197`、`:201`，JSON 载荷字段白名单 `:190-195`。

### 3.2 改造

1. 新增 `internal/redact`：
   - `RedactString`：正则族（`sk-[A-Za-z0-9]{16,}`、`Bearer\s+\S+`、`AKIA[0-9A-Z]{16}`、
     `ghp_/github_pat_`、PEM 块）+ 高熵兜底；
   - **`RegisterDynamicSecret(value) (unregister func())`**（compozy `internal/redact/dynamic.go:30`）：
     保存/注入 provider key 时把**真实密钥值**登记进脱敏表（引用计数），使其出现在任何日志/报错里都必被替换；
   - 两档脱敏入口（display / structural），对齐 compozy `transcript/redaction.go:190-201`。
2. **三个接入点**：
   - `manager/publish.go:18` 入口一处收口 → 覆盖 DB + WS + transcript + runs 物化；
   - `manager/event_sink.go:133-145` 的 `stderr_tail` 逐行过闸；
   - 错误/诊断出口（`manager/diagnostics.go`、`pkg/acl/errors.go`）统一过闸——compozy 在**唯一错误出口**
     做递归脱敏（`internal/api/core/errors.go:46`），ark-mate 对应 `pkg/code.FromError`。
3. 注入前把"凭证形状"变量从继承环境剔除（compozy `internal/session/provider_runtime_secrets.go:120`
   + `internal/procutil/env.go:84`）。

### 3.3 契约影响

无 wire 形状变化。**注意：已落库的历史事件不会自动脱敏**——建议先只做新写入；历史清洗另立子命令决策。

### 3.4 守卫与测试

新增 manager 内部测试：构造带 `sk-…` 的 delta 与 stderr，断言**落库 payload / WS 帧 / transcript 三处**
都已被替换。

**工作量：M。**

### 3.5 实施记录（2026-09-11）

已实现，`go test ./...` 与 `pnpm typecheck` 全绿。

**一处必须的修正**：包位置 `internal/redact` → **`pkg/redact`**。原方案把脱敏放 `internal/`，
但 stderr 尾巴的出口在 `pkg/acl`（`TailLines`），而 `pkg/` 不得 import `internal/`（`archtest` 守卫）——
放 `pkg/redact` 才是「可复用基础设施」的正确归属，也让 `pkg/acl`/`pkg/code`/`internal/manager` 三处共用。

**接入点实际落成 5 处**（比原方案多两处，都是"唯一出口"型收口）：

| # | 接入点 | 覆盖 | 为什么是这里 |
| --- | --- | --- | --- |
| 1 | `pkg/acl/errors.go:TailLines` | stderr 尾巴的全部消费者（诊断快照 / `turn.error` 载荷 / `Error.StderrTail`） | stderr 尾巴的**唯一**产出函数：比在 `event_sink` 逐行洗更彻底（错误对象里的尾巴也一并干净） |
| 2 | `pkg/code/code.go:Error` | 全部对外错误文案（`FromError` 与 `fromACL` 都汇到此） | 原文案可能夹带上游报错回显的 key |
| 3 | `internal/manager/publish.go` | events 表 / WS 帧 / transcript / runs 物化 | 事件唯一出口，一处过闸四下游全覆盖 |
| 4 | `internal/manager/providers.go:providerEnvFor` | 真实密钥值的**动态登记**（零误报档） | provider key 是当前唯一已知的真实密钥来源；形状未知的 key 靠它兜住 |
| 5 | `internal/manager/diagnostics.go:recordAgentError` | agent 诊断快照的 `Details` | 与 #1 双保险（诊断 REST 不经过事件出口） |

**两档强度**：`redact.String`/`Lines`（display：正则族 + 已登记值 + 高熵兜底）与
`redact.Payload`（structural：递归只洗字符串叶子，非字符串值原样保留，**未命中时原样返回入参**，
不做无谓拷贝、不改调用方对象身份）。

**高熵兜底的边界**（刻意的保守取向：transcript 是事实源，宁可漏也不误洗）：
长度 ≥40、大小写与数字三类齐备、Shannon 熵 ≥4.0。三类排除：
**纯 hex 与短 hex**（sha256/git 摘要、UUID 属正常内容）、只有一类字符的长串（全小写随机串）、
**含路径/URL 标点（`/ \ . :`）的 token**——最后这条是实测补的：首版没有它，
`TestWorkspaceRoutesWithRoots` 立刻红——错误文案里的绝对路径被整条洗成 `***`，
等于把诊断能力洗没了（比漏一个未知形状的密钥更糟）。回归用例已进 `pkg/redact` 测试。

**未做（划到 §7）**：注入前从继承环境剔除「凭证形状变量」——`FORGE_*` 之外目前不继承父进程
凭证 env 的实际风险面，与 MCP/stdio 侧同属 §7 的 env 卫生，合并做一次即可。

新增测试：`pkg/redact`（形状族 / 正常内容不被洗 / 高熵边界 / 动态登记引用计数 / Payload 只洗字符串叶子）、
`TestPublishRedactsSecretsAtSingleGate`（**三落点同时断言**：WS 帧 + events 表 + transcript 文件，
并断言入参不被就地改写）、`TestPublishRedactsDynamicProviderKey`、`TestTurnErrorPayloadRedactsStderrTail`、
`pkg/code` 的 `TestErrorRedactsSecrets`（含"已有码错误透传分支也脱敏且不改码"）、`pkg/acl` 的
`TestTailLinesRedactsSecrets`（含截断语义不变）。

---

## 4. ③ 流式 delta 合并（消写放大）

### 4.1 现状

每个 `acl.KindMessage` delta → 一条 `session.message`（`manager/event_sink.go:25-32`）→ `publish` →
`AppendEvent` 事务取号（`manager/publish.go:27`）→ WS 帧。长回复 = 几百行 events + 几百帧。
**全仓无任何节流**（`internal/manager` 内只有历史/导出分页用的 `batch` 变量）。

compozy 对照：`internal/session/prompt_chunk_coalescer.go:11-14`（25ms / 4KB / 单批 64 条），
且**终态与 attention 事件永不合并**（`:27-36`）。

### 4.2 改造

新增 `internal/manager/chunk_coalescer.go`：

- 键 `(conversationID, type)`，阈值对齐 compozy（25ms / 4KB / 64 条）；
- **只合并 `BusMessage` / `BusThought`**；其余类型（工具卡/权限卡/用户消息/终态）**一律先 flush 再发**，
  保证 seq 顺序不被文本增量越过；
- 强制 flush：`turn.ended` / `turn.error` / `run.cancelled`、`session.permission_request`、
  `session.elicitation_request`、取消、停机；
- 合并按 `msg_id` 分组拼接，**payload 形状不变**（前端 transcript 继续按 `msg_id` 追加同一条消息，UI 无需改）；
- `Service` 持有实例，`Close` 时 flush。

### 4.3 契约影响

**transcript 粒度变粗**（每轮不再每个 delta 一行）→ `manager/transcript.go:13-26` 的注释与 README 里
"保有每轮 prompt/update 原语"的表述必须同批改；事件 type 不变。建议加开关默认开（要审计粒度可关）。

### 4.4 守卫与测试

合并器单测（阈值 / 强制 flush 顺序 / 终态不丢）；**回归 memory 模式的 transcript 导入**
（`internal/store/import.go`）——这是本批唯一可能碰持久层语义的改动。

**工作量：M。**

### 4.5 实施记录（2026-09-11）

已实现：`internal/manager/chunk_coalescer.go`（+ `chunk_coalescer_test.go`），`go test ./...` 全绿。

**接入点是 `Service.publish`（唯一出口）**，而不是 event sink：`publish` = 「③ 合并非文本类型先 flush」
→ `publishRaw`（脱敏闸门 + 落 events 表 + 镜像 bus + transcript 旁路）。顺序保证只需在这一处成立，
所有调用方（含未来新增的发布点）自动受益。**不需要枚举"强制 flush 的类型清单"**：
凡 `offers(typ)==false` 的事件都是 flush 触发器，将来漏登记一个类型也不会破坏顺序（这正是
方案 §4.2 里那张清单最脆弱的地方）。

**四处按实测收紧/修正**（都写进了代码注释）：

1. **合并条件从「按 msg_id 分组」收紧为「除 `text` 外载荷完全同形」**（`sameShape` + `reflect.DeepEqual`）：
   `{i:1}` / `{i:2}` 这类除 text 外还带字段的载荷，只按 msg_id 分组会把后一条的字段吃掉（静默丢信息）。
   msg_id 变化只是"不同形"的特例 → 旧缓冲先 flush，**跨消息顺序仍不被重排**（A1,A2,B1,A3 → `a1a2`,`b1`,`a3`）。
2. **flush 顺序必须确定**：`takeLocked` 遍历 map 的序是随机的 → 同会话的 `session.message` 与
   `session.thought` 两个缓冲谁先落盘会随机（`TestEventSinkMapping` 随机红）。加 `ord` 创建序号，
   取出后按序发布。
3. **空文本不跳过**：方案里"空 delta 不发布"的优化被否——`publish` 的契约是"发布这条事件"，
   静默丢弃会整条吃掉除 text 外还带字段的载荷（`TestSQLiteRestoreAcrossConversations` 直接抓到）。
4. **`Close` 落盘挂在 `closeTranscripts()` 开头**：先落合并缓冲再关文件，长回复的最后一段不丢。

**未做（明确记录）**：`chunk_merge` 配置开关。合并只减少**行数**、不改内容与事件 type，且审计相关事件
（工具卡/权限卡/提问卡/终态）本来就不合并；加开关要动 `pkg/config`（常量 + `Valid*Modes` + `envKeys`
+ 校验 + README 配置表 + 守卫测试），收益不抵本轮成本。结构已就位：需要"逐 delta 审计"时在
`NewService` 构造处加一个 `if` 即可。

**既有测试的语义修正**：7 个测试原先假设"1 次 `publish` = 1 条事件"，按新语义改（`svc.flushChunks()`
显式落盘；需要 N 条事件的用不同 `msg_id`）；`TestPublishVersionAndBusNil` 改用**非合并类型**（工具卡）
验证"每次 publish 递增版本"，并补断言"合并中的增量不分配号序、不计版本"。
新增测试覆盖：4KB/64 条阈值、25ms 窗口自动落盘、同形合并、形状变化先落旧缓冲、类型/会话隔离、
`Close` 幂等落盘、按会话 flush、`publish` 出口的「文本先于工具卡/终态」、`closeTranscripts` 不丢尾部、
goroutine 退出。方案点名的 memory 模式 transcript 导入（`internal/store/import.go`）按 msg_id 拼接，
粒度变粗不影响 → 全量测试绿。

---

## 5. ④ `providers.json` 密钥加密（vault）

### 5.1 现状

`providerDef.Key` 明文落 `<AgentDir>/providers.json`（`manager/providers.go:17-28`，读写 `:44` / `:70-90`），
启动时注入子进程 env（`:138-150` → `pkg/acl/transport.go:34`）。

compozy 对照：AES-GCM，**AAD 绑定密文身份**（`internal/vault/ciphertext.go:34`），密钥走 keyfile 或
`COMPOZY_VAULT_KEY`（`internal/vault/crypto.go:39`），引用语法 `vault:<namespace>/<...>`（`internal/vault/types.go:30`），
另有 secret-like env 名清单（`types.go:301`）。

### 5.2 改造（最小形态）

1. 新增 `internal/vault`：
   - AES-256-GCM，**AAD 绑定 `{ref, kind}`**；
   - 主密钥 `~/.forge/vault.key`：创建走 **O_EXCL + link 原子安装**（`internal/vault/crypto.go:146`）、
     **拒符号链接**（`:114`）、**拒 group/other 权限位**（`:120`）；允许 `FORGE_VAULT_KEY` 覆盖；
2. `providers.json` 只存 `key_ref: "vault:providers/<id>"`（namespace 只留 providers，不抄他们 9 个 namespace）；
   `has_key` 语义不变；`UpdateProvider` 的"空串=清除"改为删 ref + 删密文；
3. **启动迁移**：发现旧明文 `key` → 加密写回 + 清明文，幂等（本仓允许破坏性调整本地持久数据）；
4. 注入路径不变：`providerEnvFor` 解密后返回 `KEY=VALUE`，并把明文登记进 §3 的动态脱敏表；
5. 顺带抄 env 校验：**拒绝"密钥名 + 字面值"写在配置里**（`internal/vault/types.go:301`
   + `internal/config/mcp_server_validation.go:237`）。

### 5.3 契约影响

`providers.json` 是本地文件非对外契约；REST 形状不变（`GET /providers` 仍只回 `has_key`）。
**备份语义变化**：`docs/deployment.md` 的备份清单必须加 `vault.key`（丢了 = 密钥不可恢复）——
这条不写文档就是埋雷。`pkg/`↛`internal/` 守卫要求 vault 放 `internal/`。

### 5.4 守卫与测试

vault 单测（加解密 / AAD 不匹配必失败 / keyfile 权限与符号链接拒绝）+ manager 侧明文→密文幂等迁移测试。

**工作量：M。**

### 5.5 实施记录（2026-09-11）

已实现：`internal/vault`（+ `vault_test.go`）、`manager/providers.go` 改造（+ `providers_vault_test.go`），
全量测试与 `-tags release` 全绿。

**落成形态**：

- `internal/vault`：AES-256-GCM，AAD = `kind + "\x00" + ref`（换引用或换 kind 必解不开）；
  密文 `<forge_home>/vault/<namespace>/<name>.enc`（原子写，0600）；主密钥 `<forge_home>/vault.key`
  （`O_EXCL` 临时文件 + `link` 原子安装，**并发首启只有一个赢家且绝不覆盖已有密钥**；读取时拒符号链接、
  拒 group/other 权限位）；`FORGE_VAULT_KEY`（32 字节 base64/hex）覆盖时**不读不建 keyfile**。
- `providers.json` 磁盘形状与内存形状拆开：磁盘 `providerFile` **只写 `key_ref`**（`key` 字段仅用于读旧文件
  做迁移），内存 `providerDef.Key` 是明文（注入 + `has_key` 用）。`has_key`、REST 形状、注入路径全不变。
- 幂等迁移：`loadProvidersFile` 见旧明文 → `Put` 进 vault → 写回只留 ref；测试断言**二次装载文件字节不变**。
- 清除语义：`key=""` → 删 ref + **删密文文件**（`Delete` 幂等）。

**三处方案外但必要的补充**：

1. **provider id 必须过引用校验**（`vault.ValidateRef`，仅 `a-zA-Z0-9._-`，≤64）。id 现在会进 vault 文件名，
   不收口就是一条**路径穿越**（`PATCH /providers/..%2F..%2Fetc%2Fpasswd`）。非法 id 报 `invalid_provider_id`。
2. **解不开不静默**：`providerDef.keyErr` 记录装载时的解密失败，`providerEnvFor` 不注入（不再假装有密钥），
   `CredentialProblem` 报「密钥密文无法解密：确认 vault.key 未丢失/未被更换（备份清单见 deployment.md），
   或重设 key」。`ListProviders.has_key` 仍为 true——档案确实配了密钥，问题在密钥库。
3. **vault 不可用时的降级是「保留明文」而不是「清空」**：无 forge home（`agentDir` 为空）或 keyfile
   权限坏时，明文**原样留在 `providers.json`**（`persistProvidersFile` 在这种情况下才写 `key`），
   日志与诊断报错。理由：丢了密钥比多留一会明文更糟；正常路径（有 forge home）绝不写明文。
   写入失败（`Put` 报错）走**回滚 + `vault_write_failed`**，绝不 pretend 成功。

**§5.2 第 5 条（"拒绝密钥名 + 字面值写在配置里"）的 ark-mate 版落地**：复核发现
`agentDefinitionFile` 有个从未被读取/注入的 `env map[string]string` 字段（注释自称"预留：仅持久化"）——
它唯一的作用是**诱使把密钥字面值停放在 `agents/*.json`**。已删除该字段（零行为变化：没有任何代码读它），
并在结构体注释里留下"将来实现时必须支持 `vault:` 引用而不是字面值"。MCP 侧的 env 面不存在
（`capability.McpServer` 无 `env`，见 §7 的复核结论），凭证面只剩 `Headers` → 归 §7。

**未做（记录）**：主密钥轮换 / 批量重加密工具；`FORGE_VAULT_KEY` 的配置层校验
（在 `vault.Open` 报错，启动即失败）；MCP `Headers` 接 vault（§7；届时 `pkg/capability` 需要它，
得把 vault 提到 `pkg/vault` 或由 manager 侧注入密文读写）。

**测试**：vault 单测（往返 / AAD 绑引用与 kind / 篡改 / 换主密钥 / 落盘 0600 且不含明文 / keyfile 过宽权限
与符号链接拒绝 / `FORGE_VAULT_KEY` 覆盖 + 与同密钥 keyfile 互通 / 引用穿越与非法字符拒绝 / 空 home 报错）；
manager 侧（旧明文幂等迁移 + 文件字节不变 + 注入仍可用 / Update 落盘即密文 + 覆盖 + 清除删密文幂等 /
换 keyfile 后 `CredentialProblem` 报可执行原因 / 非法 provider id 拒绝 / 无 vault 时纯内存不落盘）。
`docs/deployment.md` 数据目录表、env 表与备份清单已加 `vault.key`（丢了 = 密文不可恢复）。

---

## 6. ⑤ busy input 队列（排队 / 插话）

### 6.1 现状

`internal/manager/turn.go:38-41` 直接 `acl.NewError(acl.ClassLimit, "turn_in_progress", …)` 拒绝——
用户的输入在 UI 上就丢了。

compozy 对照：`internal/session/inputqueue/queue.go:144`（`DeliveryAfterTurn`）与 `:190`
（`DeliveryInterruptThenPrompt`），条目带 `generation` + `targetTurnID` 防错投（`:42-44`）。

### 6.2 改造

| 面 | 动作 |
| --- | --- |
| 语义 | 三值 `delivery = reject \| after_turn \| interrupt_then_prompt`；`interrupt_then_prompt` = 服务端原子化的「cancel 当前轮 → 投递」 |
| REST | `POST /conversations/:id/prompt` 增可选 `delivery`；新增 `DELETE /conversations/:id/inputs/:input_id`。**README 路由表必须同批改**（`TestReadmeRoutesMatchRouter` 双向比对，新增端点不写文档同样红） |
| 持久化 | `internal/store/migrate.go` 追加 version=2 建 `inputs` 表；同步 `internal/model` 实体 + `internal/dao/input.go` + `internal/store/inputs.go` 门面（三处手工一致是本仓既有纪律）。列：`id / conversation_id / text / delivery / target_turn_id / generation / status / created_at / claimed_at` |
| generation | 投递时校验会话未变（对齐 compozy `queue.go:42-44`），防旧队列项投进新会话 |
| 事件 | 新增 `session.input_queued` / `session.input_delivered` → **三处同步**：`manager/events.go`、`internal/archtest/contract_test.go:29-42` 的 `frozenEventTypes`、前端 `packages/types/src/events.ts` 的 `Ev` |
| 投递 | `runTurn` 收尾（`manager/turn.go:106-137`）置 idle 后取队首投递，不再经 HTTP；`interrupt_then_prompt` 复用现有有界取消路径 |
| 前端 | `apps/panel/src/components/ConversationDetail.tsx` 输入区加投递模式 + 队列列表；`packages/types/src/conversation.ts:274` 的 `PromptReq` 加 `delivery` |

**工作量：L（含前端）。建议排在 ①–④ 之后**，或先只做服务端 + REST，前端随后。

### 6.5 实施记录（2026-09-11，服务端 + REST；前端随后）

已实现：`manager/inputqueue.go`（+ `inputqueue_test.go`）、`model/enum.go` + `model/entity.go`、
`store/migrate.go`（**v6**，不是方案里写的 version=2——迁移表已经用到 5）、`dao/input.go`、
`store/inputs.go`（+ `inputs_test.go`）、`dto/dtoconversation`、`service/svcconversation` 端口、
控制器与路由、`PromptReq.delivery` 与三条事件的**三处同步**（`events.go` / `archtest` 冻结清单 /
前端 `Ev` + `DeliveryMode`）。

**落成形态**：三值 `delivery`（缺省 `reject` = 历史行为）；`after_turn` 忙时入队、轮次收尾自动投递；
`interrupt_then_prompt` 入队 + 取消当前轮；`DELETE /conversations/:id/inputs/:input_id` 撤下待投递项；
`inputs` 表（v6）+ 索引 `(conversation_id, status, created_at)`；FIFO 严格按 `(created_at, id)`。

**七处方案外但必要的决定**：

1. **补第三个事件 `session.input_cancelled`**（方案只列了两个）。「撤下」与「发出去了」是两条不同的
   事实；复用 `input_delivered` 表达撤下，等于让前端靠猜。理由码 `user_cancel | deliver_failed`。
2. **`interrupt_then_prompt` 必须「先入队、后取消」**。反过来（先取消再入队）会留一个窗口：
   取消已生效、`runTurn` 已收尾取队时，本项还没入队 → 它将**永远不被投递**。入队后再复查一次
   空闲，闭合另一半窗口（「取消落地前旧轮已自然结束」）；两路都跑也不会重复投递——认领是
   `WHERE status='queued'` 的条件更新，只有一个能赢。取消失败则**就地撤下**该项，不留孤儿。
3. **空闲时 `after_turn` 不建队列行**：没有「等的那一轮」，与 `reject` 走同一路径（直接执行），
   响应 `queued` 缺省。否则队列里会积攒一批「本该立刻执行」的行。
4. **失败轮与停机不续投**：底座刚报错就把队列一条条灌进去 = 连续失败重放；停机那一轮注定不执行。
   重启恢复（`RestoreFromStore` → `staleQueuedInputsOnRestore`）把全库 `queued` 行统一置 `stale`
   ——**不替用户按下发送**（上下文已断），保留行仅作审计。
5. **代际（generation）落在会话记录上、只在内存**：`ConversationRec.Generation` 在
   `ensureConversation` 重建活执行上下文时自增（`bumpGeneration`）。入队记下的值与投递时不等
   → 该项作废（`stale` + WARN）。**只存内存**是有意的：重启本身就让所有排队行作废，没有跨重启
   比对的语义；`inputs.generation` 落库只为台账可读。
6. **新增 `Turn.id`**（平台 uuid）：`target_turn_id` 需要「排在哪一轮后面」的稳定指认；
   它**不是投递判据**（那一轮必然先结束，否则队列永不消费）——方案里把它和 generation 并列写，
   落地时必须把这层区分写清楚。
7. **投递顺序：先启动新一轮、再发 `input_delivered`**。反过来一旦启动失败，前端会看到队列项
   凭空消失而其文本从未作为 `user_message` 出现；现在失败发 `input_cancelled(deliver_failed)`
   且文本留在台账行里。

**一处边界（值得记一笔）**：`inputs.text` 保留**用户原文**——它是「运行时输入面」，投递时原样发给底座，
与直接 prompt 的路径一致（§3 脱敏闸门管的是**出站可观测面**：事件/transcript/错误文案/日志，那条路径上
`session.user_message` 与 `session.input_queued` 的载荷都照样过闸门）。所以库里这一列是**唯一**保留用户
原文的位置（`runs.user_text`/`messages.text` 都是从已脱敏的事件载荷物化出来的）。这是「输入保真 vs 输出
脱敏」的必然分界，不是漏了口子；若将来给队列加「加密存储」，与 §5 vault 同一套即可。

**未做（记录，其中第一条在 §6.6 被推翻）**：`GET /conversations/:id/inputs`（当时的判断是
「队列视图由事件回放构建——`session.input_queued` + 两条收敛事件已足够」，漏掉了**冷挂载不回放**，
见 §6.6）；队列长度上限（单操作员场景按输入量增长，
要上限得先定「满了怎么办」的策略）；`interrupt_then_prompt` 插队（当前严格 FIFO——否则连按三次
插话会逆序执行）；前端输入区 UI（下一批；类型与事件枚举已同步，`make test` 的守卫不缺边）。

**测试**：manager 侧 8 例（忙时入队不执行 + 事件载荷 / 轮次收尾按 FIFO 投递两条 + delivered 计数 /
代际变化即作废且留 stale 台账 / interrupt 取消后投递 + `run.cancelled` / 撤下 + 幂等 + 跨会话撤不掉 +
已投递不可撤 / 非法 delivery 报错且不留行 + `reject` 保持报错 / 空闲不入队直接执行 / 重启作废 /
memory 模式全链路）；store 侧 1 例（v6 建表 + 队首按创建时间 + 认领条件更新 + 已投递不可删 +
跨会话不命中 + 批量作废幂等）。

### 6.6 实施记录（2026-09-11，前端输入区 + 两处服务端补齐）

前端这一轮把 §6 的队列摆到用户眼前，落地时发现服务端还差两块拼图——都是「只认事件」这条纪律
自己逼出来的，不是顺手加的功能：

1. **作废也必须说话**：此前 `staleQueuedInputsOnRestore` 只把 `inputs` 行置 `stale` 并打 WARN，
   **不发事件**。于是事件流里那条队项永远没有结局。队列只认事件、刷新靠回放重建——重启后刷新
   页面就会看到一条**永远投不出、也撤不掉的幻影队项**（`DELETE` 只认 `queued`，回 `deleted:false`）。
   现在恢复时先取全库快照（新增 `store.ListAllQueuedInputs` + `dao.ListAllQueued`），再批量置作废，
   最后**逐条补发** `session.input_cancelled(reason="stale")`。`ReasonStale` 与既有两个原因并列，
   三处同步（`inputqueue.go` 常量 / README 原因取值 / 前端文案）。
   「撤下与发出是两条不同的事实」的另一半是：**一条队项的结局总得是投递或撤下之一，没有第三种沉默**。
2. **冷挂载不回放历史 → 需要当前态入口**：WS `snapshot` 是空对象且不发历史（面板只在带 `since_seq`
   时才续传、否则重置视图），所以「刷新前已排队、还没发出」的输入在刷新后从界面上消失——用户会
   以为它没了，而它其实还会在轮次收尾时发出去。§6.5 当时把 `GET /conversations/:id/inputs` 记为
   「不做」，理由是事件足够，这个理由在冷挂载路径上不成立。故补：`GET /conversations/:id/inputs`
   （读模型 `QueuedInputResp{input_id,text,delivery,target_turn_id,generation,created_at}`）+
   面板在收到 `snapshot` 时拉一次当前态补齐队列。
   **事件负责过程、读模型负责当前态**——两者不是冗余：前者能重放出「曾经排过什么」，后者回答
   「此刻还在队里的是什么」。为了防止 HTTP 在途窗口把已经撤下的项复活，视图里带一个
   `queueSeq`（只随队列事件前进）：拉取期间若有队列事件到达，就**以事件为准**、丢弃这次当前态。

前端改动（`apps/panel`）：

- `console/queueView.ts`（纯函数，便于 Node 侧断言）：`transitionQueue`（入队建项、同 id 幂等覆盖、
  delivered/cancelled 出队并交出被移除项）、`queuedFromResp`（当前态 → 视图）、`deliveryLabel`/
  `deliveryHint`/`cancelReasonLabel`/`cancelNotice`/`cancelTone`/`queueExcerpt`/`queueSummary`。
- `store.ts`：`ConversationView.queue + queueSeq`；三条 `session.input_*` 事件进 `applyEvent`
  （`input_cancelled` 额外落一条**系统提示**，带原因与出路——文本从此不会出现在正文里，
  「我排的那句话去哪了」必须有答案）；`sendPrompt(text, delivery?)` 忙时不再直接 return
  （排队/插话放行，且**不为入队置忙**——忙碌水位归那一轮的 turn 事件）；新增
  `cancelQueuedInput`、`refreshQueue`（`snapshot` 帧触发）。
- `components/Composer.tsx`：忙时 textarea **不再 disabled**，底部多一个投递模式 `Select`
  （排队 / 打断并发送，默认排队——打断是有代价的动作），发送按钮文案随模式变。
- `components/QueuedInputs.tsx`（新增）：composer 上方列出待投递项（文本摘要 + 投递模式 + 逐条撤下）。
  **与原设计稿的偏差**：稿子写的是「输入区加投递模式 + 队列列表」放进会话详情抽屉；实施放在
  composer 正上方——队列讲的是「这些话还没发出去」，离输入框最近才有用，抽屉是事后查看的地方。

**测试**：`TestQueuedInputsStaledOnRestore` 增断言（`input_cancelled` 落在**事件表**里、载荷
`input_id`/`reason=stale`/`delivery` 正确）；`TestInputQueueSchemaAndClaim` 增跨会话快照断言
（`ListAllQueuedInputs` 不按会话过滤、创建时间升序）；`internal/router/rest_test.go` 增
`GET /conversations/:id/inputs` 的接线与载荷形状断言；前端为纯函数 Node 断言（5 组：
幂等入队 / 出队交出队项与 no-op / 文案与语气 / 摘要 / 当前态映射）+ `pnpm typecheck` + `pnpm build:panel`。
**未做浏览器实测**（沿用 §13.12 的记录方式）：本轮验证只到类型检查、打包与纯函数断言，
没有真机跑 `make dev` 点过界面；「刷新后队列还在」这条路径由 `refreshQueue` 的锚点逻辑与
服务端读模型测试分别覆盖，端到端仍需人工过一遍。

---

## 7. ⑥ 顺带的三条安全硬化（均可直接对照实现）

| 项 | 为什么 ark-mate 需要 | compozy 证据 |
| --- | --- | --- |
| **MCP probe 的 SSRF** | `POST /mcps/:id/probe` 按用户给的 URL 出站 = 内网扫描器。抄「解析一次、按该 IP 直连、永不二次解析」（堵 DNS rebinding）+ 私网/环回判定 | `internal/outboundpolicy/dialer.go:33`、`policy.go:173` |
| **MCP 凭证与 header 卫生**（**已修正**，见下） | `capability.McpServer.Headers` 明文落盘（`pkg/capability/types.go:53` 注释自认"可含访问凭证"）且随 ACP wire 下发 → 应接 §5 的 vault，并拒 `\r\n` 头注入 + 限制敏感头来源 | `internal/mcppolicy/headers.go:59`、`:74` |

> **修正（2026-09-11 复核）**：本条原先写的是"stdio MCP 的 env 禁用名单（`internal/config/mcp_server_validation.go:274-277`）"，
> 经复核**对 ark-mate 不成立**——`capability.McpServer` 没有 `Env` 字段，`pkg/mcp/stdio.go:61`
> 只把 `s.Args` 交给 `exec.CommandContext`，**当前不存在 env 注入面**。该名单属**预防性**条目：
> 只有当为 MCP 定义补 `env` 字段（对齐 ACP `mcpServers` 规范，见 `types.go:52-62` 的字段缺口）时，
> 才需要同时抄 `NODE_OPTIONS` / `PYTHONPATH` / `PYTHONHOME` / `LD_PRELOAD` / `DYLD_*` 禁用名单
> （`internal/config/mcp_server_validation.go:274-277`）。真正**现在**适用的是 header 那条。
| **进程句柄 PID + 启动时间双校验** | `pkg/procreg` 孤儿回收只按 PID，PID 复用会误杀 | compozy 用 `procutil.MatchesStartTime`：`internal/toolruntime/registry.go:131`。**不要**抄他们取启动时间的方式（外部 `ps -o lstart=`，`process_started_at_unix.go:19`）——ark-mate 是启动者，spawn 时记 `time.Now()` 即可 |

另：若 `FORGE_WORKSPACE_ROOTS` 目前只做字符串前缀比对，建议补工作区路径规范化三段式
（循环 percent 解码 + NFC → 最深已存在前缀 realpath → resolved-under-root，
`internal/fileutil/path_containment.go:41` / `:66` / `:157`）——ark-mate 缺的正是"最深存在前缀"这一段。

---

## 8. 排期、工作量与验收

**顺序**：①T1 → ② → ③ → ④ → ⑤（⑥ 穿插）。

理由：①T1 / ② 不动持久层语义、不动事件 type；③ 动 transcript 粒度（唯一需回归 memory 导入）；
④ 动本地文件格式 + 备份文档；⑤ 最大，且是**唯一**要动事件 type 三处同步 + 前端的。

| 项 | 工作量 | 触碰持久层 | 触碰事件 type | 触碰对外路由 |
| --- | --- | --- | --- | --- |
| ①T1 权限决策归属 | S | 否 | 否 | 否 |
| ①T2 `_meta` + 歧义可观测 | M | 否 | 否 | 否（仅 DTO 取值扩） |
| ①T3 complete 收尾 | S | 否 | 否 | 否 |
| ② 脱敏闸门 | M | 否（仅新写入路径） | 否 | 否 |
| ③ delta 合并 | M | **是**（transcript 粒度） | 否 | 否 |
| ④ vault | M | 否（本地文件格式） | 否 | 否 |
| ⑤ busy input 队列 | L | **是**（新表 + 迁移 v2） | **是**（2 个新 type） | **是**（1 新端点 + 1 改） |
| ⑥ 安全硬化 | S–M | 否 | 否 | 否 |

**每条验收基线**：`make build` + `make vet` + `make test` + `make test/release`
（③④⑤ 会碰到 release tag 的代码路径）+ `cd frontend && pnpm typecheck`。

---

## 9. 附 A：进入 backlog 的非 P0 项（P2）

1. **事件注册表带元数据 + init 期自校验**（compozy `internal/events/registry.go:74` panic 校验）：
   ark-mate `manager/events.go` 目前只有字面量 + 注释里的 payload 契约，可把 payload 契约变成结构化元数据。
2. **错误出口附可执行建议动作**（`internal/diagnostics/item.go:60`、
   `internal/diagnosticcontract/diagnostics.go:427` 按分类给动作）：配合 ①T2 的歧义诊断最有价值。
3. **boot 顺序表 + 反向 cleanup 栈**（`internal/daemon/boot_components.go:9`、`boot_cleanup.go:32`：
   回滚压栈、仅失败时逆序执行且不被取消打断）→ `internal/app` 装配。
4. **流出口精确围栏 + 服务端裁剪的 resync 原因码**（`internal/api/core/sse.go:113` 出口统一脱敏；
   `internal/api/contract/session_transcript.go:14-17` 的
   `fence_missing` / `epoch_mismatch` / `generation_mismatch` / `sequence_reset`）→
   ark-mate WS 的 `since_seq` + gap 帧可补"原因码"这一层。
5. **端点注册表即契约事实源**（`internal/api/spec/operation_builder.go:14` + `magefiles/codegen.go:54`
   重生成比对）：比 README 反引号比对更强，但要把路由表搬到 spec 层——收益/代价需单独立项。
6. **clientstate 订阅分代 + purge 预提交**（`internal/clientstate/contract.go:21-37`）：补 WS attach/detach
   的 generation，防 id 复用后的幽灵订阅。
7. **运行时熔断**（`internal/deadentity/contract.go:13` 阈值 5 / `:29` ProbeDecision）：
   runtime 连不上时现在是每次重拨每次 50201。
8. **两阶段删除 + tombstone**（`internal/session/manager_delete_tombstone.go:18-22`、
   `manager_delete_staging.go:17`）。

## 10. 附 B：已确认的诱饵（不要抄）

| 诱饵 | 原因 |
| --- | --- |
| UDS 第二 transport | 两套各 400+ 行手工路由表（`internal/api/udsapi/routes.go:6`、`internal/api/httpapi/surface_routes.go:9`），`Transports` 不驱动注册，且 spec↔真实路由无全局守卫 |
| OpenAPI codegen 取代守卫测试 | 划算但非取代：codegen 只管 spec 派生的形状，业务码语义仍需注册表 |
| `workspaceaccess` 四层同意 / `agentidentity` | 多工作空间同意与 agent 身份解析，单操作员下恒真 |
| `mcp/auth` 全流程 OAuth / `admission` 排空闸门 | 多用户与无人值守设施 |
| `providerenv` 整套 home 隔离 | ark-mate 已有独立 agentDir + 工作空间边界，整套抄属重复隔离；只摘"拒路径组件符号链接 + resolved-under-root"两个检查 |
| 每 session 一个 `events.db` 物理分库 | 单机单操作员 + 跨会话读是主路径；抄了要养 lease/只读池/每库迁移，并另建全局汇总投影补偿 |
| `internal/session` 的 300+ 文件粒度 | 抄"设计时先拆、超 500 行即架构失败"的纪律，不抄粒度 |
| 宿主端 terminal 全套 / 自研 JSON-RPC transport / `heartbeat/` | 越界或纯冗余（`heartbeat` 名字像存活探针，实际是作者态 `HEARTBEAT.md` 唤醒策略） |
| loop / automation / scheduler / memory / network / extension / bridges / hooks | 定位外 |

## 11. 附 C：反向发现——ark-mate 领先的地方

1. **elicitation**：compozy **生产代码零命中**（全仓仅 2 个测试 JSON fixture），ark-mate 有完整实现
   （schema 渲染 + scope 校验 + 超时收敛 + 决策归属校验）。
2. **多币种与快照语义**：compozy 的日汇总把金额直接相加、provenance 冲突降级 `unknown`
   （`internal/store/globaldb/schema/definitions/20_sessions.sql:202-218`、
   `queries/observe_overview.sql:1-35`）；ark-mate 是"快照取区间末 + 多币种不混加 + 上报率分列"
   （`internal/store/usage.go:16-21`），语义更严格——**若要借他们的 rollup 表结构，快照字段不能求和**。
3. **轮次终态归因**：ark-mate 的 `userCancel > intentional > cancelled > err > completed` 优先级
   （`internal/manager/turn.go:114-137`）比 compozy 的 `StopCause` 枚举更细。
4. **"主动拆除不算失败"**：ark-mate 的 `runtimeConn.intentional` 与 compozy 的
   `markStopRequested`（`internal/subprocess/process_shutdown.go:180`）等价，且已结构化成文档纪律。

---

## 12. 附 D：主要 compozy 锚点索引（按主题）

| 主题 | 锚点 |
| --- | --- |
| 权限可寻址 | `internal/acp/permission.go:14-17`（meta 键）、`:44-50`（`request_id`/`turn_id` 二选一）、`internal/acp/permission_helpers.go:13-20`（三键回退）、`internal/acp/permission_pending.go:188`（歧义即拒）、`:22-23`（pending 挂在 `AgentProcess`）、`internal/api/core/session_workspace.go:229`（映射为错误） |
| vault | `internal/vault/ciphertext.go:34`、`internal/vault/crypto.go:39` / `:114` / `:146`、`internal/vault/types.go:30` / `:301` |
| 动态脱敏 | `internal/redact/dynamic.go:30`、`internal/transcript/redaction.go:190-201`、`internal/api/core/sse.go:113` |
| 凭证 env 卫生 | `internal/session/provider_runtime_secrets.go:120`、`internal/procutil/env.go:84`、`internal/config/mcp_server_validation.go:237` / `:274-277` |
| 出站安全 | `internal/outboundpolicy/dialer.go:33`、`policy.go:173`、`internal/mcppolicy/headers.go:59`、`internal/fileutil/path_containment.go:41` / `:66` / `:157` |
| 流式合并 | `internal/session/prompt_chunk_coalescer.go:11-14` / `:27-36` |
| 输入队列 | `internal/session/inputqueue/queue.go:42-44` / `:144` / `:190` |
| 停机阶梯 | `internal/subprocess/process_shutdown.go:147` / `:180` / `:193` / `:202` / `:217` / `:234` |
| 失败分类 | `internal/store/failure.go:16-27`、`internal/acp/failure.go:23` / `:104` |
| 汇聚与成本溯源 | `internal/store/globaldb/schema/definitions/20_sessions.sql:202-218`、`queries/observe_overview.sql:1-35` |

---

## 13. 追加（2026-09-11 第二轮）：能力面（MCP / Skill）管理加固

> 第二轮只读扫描聚焦 compozy 的 **MCP 管理面**（6 切面）与 **skill 子系统**（7 切面）；
> 全部锚点已逐行回读核对（本仓侧抽查 60 条全部逐字命中）。
>
> 与前面章节的耦合关系（决定实施顺序）：
> - §13.4 的 **`SecretHeaders`** 直接依赖 **§5 vault** —— 两件事必须同批做，否则拆一半；
> - §13.2 的**装载期内容扫描**与 **§3 出口脱敏**是两个方向：§3 管"密钥别泄"，§13.2 管"注入别进"；
> - §13.3 的 **probe 缓存 key** 与 **§7 的 MCP 出站 SSRF** 落在同一处代码。
>
> 一句话判断：**compozy 把 MCP/skill 当"配置资源"管理（文件=事实源、派生记录入 SQLite、校验/生命周期/错误收口在一个域），
> ark-mate 现在只有"写入侧"**——平台 JSON → 物化到 runtime 目录，读入侧（发现/校验/完整性/启停）整体缺失。

## 13.1 现状对照

| 面 | MCP（ark-mate） | Skill（ark-mate） |
| --- | --- | --- |
| 定义源 | `<CapabilityDir>/mcp/*.json`（`pkg/capability/registry.go:46-57`、`:206-217`） | `<CapabilityDir>/skills/*.json`（`:193-204`） |
| 字段 | `{id,name,transport,command,args,url,headers}`（`types.go:52-62`）——无 `env` / `enabled` / `timeout` / 工具白名单 | `{id,name,description,body,sources[]}`（`types.go:35-50`）——无 `version` / `provenance` / 摘要 / 启停 |
| 写入侧 | **无 CRUD**（`router/routes_capability.go:16-27` 只有 GET + probe + call + resources/prompts） | 物化拷 `SKILL.md`（`skill_materialize.go:157-172`，非原子、跟随软链；SKILL.md 与归属标记两次独立写） |
| 读入侧 | 无发现/无校验清单/无引用检查 | **无磁盘发现、无内容校验、无 provenance、无启停** |
| 运行态 | probe 状态在进程内 map（`manager/mcp_probe.go:54-63`），重启即丢；工具目录按 server 整体替换（`registry.go:150-176`——这条比 compozy 干净，保留） | 可见性 = 至少一个候选 runtime 有物化源（`manager/capabilities.go:182-198`）；无 agent 级作用域 |
| 凭证 | `Headers` 明文落盘且随 ACP wire 下发（`types.go:53` 注释自认"可含访问凭证"） | — |
| 子进程 | **未进进程组**：只 `Process.Kill()` + `Wait()`（`pkg/mcp/stdio.go:86-88`），与 agent 侧 `Setpgid`（`pkg/acl/transport_unix.go:12`）不一致 | — |

## 13.2 最高优先级：skill 装载期内容扫描 + critical 拒载

这是本轮最有价值的一条（首轮遗漏：只看了类型与物化，没看 loader 链路）。

| 事实 | compozy 锚点 |
| --- | --- |
| 装载每个技能时对**正文**跑模式扫描 | `internal/skills/verify.go:102` `func VerifyContent(content string) []Warning {` |
| **9 条 critical**（改写指令 / 无视规则 / 遗忘指令 / 角色劫持 / 新指令 / system prompt override / 凭据提取 / `rm -rf` …）+ 2 条 warning + 5 万字符阈值 | `verify.go:22` / `:30` / `:38` / `:76`（逐条内联 regex）、`verify.go:11` `maxContentChars = 50_000` |
| **非内置**技能才校验（内置豁免）；critical → **拒载** | `internal/skills/registry_load.go:254`、`:262` `if hasCriticalWarning(warnings) {` |
| 解析失败降级为诊断，不拖垮整个 registry | `registry_load.go:243` `processSkillWithDiagnostics` |

**为什么是硬需求**：ark-mate 的 `Skill.Body`（`pkg/capability/types.go:41`）是平台 JSON 原文，被 `skill_materialize.go:162` **原样写入 runtime 原生技能目录**，写入前零检查——任何人往 `<CapabilityDir>/skills/*.json` 放一段"忽略之前所有指令"，平台会把它**主动物化**进 agent 的技能目录。
**落点**：新增 `pkg/capability/verify.go`（模式表照搬，severity 三级），在 `Registry.RegisterSkill`（`registry.go:60-68`）挂闸；critical 拒载并计诊断。

## 13.3 MCP 侧可参照

| # | 模式 | 为什么 ark-mate 需要 | compozy 锚点 | 建议 |
| --- | --- | --- | --- | --- |
| M1 | **明文 vs 密钥在类型上分开**：`Env`/`SecretEnv`、`Headers`/`SecretHeaders` | ark-mate 只有 `Headers` 且明文落盘（`types.go:53`） | `internal/config/mcp_server.go:9-14` | 直接抄 |
| M2 | **来源优先级显式序号表 + 遮蔽回显**（不靠加载顺序） | 现为单目录，加二级来源时立刻需要裁决 | `internal/settings/mcp_server_item.go:155` / `:172`（rank 上限 7）、`:42`（shadowed 回显） | 改造后抄（砍到 2–3 级） |
| M3 | **校验挂在类型上**：一份 `Validate` 服务文件/sidecar/内嵌/API 四个入口；按**生效 transport** 做互斥字段校验 | ark-mate 校验散在 `Registry.RegisterMcpServer`（`registry.go:71-87`） | `internal/config/mcp_server_validation.go:15` `func (s MCPServer) Validate(path string) error`、`:69`（按 transport 分支） | 直接抄 |
| M4 | **校验「投影后的生效配置」+ 引用完整性** | 加分层后必须校验最终结果；引用完整性即 MCP↔agent 白名单 | `internal/settings/mcp_mutation.go:232`（被遮蔽时跳过校验）、`internal/config/tools.go:225`（引用未知 MCP 即拒） | 直接抄 |
| M5 | **生命周期矩阵 → "改完要不要重启" + next_action 回执** | MCP 定义是启动时装载（`internal/app/app.go:232`），现在连"必须重启才生效"都不告诉用户 | `internal/config/lifecycle/lifecycle.go:96`（`mcp-servers.*` 默认 restart-required）、`internal/settings/collection_lifecycle.go:24`（新增降为 live-add）、`internal/settings/config_apply_runtime.go:126`（按 lifecycle 分派） | 改造后抄（两个枚举即可） |
| M6 | **凭证只回 key**：`vault:` scheme + 读模型只给 `secret_env_keys` + 覆写时按 key 保留绑定 | 与 M1/§5 同批 | `internal/vault/types.go:98`、`internal/settings/mcp_secret_preservation.go:48`、`internal/api/contract/settings_collection_payloads.go:168` | 直接抄 |
| M7 | **header/env 三档拒绝**：敏感头来源分级、保留名不可覆盖、CRLF、env 黑名单、stdio 不继承父 env | ark-mate 全无 | `internal/mcppolicy/headers.go:10` / `:59` / `:62` / `:79`、`internal/config/mcp_server_validation.go:274-277` | 直接抄 |
| M8 | **探测缓存 key 编入「定义指纹 + 协议版本 + auth 哈希」+ 服务端 TTL** | ark-mate probe 结果无 key、无 TTL、重启即丢 → 配置改了也命中旧结果 | `internal/mcp/executor_cache.go:240`、`internal/mcp/executor_tools.go:51`、`internal/mcp/executor_client.go:57` / `:61` / `:88` | 直接抄（比"落库"更本质：key 里编定义指纹，改配置自然 miss） |
| M9 | **每次变更落 apply record（失败也落）+ 诊断码注册表** | ark-mate 无审计面 | `internal/settings/config_apply_records.go:41`、`internal/diagnosticcontract/diagnostics.go:114` | 改造后抄（留 actor/target/action/status/diagnostic/created_at） |
| M10 | **写入 = 编辑用户文件 + 校验生效配置后再落盘 + 保留未知键** | 补 CRUD 时最省事也最不砸用户配置 | `internal/config/mcpjson_write.go:45-79`（`:71` validateEffectiveConfigWrite、`:75` 落盘）、`internal/config/mcpjson.go:19`（兼容 `mcpServers`/`mcp_servers` 两键） | 直接抄 |

## 13.4 Skill 侧可参照

| # | 模式 | 为什么 ark-mate 需要 | compozy 锚点 | 建议 |
| --- | --- | --- | --- | --- |
| S1 | **装载期内容扫描 + critical 拒载** | 见 §13.2 | `internal/skills/verify.go:102` / `:11`、`registry_load.go:262` | 直接抄 |
| S2 | **目录级 SHA-256**：排序相对路径 + 模式位 + 内容；遍历排除 sidecar 自身；软链不 follow、target 字面量入哈希、逃逸即拒 | 物化产物无摘要记录（审计/复现不了） | `internal/skills/provenance.go:54` / `:76` / `:91` / `:189` / `:216`、装载校验 `registry_load.go:319` | 直接抄 |
| S3 | **安装事务**：staging 目录 → hash → sidecar → 原子 rename；拒覆盖非自有目录 | `skill_materialize.go:162` 是裸 `os.WriteFile`（非原子、跟随软链） | `internal/skills/marketplace/install.go:37` / `:128` / `:150` | 直接抄 |
| S4 | **禁用 = 名字 overlay（不删除、不重建快照）** | ark-mate 无启停面，想关一个技能只能删文件 | `internal/skills/registry_disabled.go:8`、`registry_toggle.go:18`、`registry.go:61`、`catalog.go:214` | 直接抄（作用域收成 global/workspace 两级） |
| S5 | **`Enabled` ⟂ `Activation`**：启用与"当前环境是否适用"正交，不适用要给人话原因 | 技能与 MCP 目录互不知情，运行时才发现不可用 | `internal/skills/types.go:59-64`（`requires_tools`/`requires_capabilities`）、`:98-102`（`Reasons`）、`activation.go:82` | 改造后抄 |
| S6 | **技能不注册工具，只暴露只读三件套（全 RiskRead）** | 与"能力不重造"同向的边界纪律：技能=上下文注入，不是能力扩展 | `internal/tools/builtin/skills.go:24`、`builtin_ids.go:28`、`skills/catalog.go:20` | 直接抄（结论：别让 `Skill` 带可执行工具） |
| S7 | **审批令牌绑定输入摘要** | ark-mate 权限记忆是 `action+identifier`（`manager/permission_memory.go`），无输入摘要 → 批准后输入被改挡不住 | `internal/tools/approval_token.go:238` / `:253` | 改造后抄 |
| S8 | **资源路径清洗 + 只读/特权变更路由分离 + 冲突落审计事件** | 读技能资源的路径穿越；"谁压制了谁"要可解释 | `internal/skills/resource.go:19` / `:65`、`internal/api/httpapi/routes.go:263` / `:267`、`internal/skills/observe_events.go:149` / `:275`、`expose_operation.go:118`（preflight→commit→rollback） | 改造后抄 |

## 13.5 修订后的实施顺序（并入 §8）

skill 侧优先级高于首轮判断：**内容扫描（S1）> 启停 + 激活（S4/S5）> 物化原子化 + 摘要（S2/S3）**，
因为它们分属"安全"与"可用性"，而 S1 是当前唯一"平台主动把不可信文本装进 agent"的路径。
MCP 侧与 §5/§7 合批：**M1+M6 与 §5 vault 同批**，**M8+M5 与 §7 的 probe 加固同批**，其余（M3/M4/M7/M10）在补 CRUD 时一次做完。

## 13.6 诱饵（能力面专项）

marketplace 运营（`internal/marketplace/*`、registry 下载器、`AllowedMarketplaceMCP` 信任白名单、安装事件）、
MCP OAuth DCR/CIMD 全流程与 5 个 `/auth/*` 端点、hosted MCP proxy 与 projection singleflight 缓存、
UDS peer-credential 与双传输面共享 handler、privileged 变更守卫、resource scope 四分层与 scope lattice、
`desired/active hash` 与 apply 世代、`deadentity` 持久化抑制（5 次 + 1 分钟半开）、`toolProjectionEpoch` 全局代际、
extension/hooks/network 等无关 settings 集合、skill 八级优先级梯级（单一来源下无意义）、
`registry.go:222`（**每次读正文前**重算整目录哈希——单操作员下是常驻税，只抄"装载时校验 + 变更时重校验"）。

## 13.7 两处「compozy 自己也没有」——参照时要自己补

1. **删除前不检查引用**：compozy 删 MCP server 不查 agent 引用（此为"未发现"结论，非逐行证据，
   子扫描已如实标注）。ark-mate 有同型立场可直接套用——provider 悬空引用必须显式失败
   （`internal/manager/providers.go:157-173`：*"悬空引用会让本该注入的密钥静默缺失，属配置错误——宁可显式失败"*）。
   补 MCP CRUD 时按此办理：删除/停用前查 `agent.Mcps` 白名单引用，有引用则拒绝并回带影响面。
2. **MCP stdio 未进进程组**：compozy 只用 `exec.CommandContext`；**ark-mate 同样**
   （`pkg/mcp/stdio.go:86-88` 只 `Process.Kill()` + `Wait()`），而 agent 侧已经做对
   （`pkg/acl/transport_unix.go:12` `SysProcAttr{Setpgid: true}`）。
   `npx -y @modelcontextprotocol/server-*` 这类命令会留下孙进程 → 抄 `pkg/acl` 的进程组清理即可（小改动，建议随 M1 一起做）。
   **已修（2026-09-11，见 §13.8）**。

## 13.8 实施记录（2026-09-11，S1 + §13.7-2；其余按 §13.5 顺序排后续轮次）

**S1 装载期内容扫描 + critical 拒载**（`pkg/capability/verify.go` + `verify_test.go`）：

- **模式表逐条对照** compozy `internal/skills/verify.go:22-91`：9 条 critical（`ignore-previous-instructions`
  /`disregard-existing-rules`/`forget-your-instructions`/`role-hijack-you-are-now`/`new-instructions`/
  `system-prompt-override`/`delete-all-files`/`rm-rf`/`credential-extraction`）+ 2 条 warning
  （`sensitive-path-reference`/`excessive-tool-chaining`）+ 1 条 info（`content-too-long`，`maxContentChars = 50_000`）。
  正则**逐字一致**（`sensitive-path-reference` 里那段反引号拼接也照抄，否则字符类比会漂），pattern id 保持一致
  以便对照阅读；message 按本仓文案要求译成简体中文。
- **三级严重度** `SeverityInfo/Warning/Critical`（数值序即排序与拒载判据）；`VerifyContent` 结果按
  「严重度降序 + pattern 升序」稳定排序（同 compozy），同 id 只报一次。
- **两道闸门**：`Registry.RegisterSkill`（唯一入表口；critical → 返回错误 → `scanDir` 告警跳过，绝不入表）
  + `MaterializeSkillFor`（导出 API，调用方可拿字面量绕过注册表；**物化是「把文本装进 agent」的最后一米**，
  宁可重复判断）。物化层这一道是本仓补的——compozy 那边只有装载期一道。
- **warning 不回滚、只留痕**：`Skill.Warnings`（`json:"-"`，派生字段不落盘/不从盘读，每次装载重算，
  免得「正文改了告警还是旧的」）→ `SkillResp.Warnings`（severity/pattern/message）→ 前端 `SkillWarning`。
- **无豁免**：compozy 对 bundled 来源豁免校验（`registry_load.go:254`），ark-mate 没有内置 skill 来源
  （只来自 `<CapabilityDir>/skills/*.json` 与显式注入），所以**不设豁免**——少一个「谁能绕过闸门」的判断点。
- **已知边界（写进测试）**：规则是**字面匹配**（英文正则），中文改写/语序改写不命中
  （`TestVerifyContentHitsAreLiteral`）。它是「拦下最省事的那类注入」，不是语义检测器；升级到语义判定
  是另一个量级的事，不在这轮假装做到。

**§13.7-2 MCP stdio 进程组**（`pkg/mcp/proc_unix.go` + `proc_other.go` + `stdio_proc_test.go`）：

- `dialStdio` 现在 `Setpgid`（照抄 `pkg/acl/transport_unix.go:12` 的做法与平台回退文件的组织方式），
  `close()` 走 `killProc` 杀**整个进程组**。
- 测试用 `/bin/sh -c 'sleep 300 & echo $! >&2; wait'` 复现「自己 fork 的 stdio server」，断言孙进程消失。
  **该用例有牙**：把 `killProc` 退回 `Process.Kill()` 后它会在 3s 内失败并报出「close 阻塞」——
  这暴露了此缺陷的第二副面孔：孙进程继承 stdout/stderr，`cmd.Wait()` 会一直等到它退出，
  于是 close 卡住、探测/回收挂在管道上（不只是「留个孤儿」）。测试因此把 close 放进 goroutine 并设 3s 上限，
  让「卡住」变成一条明确断言失败而不是挂死测试套件。

**未做（按 §13.5 排后续）**：M3/M4/M7/M10（补 MCP CRUD 时一次做完）、
M5/M8（生命周期矩阵 + probe 缓存 key，与 §7 的 probe 加固合批）、S7/S8（审批令牌绑输入摘要、资源路径清洗）。

## 13.9 实施记录（2026-09-11 第二轮，S4 + S5）

**S4 启停 = overlay（`Skill.Enabled` + `manager/skills.go` + `PATCH /skills/:id`）**：

- **禁用不删东西**：定义文件、正文、sources 一律保留，只把定义文件里的 `enabled` 置 false
  （compozy 用独立的 disabled 名单文件，因为它从 marketplace 装技能、定义目录不归用户；
  ark-mate 的技能定义本来就是**用户自己的文件**，所以开关写回定义本身——少一个"两处状态谁说了算"的问题）。
  `Enabled *bool` 用指针是为了区分"未声明"（默认启用）与"显式关掉"；`enabled` 键只在写回时出现。
- **写盘按 M10 纪律**：读原文件为 `map[string]json.RawMessage` → 只改 `enabled` → `MarshalIndent` →
  临时文件 + `rename` 原子替换。**未知键原样保留**（用户自己加的 `x-user-note` 不会消失），
  坏 JSON 直接报错**不顺手"修好"**——平台没有资格猜用户想写什么。
- **没定义文件就显式失败**（`skill_not_persistable`，含应有的路径与出路）：代码注入/内存注册的
  skill 无处写回，假装成功会让用户以为改完了（对齐本仓"宁可显式失败"的立场）。
- **立刻重校物化**：app 启动重校时把 (root, runtime) 目标登记到 Service，启停后同步重跑同一组目标。
  **禁用 = 撤回**：已物化的副本从 runtime 目录里被摘掉（删除联动只动带 `.forge-managed` 标记的目录，
  用户自有同名目录绝不误删，有测试钉住）。重校失败**不回滚**定义变更（文件已改，谎报失败更糟），
  失败原因随响应回带 `materialize_errors`。
- **顺序刻意**：先磁盘（事实源）、再内存注册表、最后物化——每一步失败时后面的都不做，
  避免出现"内存说禁用、磁盘说启用"的摇摆态。

**S5 激活与启用正交（`SkillActivationFor` + `AgentSkillResp.applicable/reasons`）**：

- **不再静默过滤**：此前 agent 能力目录把"候选 runtime 没有物化源"的技能直接**丢掉**，
  用户看到的是"技能怎么没了"。现在全量列出，每条给 `enabled` / `applicable` / `reasons`。
- **人话原因 + 出路**（分层判定）：已被禁用（并说明改回 `enabled:true` 即可恢复）/
  sources 为空 / 候选 runtime 都没在 sources 里声明（**并列出声明了哪些 runtime**）/
  声明了但 format 未经实证（列出"哪个 runtime + 哪个 format"与已实证的 format 白名单）。
  与 compozy 的差异：那边是 `platforms/environments/requires_tools/requires_capabilities`
  四族 gate（它有 marketplace 与部署环境概念），ark-mate 只有"候选 runtime + 物化 format"
  两个维度，故**不照抄四族结构**，只抄"Gate → Reason(code+message) 必须可解释"这条纪律。
- `pkg/capability.SupportedFormat` 作为 format 白名单的公开入口，并有测试断言它与物化层
  `materializedContentFor` 判定一致——两处漂移会让"适用"说谎。

## 13.10 实施记录（2026-09-11 第三轮，S2 + S3）

**S2 目录级 SHA-256 摘要**（`pkg/capability/provenance.go` + `GET /skills/:id/materialization`）：

- **算法逐条对照** compozy `internal/skills/provenance.go:54`（`ComputeDirectoryHash`）与 `:189`（`writeHashEntry`）：
  相对路径收集后排序；普通文件写 `file:<slash 路径>\nmode:<权限>\n` + 内容 + `\0`（**权限入哈希**，chmod 也算变更）；
  软链**不 follow**，写 `symlink:<路径>\nmode:<权限>\ntarget:<字面目标>\n`，且目标解析后逃出目录即拒
  （`ErrSymlinkEscape`）；设备/管道等未知类型直接报错；遍历**排除平台记账文件**（`.forge-managed` 与 sidecar 自身，
  否则每次重校都"看起来变了"）。
- **sidecar 记什么**：`{version, skill_id, runtime, format, hash, bytes, written_at}`（0600）。
  与 compozy 的差异：那边 provenance 服务于 marketplace（registry/slug/version），ark-mate 没有 marketplace，
  来源就是本机定义文件，所以记的是本地可审计所需的那几项，**不假装有版本号**。
- **三件事因此成立**：审计（期望摘要 vs 实测摘要）、复现（换机器重算一致即产物一致）、篡改可见
  （改内容/加文件/改权限/换软链 → `tampered`，下一次重校即修复）。
- **审计面是只读端点**：`GET /skills/:id/materialization` 每处物化目标一条 `status`
  （`in_sync`/`missing`/`tampered`/`stale`/`disabled`/`user_owned`/`not_applicable`）+ `hash`/`actual_hash`
  + `owners` + `written_at`。**只报不修**：修复归重校/启停（读端点不写盘，符合"读不产生副作用"）。
- **判定顺序即优先级**（写进 DTO 注释与测试）：禁用 → 物化不出来 → 不存在 → 非自有 → 摘要（无 sidecar=stale、
  摘要不符=tampered、摘要≠当前期望=stale、否则 in_sync）。
- **与重校共用一份判定**：`skillUpToDate` 现在要求「归属标记含该 runtime + SKILL.md 内容一致 + sidecar 是同一个
  skill + 记录摘要 == 期望载荷摘要 + 磁盘实测摘要 == 期望」——多塞文件/改权限/换软链这些**内容比对看不出来**的
  变化，摘要全看得出来。老产物（无 sidecar）第一次重校**就地升级**，之后保持幂等。
- **一处踩坑记录**：sidecar 里的 `runtime`/`format` **不参与幂等判定**。共享 root 下同一目录会被多个 runtime
  各投影一次，最后写入者是谁无关紧要（归属由 `.forge-managed` 标记维护）；一开始把它们纳入判定，共享 root 的
  两个 runtime 会互相把对方写的产物判成"过期"而反复重写（`TestReconcileSkillsForPerRuntime` /
  `TestReconcileSharedRootResidualCleanup` 立刻红）。这两条既有测试正好当了护栏。

**S3 安装/物化事务**（`MaterializeSkillFor` + `replaceDir` + `ensureReplaceableDir`）：

- **此前**：裸 `os.MkdirAll` + `os.WriteFile`（非原子、跟随软链、同名即覆盖别人目录）。
- **现在**：`ensureReplaceableDir` 先判归属（不存在 → 可建；带 `.forge-managed` → 可替换；**是软链 / 不是目录 /
  无标记 → 显式拒绝**，错误里给出去路）→ staging 临时目录写全（SKILL.md + 归属标记 + sidecar）→ **重算 staging
  摘要自校验**（"我以为我写了什么"变成可判定事实）→ `replaceDir` 原子换入（旧目录先挪到 `<dir>.forge-old`，
  换入失败**挪回来**；成功后清理）。收益：agent 永远看不到半成品；失败时旧产物原样在；`MkdirTemp` 的随机后缀
  天然避免并发同写；根目录不留 `.forge-stage-*`/`.forge-old` 残留（有断言）。
- **不做**：compozy 的 `MoveInstalledDir` 还带目录句柄持有 + publication lock + 清理诊断（多进程/多租户场景需要）。
  ark-mate 是**单操作者本地平台**，不加锁；这条差异记在这里，免得后人以为漏抄。

## 13.11 实施记录（2026-09-11 第四轮，M1 + M6）

**M1 明文与密钥在类型上分开**（`McpServer` 四个字典 + `pkg/capability/secretref.go`）：

- 字段定型：`Env`/`Headers` 明文，`SecretEnv`/`SecretHeaders` **值只能是引用**。
  引用两种：`vault:<ns>/<name>`（§5 的 AES-256-GCM 密文）与 `env:<ENV_NAME>`（进程环境变量，
  盘上不留痕，容器/CI 用）。**明文密钥一律拒收**——这是 M1 存在的全部理由。
- **引用解析发生在注入前一刻**，由 `internal/manager` 调 vault 完成（`resolveMcpSecrets`/`resolveSecretRef`）。
  这样 `pkg/capability` 不必 import `internal/vault`——**原计划里的 `internal/vault` → `pkg/vault` 上移
  因此不必要了**（少一次跨层搬迁的风险）；`pkg/` 只负责"什么算合法引用"（纯词法），
  "谁能解密"留在应用层。
- **词法一致性守卫**：`capability.ValidateSecretRef` 与 `vault.ValidateRef` 必须同判，由 manager 侧的
  `TestSecretRefLexiconMatchesVault` 逐样本断言（放 manager 是因为只有那里同时 import 得动两边）。
- **注册期校验**：明文项不许空键/空值（多半是写漏了）、密钥项只收引用、**同名键不得同时出现在明文与密钥字典**
  （注入时谁赢没有好答案，不如拒绝）。
- **一处有意的"只告警不拒绝"**：明文 `headers` 命中像凭据的键名（`Authorization`/`X-Api-Key`/`*-token`…）时
  注册期 WARN 建议改放 `secret_headers`。**值层识别**（`Bearer …`/`sk-…`）不在这里做——那是 §7 的凭据卫生，
  靠值猜会挡掉正常配置。类型分离管的是"秘密该放哪个字段"，不是"哪个值看起来像秘密"。
- **解析失败 = 不注入该 server**（逐条 WARN：键名 + 引用 + 出路，**不含值**）：密文缺失 / env 未设 /
  vault 不可用 / kind 不符（AAD 带 kind，provider 密钥与 MCP 密钥不可互换）。不静默塞空凭证——
  那会让问题以"tools/list 401"的面目出现在很远的地方。

**M6 读模型只回 key**（`McpServerResp`）：

- 新增 `env_keys`（明文键，可展示）、`secret_env_keys`/`secret_header_keys`（**只有键名**，连引用都不回——
  引用能反推"这台机器上有没有那个密钥"）、`unresolved_secret_keys`（绑了但解析不了）。
  明文 header 键仍不读回（保持原有立场）。
- `unresolved_secret_keys` 用 `vault.Store.Has`（只看密文文件在不在，**不解密**）：列表端点不该为了体检
  去解密；"密文在但解不开"（vault.key 换了）属于更严重的问题，会在注入时报出来。
- **不泄漏是有断言的**：`TestMcpSecretResolution` 序列化整个列表响应，断言里面既没有密钥值、
  也没有 `vault:` 引用体、也没有从 env 解析出来的明文。
- **未做**：M6 里"覆写时按 key 保留绑定"（`mcp_secret_preservation.go:48` 那件事）依赖 MCP 的写入面，
  随 M3/M4/M7/M10 那批 CRUD 一起做——现在没有 PATCH `/mcps/:id`，无处应用。

**测试面**：`pkg/capability/secretref_test.go`（词法表 + 绑定校验 + 注册闸门）、
`pkg/acl/mcp_inject_test.go`（env → ACP `EnvVariable` 有序下发，nil → 空数组）、
`internal/manager/mcp_secret_test.go`（解析成功/失败、跨包词法一致、读模型不泄漏）。

## 13.12 实施记录（2026-09-11 第五轮，面板接线）

§13 的服务端能力此前只在 REST 上可用，面板看不见——这一轮把**已有**能力接到 UI（不新增端点，
故 README 路由表不变）。改动集中在 `apps/panel/src/pages/CapabilitiesPage.tsx`、
`apps/panel/src/console/capabilityView.ts`（纯函数视图模型）、`packages/api/src/resources/forge.ts`。

- **S4 启停**：全局 Skills 行加 `Switch`（`PATCH /skills/:id`）。回执按事实拼一句话：
  写回/撤回条数 + `materialize_errors`（有就带上）+ `next_action`；成功后重拉 `/skills` 与
  agent 能力目录（否则行标与物化状态会自相矛盾）。
- **S5 启用 ⟂ 适用**：Agent 视角 skill 行不再是"只有能用的才出现"——新增 `SkillActivationTags`，
  「已禁用」「不适用」并列显示（两者可同时成立），不适用 hover 出 `reasons` 原文。
- **S2 体检**：Skill 行加「体检」按钮 → 弹窗逐目标给状态、期望/实测摘要、归属与写入时间。
  弹窗顶部明写**只报不修**（修复走启用/重校；`user_owned` 平台不碰），并给「再体检」；
  未知 status 原样显示而不是当成"正常"（版本错配时看得见）。
- **S1 告警**：Skill 行显示 `N 条告警` 标签，hover 逐条列 severity/pattern/message
  （critical 已拒载、不会出现在列表里；这条只是把 warning 暴露出来，不是新增判定）。
- **M6 密钥**：MCP 行（Agent 视角与全局两处）显示 `密钥 N` / `密钥未就绪 N` 两个标签，
  hover 列**键名**与排查出路。`McpRow` 因此多三个字段（键名数组），值在任何界面都不经过前端。
- **验证与其边界**：`pnpm typecheck`、`pnpm build:panel` 通过；新增的纯函数（`materializationSummary`
  / `materializationMeta` / `materializationNeedsAttention` / `skillActivationView` /
  `skillWarningsView` / `mcpRows` 的密钥字段）用 Node 直接 import TS 模块跑了 6 组断言
  （`node --experimental-strip-types`，临时脚本不入库）。**没有做浏览器驱动验收**——所以
  「渲染无运行时错误」这一条属于未验证项，靠类型检查与纯函数断言覆盖不到；真要它，得按
  README 里既有的真实 Chrome DoD 流程补一次。
- **仍未做**：§6 忙时输入队列的前端（投递模式选择器 + 队列列表 + 撤下）——类型与事件枚举早已同步，
  是面板侧唯一还欠的一处。

**顺带的既有测试改动（有意为之，非放宽）**：`TestAgentCapabilities` 原来断言无源技能"不可见"
（`capabilities_test.go:174`），S5 起改为断言"列出但 `applicable=false` 且有原因"——
这是本次的契约变更点，注释已写明。
