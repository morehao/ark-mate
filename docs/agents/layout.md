# 布局与目录边界

> 从仓库根 `AGENTS.md` 拆出（2026-10-18）——本文件是分册后的唯一事实源；`AGENTS.md` 只保留入口与硬约束。
> 分层的机器判据在 `backend/internal/archtest`（清单见 [`guards.md`](guards.md)）。

- `backend/`：唯一 Go module（`github.com/morehao/ark-mate`，go 1.26.1）。**仓库根没有 go.mod / go.work——所有 go 命令必须在 `backend/` 下运行**。
  - **`pkg/` 与 `internal/` 的分界**（对齐 ark-iam）：可复用基础设施进 `pkg/`，应用专属逻辑留 `internal/`。`pkg/` 内部**不得 import `internal/`**（由 `internal/archtest` 守卫）。
    - `pkg/`：`code`（对外业务错误码唯一事实源：常量 + 注册表 + `FromError` 归一）、`acl`（ACP 客户端）、`runtime/{dsh}`（adapter + 注册表，另有 `runtime` 根包的 Spec/投影/指纹/三张**端口**表：`Syncer`/`NativePermissionAdapter`/`ModelValueAdapter`）、`mcp`（自研 MCP 客户端）、`capability`、`config`（进程配置）、`procreg`（进程注册表）、`dbclient`（gorm 句柄装配 + SQLite URL 拼装）、`logging`（第三方 slog 边界桥）、`credentials`（v20 凭据分层：`继承环境 > 托管文档 > `<cwd>/.env` > `<home>/.env`` 的**唯一**实现 + 严格 `引用 → 值` 的托管文档 `<MATE_HOME>/credentials.json`，0600 且权限不合即失败）、`object/objusage`（跨层共享值对象）、`object/objid`（**平台铸造 id 与底座 id 包装的唯一实现**：`ses_`/`msg_`/`run_`/`inp_`/`wsp_`/`mdl_`/`pem_`/`evt_` + 26 位 ULID，进程内单调、字典序 = 铸造序；底座原文一律加 `rt_` 壳，包装/解包严格不静默容错）。
    - `internal/`（v3.1 三层 + 单装配根，见 `.dsh/docs/specs/2026-10-17-backend-architecture-v3.1.md`）：
      - `assembly`（**唯一装配根**：构造 store / 领域平面 / 执行面 / 用例 / 控制器，再交给 `router`；执行 `registry.Sequence` 启停）、`app`（**应用壳**：HTTP 启停 / ready / 信号 / 关闭）、`cli`（**命令树**：子命令名、帮助、参数分发）、`cliops`（命令实现：doctor / migrate / capability / provider / mcp / demo / config / transcripts / serve）。
      - `router`（gin engine + 路由表 + SPA/WS/CSP，只收 `router.Deps.Controllers`）、`controller/ctr<领域>`（**只收 service 接口 + DTO + gin**，不 import domain/store）、`middleware`、`dto/dto<领域>`（请求/响应 DTO）。
      - `service/svc<领域>`（**用例层**：包内声明接口（`XxxSvc`）+ 实现 + `NewXxx(deps...)`；实例**只在 `assembly` 造**）、`domain/<领域>`（纯领域：不变量/状态机）、`mechanism`（跨域机制）、`sessionruntime`（会话编排层）、`registry`（装配层：Planes + 启停表 + 资源同步）、`runtimeconn`（执行面）、`gateway`、`eventbus`、`credential`。
      - `model`（持久层实体：**一表一文件**，17 张表 17 个文件；结构体 `<域词>Entity` / `<域词>EntityList`、表名常量 `TableName<域词>` 随实体文件走；`enum.go`/`serializer.go`/`time.go` 只放列与取值约定，由 `TestModelEntitiesFollowNamingConvention` 钉住）、`dao`（单表 gormdao）、`store`（跨表事务 + 读模型）、`testutil`（跨包测试夹具；**不得 import `sessionruntime`**）、`archtest`（架构守卫）。
      - 依赖方向由 `internal/archtest/layers_v31_test.go` 的**层表**统一钉住（层号从低到高：object/version → model/eventbus/infra → code/credential → runtime → infra/dsh → config → dto/dao → store → mechanism → domain → runtimeconn/registry/service/sessionruntime → middleware/gateway → controller → router → assembly → app → cliops → cli）。

  - **依赖方向单一**：
    - 应用侧：`cmd → app → router → controller → service → dao/store`；`dto` 被 **controller 与 service 共用**（跨这两条边传的就是 dto，service 也收 dto、回 dto），`dto` 自身不依赖应用层。
    - **装配只能自上而下，不许反向**：`assembly` 构造用例与控制器，`router` 只收已构造的控制器（`router.Deps`），`app` 只消费 `assembly.BuildResult`。**`app`/`router`/`cli` 都不得引用 `service/svc*`**，控制器也不得 import `domain`/`store`/`model`——判据见 `layers_v31_test.go`。
    - 持久侧：`dbclient → dao → store`，`model` 被三者依赖且自身不依赖任何一层；`dao` 不得 import `store`（会成环），`model` 不得 import `dao`/`store`。
  - **用例层（`service/svc<领域>`）的三条硬规矩**（2026-10-16 定案；违反者由 `internal/archtest` 拦，别靠注释）：

    1. **只有 `internal/controller/ctr<领域>` 可以引用 `internal/service/svc<领域>`**——「引用」包括调用、构造与**写类型名**。
       `app` / `router` / `sessionruntime` / `domain` / `mechanism` / `dao` / `store` / `pkg` 一律不许出现 `svc*` 类型。
       连带的装配口径：**`app` 不引用 controller**（`cmd → app → router → controller` 是有向的），
       `router` 拿到的只是依赖事实，controller 在自己的构造函数里把 service 造出来。
    2. **`service/svc*` 之间零互相 import、零互调用**（同层的另外半边由既有守卫钉）。需要别的域的事实时，
       在自己包里声明一个**窄接口**（如 `automation.Host`），由接线点（controller / router）把实现接上；
       需要共享的纯值或判决下沉到 `domain/*` / `pkg/*`。
    3. **不许别名与转发骚操作**：不写 `type XxxSvc = svcxxx.XxxSvc` 这类「端口别名」，
       也不要「只做一行转发」的适配器层。controller 直接持有 **service 包内声明的接口**，一个类型只有一处定义。
        （2026-10-16 的 P4a 一批曾用别名把 8 个端口挂在 `router.Ports` 上，事后被判定为骚操作，已删。）

  - **`service/svc<领域>` 的形状**（新写一个域时照抄）：接口（`XxxSvc`，消费方只认它）+ 实现（导出结构体或私有结构体）
    + 构造函数 `NewXxx(deps...) XxxSvc`；校验、错误码、响应装配、渲染都在这一层；controller 里只做
    参数绑定、简单校验、调 service、回响应（**任何业务判断都不许写在 controller**）。

- `frontend/`：pnpm workspace（`apps/web` = **入口 + 组装**（`main.tsx` 挂载 + vite 构建配置）；`packages/{console,ui,types,api,desktop-contract,tsconfig}`——**界面本身在 `packages/console`**，2026-10-11 P5 从 `apps/web/src` 搬过来，行为零变化：搬迁保留原目录结构，所以 `internal/archtest` 里禁 `packages/*` 反向引 `apps/*` 的那条从"挡路的墙"变成了"搬迁方向的保证"）。Node 看 `frontend/.nvmrc`（24.x，团队用 fnm）、pnpm 看 `frontend/package.json` 的 `packageManager`；`engines` + Makefile 守卫 node 主版本不符即失败。
- `docs/` 是**当前有效的对外/产品面文档**（2026-10-16 重建：`positioning` / `decisions` / `architecture` / `roadmap` / `unimplemented` / `acceptance-matrix` / `dsh-acp-adaptation` / `deployment`；2026-10-18 起另有 `agents/`——仓库根 `AGENTS.md` 的分册，见 [`docs/agents/README.md`](README.md)）。旧设计文档中仍有效的内容已抽出，历史整体留在 git 里；**外部产品的对照与逆向分析不进 `docs/`**，一律落 `.dsh/docs/`。`docs/**` 的相互引用由 `TestDocsReferencesResolve` 守卫（指向不存在的文件即红）。对外契约以 README「控制面接口摘要」与 `backend/internal/router/router.go` 为准。
- `docs/assets/`：文档的**静态资源唯一收口**（图片/矢量图/后续其他静态文件），不散落在 `docs/` 根下；文档内用相对路径引用（`![alt](assets/xxx.svg)`）。
