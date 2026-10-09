# ark-forge 能力变更生效边界技术方案（MCP / Skill 定义变更何时落到 agent）

> 定位：`docs/capability-ecosystem-design-20260922.md` 的续篇。前者回答「能力数据面长什么样」（三张表、投影、密钥、写面、Library），本方案只回答一个更窄的问题——**通过写面改了一个 skill / MCP 定义之后，已经在跑的那些会话什么时候拿到它**。
>
> 状态：设计待评审。本轮**不改代码**。

---

## 1. 背景与目标

### 1.1 业务背景与现状

能力数据面已经收口：`<CapabilityDir>/{skills,mcp,tools}/*.json` 是唯一事实源，进程内 `capability.Registry` 三表聚合（`backend/pkg/capability/registry.go`），REST 写面带完整闸门与 C9 落盘纪律（`backend/internal/manager/capability_write.go`、`skills_write.go`、`mcps_write.go`）。**注（2026-10-08）**：这条"文件为唯一事实源"的形态已被 Q-5 改变——事实源迁 DB、文件降级为导入/导出（§10.5）；下文凡涉及文件模型的描述都以 §10.5 为准。两条投放链路也各自成立：

- **MCP**：`session/new` 时按「会话选定 > agent 白名单 > 全量」+ runtime transport 承载过滤后经 ACP 注入（`backend/internal/manager/capabilities.go:378`、`backend/internal/manager/conversation_start.go:104`）。
- **Skill**：按 runtime 的投影声明物化到原生目录，两个 adapter 当前同根同形态（`backend/pkg/runtime/adapters.go:39`、`:104`），写面末尾一次 `reconcileSkillProjections()` 两边一起写/撤（`backend/internal/manager/skills_write.go:79`）。

### 1.2 痛点分析

用户的预期是「**mcp 和 skill 的变动，直接对两个 agent runtime 都生效**」。当前实现只在部分路径上满足它：

1. **已有会话不因定义变更重算能力**。缺省路径（未声明 `capabilities`）的会话指纹恒为空，代际键与引入代际机制之前逐字相同，因此**永不迁移**（`backend/internal/manager/capability_fingerprint.go:99-101` 与 `backend/internal/manager/generation.go:27-30` 的注释都明确写了这是刻意的）。只有声明过 `capabilities` 的会话才会在轮次边界按内容指纹迁移。
2. **写面不回带生效面**。改完一个 MCP，没有任何字段告诉用户「它会不会到达 dsh」「哪几条会话会变」，只有一句 `next_action` 文案（`backend/internal/manager/mcps_write.go:121-131`）。
3. **transport 承载差异不可见**。`opencode` 声明 `stdio/sse/http`、`dsh` 声明 `stdio/streamable_http`（`backend/pkg/runtime/adapters.go:36`、`:101`）——**stdio 是唯一两边通吃的传输**。这是底座事实，不是缺陷，但它目前只在 `/library` 的 `applicability` 里可见，写面完全静默。
4. **接口不对称**：skill 有 `PATCH /skills/:id` 启停，MCP 没有，只能 `PUT /mcps/:id {"enabled":false}`（`backend/internal/router/routes_capability.go`）。
5. **【非目标】手工改定义文件不生效**：装载只在 serve 启动时发生一次（`backend/internal/app/app.go:391`），没有 watcher、没有 reload 端点。按本轮决策，**这不是要修的缺口**；而且在 Q-5 迁 DB 之后，它从"约定"变成**结构性事实**——文件已不是事实源（见 §1.4 C-1、§4.7-⑫、§10.5）。

### 1.3 目标与非目标

**目标**

- G-1 通过 REST 写面做的**任何成功变更**，都要对**已存在的会话**生效，边界是**下一个轮次**（唯一能生效的时刻，理由见 §3.5-①）。
- G-2 生效面**只能限制在底座事实允许的范围内**，且这个限制必须**可见**（写面回执给 `applies_to_runtimes` 与不可达原因）。
- G-3 变更**不打断在途轮次**、不改写已经发出的那一轮的行为，也不引入「立即生效」这类做不到的语义。
- G-4 不破坏 R11（缺省零回归）、不新增事件 type、不新增第二条迁移入口。

**非目标（明确不做）**

- N-1 目录监听 / `POST /capabilities/reload`；**运行期直接编辑定义文件不在承诺范围内**。
- N-2 「立即应用」按钮或写面直接踢连接（§3.5-②）。
- N-3 新增 `capability.changed` 之类的事件 type（§3.5-③）。
- N-4 让会话级 skill 子集真正限制投放面——物化根是进程级的，M3 的既定边界不变（§4.7-⑨）。
- N-5 消除两个 runtime 的 transport 承载差异（底座事实）。

### 1.4 约束与非功能需求

- **C-1 事实源：文件 → DB（Q-5 已定，2026-10-08）**。开发阶段允许迁移本地持久数据（仓库 `AGENTS.md`「变更授权」），因此能力的**事实源迁到 DB**，`<CapabilityDir>` 降级为**导入/导出**载体。C9 纪律的**意图**保留（只改目标键、保留未知键、原子性、并发乐观锁），**机制**换成 DB 事务 + 行 revision 比较（比 mtime+size 更强）。详见 §10.5 与实施计划 `.dsh/docs/plans/2026-10-08-capability-store-migration-plan.md`。于是「不支持手工改文件」从"约定"升级为**结构性事实**：文件已不是事实源。
  **两处口径精确化（2026-10-08，计划评审后）**：① **只迁 `skills` + `mcp_servers` 两张表**——`tools` 没有任何写路径（`<dir>/tools/*.json` 只被读，探测产物 `ReplaceToolsForMcpServer` 仅在内存），是每轮重建的派生数据；② **memory 模式例外**：`FORGE_STORE=memory`（无持久层）时能力仍从文件装载，本条"结构性事实"只对 **sqlite 模式**成立。
- **C-2 判据函数保持只读**：`planGenerationSwitch` 被单测逐条钉住，注释明确「只读、无副作用」（`backend/internal/manager/generation.go:70-71`）。任何状态对齐都不许塞进去。
- **C-3 四条代际纪律不许破**（`backend/internal/manager/generation.go:16-25`）：只在真的变了时切 / 忙则顺延 / 探针不可读保守当忙 / 纯内容变化迁移失败回退。
- **C-4 缩权与扩权的不对称不许动**：基础键变化（权限收回 / 换 provider）仍然「立刻生效且失败不回退」；本方案引入的 rev 变化属于**内容变化**，失败必须回退。
- **C-5 平台不管理底座自己的 MCP 子进程生命周期（范围收窄，2026-10-08）**：**远程传输**的 MCP 改由平台侧网关代连（Q-6 已定，§10.6），代价是平台开始承担"连接与重连"这份责任；**本地 stdio server 的子进程仍归底座自建自管**，除非另案决定 S2（§10.6-④）。
- **C-6 密钥值绝不进任何新增的标识**（沿用指纹那三条边界，`backend/internal/manager/capability_fingerprint.go:13-21`）。

### 1.5 验收标准

见 §6——每条都可测，且都指定了测试落点。

---

## 2. 现状盘点（写方案前必须认下的事实）

### 2.1 已经成立、不需要改的两条

| 事实 | 证据 |
|---|---|
| **Skill 的磁盘投放已经是一次改动两边同时变**：两个 adapter 同根同 layout，写面末尾遍历全部已解析投影重校 | `backend/pkg/runtime/adapters.go:39,104`；`backend/internal/manager/skills_write.go:79-91` |
| **MCP 对新建会话立即生效**：`session/new` 读的是当时注册表 | `backend/internal/manager/conversation_start.go:104` |

### 2.2 四条要处理的差距

| 编号 | 差距 | 证据 |
|---|---|---|
| G1 | 缺省路径会话**永不**因定义变更迁移 | `backend/internal/manager/capability_fingerprint.go:99-101`、`backend/internal/manager/generation.go:99-115` |
| G2 | 写面不回带生效面 | `backend/internal/manager/mcps_write.go:117-132`、`backend/internal/manager/skills_write.go:204-219` |
| G3 | transport 承载差异写面不可见 | `backend/pkg/runtime/adapters.go:36,101` |
| G4 | MCP 启停无 `PATCH` | `backend/internal/router/routes_capability.go` |

### 2.3 生效面的四层过滤（决定「是否两边都生效」）

```
定义（Registry）
  → ① runtime transport 承载（MCP）/ runtimes 白名单（skill）
  → ② agent 白名单（agent.Mcps）
  → ③ 会话级 capabilities 选定（M3）
  → ④ 进程代际（能力集是进程级的，同代际共享一个 runtime 子进程）
```

①是底座事实，②③是用户显式限制，④是协议事实。本方案只修「定义变更如何穿过 ④ 到达已有会话」，并让 ① 在写面可见。

---

## 3. 方案设计

### 3.1 总体思路

**给"缺省路径"补一个进程内的能力变更计数（per-runtime revision），把它作为该路径的代际 token 参与代际键的组装；会话行记住自己钉住的那个 token，在轮次边界比较。**

一句话：**选定路径继续用内容指纹（更精确），缺省路径改用变更计数（因为内容指纹会破坏 R11）。**

### 3.2 对外语义承诺（v1）

> 通过 REST 写面对 skill / MCP 定义做的任何**成功**变更，最迟在**相关会话的下一次轮次开始时**生效：那一轮起，注入的 MCP 子集与底座目录里的 skill 产物都按新定义。
>
> 边界与例外：
> 1. 不打断在途轮次；有未应答的权限/提问时顺延到再下一轮（C-3 纪律不变）。
> 2. 只对**底座事实允许到达**的 runtime 生效（`stdio` 通吃；`sse`/`http` 到不了 dsh，`streamable_http` 到不了 opencode）。写面回执必须如实回带。
> 3. 代际数达上限（`FORGE_GENERATION_LIMIT`，默认 4）时，迁移会带码拒绝 `40901 generation_limit_reached`（继承既有语义，不新增特例）。
> 4. **运行期直接编辑定义文件不在承诺范围内**——Q-5 迁 DB 后文件不再是事实源，因此这条是结构性的；从旧版本升级时文件会被一次性导入（§10.5-④）。
> 5. Skill 侧有一件事**未实测**，但它不是"是否会重读"，而是"**底座开箱是否读我们写的默认根**"（`backend/pkg/runtime/adapters.go:56-61` 的 `Tested=false`，形态本身 2026-09-09 验证过）。区别很重要：只要边界重建进程，**新进程启动时读投影根**是底座的正常启动行为，"对新拉起的进程生效"与"下一轮生效"在机制上是同一件事（§10.7）。所以这条是 **skill 功能的可用性验收**，而**不是**承诺口径的上限；修正后的口径见 §10.7 与 ADR-15。

### 3.3 核心机制：能力变更计数（revision）+ 启动代（epoch）

**计数侧（Service 内）**

- `capabilityRev map[runtimeID]uint64`：**按 runtime 计**的单调递增计数，由 `s.mu` 保护。
- `capabilityEpoch string`：进程启动时生成一次（时间戳 + 随机后缀即可）。作用见下方「跨重启」段。
- 每次写面**落盘成功后**按受影响 runtime +1（谁能 +1、谁不能，见 §4.2）。

**会话侧**

- `conversations` 新增一列 `capability_profile TEXT NOT NULL DEFAULT ''`（v8 迁移）。
- 值形态：`''`（从未发生过变更）或 `caprev:<epoch>:<n>`。
- 与既有 `capability_fingerprint` 列**语义分开、互不替代**：前者是缺省路径的变更计数 token，后者是选定路径的内容寻址指纹（`'' = 未声明能力选择` 的对外语义不许动，见 `backend/internal/dto/dtoconversation/response.go:45-56`）。

**代际键**

`generationKey(connKey, fingerprint)`（`backend/internal/manager/connection.go:45-50`）的第二个参数从「指纹」**泛化为「代际 token」**——函数实现一字节不改，但调用链上有一个必须显式拆开的耦合（见 §4.3.1）：

```
缺省路径：token = ""  （rev 为 0，从未变更）
          token = "caprev:<epoch>:<n>"  （rev > 0）
选定路径：token = capabilityFingerprint(...)  （与今天逐字相同）
```

这样 `plan.To != plan.From` 在缺省路径 rev 变化时自然成立，后续 busy/defer/switch 判定与不对称规则**全部复用既有代码**，不需要新增分支。

**R11 的守门**：rev 未变时缺省路径的 token 恒为 `""` → `generationKey` 原样返回基础键（`connection.go:46-48`）→ 键与引入代际机制之前**逐字相同**。因此"升级即抖动"不会发生。

**跨重启（epoch 的作用）**

token 里带了 epoch，所以**跨重启的 token 不可比**：进程重启后 rev 计数归零 → 当前 token 回到 `''`，而库里那条记录还留着 `caprev:<旧 epoch>:3`——若按 `!=` 判定就会误判为"变更了"，于是每次重启后每条缺省会话的首次轮次边界都要白白迁移一次（而 handoff 是有损的）。事实上重启后的会话本来就是按当前定义重建的（最新），不存在遗留差异。

处理方式：**比较时先看 epoch**——epoch 不同 → 视为同代际（keep），并把对齐交给重建路径。

**为什么对齐不需要额外写路径**（关键正确性论证）：

1. 进程重启后活跃集合为空（`orchestrator.conversations` 是内存态），所以对一个旧会话的第一次 prompt 必然经 `ensureConversation` → `connectConversation`（`backend/internal/manager/conversation_start.go:161-201`、`:239-250`）。
2. `connectConversation` 末尾走 `registerConversation`（`backend/internal/manager/service.go:250`）持久化会话记录——**就在这里写入 `(当前 epoch, 当前 rev)`**。
3. 于是轮次边界上的 `planGenerationSwitch` 读到的记录一定是**本进程写的**，epoch 恒等；epoch 分支只作**防御性判据**存在（它让"跨重启 token 不可比"成为显式事实，而不是依赖上面这条调用顺序的巧合——与 `runtimeConn.intentional` 把"谁先断开"变成显式标志同一个取向）。

另外要同时具备的：**重建路径必须把当前 token 一并算出来**。`ensureConversation` 与 `maybeMigrateGeneration` 两处计算目标代际时都调 `capabilityFingerprint`——缺省路径下要改成取当前 rev token（§4.3）。

**memory 模式**：无持久层时 `capability_profile` 只存在内存记录里；会话不跨重启存活，epoch 分支实际不会触发（文档与测试都要体现这条差异）。

### 3.4 与既有能力指纹的分工（两条路径各自用什么判据）

| 会话类型 | 判据 | 理由 |
|---|---|---|
| 声明了 `capabilities`（`sel != nil`） | **内容指纹**（不动） | 内容寻址更精确：改了一个对该 runtime 不可达的 server，指纹不变 → 不迁移（正确）；改了又改回原样 → 指纹相同 → 不白迁。 |
| 未声明（`sel == nil`） | **变更计数 token**（新增） | 缺省集无法在不破坏 R11 的前提下内容寻址：指纹算法把 runtime Spec/版本/投影声明都纳入了输入（`backend/internal/manager/capability_fingerprint.go:86-92`、`backend/pkg/runtime/fingerprint.go`），一旦升级 forge 版本或改了投影声明，指纹就变 → 所有缺省会话白迁一次。 |

**唯一例外的边界**：选定路径的会话**不**参与 rev 比较。代价是"改了一个 dsh 不可达的 sse server"不会让选定路径的 dsh 会话迁移——这正是想要的。

### 3.5 备选方案与取舍

#### ① 生效时刻：为什么只能是「下一个轮次」

ACP 没有「给活会话换能力」的通道，能力集与投影根都是**进程级**的（`backend/internal/manager/generation.go:12-14`）。要换能力就必须换进程，而换进程的时机只能是两轮之间。

#### ② 被否：「立即应用」按钮 / 写面直接踢连接

- 写面直接踢连接会**打断在途轮次**，与"绝不打断在途轮次"这条既有一致性承诺冲突（`backend/internal/manager/generation.go:22`）；若改成"忙则顺延"，它就在语义上**逐字退化成**本方案。
- 按钮唯一能提前的是**空闲会话的进程预热**——用户按下后会话的能力面与什么都不按**完全一样**。一个大概率什么都不提前发生的按钮，正是本仓反复避免的「发明一个看起来有用的状态」（对照 `backend/internal/manager/library.go:224-226` 拒绝给 agent 造 `enabled` 开关那段）。
- "我不想等"的真实出口是现成的 `POST /agents/:id/stop`（粗粒度，语义诚实）。将来若"预热"成为真实需求（大上下文会话 handoff 慢到可感知），再加，且必须命名为**「预迁移 / 预热到新代际」**。

#### ③ 被否：新增 `capability.changed` 事件

- `events` 表的 `conversation_id` 是 `NOT NULL REFERENCES conversations(id)`，外键由 `pkg/dbclient` 的守卫钉着（`backend/internal/store/migrate.go:51`）——**平台级事实没有会话可挂**。`automation.run` 撞的就是这面墙，所以它"只进 bus，不落 events 表"（`backend/internal/manager/automation_sched.go:341-348`），运行史另落定义文件的 `recent_runs`。
- 于是"待更新"做成事件就只能走实时通道：WS 冷挂载只发 snapshot 不回放历史，**刷新一次徽标就凭空消失**——除非再造一个 GET 把它查回来，那等于承认读模型才是事实源。本仓对此已有明文分工：**「事件负责过程、GET 负责当前态」**（README busy input 队列一节）。
- **过程面本来就不缺**：迁移发生那一刻已有 `session.resumed reason=generation` + 代际标识（`backend/internal/manager/events.go:8-14`）。
- 代价对比一边倒：新增 type 要动 `events.go` 常量 + `TestEventTypeContractFrozen` 冻结快照 + 前端 `Ev`（`TestFrontendEventTypesMatchRegistry`），两条守卫立刻变红，收益只是"徽标不用刷新就亮"。

#### ④ 被否：把缺省路径也纳入内容指纹

见 §3.4 表格——会破坏 R11（`generationKey` 的"指纹为空 → 原样返回基础键"就是为它设的，`connection.go:39-40`）。

#### ⑤ 被否：目录监听 / reload 端点

按本轮决策（N-1）不做。但要在文档里留一条**给后人**的约束：今天 `Registry.LoadDir` **只合并、不摘除**（`backend/pkg/capability/registry.go:48-57`），直接拿它做 reload 会让"已删掉的定义"以幽灵形式留在注册表里——将来真要做，必须先补整体替换语义并同步重校 skill 物化。**注（2026-10-08）**：Q-5 迁 DB 后装载源变成 store，这条幽灵定义缺陷随之消失（§10.5-⑥）；但"运行期改文件不生效"这一点仍是**结构性**的，不再需要靠约定维持。

#### ⑥ 被否：全局 rev（不分 runtime）

会让"改一个 dsh 根本不承载的 sse server"也迁 dsh 会话。per-runtime 的判定成本就是 `supportedMcpTransports`（内存查表），收益明确，故采用 per-runtime。

---

## 4. 详细设计

### 4.1 数据模型（schema v8）

```sql
ALTER TABLE conversations ADD COLUMN capability_profile TEXT NOT NULL DEFAULT '';
```

- 迁移内核按既有有序版本追加（`backend/internal/store/migrate.go`，当前最新为 v7）；**不用 gorm AutoMigrate**（表达不了有序版本/版本拒绝启动/staging 回滚）。
- 落库格式是硬契约：时间列 TEXT + RFC3339Nano(UTC)、布尔列 INTEGER 0/1（`backend/internal/store/format_test.go`）——本列是 TEXT，无新增格式面。
- `ConversationRec` 增字段 `CapabilityProfile string`（`backend/internal/manager/service.go:45-83` 一带），`store` 读写映射同步。
- **不**动 `capability_fingerprint` 列与其索引 `idx_conversations_fingerprint`（代际回收按它查，`backend/internal/store/migrate.go:151-158`）。

### 4.2 计数点：谁能 +1、谁不能

| 触发 | 是否 +1 | 加在哪些 runtime | 理由 |
|---|---|---|---|
| `CreateSkill` / `UpdateSkill` / `DeleteSkill` / `SetSkillEnabled` | ✅ | **全部已解析出 skill 投影的 runtime** | skill 投放面是跨 runtime 的（两个 adapter 同根），一次物化重校本来就覆盖全部 |
| `CreateMcpServer` / `UpdateMcpServer` / `DeleteMcpServer` / 经 `PUT` 改 `enabled` | ✅ | **承载该 server transport 的已注册 runtime** | 只影响能到达的那几个；`stdio` 会同时命中两个 |
| `POST /mcps/:id/probe`（写 Tool 目录） | ❌ **绝不** | — | Tool 目录是**平台的**读模型/选择面，不是 agent 的工具面——agent 的 MCP 是底座自己连的。让 probe +1 会把"探测一下"变成"所有会话重启一轮" |
| `GET /library`、任何读面 | ❌ | — | 只读 |
| `LoadCapabilityDefinitions`（启动装载） | ❌ | — | 启动时 rev 从 0 开始；新会话记录的也是 0 |
| `forge capability migrate`（离线改文件） | ❌（且不在承诺内） | — | 需重启才装载（§3.2 例外 4） |

两条硬约束：

1. **只在落盘成功后 +1**。写面失败要回滚内存（`backend/internal/manager/skills_write.go:193-202`），此时**不许** +1——否则"rev 变了但定义没变"会造成一次白迁移。
2. **一次请求只 +1**。批量语义（将来可能有）要按"请求"而不是"条目"计数。

### 4.3 判据与迁移（`planGenerationSwitch` 的改动）

现状（`backend/internal/manager/generation.go:99-115`）：

```go
wantFP := as.Fingerprint
if sel := ParseCapabilitySelection(rec.CapabilityJSON); sel != nil {
    if fp := o.svc.capabilityFingerprint(agent, spec.ID, sel); fp != "" {
        wantFP = fp
    }
}
plan := generationPlan{From: rc.key, To: generationKey(baseNow, wantFP), ...}
```

改为（伪码，语义）：

```go
want := pinnedGeneration                        // 活跃句柄钉住的代际选择（本进程写的）
if sel := ParseCapabilitySelection(rec.CapabilityJSON); sel != nil {
    want = o.svc.capabilityGenerationFor(agent, spec.ID, sel)   // 选定路径：内容指纹（不变）
} else if g := o.svc.capabilityGenerationFor(agent, spec.ID, nil); g.Profile != rec.CapabilityProfile && epochsMatch(rec.CapabilityProfile) {
    want = g                                    // 缺省路径：变更计数 token
}
// epoch 不同 → want 保持 pinned（防御性 keep，见 §3.3）
plan := generationPlan{From: rc.key, To: generationKey(baseNow, want.KeyToken), ...}
```

#### 4.3.1 一个必须显式拆开的耦合（本节最重要的实现约束）

今天 `connectConversation(ctx, rec, agent, spec, cwd, sel, fingerprint string)` 的 `fingerprint` 参数**同时承担两件事**：

1. 进连接键（`acquire(..., fingerprint)` → `generationKey(baseKey, fingerprint)`，`backend/internal/manager/conversation_start.go:239-240`、`backend/internal/manager/connection.go:301`）；
2. 落库/落句柄（`rec.Fingerprint = fingerprint` + `registerConversation` + `ActiveConversation.Fingerprint`，`backend/internal/manager/conversation_start.go:288-295`）。

本方案要求这两件事在缺省路径上**取不同的值**（进键 = rev token，落库 = `''`），所以必须把它拆成一个显式的小值对象，而不是继续共用一个字符串：

```go
// capabilityGeneration 一次「这套会话该挂哪个代际」的解析结果。
// 拆三个字段是为了让「进键的标识」与「落库的两个列」各自只有一个来源——
// 共用一个字符串正是今天 R11 与列语义互相绑死的根因。
type capabilityGeneration struct {
    KeyToken    string // 进连接键的代际 token：选定=内容指纹；缺省=rev token（''=从未变更）
    Fingerprint string // 落 capability_fingerprint 列：选定=内容指纹；缺省=''
    Profile     string // 落 capability_profile 列：缺省=rev token；选定=''
}
```

由此得到**改动点清单**（实现时逐条对照，全部是这一处耦合的下游）：

| 位置 | 改动 |
|---|---|
| `Service.capabilityGenerationFor(agent, runtimeID, sel)` | **新增，唯一实现**：按 §3.4 的分工产出 `capabilityGeneration`。它是"两处各算一遍迟早就漂移"的又一例防护（对照 `baseKeyFor` 的注释 `backend/internal/manager/connection.go:52-56`） |
| `Service.generationFromRecord(rec)` | 新增：从 `rec.Fingerprint` + `rec.CapabilityProfile` 还原"钉住的代际选择"（`KeyToken = Profile 非空 ? Profile : Fingerprint`） |
| `ensureConversation`（`backend/internal/manager/conversation_start.go:203-207`） | `pinned`/`want` 两个字符串换成上面两个值对象；`if want == "" { want = pinned }` 的回落语义改为"未解析出选择 → 保持钉住" |
| 同上 `:209-216` | 迁移失败回退的比较对象由 `want != pinned` 改为 `want.KeyToken != pinned.KeyToken` |
| 同上 `:223-225` | `want != pinned && as.Fingerprint == want` → `want.KeyToken != pinned.KeyToken && as.CapToken == want.KeyToken`（`reason=generation` 的判据必须在**token** 上，不能继续用 `Fingerprint`——缺省路径的 `Fingerprint` 恒为空，用它会**永不**报 generation，把"能力已更新"误报成普通 `restart`，前端那句提示就失效了） |
| `connectConversation` 签名与 `:239-240` | 参数改收 `capabilityGeneration`；`acquire(..., gen.KeyToken)` |
| 同上 `:288-295` | `rec.Fingerprint = gen.Fingerprint`、`rec.CapabilityProfile = gen.Profile`、`ActiveConversation{CapToken: gen.KeyToken, Fingerprint: gen.Fingerprint}` |
| `maybeMigrateGeneration`（`backend/internal/manager/generation.go:190-202`） | 目标代际同样改为 `capabilityGenerationFor`（"沿用句柄当前指纹"的口径改成"沿用句柄当前 KeyToken"） |
| `startConversationOn`（`backend/internal/manager/conversation_start.go:82`、`:128-131`） | 新建会话时按同一个值对象算，并把两列都写进 `registerConversation` 的记录 |
| `planGenerationSwitch`（`backend/internal/manager/generation.go:99-115`） | 如上的缺省分支 + `generationPlan` 增 `TokenChanged`（`FingerprintChanged` 保留原义，只表示选定路径的内容指纹变化） |

其余要点：

- **`ActiveConversation` 增内存字段 `CapToken`**（`backend/internal/manager/orchestrator.go:36-60`），`Fingerprint` 保留原义（选定路径的内容指纹，缺省路径为空）——对外契约（`ConversationCapabilitiesResp.Fingerprint` 的"空 = 未声明能力选择"）因此不动。
- `generationPlan` 的 `Reason` 文案要能区分"内容指纹变了"与"变更计数变了"，否则排障时看不出这次换代际是哪种原因。
- 四条纪律与不对称规则**不改**：rev 变化属内容变化 → 迁移失败回退旧代际继续跑（`backend/internal/manager/generation.go:212-215`）。
- 顺带的一个收益：rev 变化时新代际在下一轮边界建立，旧代际**无人引用即被回收**（`backend/internal/manager/generation.go:219-225`），不需要额外处理。

### 4.4 epoch 对齐（唯一写路径）

- 写点：`registerConversation`（`backend/internal/manager/service.go:250`）——新建会话与每次重开都会经过它，一处覆盖两条路径。
- 读点：`planGenerationSwitch` 只读比较，不做对齐（C-2）。
- 断言测试：构造"库里是旧 epoch 的 token + 新进程 epoch"的记录，`planGenerationSwitch` 必须返回 `keep`（不是为了走通某个用例，而是把跨重启不可比这件事**钉成显式事实**）。

### 4.5 接口设计

**① 写面回执（v1 必做，解决 G2/G3）**

`POST/PUT/DELETE /skills`、`POST/PUT/DELETE /mcps`、`PATCH /skills/:id` 的响应增两段：

```jsonc
{
  "applies_to_runtimes": [
    {"runtime": "opencode", "ok": true,  "reason": "以 stdio 传输注入"},
    {"runtime": "deepseekharness", "ok": false, "reason": "deepseekharness 不承载 sse 传输（它支持：stdio/streamable_http）"}
  ],
  "affected_conversations": 3,      // 该 token 落后的会话数（可为 0）
  "apply_boundary": "next_turn"     // 固定值，把"边界"变成可读事实而不是文档里的暗知识
}
```

- 判定**必须复用**现成的同源函数 `mcpRuntimeVerdict` / `skillRuntimeVerdict`（`backend/internal/manager/library.go:118-142`、`:181-203`）——写面与 `/library` 两处给出不同结论是最难排查的一类不一致（`backend/internal/manager/capability_selection_validate.go:22-23` 已经确立了这条纪律）。
- `affected_conversations` 在 sqlite 模式下按 `capability_profile != 当前 token` 计数；memory 模式下按内存记录计数。

**② 会话读模型 `capability_stale`（v2，可选）**

列表/详情加派生字段，回答"哪几条会话还挂着旧能力"。设计方案见开放问题 Q-2（需要指纹缓存，否则 `GET /conversations` 每次为选定路径现算指纹）。

**③ 事件 payload（增量，不新增 type）**

`session.resumed`（`reason=generation`）的 payload 增 `capability_profile`（不透明串），`capability_fingerprint` 保持原义（缺省路径仍为空）。这是 payload 增量，**不触** `TestEventTypeContractFrozen` 的 type 清单；前端 `@ark/types` 的类型同步即可。

### 4.6 对称性补齐（G4）

新增 `PATCH /mcps/:id {enabled}`，语义与 `PATCH /skills/:id` 逐字对齐（overlay：只改定义里的一个键、其余字段与密钥引用保留、立刻生效于下一轮）。注意 `TestReadmeRoutesMatchRouter` 是**双向**守卫：加路由必须同一次提交更新 README 路由表。

### 4.7 关键逻辑与边界条件（实现时逐条对照）

| # | 边界 | 处置 |
|---|---|---|
| ① | 写面失败 / 回滚 | 不 +1（§4.2 硬约束 1） |
| ② | probe 写 Tool 目录 | 不 +1（§4.2 表格） |
| ③ | rev 未变 | 缺省 token 仍是 `""` → 键不变 → `keep`（R11 守门） |
| ④ | 跨重启 | epoch 不同 → keep + 由重建路径对齐（§3.3/§4.4） |
| ⑤ | memory 模式 | 无持久层；会话不跨重启，token 只在内存 |
| ⑥ | 忙 / 有未决权限 / 探针不可读 | 顺延（既有代码，不改） |
| ⑦ | 迁移失败（内容变化） | 回退旧代际 + `generation_switch_failed` 诊断（既有代码，不改） |
| ⑧ | 基础键变化（缩权） | 仍 `40901 generation_switch_required`，**与 rev 无关**（C-4） |
| ⑨ | 会话级 skill 子集 | 物化仍是进程级（M3 既定边界）：选定能隔离出独立进程，但**不能**把某个 skill 从该底座目录里排除。文档与 UI 口径要保持一致，别让 rev 机制看起来"顺带修好了它" |
| ⑩ | 代际上限 | `admitGeneration` 满额 → `40901 generation_limit_reached`（`backend/internal/manager/connection.go:162-190`）。**这是"变更即生效"的公开例外**，写进 §3.2 与 README |
| ⑪ | 一次写面 → 多个 runtime +1 | skill 变更会让两个 runtime 的缺省会话各迁一次；这是**正确**的（两个底座都真的会变），不是白迁移 |
| ⑫ | 手工改文件 / 导入导出 | 迁 DB 后"运行期编辑定义文件"**结构上不生效**（文件不再是事实源，§10.5）；`capability migrate` 转为导入器，`forge capability export/import` 是其对偶 |
| ⑬ | 底座是否读/重读 skill 文件 | 两件事要分开（§10.7）：**"开箱是否读我们写的默认根"** 是 skill **功能验收**（`adapters.go:56-61` 的 `Tested=false`）；**"是否重读"**（E5）只决定**能否省掉重建**，属成本项。MCP 侧的"下一轮生效"由 ACP 协议保证（`session/new`/`load`/`resume` 都带 `mcpServers`），**不需要探针** |
| ⑭ | 高频写面（脚本化） | 每次成功写面都会在下一轮边界换一次代际 → 代际抖动。文档给出运维建议（批量改完再发下一轮），不在 v1 做合并窗口 |

---

## 5. 影响面与兼容性

| 区域 | 改动 | 守卫/同步要求 |
|---|---|---|
| `internal/store` | v8 迁移追加一列 | `format_test.go` 的落库格式契约；版本拒绝启动逻辑不动 |
| `internal/model` | 实体加 `capability_profile` | 与迁移内核手工一致（三处之一） |
| `internal/manager/service.go` | `ConversationRec.CapabilityProfile`、`capabilityRev`/`capabilityEpoch` | — |
| `internal/manager/capabilities.go` 等写面 | +1 与回执 | 写面 4 处 + token 计算收敛为一处 |
| `internal/manager/generation.go` | 判据加缺省分支 | 四条纪律的单测全绿 |
| `internal/manager/conversation_start.go` | `capabilityGeneration` 拆参数：`ensureConversation` 的 `pinned/want`、`connectConversation` 签名与落账、`startConversationOn` 的建会话路径、`reason=generation` 判据改比 token | §4.3.1 清单逐条；A13 |
| `internal/manager/connection.go` | `generationKey` 注释 + `ActiveConversation.CapToken` | `admitGeneration`/回收语义不变 |
| `internal/dto/dtocapability` | 回执字段 | 与 `/library` 判定同源 |
| `internal/router/routes_capability.go` + README | `PATCH /mcps/:id` | **`TestReadmeRoutesMatchRouter` 双向** |
| 事件 | payload 增 `capability_profile` | type 清单**不变**（冻结快照不动）；前端 `Ev`/类型同步 |
| `frontend/apps/web` | 写面回执展示（MCP 行"可到达 runtime"、skill 行同理） | 无新状态机；M5 的徽标 + diff 另议 |
| **（M6）`internal/model`/`dao`/`store`** | 新增 `skills`/`mcp_servers`/`tools` 三表 + 变更（revision/审计）表 | 落库格式契约；**单独出计划**（§10.5-⑦） |
| **（M6）写面** | `skills_write.go`/`mcps_write.go` 改事务 + revision CAS；`capability/definition.go` 降级为导入导出编解码 | `definition_changed`(40901) 的判据从 mtime+size 换成 revision；`skill_not_persistable` 路径消失 |
| **（M7）`pkg/mcp`** | 补 **服务端** stdio 实现（现有的是客户端） | 边界：`pkg/` 不得 import `internal/`（archtest） |
| **（M7）注入路径** | `McpServersFor` 按传输分派：远程 → 网关条目、stdio → 直连；`Spec.McpTransports` 用途收窄 | README「能力的生效边界」要改口径 |

**兼容性**：`capability_profile` 默认 `''` 与"从未变更"同义，存量会话天然落在缺省路径；不改变任何现有 wire 字段的语义；不新增事件 type。

---

## 6. 回归与验证范围

| # | 验收项 | 测试落点 |
|---|---|---|
| A1 | 写面改 MCP → 缺省空闲会话的下一次 prompt 用新定义注入 | `internal/manager`（`testutil.StubRegistry()` + 自建 responder 断言 `session/new` 参数） |
| A2 | 写面未触及的 runtime 的会话**不**迁移（per-runtime 计数） | 同上 |
| A3 | `POST /mcps/:id/probe` **不**触发任何迁移 | 同上 |
| A4 | 跨重启（旧 epoch token + 新进程）→ `planGenerationSwitch` 返回 `keep` | `internal/manager/capability_fingerprint_test.go` / `generation_test.go` 风格 |
| A5 | 忙 / 未决权限 / 探针不可读 → `defer` | 既有 `capability_selection_restart_test.go` 风格 |
| A6 | 迁移失败 → 回退旧代际 + 诊断，不产生"会话不可用" | 既有 `generation_*_test.go` |
| A7 | 缩权仍 `generation_switch_required`（不对称不变） | 既有用例必须全绿（本方案不改这条） |
| A8 | 代际满额 → `generation_limit_reached`（不静默） | 既有 `admitGeneration` 用例 |
| A9 | 写面回执的 `applies_to_runtimes` 与 `/library` 的 `applicability` **同源一致** | 同源函数断言（两处调用同一判定） |
| A10 | 缺省路径在无变更时键与引入前**逐字相同**（R11） | `generationKey` 单测 + 缺省会话共享暖进程的用例 |
| A11 | `PATCH /mcps/:id` 生效且 README 守卫绿 | `internal/router` + `TestReadmeRoutesMatchRouter` |
| A12 | 事件 type 清单未变（本方案不新增 type） | `TestEventTypeContractFrozen` 与前端同步守卫保持绿 |
| A13 | 缺省路径换代际后，`capability_fingerprint` 列仍为 `''`、`session.resumed` 的 `reason` 仍为 `generation`（不是 `restart`） | ADR-9 的回归；断言列值与事件 payload |
| A14 | 定义已变更后，一条缺省会话被**重建**（模拟进程死亡 / 空闲回收）时挂到**当前**代际，而不是回落 `baseKey` 那一代 | §4.3.1 `ensureConversation` 的 `want` 回落语义；这是"重建后悄悄换回旧能力集"的反向失效模式 |

**不进 CI 的部分**：真底座端到端（"改完 skill，opencode 下一轮真的读到新技能"）属于 P-2 探针范围，仍走 `docs` 里既定的实测路径，不在单测里假装。

---

## 7. 实施计划（里程碑，每阶段可独立上线）

| 阶段 | 内容 | 产出/验收 |
|---|---|---|
| **M1 数据与计数** | v8 迁移 + `ConversationRec` 字段 + `capabilityEpoch`/`capabilityRev` + 写面 +1（含"probe 不 +1"与"失败不 +1"） | A3；`format_test.go` 绿 |
| **M2 迁移判据** | `generationKey` 泛化 + `capabilityGeneration` 拆参数（§4.3.1 全表）+ `planGenerationSwitch` 缺省分支 + epoch 防御 + `registerConversation` 对齐 | A1、A2、A4–A8、A10、A13、A14 |
| **M3 可见性** | 写面回执 `applies_to_runtimes`/`affected_conversations`/`apply_boundary`（同源判定）+ 事件 payload 增量 | A9、A12 |
| **M4 对称性与文档** | `PATCH /mcps/:id` + README 路由表 + README「能力的生效边界」段落更新 + `capability migrate` 标注离线 | A11 |
| **M5** | 会话 `capability_stale` 派生字段（含指纹缓存），按 §10.2 升级为 stale + diff | Q-2 裁决为终局必做，不再是"可选" |
| **M6 事实源迁 DB**（能力面 **2026-10-09 已落地**；agent 面 **2026-10-09 已落地**；automation 扩展段进行中） | §10.5 全量：`skills`/`mcp_servers` + **`agents`/`automations`（Q-7 一起迁）** 表 + 变更表（revision/审计）+ 一次性幂等导入 + 写面改事务与 revision CAS + `capability migrate` 转导入器 + `export/import`。**计划文档：`.dsh/docs/plans/2026-10-08-capability-store-migration-plan.md`**（能力面 8–12 人日；含 agent/automation 的扩展段另计） | §10.5、§10.5-⑧；A-系列需补"导入幂等""revision CAS 冲突返回 `definition_changed`"两条 |
| **M7 MCP 平台侧网关（S1 只收远程）** | §10.6：`forge mcp bridge`（stdio MCP 服务端）+ 注入分派（远程走网关、stdio 仍直连）+ 工具命名策略 + **指纹改用注入条目**。**计划文档：`.dsh/docs/plans/2026-10-08-mcp-platform-gateway-plan.md`**（9–13 人日） | §10.6；补"停用后下一次列工具即消失""密钥不出平台""加远程 server 不换代际"三条验收 |
| **M8 探测与验收口径** | §10.7（终局版）：**投影端到端链**（`Tested=false` 的正主）→ 移出 T-1，改挂 **skill 功能验收**并立即补测；E4/E6 不设 owner（网关落地后消解，仅 stdio 直连留待办）；E2/E3 归 `CapabilityScope` 触发项；E5 归成本项 | README/UI 口径按能力种类分别措辞；投影端到端实测通过后把该 **`Projection.Tested`** 置 true 并把版本写进它的 `Note`（**不是** `Spec.TestedOnVersion`——那个字段属 `CapabilityScope`/E2/E3，两者同名不同物，见探针 note §7） |

> **M8 进度（2026-10-08 起，10-09 收口）**：**opencode 侧已通过**并置 `Projection.Tested: true`（v1.18.35；取证 + 反证见 `.dsh/docs/notes/2026-10-08-skill-projection-e2e-probe-notes.md` 阶段 A）；**dsh 侧已通过**并同样置 `Projection.Tested: true`（v0.2.0-rc.2，见该 note 阶段 B）——原先记的"本机缺 `acp` profile 是硬前置"**是错的**：`dsh-app-boot` 的 `PROFILE_TEMPLATES` 使 profile 在**首次使用时自动初始化**（实测自建成功）。dsh 侧的取证是两段：源码/配置链（`acp` 模板含 `dsh-base` → 其 patch 常态挂 `skill-filesystem`+`tool-skill` → `agentsHome` 默认 `join(homedir(),".agents")`，故 rank 500 根正是 `${HOME}/.agents/skills`）+ 运行级观察（仅存在于该根的 `officecli` 被真实会话投给模型，且它非内置）。**仍未逐字复跑**：运行级观察在 desktop profile，acp profile 的逐字复跑归 M8 的平台级端到端。
>
> 探针产出的**新增待办已裁决并落地**：skill id 词法闸门走"**收紧写面**"——`pkg/capability/registry.go` 增 `skillIDRE = ^[a-z0-9]+(?:-[a-z0-9]+)*$`（`RegisterSkill` 加 ≤64 判据），`MaterializeSkill` 同规则再拦一道，不合规一律 `40001 definition_invalid` 且零字节落盘；`nameRE` 保留给 MCP/tool。**理由从"依赖未文档化的宽容"升级为硬缺陷**：dsh 对 frontmatter 的 `name` 强制这条词法且**不合规静默丢弃整条**，而平台物化正是把 id 原样写成 `name` ⇒ 放行不合规 id = 造出「物化体检报 `in_sync`、底座永远读不到」的 skill（实测确认）。连带产出两条 README 口径要注意的边界：平台选的根是 **rank 500（最低优先）**，项目级 `<cwd>/.agents/skills`(200) 与 `~/.dsh/skills`(400) 会遮蔽它；且 `DSH_AGENTS_HOME` 会撬走底座的读取根——**平台自己的配置隔离曾注入它**（2026-10-09 修：E5 实测 dsh 会话`<available_skills>` 里看不到投影根下的 skill；护栏见 `TestIsolationKeepsSkillRootAlignedWithProjection`），现在刻意不注入；用户若在平台进程环境里设了它仍会与写入根分叉。

> **M6 进度（2026-10-09）**：**能力面（§2–§6）已全部落地**——v8 迁移建 `skills`/`mcp_servers`/`capability_revisions` 三张表（`internal/store/migrate.go`）、写面按载体分派（`manager/capability_repo.go`：memory 走 `DefinitionFile`、sqlite 走"行 CAS + 同事务一条审计"）、装载侧整体替换式从 store 装载、一次性幂等导入（`meta.imported_capabilities` 目录指纹 + 按行判重、**绝不覆盖**）+ `forge capability import|export`。对外契约逐字不变（`definition_changed`/40901、`skill_exists`、`definition_invalid`/40001 的判据与文案都按载体分派但同码同义），并由"同一套写面断言跑两种载体"的用例（`capability_writeface_test.go`）钉住。**两处与 §10.5 原文的偏差（以代码为准）**：① `skill_not_persistable` **没有消失**——sqlite 模式下"DB 里没有这一行"（代码注入/内存注册的技能）仍会命中它，只是文案与出路换成"先用写面或导入落行"；② `SourceFile` 字段**保留**（DB 载体的条目取空串），因为 `Library` 的 origin 判定与面板展示仍读它——DB 化之后"文件来源"不再成立，口径在扩展段（M6-6）重定义。

> **M6-6 进度（agent 面已落地，2026-10-09）**：v9 迁移建 `agents` 表（与 skills/mcp_servers 同形：`id` + `body` + `revision` + `updated_at`），变更审计**复用** `capability_revisions`（`kind=agent`——"定义变更审计"只有一份）；「当前 Agent」的显式选择从 `<AgentDir>/default.json` 迁到 `meta.default_agent`（§7 P-8）。写面按载体分派（`manager/agents_carrier.go`：memory 走定义文件、sqlite 走"行 CAS + 同事务一条审计"），装载侧整体替换式（先清非种子再逐行铺）、一次性幂等导入（`meta.imported_agents` 目录指纹）+ `forge agent import|export`。**三条与计划的偏差/补强（以代码为准）**：① **`<AgentDir>` 保留**（P-9 原样）：`providers.json` 与 vault 锚点（该目录的父目录）仍在这里，sqlite 模式只是把它降级为导入/导出目录；② 行的正文以**行键为权威**（正文 id 与行键不一致时 WARN 并以行键为准），装载合并**非零字段才覆盖**——行里只写 `name` 时种子的 `runtimes` 原样保留（P-7 那条坑的护栏已落成用例）；③ 保留前缀 `agent-*` 与内置种子名**不能**作为定义行进库（`seedAgent` 只看 id 判只读，否则会造出删不掉、也改不了 runtime 的幽灵定义）。**写面顺序也比 M6 之前更严**：由"先改内存再写文件"改成"先校验 → 再提交载体 → 最后改内存"（载体失败时内存一个字段都不动）。

> **M6-7 进度（automation 面已落地，2026-10-09）**：v10 迁移建 `automations` 表——**定义列**（`id`/`body`/`revision`/`updated_at`）与**运行态列**（`last_fired_at`/`last_status`/`last_reason`/`last_conversation_id`/`consecutive_failures`/`recent_runs`）分列（§7 P-10/P-11；`recent_runs` 是有界 JSON 列，不搬 `events` 表——duplicate/overlap/skipped 没有会话可挂，会被 `events.conversation_id NOT NULL REFERENCES conversations` 挡下）。定义列的 CAS 与审计**复用了** `dao.DefinitionDao` 与 `capability_revisions`（`kind=automation`）——三张定义表只有一份 CAS 实现。写面按载体分派（`manager/automation_carrier.go`：memory 走定义文件 + mtime 锁，sqlite 走行 CAS + 同事务一条审计），装载侧**整体替换式**（库里删掉的行不会以幽灵任务留在内存）、坏定义仍留列表带 `loadErr`。**P-12（本次唯一改了对外失败语义的一条）**：sqlite 模式下"一次调度推进"被拆成**认领事务 → 建会话 → 记录事务**，认领 = `WHERE id=? AND last_fired_at=<读到的值>` 的行 CAS（0 行 = 别人先认领 → 判 `duplicate`，**不建会话**），因此同一 occurrence **至多触发一次**；两个事务任一失败都**不推进内存游标**（与文件载体"写失败不阻断调度"相反），下一 tick 按已认领的游标判 duplicate 而**不会重复触发**，代价是那一轮留痕丢失（日志为 ERROR 级）。**三处与计划的偏差/补强（以代码为准）**：① `state` **留在定义正文**（不在计划 SQL 的运行态列里）：它刻意不在 `automation.ReservedKeys` 里——启用/停用/归档本来就是用户操作，`needs_attention` 改回 `active` 是"我修好了"的正常表达；代价是它的写回会前进一版 `revision`（与文件载体的 mtime 同解），而其余六个运行态列不参与 revision（调度器跑一轮不再让用户的保存请求无故撞 40901）；② 导入把**一个文件里的两类东西**按保留键切开、**同一事务**写进定义列与运行态列（`store.DefinitionImportItem.AutomationRuntime`），导出反向合成同一个 `<id>.json`，于是"导出 → 空库导入 → 再导出"逐字节相同；`last_fired_at` 归一为 UTC + RFC3339Nano（它是 CAS 的比较值）、`recent_runs` 进列时按 `RecentRunsMax` 截断；③ 路由注释里运行态键的码从笔误的 `40000` 改成实际的 **`40001`**（`invalid_param`），实现未变。

> **M6-8 进度（2026-10-09）**：核 P-13 时发现前提也不成立——`mcps` 白名单在写面 DTO（`AgentCreateReq`/`AgentUpdateReq`/`AgentResp`）里**根本没有这个字段**，M6-6 之后它只从 DB 行读、而 `forge agent import` 又刻意不覆盖已有行 ⇒ sqlite 模式下事实上不可改。所以本条落成两半：**① 写面收下 `mcps`**（三态：键缺失不改 / `null` 清空回「未声明」/ `[]` 禁用全部 / 数组白名单；类型不符 → 40000 `invalid_param`，零副作用；创建面同样收下；两条载体落同一份字节）；**② 写面之后的能力面变更通知**：白名单**语义真的变了**（同集合换顺序/重复项不算）才作废该 agent 的**空闲**会话句柄，下一轮 `ensureConversation` 按当前定义重建并重新注入 MCP。**与 §3（缺省路径 rev）的分工**：那条解决「skill/MCP **定义**改动对缺省路径下一轮生效」，本条解决「**agent 定义**里的能力字段改动生效」——后者不依赖前者，因为它不需要给缺省路径换键。三处刻意边界（与 README 同名段落一致）：只作废**空闲**会话（有在途轮次 / 未应答权限或提问的顺延，与 ADR-13 纪律③同判据）、**不动代际键也不关进程**（R11 逐字不变、暖进程继续复用）、语义等价的改动不产生重建。

> **M6-9 进度（2026-10-09）**：装配期时序收口。三条时序（开库 → 三域存量导入 → 装载 → 起调度）在 `app.go` 里本来就成立，"装载失败只告警不阻断"也成立（三个 `LoadXDefinitions*` 只返回计数，坏定义逐条 WARN 跳过；`loadDefinitions` 的 error 返回位今天恒为 nil）。缺的是**守卫**——三者被违反时都不报错，只表现成"东西看不见"或"启动恢复没发生"（过期 `once` 的置 inactive 只在启动时做一次，没有任何后续动作会补做）。新增 `internal/app/definitions_order_test.go` 的 `TestInitOrderingImportLoadSchedule` 走真 `Init`（sqlite + 三域夹具 + 起调度），用**行为**钉住整条链：能力面/agent 面本轮就可见 + 过期 `once` 已被置 `inactive` 且留 `skipped` 痕迹。

> **M7-1 进度（网关骨架，2026-10-09）**：`forge mcp bridge --agent <id> --runtime <id>` 可跑——`pkg/mcp` 补出**服务端方向**（`initialize`/`initialized`/`tools/list`+翻页/`tools/call`/`ping`；坏帧 `-32700`、未知方法 `-32601`、未知工具 `-32602`、工具**执行**失败走 `isError:true` 的**结果**而不是协议错误；stdin EOF → 正常返回），`internal/gateway` 提供 `Scope`/`Source`/`Run`，CLI 只把协议流给 stdout、一切诊断给 stderr。**服务端写在 `pkg/mcp` 是刻意的**：帧类型是自研的，两侧合一处才能自研互测（本仓 `go.mod` 无 MCP SDK）；`TestServerInteropWithOwnClient` 用真子进程让本包客户端连本包服务端跑通 list/call。骨架期 `Source == nil`（工具面恒空、调用一律未知工具）**刻意响亮**：启动 WARN + 取数失败 ERROR，且在被注入之前（M7-3）对真实会话不可达。

> **M7-2 进度（取数与代理，2026-10-09）**：§10.6-②③ 的两条承诺落地。**② 密钥不出平台**：远程 MCP 的凭据在网关进程内解析（复用 `manager.ResolveMcpSecrets` 同一实现），底座可见面里只有一条 stdio 命令。**③ 调用期事实**：每次 `tools/list`/`tools/call` **重读**定义与 `enabled`（新增 `ReloadCapabilityDefinitions`/`ReloadAgentDefinitionsFromStore`，与启动装载同一份实现、只降日志档位并把逐行问题改成"同一条只 WARN 一次"），于是"停用一台 server"在下一次取数即消失、**已有会话**的下一次调用直接失败，密钥轮换下一次调用即用新值——都不需要换代际、不需要重启。选址语义抽到 `pkg/capability.SelectMcpServers`（注入面与网关面**唯一**实现，`RemoteMcpTransports()` 划出"网关代连"那一半）；连接池 `pkg/mcp/pool.go` 按 server 池化已握手会话（同会话串行、服务端错误响应不作废会话、**定义/密钥快照一变立刻作废旧连接**、上限 + 空闲回收）。**两处对 P-2 的补强（以代码为准）**：① 条目除 (agent, runtime) 外还带**会话钉住的 `mcps` 白名单**（JSON 三态）——它是用户约束，不带会让钉过 `["a"]` 的会话按 agent 默认面拿到更多远程 server（授权放大）；它不因定义集变化而变化，故不破坏"条目恒定"；② 网关**不按 runtime 的 transport 声明过滤**（那正是网关的收益：底座只看见一条 stdio 条目），`Spec.McpTransports` 从此只约束 stdio 直连那条路径（M7-3 逐个复核它的消费者）。

> **M7-3 进度（注入分派与指纹，2026-10-09）**：§10.6-①④ 落地。**① 远程收进网关**：`McpServersFor` 只留 stdio 直连那一半，远程全部折叠成**一条** `forge-gateway` stdio 条目（`<平台可执行文件> mcp bridge --agent <id> --runtime <id> [--mcps <钉住的白名单>]`）；远程 ACP 端点（`Launch.Transport` 是 http/ws）**不**下发这条条目——它是本机子进程配方，对端起不来，这类 runtime 保持旧形态（零回归）。**④ 指纹改用注入条目**：指纹对 manager 的**注入计划**取值——计划里只有定义（含 `vault:`/`env:` 引用）与网关条目（只有 scope），**解析后的明文永远不进指纹**（§13.4 边界①）。由此得到本计划最重要的一条性质：**条目恒定**——条目内容与"有几台远程 server"无关，所以增删改远程定义、轮换密钥都不换代际（唯一仍会变的只有会话钉住的 `mcps` 与 agent 本身），而**从零到一**那次也不换代际（无条件注入是它的前提；缺省路径指纹恒 `''` 不迁移，条件注入会让那条会话永远看不到后来的远程 server）。代价是一条常驻的空闲 `forge mcp bridge` 子进程，回退开关 `FORGE_MCP_GATEWAY=off`（P-5）。**三处读面同向放宽**：`AgentCapabilities` 的逐 runtime 承载矩阵、`Library` 的适用性判据、建会话的 `validateSelection`（以及 `CapabilityRouted` 的 transport 排除）——网关在环时远程 server 对每个已注册 runtime 都可达；`off` 时四处一起回到老口径，读面与注入面永不同向漂移。**一处保守取舍**：条目 args 带 `--agent`（网关按当下 agent 声明重解析面，保证 P-4），agent id 因此进指纹 ⇒ 配置相同的两个**不同 agent** 不再共享暖进程（宁可多一个进程，也不把 A 的面投给 B；底座是否按进程缓存工具面尚无定论）。

**计划文档索引**：**M1–M5 合为一篇** `.dsh/docs/plans/2026-10-08-capability-generation-plan.md`（12–16.5 人日；它们是同一套机制的五个阶段，共用同一批类型与同一条调用链，故不再逐阶段拆篇）；M6 → `2026-10-08-capability-store-migration-plan.md`；M7 → `2026-10-08-mcp-platform-gateway-plan.md`。

依赖关系与**建议顺序**：

- M2 依赖 M1；M3/M4 与 M2 可并行。
- **M6 与 M1 有直接交叠**：§3.3 的进程内 `capabilityRev` 在 M6 之后就是 DB revision 的一层缓存。既然 Q-5 已定、开发阶段允许破坏性调整，**更省的做法是让 M6 的"变更表 + revision"先行，M1 直接骑在 DB revision 上**，而不是先写一个进程内计数器再拆掉。若节奏上要先出 M1，则必须把 rev 的取值收敛到一个接口后面，便于整体替换。
- **M7 原则上可独立于 M1–M5**，但落地后应回头简化 M2：远程 MCP 不再进连接键指纹（§10.6-⑤），MCP 侧的判据可能整体消失。
- **M8 与 M6/M7 无依赖**，且投影端到端链那条应当**尽早**跑（它决定 skill 功能本身可不可用，不只是承诺口径）。

---

## 8. 风险评估与应对

| 风险 | 影响 | 应对 |
|---|---|---|
| **白迁移**：改了一个不影响能力的键（如注释字段）也会在下一轮换代际 | 一次性 resume/handoff 开销 | **接受**并写进承诺（"改了定义 = 下一轮生效"）。理由：R2 的"我改了怎么没生效"比"我改了个注释怎么重启了"更难排查；且不引入第二套指纹语义 |
| **handoff 有损**：迁移走 resume/handoff 兜底（`FORGE_HANDOFF_MAX_CHARS`） | 每次定义变更、每条会话至多丢一次上下文 | 承诺里写明；回执给 `affected_conversations` 让人有预期；不做"每轮都切"（C-3 纪律 ① 会挡住无变化的情况） |
| **代际上限冲突** | 满额时"改动即生效"会拒发 | 继承既有语义 + 明确报错文案（§4.7-⑩）；文档给出 `FORGE_GENERATION_LIMIT` 出路 |
| **skill 投影端到端未实测** | 承诺可能过头 | §3.2 例外 5（终局版）：这条是 **skill 功能可用性验收**，不是承诺上限——边界重建进程即兑现"下一轮"（§10.7）；UI 继续按 `untested` 呈现，端到端实测通过后置 `Tested=true` |
| **高频写面 → 代际抖动** | 反复起进程 | §4.7-⑭ 运维建议；v1 不做合并窗口（做了就要定窗口时长，属发明语义） |
| **两处判据漂移** | "面板说会生效、实际没生效" | 代际选择收敛成 `capabilityGenerationFor` 唯一实现；写面回执与 `/library` 共用判定函数（A9 断言） |

---

## 9. 开放问题

- **Q-1 per-runtime 计数的粒度够不够**：skill 变更会对**所有**有 skill 投影的 runtime +1（即使某 runtime 的 skill 白名单把这条排除了）。更细需要内容寻址，会与选定路径的指纹重复。**终局裁决：按 §10.2 用"写时预算的分面摘要"取代"写面一律 +1"，这条自然消解**；v1 维持现状。
- **Q-2 要不要会话 `capability_stale` 字段**：选定路径的 stale 需要现算指纹；若要做，必须配"按 (agent, runtime, selection) 缓存 + 写面失效"，并把 rev 当作缓存失效信号。**终局裁决：必做，且形态是 stale + diff（§10.2）**；v1 可先只做写面回执。
- **Q-3 满额时的默认动作**：今天是带码拒绝。是否要给一个"强制复用最旧代际"的显式选项？倾向不给（复用错误的能力集是安全面问题）。
- **Q-4 事件 payload 是否值得加能力标识**：不加也能工作（前端靠写面回执 + 重取列表）。**倾向加**（成本极低、排障有用），字段名按终局取 `capability_profile`（见 §10.4-①）。
- **Q-5 事实源是否从文件迁到 DB**：✅ **已定（2026-10-08）：迁**。开发阶段允许迁移本地持久数据；落地设计见 **§10.5**。
- **Q-6 授权收窄要不要走"调用期实时校验"**（而非换代际）：✅ **已定（2026-10-08）：走，远程 MCP 收进平台侧网关**。落地设计见 **§10.6**；这是"统一管理"与"收窄即时生效"两件事的共同前提。
- **Q-7 agent / automation 定义是否一起迁 DB**：✅ **已定（2026-10-08）：一起迁**。理由：不迁会留下"改 MCP 即时生效、改 agent 白名单要重启"的用户可见不一致，而 `Library` 的读路径 join 本来就不关心两侧存储形态。**代价要正视（已核实）**：① agent 有**代码内种子**（`builtinAgents`，只在内存、从不写文件），覆盖是"**只在字段非零值时才覆盖**"的部分合并；② `default.json`（"当前 agent"）与 agent 文件同目录、删除时要连带清理；③ `providers.json` 也在同目录，且 **vault 根由该目录父级推导**——**目录必须保留**；④ automation 定义里混着平台独占运行态键（`last_fired_at`/`recent_runs` 等）。落地设计见 §10.5-⑧ 与实施计划扩展段。
- **Q-8 `forge mcp bridge` 是否也代管 stdio server 的子进程**（§10.6-④ 的 S2）：**已给出决策规则（2026-10-08）：不现在拍，按两条廉价实测分派**。S2 的**独占**收益只有两条——① stdio 的密钥不再进底座（今天 `resolveMcpSecrets` 把明文经 `mcpServers[].env` 下发，`pkg/acl/mcp.go:22` 注释直言"这里只负责下发"）；② stdio 工具也走调用期策略。代价是接手三条**底座实现细节、非协议保证**的语义：**cwd**（会话级事实）、**spawn 粒度**（每会话还是每进程）、**子进程回收**（可复用 `procreg`+`ReapOrphans`）。分派规则：用最小 stdio server 打点测"是否按会话 spawn + 是否用会话 cwd"，是 → 做；否 → 暂不做（另有"现实里有多少密钥型 stdio server"一问，几乎没有则收益落空）。**倾向：S1 先上，两条实测并入 P-2 探针，S2 按实测结果设计**。

---

## 10. 终局形态与决策（按终局倒推）

> 本节回答"如果按终局设计，前面那几个开放问题该怎么拍"。它不是新增范围——落地仍走 §7 的里程碑；本节只把**命名、分工与演进方向**一次定对，避免 v1 变成死路。

### 10.1 终局的四个支柱

1. **能力档案（capability profile）是一等实体**：内容寻址 + 不可变 + 可查询（成员明细、创建时间、可回滚）。今天的 `capabilityFingerprint` 已经具备内容寻址的**语义**，但它是匿名哈希、只活在连接键与会话行里——没有实体、没有明细、无法 diff、无法回答"这条会话的能力和现在差在哪"。
2. **滚动粒度由实测 `CapabilityScope` 决定**：`session` → 只换会话；`process`（今天两个 adapter 都是）→ 换进程。这条**不是本方案的发明**，而是仓库自己的架构意图——`backend/internal/archtest/capability_scope_test.go:11-20` 明说 `CapabilityScope` "决定平台能否让多个会话共享一个进程"，并规定放开的唯一路径是先跑 P-2 探针拿到结论。
3. **判定在写时算、读时 O(1)**：把内容寻址从读路径搬到写路径——写面对每个 `(agent, runtime)` 预算生效面摘要，会话选定维度用 `(selKey, defRev)` 当缓存键（rev 一变缓存天然失效，不需要显式失效逻辑）。
4. **可见性与审计必须可消费**：`stale` + **diff**（差哪几个成员），而不是一个布尔，也不是一张没人读的表（反面证据见 §11.3-①）。

### 10.2 终局倒推的决策表

| 决策点 | v1（本方案） | 终局 | 终局依据 |
|---|---|---|---|
| **生效边界** | 轮次边界；缺省路径靠 rev token 迁移 | 同左，但**粒度按实测 scope 分派**：`session` 的 runtime 上，MCP-only 变更只换会话、不换进程 | `CapabilityScope` 的既有语义（`archtest/capability_scope_test.go:11-12`）；E2/E3 裁决 |
| **「立即应用」按钮** | 不做（ADR-4） | **仍不做**。若将来做，只能叫「预迁移/预热」，且必须与自动路径共用同一判据 | 自动路径在 scope=session 时已是最小代价；忙时任何按钮都只能报顺延，与自动行为逐字相同 |
| **授权收窄**（停用某 MCP/工具） | 与能力变更同路（换代际） | **拆出独立通道**：调用期按当下策略重查，不进能力面（**Q-6 已定** → 远程 MCP 收进平台侧网关，§10.6） | movo 的 `_authorize`（`movo …/enterprise_capabilities/tools/service.py:479-505`）；网关落地后平台在环上，收窄即时生效不再依赖底座行为 |
| **事件** | 不加 type；payload 增不透明标识 | 不加 type；字段名统一用 `capability_profile`（v1 值=rev token，终局值=digest） | 能力身份进事件/凭据用于溯源是既有实践（movo 的 token claims 带 `profile_version`） |
| **stale 读模型**（Q-2） | 写面回执 + 列表 `capability_stale` 布尔 | **必做**，形态升级为 stale + diff | 反面证据：movo 完全没有 stale 模型（§11.3-①） |
| **判据** | rev（缺省路径）+ 内容指纹（选定路径） | rev 与 digest **分工**：digest=身份（是不是同一套），rev=失效信号（缓存/对齐）；内容寻址搬到写时 | 同时消除"白迁移"（§8 风险 1）与"每轮现算"两种成本 |
| **事实源**（Q-5） | 文件为事实源 + API 独占写（不动） | **迁 DB 为事实源**（**已定**，§10.5），文件降级为导入/导出与投影产物 | 开发阶段可迁移持久数据；"不支持手工改文件"已抽掉文件模型的主要收益，而不可变版本史/审计/diff 在 DB 里是白拿的 |

### 10.3 终局的两个前置（必须先有结论）

- **前置一：把 E 系列按"环归属"重切，而不是整体升格（Q-5/Q-6 之后已重推，见 §10.7）。**
  `docs/capability-ecosystem-design-20260922.md:797-823` 的实验矩阵里，两组实验回答的是**两个不同问题**：E2/E3 决定 `CapabilityScope`（绑定范围，省内存），E4/E5/E6 决定**生效边界**。
  **终局版结论**：MCP 面与 skill 面的"下一轮生效"都由**平台机制**保证（协议内建的 MCP 重绑 + 进程生命周期归平台），因此 E4/E6 在网关落地后**被架构消解**、E2/E3 价值明显降低、E5 只剩成本意义；唯一真正要做的测量是"**投影端到端链**"，而且它属 **skill 功能验收**、不属于承诺口径。详见 §10.7。
- **前置二：事实源分叉（Q-5）。** ✅ **已定（2026-10-08）：迁 DB**。终局的三个诉求（不可变版本史、变更审计、成员级 diff）在"文件即事实源"下都要额外造物，在 DB 里是白拿的。本方案原本主张"v1 不动"，既然开发阶段允许迁移，就**一次到位**——但落地上仍是 §10.5 的顺序：先把 API 定为唯一写路径（本方案已如此），再换事实源，读写 API 不变。

### 10.4 v1 → 终局的演进桥（三条"一次到位"）

1. **列名/字段名按终局取**：新列叫 `capability_profile`（不是 `capability_rev_token`），事件 payload 字段同名。值形态 v1 是 `caprev:<epoch>:<n>`、终局是 digest。**可以安全演进的前提是：不把它透出为对外契约**——v1 只用于内部比较，对外只给 `capability_stale` 布尔与写面回执。只要守住这一条，换值形态就不需要二次 schema 迁移。
2. **`capabilityGeneration` 的拆分本身就是终局要的形状**：`KeyToken`（进键，终局=digest 或指纹）与 `Fingerprint`/`Profile`（落库的两列）各自只有一个来源。终局只换字段取值，不换结构。
3. **摘要必须能"分面"**：终局的 digest 要能分开比较 **工具面 / 技能面 / 凭据与端点面**——只有会改变底座可见面的变化才触发滚动。今天的 `capabilityFingerprint` 已按维度组装（`projectedSkillParts` / `mcpFingerprintParts` / `runtimeFingerprintPart`，`backend/internal/manager/capability_fingerprint.go:32-97`），分面能力其实**已经存在**，只是被包在一个函数里；终局把它们提升为可独立比较的几个面。不做这一步就会重复 movo 的缺陷（改一个凭据也换会话，§11.3-②）。**§10.6 的网关落地后，工具面应当整体从"进键的指纹"里移出**（变成网关侧的运行时状态），指纹只留技能面与进程面。

### 10.5 支柱一落地：事实源迁 DB（Q-5 已定：开发阶段可迁移）

1. **表按域拆，不合成一张 capability 表。** 沿用既有立场——`docs/capability-ecosystem-design-20260922.md:390` 已把"统一存储"判为**假统一**（skill 有正文与投放声明、MCP 有 transport 与密钥引用、tool 挂在 server 下，字段形态本质不同），选的是 E1「各域自己的 owner 与事实源 + 读路径 join」。落地即 **`skills` + `mcp_servers` 两张表**（`tools` 不迁：它没有写路径，是每轮探测重建的派生数据——依据见实施计划 P-1），`Library` 继续做**读路径 join**。
2. **C9 留意图、换机制。** 今天 `DefinitionFile` 提供四件事，逐条对应到 DB：
   | 文件模型（C9） | DB 模型 |
   |---|---|
   | 只改目标键、保留未知键 | 行的 `body` 存 JSON，`rawPatch` 的 patch 语义**原样复用** |
   | 同目录 staging + 原子 rename | 事务（DB 天生） |
   | mtime + size 乐观锁 | **行 revision 比较并替换**（`UPDATE … WHERE id=? AND revision=?`），不再受时钟/文件系统精度影响 |
   | 保留文件模式 | 不适用 |
   连带效果：`SourceFile` 字段与 `skill_not_persistable`（无定义文件）**消失**——写面不再有"不可持久化"这一类错误码路径。
   > **落地回填（2026-10-09，M6-3/M6-5）**：这两条**没有按原文消失**，而是换了口径保留。`SourceFile` 仍在
   > （DB 载体的条目取空串），因为 `Library` 的 origin 判定与面板仍在读它；`skill_not_persistable` 在
   > sqlite 模式下由"没有定义行"触发（代码注入/内存注册的技能无处写回）。保留的理由是**对外错误面的
   > 稳定性优先于文档里那句"消失"**：把"启停一个无定义行的技能"从"显式报错 + 给出路"改判成"静默成功"
   > 会让用户以为开关生效了。
3. **revision 与变更审计是白拿的**（这正是 §10.1 支柱 1/3 想要的）：新增一张变更表（如 `capability_revisions`：`kind, id, revision, action, actor, at, digest`），于是
   - 写面的"当前代际 rev"直接取全局单调序，§3.3 里的进程内 `capabilityRev` **降级为缓存**；
   - `capability_stale` 与 diff 有了**持久依据**（不用每次现算）；
   - 审计**可被消费**——对照 movo 的只写不读（§11.3-①）。
4. **一次性导入既有文件。** 升级后把 `<CapabilityDir>/{skills,mcp,tools}/*.json` **幂等导入**（作为迁移内核的一步）。导入必须复用 `capability.RegisterSkill/RegisterMcpServer/RegisterTool` 的校验，否则 DB 会进不合法行；**逐条报告失败而不是整体失败**（参考 §11.2-⑤ 的分寸）。文件保留为 `forge capability export/import` 的载体，`capability migrate` 从"运维命令"转为"导入器"。
5. **装配变化很小，但有一条边界要守。** `Registry` 仍是内存索引（解析、指纹、投放都读它），只是装载源从目录换成 store，写面变成"事务提交 → 更新内存索引"。**DB 访问必须留在 `internal` 侧**（`internal/dao` + `internal/store`）：`pkg/capability` 不得 import `internal/*`（archtest 守卫），所以它只接收已解析的定义，"从 DB 装载"放在 `internal/manager` 或 `internal/store`。
6. **顺带消掉的旧问题**：`Registry.LoadDir` 只合并不摘除（幽灵定义，§3.5-⑤）随文件装载一起消失；`FORGE_CAPABILITIES` 从"事实源目录"变成**导入/导出目录**（落地时把导出也算进来：`forge capability export` 把 DB 写回该目录，此后文件是导出物）。
7. **影响面**：`internal/model`（`skills`/`mcp_servers` + **`agents`/`automations`（Q-7）** + 变更表）、`internal/store` 迁移内核（v8 起）、`internal/dao`、`manager/capabilities.go` 的装载、`manager/{skills,mcps}_write.go` 改事务 + revision CAS、`manager/agents.go`（种子×DB 的部分合并、`default.json` 落 meta）、`manager/automation.go` + `automation_sched.go`（运行态分列、调度推进事务化）、`capability/definition.go` 降级为导入/导出编解码、`app/command/capability.go`、README、`internal/store/format_test.go`。
8. **agent / automation 定义一并迁（Q-7 已定，2026-10-08）**：不做"下一轮再说"。理由是不迁会留下"改 MCP 定义即时生效、改 agent 的 `Mcps` 白名单却要重启装载"的用户可见不一致，而 `Library` 的读路径 join 本来就不关心两侧存储形态。**三处必须在实施计划里正面处理**：① agent 有**代码内种子**（`builtinAgents`，只在内存、从不写文件），而文件/DB 覆盖种子时是**"只在字段非零值时才覆盖"**的部分合并——整行写会抹空种子字段；② `default.json` 是**目录级配置**（语义是"当前 agent"）却与 agent 文件同目录，且删 agent 时要连带清理；③ automation 的定义里**混着平台独占的运行态键**（`last_fired_at`/`last_status`/`recent_runs` 等，README 明写它们对写面不可写）——迁 DB 是顺手把"定义"与"运行态"**结构性**分开的机会（今天只靠拒写名单拦）。范围与估时见实施计划 `.dsh/docs/plans/2026-10-08-capability-store-migration-plan.md` 的扩展段。

### 10.6 支柱二落地：远程 MCP 收进平台侧网关（Q-6 已定：方便统一管理）

> **落地状态（2026-10-09）：S1 已实现并通过全部验收**，实现计划见 `.dsh/docs/plans/2026-10-08-mcp-platform-gateway-plan.md`（M7-1…M7-5）。逐条对应：**① 形态**——`forge mcp bridge`（`pkg/mcp` 的服务端最小集 + `internal/gateway` 的数据面/代理），底座侧**恒定**是一条 stdio 条目 `forge-gateway`（`<平台可执行文件> mcp bridge --agent <id> --runtime <id> [--mcps <钉住的白名单>]`）；逐 runtime 的 transport 承载矩阵在网关路径上消失，它此后只约束 **stdio 直连**那一条路（M7-3 逐个复核了它的四个消费者）。**② 密钥不出平台**——远程凭据只在网关进程内解析（复用 `ResolveMcpSecrets`），条目的命令行里只有 scope；指纹亦只含定义与引用，不含解析后的明文。**③ 调用期事实**——每次 `tools/list`/`tools/call` 前重读定义与 `enabled`（`ReloadCapabilityDefinitions`/`ReloadAgentDefinitionsFromStore`），停用/删除/轮换密钥在下一次取数或调用即生效，不需要换代际、不需要重启；探测面（`POST /mcps/:id/probe`）与网关真实面**同一个命名实现**（`capability.GatewayToolName`，`<serverID>__<tool>`），并有对拍用例逐项比对名字与描述。**④ S2 仍未做**（原样保留上一条的分派规则）。**⑤ 生效边界的落地结论**：网关条目恒定 ⇒ 增删改远程 MCP **不改变底座的配置面**，故不换代际；代价是"没有远程 server 也会多一个空闲 bridge 子进程"，回退开关 `FORGE_MCP_GATEWAY=off`（`auto` 默认）。**E4/E6（底座何时重列工具面）不再是阻塞项但仍是测量项**：网关侧已按每次调用记 `(server, tool, outcome, latency_ms)` 作为一手证据面，实测留待后续一次真底座长会话观察；本次收口不臆造结论。

1. **形态：一个稳定的 stdio 网关，而不是给每种远程传输各造一条注入路径。** 平台提供类似 `forge mcp bridge` 的 **stdio MCP server**，由底座按普通 stdio server 拉起，网关内部转发到远端（http / sse / streamable_http）。
   - 对底座而言永远只有**一个** MCP 条目且 transport 恒为 stdio。**"逐 runtime 的 transport 承载矩阵"这个问题就此消失**——这是最大收益：`opencode: stdio/sse/http` 与 `dsh: stdio/streamable_http` 的差异不再限制任何东西。**规范背书**：所有底座 **MUST** 支持 stdio，http/sse 是可选能力（§12.3-②）；而"客户端自带 MCP server 来直接给模型提供工具"正是规范明确预期的用法（§12.3-③）。用户最初那句"变动对两个 runtime 都生效"因此在**架构层面**成立，而不是靠两边恰好都支持某种传输。
2. **密钥不出平台。** 远端凭据在网关进程内解析（复用 `resolveMcpSecrets`），底座配置里只有 stdio 命令，平台**不再需要**把明文 env/header 放进底座可见的配置面——直接强化 README「MCP 明文 ⟂ 密钥」那条立场。
3. **授权收窄与工具可见性变成调用期事实**（Q-6 的真正回报）：网关是平台进程，因此每次 `tools/list` 都能按**当下**定义过滤、每次 `tools/call` 都能按**当下**策略重查（movo 的 `_authorize` 模式，§11.2-④）。于是
   - 停用/删除一个 MCP 或一个工具 → 下一次列工具就不再出现，**不必等换代际**；
   - 密钥轮换 → 只影响网关，底座无感（对照 movo"改一个凭据也换会话"的缺陷，§11.3-②）；
   - `probe` 与 agent 真实工具面**同源**（都是网关的 `tools/list`），平台侧工具目录不再可能漂移。
4. **stdio server 是否也收进来（S2）——另案。** 收益是"统一管理"最彻底（一处配置/审计/密钥/策略）；代价是**跨过 C-5**：stdio 的路径校验、`env` 明文语义、子进程回收（孤儿、僵尸、cwd、信号）都是新的责任面。**倾向分两步**：S1 只收远程传输（本决策），S2 另议。
5. **对生效边界的影响（新结论，但要保守落地）**：底座只看见一个稳定网关后，**增删一个远程 MCP 不再改变底座的配置面** → 原则上**不再需要换代际**，只要底座会重新 `tools/list`。这取决于"底座何时列工具"（会话开始 or 每轮），正是 E4/E6 那组问题——而且**落地网关后可以用网关侧日志直接观测**，不必再靠行为推断。
   **另有一条协议内的便宜路径**（§12.3-①）：`session/load` / `session/resume` 都接受新的 `mcpServers` 并要求底座重连——即**不换进程**也能重绑 MCP 面。所以"MCP 变更必须换进程"从来不是协议约束，而是本仓当前的实现选择（判据变了 → 换连接键 → 换进程）。
   **但 v1 的正确做法仍是先按既有代际机制处理**（改动 → 下一轮边界滚动），把上面两条（免滚动、免换进程）当作**可实测、可逐步拿到的优化**，而不是开写前的假设（与 §3.5 的取向一致：不赌未实测的底座行为）。
6. **命名冲突是新的平台侧责任**：多个 server 的工具可能重名，需要平台侧命名策略（前缀 + 摘要后缀；参考 movo 的 `askai_mcp_<safe>_<sha10>` 与"重名整体失败"的 fail-closed 边界，§11.2-⑤）。
7. **影响面**：`pkg/mcp`（客户端已有，需补**服务端** stdio 实现）、新增 `internal/gateway`（或 `manager/mcp_bridge*.go`）、`McpServersFor` 的注入分派（远程 → 网关条目；stdio → 直连）、`Spec.McpTransports` 的用途收窄（仅约束 stdio 直连那条路径）、`pkg/runtime` 指纹（工具面移出进键指纹，§10.4-③）、README。

### 10.7 ADR-12 的裁决（**终局版**）：不升格，改为**按"环归属"重切**，其中 E4/E6 在终局**消解**

> 先前的版本按"承诺 vs 优化"切分。Q-5/Q-6/Q-7 落定之后重推，结论更干脆：**终局的承诺不来自"底座会不会重读"，而来自"平台有没有把这件事收进自己的环"。**

**终局原则（从 §10.1 支柱 1/3 直接推得）：能进平台环的，就不赌底座行为。**

平台**已经**握有两面保证，它们都是平台机制、不是底座行为：

- **MCP 面**：`session/new`、`session/load`、`session/resume` 三处都携带 `mcpServers` 且协议要求底座重连（§12.3-①，本仓三条路径已接线 `pkg/acl/session.go:139`/`:160`/`:182`）→ 平台**自己决定**何时重绑。§10.6 的网关再进一步：远程 MCP 的可见性与收窄变成**调用期**平台事实。
- **skill 面**：投影根的**进程生命周期归平台**（`ApplyIsolationEnv` + 代际机制）。在轮次边界重建进程后，**新进程在启动时读投影根**是底座的正常启动行为，不是"重读"能力。于是"对**新拉起的进程**生效"与"**下一轮**生效"在机制上是**同一件事**——只要边界真的重建进程。

**所以"下一轮生效"不需要探针，需要的是"缺省路径也走代际"**——那是工程（M1/M2 的 rev 迁移 + R11 不破），不是测量。这也修正了 §3.2 例外 5 的措辞问题：它把"投影**是否被读到**"与"是否会**重读**"混在了一句承诺里。

按这个原则重切 E 系列：

| 实验 | 终局归属 | 终局还值多少 |
|---|---|---|
| **投影端到端链** | **不再是探针，而是 skill 的"功能验收"** | **最高**：它问的是"平台写到默认根、底座开箱读不读"——即 **skill 功能到底能不能用**，与承诺口径无关。`backend/pkg/runtime/adapters.go:56-61` 的 `Tested=false` 正主 |
| E2/E3（绑定范围） | `CapabilityScope`（支柱 2） | **明显降低**：网关让"MCP 变更不再换进程"，要共享进程的理由随之变少 → 有人真要 `session` 粒度时再测；默认 `process` 既诚实又够用 |
| E4（底座何时重列工具） | 交给网关日志观测（§10.6-⑤） | **近乎消解**：经网关的 server 由平台现算；只有 **stdio 直连**那条路径还需要它 |
| E6（缩权是否即时） | 同左 | **消解**：经网关的 server 由平台保证"断"（§12.3-⑦：协议只保证"连上请求里的"，不保证"断掉不在列表里的"）；只有直连路径需要实测 |
| E5（运行中是否重读 skill 根） | 成本项 | **只剩成本意义**：进程重建已能兑现"下一轮"，E5 的价值只是"**能否不重建**"。等重建成本被实测为问题时再做 |

**因此我的建议（终局版）**：不把 E1–E6 整体升格，也**不把它们当作"优化前置"**——而是：

1. **把"投影端到端链"移出 T-1**，改挂到 **skill 功能验收**上（它本来就是功能正确性，不是承诺口径问题），并**立即补测**；
2. **E4/E6 不设 owner**，标注"**终局已由平台在环取代**"，只在"stdio 直连"这一残余路径上保留一句实测待办；
3. **E2/E3 归 `CapabilityScope`**，触发条件写成"有人真的需要 `session` 粒度时"，而不是"某天有空"；
4. **E5 归成本项**，触发条件是"进程重建成本成为实测问题"；
5. **本方案的对外承诺验收前提改为三条**：① 缺省路径的 rev 迁移（M1/M2 + R11 零回归）；② **投影端到端实测通过**（新，且是唯一的测量项）；③ 承诺按能力种类分别措辞（ADR-15）。

**一句话**：终局用「**少滚动 + 平台在环**」取代「细粒度 + 探测底座」。T-1 的旧措辞（"两组实验打包、不阻塞任何里程碑"）在网关落地后**其中一半已经没有内容**——不是被降级，是被架构消解了。

---

## 11. 外部参考：movo（同栈产品的做法与教训）

调研对象 `/Users/morehao/Documents/study/ai-agent/movo`（Python + Vue，底层同样以 DSH 作为 ACP runtime）。**本节每条引用都按行范围打印原文核对过**（不是二手转述）；路径以该仓库根为基准。

### 11.1 它的形态（以及与本方案最要紧的差异）

- **能力档案是一等实体**：`RuntimeProfileSnapshot` 是 `frozen=True` 的值对象，带 sha256 `content_hash` 与内容派生的 `profile_version`（`rp-<hash[:24]>`），见 `profile/models.py:13-18`、`profile/compiler.py:77-82`；同版本不同内容直接报 `profile version is immutable`（`profile/store.py:108-113`）。
- **会话钉住一个不可变版本**：`profile_version` 落在 binding 行上（`dsh_runtime/bindings/repository.py:41`）。
- **MCP 完全不走底座注入，而在平台侧执行**：全仓 `mcpServers` **零命中**；底座只拿到"HTTP 工具网关 + 短期 HMAC token"，token claims 带 `profile_version`/`tool_names`/`scopes`（`dsh_runtime/tool_gateway/token.py:41-53`）。
- **生效时机与本方案同构**（这是最有价值的一条互证）：每轮 `prepare_turn` 重编译 → hash 相同则零副作用返回（`profile/synchronizer.py:54-62`）→ 不同则发布新版本、**先建后继（seed 自前驱）再 best-effort 拆前驱会话**（`synchronizer.py:64-70`、`runtime_coordinator.py:105-134`）。
- **粒度必须说清**：新版本 = 新 isolationKey（`runtime_coordinator.py:19-20`）= **同一宿主进程内的新 runtime 对象**（`services/chat-api/dsh/runtime-host/src/runtime-manager.mjs:5-8` 的 `#runtimes = new Map()`），**不是新 OS 进程**。所以它的切换成本结构不能直接类比 ark-forge（一个 `runtimeConn` = 一个 OS 进程）。

### 11.2 值得借鉴

1. **让能力档案成为可查、可校验的一等实体**，而不只是连接键里的匿名哈希：它有版本表，且离线导入时会复算摘要并校验 `profile_version == rp-<hash[:24]>`，不符即报 `Runtime Profile bundle integrity check failed`（`profile/bundle.py:18-27`）。→ 支撑 §10.1 支柱 1，也让"写面回执 + diff"有地方可算。
2. **"先建新、再 best-effort 拆旧"的滚动顺序**：后继就绪并持久化之后才丢弃前驱，失败时调用方仍持有旧绑定（`synchronizer.py:65-70`）。这与 ark-forge `connectConversation` 的"迁移失败回退钉住旧代际"（`backend/internal/manager/conversation_start.go:209-217`）是**同一条安全原则的两种写法**，互为外部证据。
3. **忙时的并发正确性用条件更新（CAS）表达**：只有前驱 `active_turn` 为空或终态才允许被替换，否则 `BindingReplacementConflict`（`bindings/repository.py:78-81`），上层转成可判定的忙错误（`chat_service.py:222-235`）。→ ark-forge 的对应物是 `admitGeneration` + 在途轮次/未决权限判定。**分歧点**：它忙则**整轮拒绝**，本方案忙则**顺延到下一轮、本轮继续用旧代际跑**——后者用户无感，是本方案更优的一处。
4. **授权收窄的零中断通道**：会话钉住的档案不可变，但**每次工具调用都按当下策略重查**（岗位策略 + 资源授权，`enterprise_capabilities/tools/service.py:479-505`），于是"停用某工具"立即生效、不必等切换。→ **Q-6 已定（2026-10-08）：采纳，远程 MCP 收进平台侧网关**，落地设计见 §10.6。
5. **失败边界粒度化**：MCP 子工具 schema 无法规范化时只丢该子工具、不炸整个 server；而工具**重名**则整体编译失败（fail-closed）。→ ark-forge 的 `ReplaceToolsForMcpServer` 是整 server 替换，可参考这条"局部降级"与"结构性错误整体失败"的分寸。

### 11.3 引以为戒

1. **审计只写不读、无 stale 读模型**：`runtime_profile_audit` 只有写入（`profile/store.py:173-182`），全仓无读取方、无 API 暴露；唯一"哪个会话换了能力"的记录是日志。结果：**用户完全不知道自己的会话跑在旧能力上**。→ 直接支撑 Q-2 在终局必做，以及本方案写面回执的必要性。
2. **单层内容哈希粒度太粗**：hash 覆盖整个档案，改一个凭据也会换会话。→ 支撑 §10.4-③ 的"分面"要求。ark-forge 已把密钥**引用**（而非值）纳入指纹（三条边界见 `backend/internal/manager/capability_fingerprint.go:13-22`，实现见 `:64` 的 `mcpFingerprintParts`），这条优势要保住、别退化。
3. **旧 runtime 不回收**：Python 侧 `gateway.dispose_runtime` 有定义却**零调用者**（`dsh_runtime/gateway.py:106`），而 JS 宿主其实提供了该端点（`runtime-host/src/runtime-http-server.mjs:96`）——即能力端到端具备、调用方漏了；轮换只销毁**会话**（`runtime_coordinator.py:128-134`）。频繁改档案会按 `tenant:<id>:profile:<hash>` 累积 runtime。→ ark-forge 的 `reclaimGeneration`/孤儿回收是对的，保留。
4. **凭据明文且原样回吐前端**：`external_tools.py` 的序列化把 `config` 整体回吐（`:99-104`，`authToken` 就在 `config` 里）。→ ark-forge 的 vault 引用方向正确，不要被带偏。
5. **物化无体检**：内容寻址目录"已存在即直接复用"（`runtime-host/src/skill-bundle-materializer.mjs:40-43`：解包前只校验一次 archive 摘要，落地目录存在就直接 return），目录被外部篡改不会发现。→ ark-forge 的 provenance sidecar + 物化体检是优势，保留。

### 11.4 它没有回答的问题（对本方案最要紧的一条）

**"运行中的底座是否重读技能目录"在 movo 不存在**——它根本不投影 `SKILL.md`：技能正文作为字符串进档案、经内存 provider 下发给 ACP 宿主（frontmatter 校验只出现在 ZIP 安装入口 `services/skill_packages/validator.py:91-105`）。它是用"换 runtime 对象 + 重建会话"**回避**了热重载，而不是解决了它。所以：

- 本方案 §3.2 例外 5 修正后的口径（"投影端到端链属 **skill 功能验收**，不是承诺上限"）**不能靠 movo 的证据升级**——movo 不投影 `SKILL.md`，对这条没有证据；仍需仓库自己做投影端到端实测（§10.7）。而"底座是否会**重读**"（E5）在终局只决定**能否省掉重建**（`docs/capability-ecosystem-design-20260922.md:807`）；
- 反向也成立：movo 的形态说明"内容寻址即投影"是一条**真实可行的替代路线**（目录名 = 摘要，一次性消掉同名冲突/并发写/重复物化），但代价是放弃"底座原生扫描技能目录"这条路径——ark-forge 两个 adapter 明确声明的是后者（`backend/pkg/runtime/adapters.go:60-68`、`:115-135`），不是可以随手换的。

### 11.5 一句话结论

movo 在「能力档案内容寻址 + 轮次边界滚动」上与本方案**同向**，在「可见性 / 审计可消费 / 资源回收 / 凭据保护」上**全面更弱**；它最有价值的单点是**平台侧工具网关 + token 白名单 + 调用期授权重查**——一套设计同时消掉了"逐底座 transport 承载矩阵"和"授权无法即时生效"两个难题。

---

## 12. 附录

### 12.1 决策记录（ADR）


| 编号 | 决策 | 理由 |
|---|---|---|
| ADR-1 | 缺省路径用**变更计数**而不是内容指纹 | 内容指纹含 runtime Spec/版本/投影声明，升级即抖动，破坏 R11 |
| ADR-2 | token 形如 `caprev:<epoch>:<n>`，比较时先看 epoch | 跨重启不可比；重启后会话必由重建路径对齐，正确性不依赖巧合 |
| ADR-3 | epoch 对齐放在 `registerConversation`，判据函数保持只读 | `planGenerationSwitch` 被单测逐条钉住且声明无副作用 |
| ADR-4 | **不做**「立即应用」按钮 | ACP 无活会话换能力通道，它能提前的只有预热；按钮在忙时的行为与自动路径逐字相同 |
| ADR-5 | **不新增**事件 type | 平台级事实在 `events` 表无落点（外键 + `foreign_keys(1)`）；"待更新"是当前态，属读模型 |
| ADR-6 | **不支持**运行期改定义文件；Q-5 之后这是**结构性事实**（文件不是事实源），不再是"约定" | 用户决策 + Q-5；README 与 `capability migrate`/`export/import` 的定位要按"文件=导入导出载体"改写 |
| ADR-7 | 选定路径**不**参与 rev 比较 | 内容寻址更精确，避免无谓迁移 |
| ADR-8 | `probe` 绝不 +1 | Tool 目录是平台读模型，不是 agent 工具面 |
| ADR-9 | 把「进键的 token」与「落库的指纹」拆成两个显式字段（`capabilityGeneration`） | 今天二者共用 `connectConversation` 的一个字符串参数；缺省路径要求它们取不同的值（进键=rev token、落库=`''`），继续共用会把 rev token 写进 `capability_fingerprint` 列，破坏"空 = 未声明能力选择"的对外契约，并让 `reason=generation` 的判据失效 |
| ADR-10 | 新列与事件 payload 字段都叫 `capability_profile`（不用 `capability_rev_token`/`capability_token`） | 按终局命名一次到位，避免二次迁移；值形态从 rev token 演进到 digest 的前提是它**不透出为对外契约**（v1 只内部比较 + 对外给 `capability_stale` 布尔与写面回执） |
| ADR-11 | 能力摘要必须能**分面比较**（工具面 / 技能面 / 凭据与端点面），只有会改变底座可见面的变化才触发滚动 | 避免重复 movo"改一个凭据也换会话"的缺陷（§11.3-②）；ark-forge 已把密钥**值**排除在指纹外（`capability_fingerprint.go:64-84`），这条优势必须保住 |
| ADR-12 | **按"环归属"重切 E 系列，不整体升格**（终局版，§10.7）：**投影端到端链** → 移出 T-1，改挂 skill **功能验收**（唯一要测的）；**E4/E6 → 不设 owner**（网关落地后由平台在环消解，仅 stdio 直连路径留待办）；**E2/E3 → `CapabilityScope` 的触发项**（有人真需 `session` 粒度时）；**E5 → 成本项**（重建成本成为实测问题时） | 终局承诺来自**平台机制**而非底座行为：MCP 面由协议内建的 `mcpServers` 重绑保证（`session/new`/`load`/`resume`），skill 面由**进程生命周期归平台**保证（边界重建进程 = 新进程启动时读投影根）。所以"下一轮生效"要的是**缺省路径也走代际**（工程），不是探针（测量）。终局用「少滚动 + 平台在环」取代「细粒度 + 探测底座」 |
| ADR-13 | **能力的**事实源从文件迁到 DB（Q-5，2026-10-08 定）；文件降级为导入/导出载体 | 开发阶段允许迁移持久数据；C9 的**意图**保留、**机制**换成事务 + 行 revision CAS；顺带白拿版本史/审计/diff，并消掉 `LoadDir` 只合并不摘除的幽灵定义缺陷（§10.5） |
| ADR-14 | **远程 MCP 收进平台侧 stdio 网关**（Q-6，2026-10-08 定）；stdio 代管（S2）**不现在拍，按两条实测分派**（Q-8） | 一个稳定条目让"逐 runtime transport 承载矩阵"从架构上消失，密钥不出平台，工具可见性与授权收窄变成调用期事实（§10.6）；代价是平台开始承担远程连接与重连（C-5 收窄）。S2 的独占收益只有"stdio 密钥不进底座"与"stdio 工具也走调用期策略"两条，而代价是接手 cwd/spawn 粒度/子进程回收三条底座实现细节（见实施计划 P-6） |
| ADR-15 | 对外承诺**按能力种类分别措辞**（MCP：下一轮；skill：待投影实测后升格） | 两类能力的不确定性不同源，不共享同一个承诺口径（§10.7） |
| ADR-16 | **agent / automation 定义与能力定义一起迁 DB**（Q-7，2026-10-08 定） | 不迁会留下"改 MCP 即时生效、改 agent 白名单要重启"的用户可见不一致；顺带把 automation 的**定义**与**平台独占运行态**（`last_fired_at`/`recent_runs`）**结构性**分开（今天只靠拒写名单拦），并处理 agent 的代码内种子与 `default.json`（§10.5-⑧） |

### 12.2 参考资料

- `docs/capability-ecosystem-design-20260922.md`（能力数据面与投影，本方案的直接前序；§797-823 是 E1–E6 实验矩阵）
- `docs/agent-runtime-refactor-design-20260908.md` §4.4 / §13（能力数据面与 §13.4 的启停/审计）
- `.dsh/docs/plans/2026-09-22-capability-ecosystem-plan.md:780`（T-1：E1–E6 未执行；本方案 §10.3 建议拆分）
- `backend/internal/archtest/capability_scope_test.go:9-20`（第 10 条守卫：`CapabilityScope` 的语义与放开路径）
- README「能力的生效边界（M3 / ADR-13）」「MCP 明文 ⟂ 密钥」「物化摘要与审计」三节
- 外部参考：`/Users/morehao/Documents/study/ai-agent/movo`（§11；路径以该仓库根为基准）

### 12.3 ACP 协议能力边界（2026-10-08 核对记录）

> 目的：把"哪些能靠协议、哪些必须自己造"一次说清，免得后人在"是不是我们没用对 API"上反复。规范依据：agentclientprotocol.com `protocol/v1/session-setup`（MCP Servers 一节）；本仓接线依据：`backend/pkg/acl/session.go`、`mcp.go`。

**① 能复用：MCP 集的绑定与重绑是协议内建的。** `mcpServers` 出现在**三个**会话生命周期请求里：`session/new`、`session/load`、`session/resume`。后两者规范明确要求底座"**重新连接到请求里的 MCP servers**"。本仓三条路径都已接线（`pkg/acl/session.go:139`/`:160`/`:182`）。
→ 推论（重要）：**MCP 面的能力重绑不需要新进程**。今天 `ensureConversation` 在判据变化时换连接键、进而换进程（`runtimeConn` = 一个 OS 进程），而协议给了一条更便宜的路：在**同一进程**上 `session/resume` + 新的 `mcpServers`。前提是底座声明 `sessionCapabilities.resume`（本仓已建模：`SessionCapabilities`/`ResumeSession` 返回 `resume_session_unsupported`）。

**② 能复用：所有底座必须支持 stdio，http/sse 是可选的。** 规范原文：All Agents **MUST** support the stdio transport, while HTTP and SSE transports are optional capabilities（靠 `mcpCapabilities.http/sse` 在 initialize 时检查）。
→ 这正是本仓 `Spec.McpTransports` 逐 adapter 声明的依据；也是 §10.6 走 **stdio 网关**的规范背书：把"可选能力"变成"必选能力"。

**③ 能复用：平台自带 MCP server 就是规范预期的扩展方式。** 规范原文：Clients **MAY** use this ability to provide tools directly to the underlying language model by **including their own MCP server**。
→ §10.6 的网关不是绕开 ACP，而是用 ACP 给的唯一扩展点。

**④ 不能复用：没有"会话中途换 MCP 集"的方法。** 除 new/load/resume 之外没有任何 MCP 变更入口（无 `session/set_mcp_servers` 之类）。→ 一轮进行中改能力，协议层没有入口，只能顺延到边界——这是 §4.4「忙则顺延」的**协议根因**，不是策略选择。

**⑤ 不能复用：没有平台侧工具通道、没有"枚举底座当前工具面"的方法。** 工具面靠 MCP 的 `tools/list`（底座自己去取）或事件回放观测。→ §10.6-③ 的"probe 与真实工具面同源"只能靠网关解决。

**⑥ 不能复用：没有任何"让底座重读 skill 目录"的方法。** skill 投影是**底座启动时扫描**的行为，协议不涉及。→ **投影端到端链**的必要性不变（它是 skill 功能的可用性验收）；而"是否**重读**"（E5）在终局只决定**能否省掉重建**（§10.7）。

**⑦ 协议保证的边界要读准**：①里的保证是"**连上请求里的那些**"，**不保证"断掉不在请求里的那些"**。→ 这正是 E6（删掉在用的 MCP 是否立即不可用）仍然要实测的原因；也是 §10.6-③ 网关方案在收窄语义上优于协议直连的原因（平台在环上，自己就能保证断）。

### 12.4 评审检查清单

- [ ] §3.2 的语义承诺是否可对外说（尤其例外 3/4/5）
- [ ] §4.2 的 +1 清单是否同意（probe 不 +1、失败不 +1）
- [ ] ADR-4（不做按钮）、ADR-5（不加事件）、ADR-9（拆 token/指纹）是否同意
- [ ] R11（A10）与四条代际纪律的回归是否作为合入前提
- [ ] §4.3.1 的改动点清单是否被 M2 的实施者逐条认领（这是本方案唯一容易做漏的地方）
- [ ] §10.2 决策表逐行确认（按钮、事件、授权收窄、stale、判据、事实源）
- [x] Q-5（事实源迁 DB）、Q-6（远程 MCP 收进平台侧网关）、**Q-7（agent/automation 一起迁）**——**已定 2026-10-08**
- [x] Q-8（网关是否代管 stdio）——**已给决策规则**：S1 先上，S2 按"是否按会话 spawn + 会话 cwd"两条实测分派（§9）
- [ ] ADR-12（**终局版**）：是否同意"按环归属重切"——投影端到端链移出 T-1 改挂 skill 验收（并立即补测）、E4/E6 不设 owner、E2/E3 归 `CapabilityScope`、E5 归成本项
- [x] M6/M7 已各自单独出计划文档：`.dsh/docs/plans/2026-10-08-capability-store-migration-plan.md`、`.dsh/docs/plans/2026-10-08-mcp-platform-gateway-plan.md`——**已定 2026-10-09：不移进 `docs/`**。理由是两类文档的生命周期不同：`docs/` 放的是**历史设计记录**（写成即冻结，说清"当时为什么这样定"），而这两份是**执行计划**（勾选框即进度、随开工不断改写，未开工的条目还会整段重写）。把它们搬进 `docs/` 会有两种坏结果——要么"历史记录"这个语义被可改写的勾选框污染，要么每次推进一步都要去改一份本该冻结的文档。留在 `.dsh/docs/plans/` 既保持可改写、又不破坏 `docs/` 的语义；代价是它们**不入库**（`.dsh/` 在 `.gitignore`），跨机器交接需连同 `.dsh/` 一起带
