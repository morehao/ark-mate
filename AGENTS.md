# AGENTS.md — ark-forge

ACP 兼容 runtime（opencode / deepseekharness / echo）的统一控制面平台。前后端平级 monorepo。代码注释与 commit message 用简体中文；commit 用 conventional 前缀（`feat`/`fix`/`refactor` + 中文 scope）。

## 布局与目录边界

- `backend/`：唯一 Go module（`github.com/morehao/ark-forge`，go 1.26.1）。**仓库根没有 go.mod / go.work——所有 go 命令必须在 `backend/` 下运行**。
  - **`pkg/` 与 `internal/` 的分界**（对齐 ark-iam）：可复用基础设施进 `pkg/`，应用专属逻辑留 `internal/`。`pkg/` 内部**不得 import `internal/`**（由 `internal/archtest` 守卫）。
    - `pkg/`：`code`（对外业务错误码唯一事实源：常量 + 注册表 + `FromError` 归一）、`acl`（ACP 客户端）、`runtime/{opencode,deepseekharness,echo}`（adapter + 注册表）、`mcp`（自研 MCP 客户端）、`capability`、`routing`、`config`（进程配置）、`procreg`（进程注册表）、`web`（release 内嵌前端宿主）、`dbclient`（gorm 句柄装配 + 自注册纯 Go SQLite dialector）、`object/objusage`（跨层共享值对象）。
    - `internal/`：`app`（装配 + 启停生命周期）、`app/command`（CLI 子命令）、`router`（唯一路由装配点，按领域分 `routes_*.go`）、`controller/ctr<领域>`（传输层控制器）、`middleware`、`dto/dto<领域>`（请求/响应 DTO）、`service/svc<领域>`（领域端口）、`manager`（领域实现：Service / Orchestrator / 事件 / 权限；**按领域分文件，不设 god file**——`service.go`/`orchestrator.go` 只留类型、构造与装配，会话动作在 `conversations.go`、轮次在 `turn.go`、连接在 `connection.go`、进程回收在 `orphan_reap.go`、事件出口在 `event_sink.go`、权限/提问在 `interaction.go`/`permission_flow.go`）、`model`（持久层实体 + 列/取值约定）、`dao`（单表数据访问，基于 golib gormdao）、`store`（持久层门面：句柄 + 跨表事务 + 读模型 API）、`eventbus`、`testutil`（跨包复用的测试夹具；**不得 import `manager`**——manager 的测试是内部测试包，反向依赖会形成 `import cycle not allowed in test`，manager 专属组合留在其 `testrig_test.go`，由 `archtest` 守卫）、`archtest`（架构守卫测试）。
  - **依赖方向单一**：
    - 应用侧：`cmd → app → router → controller → service(端口) ← manager(实现)`；`dto` 被两侧共用但自身不依赖应用层。
    - 持久侧：`dbclient → dao → store`，`model` 被三者依赖且自身不依赖任何一层；`dao` 不得 import `store`（会成环），`model` 不得 import `dao`/`store`。
  - 以上六条边界都由 `internal/archtest` 的守卫测试强制（`go test ./internal/archtest/`）；同目录还另有**三条**契约/出口守卫：事件 type 冻结快照（`TestEventTypeContractFrozen`）、前端事件同步边（`TestFrontendEventTypesMatchRegistry`）、控制器错误出口带码（`TestControllerErrorExitsCarryCode`）。**共 9 条**——加守卫时记得同步这句话。
- `frontend/`：pnpm workspace（`apps/panel` 控制台 + `packages/{ui,types,api,tsconfig}`）。Node 看 `frontend/.nvmrc`（24.x，团队用 fnm）、pnpm 看 `frontend/package.json` 的 `packageManager`；`engines` + Makefile 守卫 node 主版本不符即失败。
- `docs/` 设计文档是**历史记录**（会话早期叫 sessions，P0 起为 conversations）；对外契约以 README「控制面接口摘要」与 `backend/internal/router/router.go` 为准。

## 错误码（唯一事实源 `backend/pkg/code`）

- 信封 `code` 是**对外契约**，三处必须同步：`pkg/code`（事实源：常量 + 默认文案）、README「业务错误码」表（文档）、前端 `packages/types/src/response.ts` 的 `BizCode`（消费方）。三条边都由守卫钉住——`TestReadmeCodesMatchRegistry`（pkg/code ↔ README）、`TestFrontendBizCodesMatchRegistry`（pkg/code ↔ 前端，**用 Go 读 TS 源码**，不引前端测试框架，忘记同步前端同样红）。新增一个码 = 加常量 + 默认文案 + README 行 + 前端常量。
- 编号沿用既有 5 位 HTTP 语义码（`40000/40001/40101/40401/40501/40901/50000/50001/50201`），**不按 ark-iam 的 6 位领域段重编**——它们已经是对外契约。ark-iam 借的是结构（`code.go` 注册表 + 按领域分文件），不是编号。
- `50000` 是**唯一兜底码**：任何未归类 error 经 `code.FromError` 出口都落成它。历史上有 `1`（控制器手写）与 `50000`（领域层）两个泛化码，已统一为 `50000`。
- **控制器错误出口一律带码**：`gincontext.Fail(c, code.FromError(err))` 或 `code.Error(code.X, …)`，不得透传裸 `err`——gincontext 对不带 `gerror.Error` 的错误填 `code: -1`，客户端拿不到可判定的码。由 `TestControllerErrorExitsCarryCode` 静态守卫。
- acl 分类 → 码的映射表在 `pkg/code/acl.go`（不在 `pkg/acl`：acl 是可复用的 ACP 客户端，不该知道 ark-forge 的信封编号）。`agent_not_found`/`conversation_not_found`/`runtime_not_found`/`mcp_not_found`/`provider_not_found` 从 `ClassRequest` 提档到 `40401`。

## 常用命令（仓库根）

- `make dev` 前后端联调（go run serve → 等 :10001 health → vite :5173，Ctrl-C 一起收）；`make server` 仅后端；`make web` 只构建前端并刷新 embed 宿主；`make all` 出单二进制 `./forge`（先 web 再 `go build -tags release`）。
- `make build/vet/test` = `cd backend && go build|vet|test ./...`。无独立 lint。另有 **`make test/release`**（`go test -tags release ./...`，依赖 `web` 目标先出前端产物）：**带 release tag 的代码只有这条能覆盖**，默认 `go test` 根本编译不到它们（历史上 `NoRoute` 的非 API 行为就是这么长期没人验的）。
- 容器：`make image`（默认最小镜像）/ `make image/runtimes WITH_DSH=true WITH_OPENCODE=true`（内置底座）/ `IMAGE_TAG=runtimes make image/run`；开关只认 `true|false` 且必须与 `--target` 配套（最小 target 传开会构建失败，不静默忽略）。基础镜像与仓库地址可用变量覆盖（`NODE_IMAGE/GO_IMAGE/RUNTIME_IMAGE/GO_PROXY/NPM_REGISTRY`），细节见 `docs/deployment.md` §4。
- 单包 / 单测试：`cd backend && go test ./internal/manager -run TestName -v`。全量 `go test ./...` 约 2 分钟、**不依赖外部 runtime**（echo 底座在进程内驱动）。
- 前端：`cd frontend && pnpm dev / typecheck / build`。

## 构建形态

- embed 仅随 `-tags release` 编译（`backend/pkg/web/web.go` 带 `//go:build release`）；默认构建不依赖前端产物。`backend/pkg/web/dist/` 不入库（由 `make web` 从 `apps/panel` 产物拷贝刷新）。
- `forge` CLI 无默认子命令：`serve / demo / migrate / transcripts prune / config / acp-serve / acp-demo`。`forge demo` 需本机装 opencode/dsh（echo runtime 除外）。

## session 与 conversation：两个名字，一个东西（外加一个同名不同物）

平台聚合的概念从 P0 起叫 **conversation**，但**对外 wire 上它叫 `session`**——这是冻结的历史词汇，不是待修的笔误：

- **wire 用 `session`**：事件 type 16/19 条是 `session.*`（`session.created`/`session.message`/`session.permission_resolved`…）、6 个 JSON 字段是 `session_id`、transcript 落盘 header 也是 `session_id`。前端 `@ark/types` 的 `events.ts` 与 `conversation.ts`、磁盘上既有的 transcript、`forge migrate` 全都依赖它。
- **平台领域词用 `conversation`**：REST 资源路径 `/conversations/:id`、Go 类型 `Conversation`/`ActiveConversation`、`conversation_start.go`。
- **`Session` 还另指一样东西**：**runtime 侧的 ACP 会话**。`ProviderSessionID`（原生底座会话 id）是它，`LoadSession`/`SessionCapabilities`/`SessionOptions`/`SessionInfo`/`NewSession`/`ResumeSession`/`SessionsPersistent`/`MultiSessionConcurrent` 也都是它——**这批不能改名**，改了就是把两个概念混成一个。注意 `sessions_persistent`/`multi_session_concurrent` 还是 health/diagnostics 的 wire key。

**最容易踩的一处**：DTO / 前端里的 `SessionID` 指的是**平台会话 id**（= conversation id），而 `ProviderSessionID` 才是 runtime 的。两者在 `ConversationResp` 上并存（`service.go`）。

**决策**：`session` 作为对外词汇**冻结**，与 ACP 自身协议一致（`NewSession`、`Acp-Session-Id` 响应头），改名是破坏性变更且零功能收益。只在**纯 Go 内部名**上保持「操作 `ActiveConversation` 的函数/文件就叫 conversation」（如 `applyModelOnConversation`、`conversation_start.go`）。
事件契约的三条边由 `TestEventTypeContractFrozen`（常量 ↔ 冻结清单）与 `TestFrontendEventTypesMatchRegistry`（清单 ↔ 前端 `Ev`）共同钉住；错误码同理见下一节。

## 一致性守卫 / 测试约定

- `TestReadmeRoutesMatchRouter`（`internal/router/router_test.go`）把 README 反引号内的 `/v1/forge` 路径与真实注册路由做**双向**比对：README 引用的路径必须注册（抓改名/删除），每条注册路由也必须在 README 出现（抓新增）。所以**改路由与改文档必须同一次提交**——新增端点不写文档同样红。因此没有豁免名单：**兼容别名也是端点，也要成行写进路由表**（`/permissions/always` 两个别名就是这么被补进文档的）。README 里也不得再出现历史名 `/sessions`。
- 域事件 type 权威定义在 `backend/internal/manager/events.go`。
- **连接拆除语义（`manager`，改动前先读 `disconnectConversation` 注释）**：连接断开只是连接事实，绝不等于轮次失败。主动拆除（`Close` 停机 / `stopAgent` / 空闲回收）在途轮次归 `interrupted`（可续聊重发）、空闲会话状态**原样保留**；只有对端或子进程意外断开才可能把在途轮次收成失败，空闲会话同样不被改写（只落 agent 诊断 `connection_lost`）。拆除原因由 `runtimeConn.intentional` 承载（`watchConn` 观察到对端结束 = 意外），断连时句柄摘除但会话记录不动——下次 prompt 经 `ensureConversation` 按 native resume / handoff memo 重建（ADR-4：执行可丢弃、会话平台拥有），**可续聊性只看会话是否存在**，上一轮的终态不是作废判据。
- WS / REST 契约改动优先看 `internal/router/routes_*.go` 与 `internal/controller/ctr*`，测试多为走真实装配路径
  （`manager.NewService` + `router.New`）的进程内测试；`TestReadmeRoutesMatchRouter` 走的就是真实引擎。

## 前端风格约束（frontend/DESIGN.md）

- 颜色/圆角/间距唯一事实源 = `packages/ui/src/theme.ts` 的 `tokens`；改设计值须同步 DESIGN.md front matter（两处人工一致），业务代码禁止硬编码色值。
- 交互态 CSS 唯一收口在 `apps/panel/src/console.css.ts`，不新增 css 文件；聊天正文样式由 `Transcript.tsx` 自绘，勿搬进 antd。

## 变更授权

- **允许破坏性调整**：删除/重命名既有对外形态（兼容别名如 `/permissions/always`、历史路径）、改造本地持久数据（SQLite schema、transcript 格式、agents definitions）与大范围重构（包边界、装配路径）均被允许，**不要求保留过渡性兼容层**。
- 例外（仍强制）：改路由须同步 README 接口摘要（`TestReadmeRoutesMatchRouter` 守卫保持绿）；改事件 type / 数据 schema 须同步 `manager/events.go`、`internal/model` 实体与 `internal/store` 迁移内核（三处手工一致）；改动后测试与前端 typecheck 保持通过。

## 运行配置要点

- 默认仅回环 `127.0.0.1:10001`，无鉴权（对外需先设 `FORGE_TOKEN`，WS 经 `?token=` 传）。部署见 `docs/deployment.md`。
- 持久层实现：gorm（`pkg/dbclient` 装配 + `internal/model` 实体 + `internal/dao` 访问）。**SQLite 用自注册的纯 Go dialector**（`glebarez/sqlite`）——不要 import golib 的 `dbgorm/driver/sqlite`，它底层是 CGO 的 mattn/go-sqlite3，与本仓 `CGO_ENABLED=0` 单二进制构建冲突；且 `dbgorm.matchDialector` 遍历注册表 map，两个都能匹配 `sqlite://` 的 factory 会让匹配结果不确定。
- **落库格式是硬契约**（`internal/store/format_test.go` 锁定）：时间列一律 TEXT + RFC3339Nano(UTC)，布尔列一律 INTEGER 0/1。两者都由 `internal/model` 的 serializer 保证——实体照常写 `time.Time`/`bool`，但**裸 SQL 的入参不经过 serializer**，时间要先用 `model.FormatTime` 格式化、布尔要用 `model.FlagValue` 转换。schema 仍由 `internal/store` 的自管迁移内核创建（不用 gorm AutoMigrate：它表达不了有序版本、版本拒绝启动与导入 staging 回滚）。
- 持久层 `FORGE_STORE=sqlite|memory|auto`：make dev/server 默认 sqlite（库 `~/.forge/forge.db`）；sqlite 才有 runs 读模型与 `/usage/summary`（memory → 40501）。memory 模式以 transcript（`~/.forge/transcripts`）为事实源，`forge migrate` 幂等导入 sqlite。
- 配置优先级：默认值 < `~/.forge/config/config.json` < `~/.forge/config/env` < 进程 `FORGE_*` < CLI flag；非法键名与非法取值启动即报错（枚举型键如 `native_permission`/`mcp_auto_probe`/`store` 都按 `Valid*Modes` 校验并报错，提示里列出合法取值）。**没有例外**：`FORGE_STORE` 的非法值同样报错，只有**空/未设**才当 auto（无偏好）——`auto` 本身也是合法取值，含义同未设。`forge config --json` 可查最终值与来源。

  这条早先是反的（`FORGE_STORE` 非法值静默回落 auto）。改掉的直接理由是**用户无从察觉**：回落后的 `forge config` 输出与合法的 `auto` 逐字相同；而 auto 会按"DB 文件在不在"解析成 sqlite 或 memory，一个错字（`sqlit`）就可能把人悄悄丢进 memory，表现为"数据丢了"。且查历史，`FORGE_STORE` 从引入那版起的取值集就是 `sqlite|memory|auto`，**从未有过今天已非法的取值**，严格化不淘汰任何曾经可用的配置。
