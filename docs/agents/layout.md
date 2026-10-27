# 布局与目录边界

> 从仓库根 `AGENTS.md` 拆出（2026-10-18）——本文件是分册后的唯一事实源；`AGENTS.md` 只保留入口与硬约束。
> 分层的机器判据在 `backend/internal/archtest`（清单见 [`guards.md`](guards.md)）。

- `backend/`：唯一 Go module（`github.com/morehao/ark-mate`，go 1.26.1）。**仓库根没有 go.mod / go.work——所有 go 命令必须在 `backend/` 下运行**。
  - **`backend/pkg` 已全量并入 `internal/`**（v3.1 §3.1.12，P2）：仓库只有一个代码层，不再有"可复用基础设施 vs 应用专属"的目录分界。原先在 `pkg/` 的包按职责落到 `internal/` 下：
    - `internal/infra/protocol/{acp,mcp}` + `internal/infra/{logging,credentials,dbclient,procreg,dsh,host,installed}`：协议客户端与运行期基础设施。`infra/dsh` 是**唯一 adapter**（实现 `runtime.Adapter`：`Spec()` + `RegisterPorts()`，五张端口表：syncer / 原生权限 / 模型值 / 工具失败归类 / 产品身份）。
    - `internal/runtime`：注册表 + `Spec` 契约 + `Adapter` 接口 + 隔离投影。**不得 import 任何 adapter**（`TestRuntimeDoesNotImportRuntimeAdapters`）。
    - `internal/core/{code,capability,automation,identity,resource,redact,object/*}`：错误码、能力装载、自动化解析、身份常量、跨层值对象等；`internal/config` 仍是独立的进程配置包。
    - 新增/搬迁包时只问一句：**它在层表的哪一层**（见下），不再问"进 pkg 还是 internal"。
  - **依赖方向单一**：
    - 层带（L0–L9 终局表，见 [`../architecture.md`](../architecture.md) 的 §4.1）：`persistence/model` → 值/协议 → `domain` → `dto` → `service` → `persistence/{store,dao}` + `infra` → 机制与注册表 → `sessionruntime` → `controller` → 装配与交付。**只能向内 import**；`dto` 被 controller / service / CLI / WS 共用（同一份对外契约），`domain` 在它的里侧、不认识它。
    - **装配只能自上而下，不许反向**：`assembly` 构造用例与控制器，`router` 只收已构造的控制器（`router.Deps`），`app` 只消费 `assembly.BuildResult`。**`app`/`router`/`cli` 都不得引用 `service/svc*`**，控制器也不得 import `domain`/`store`/`model`——判据见 `layers_v32_test.go`。
    - **入站面是三个并列顶层目录，不是一条带**（决策 v33，2026-10-22；本条回退 2026-10-20 S3 把三者收进 `delivery/` 的那一步）：
      `controller`（L8，参数绑定 + 简单校验 + 调 svc + 回响应，13 个 `ctr<领域>` 包）· `middleware`（L8，入站横切：凭据鉴权 / Host-Origin 判定）·
      `router`（L9，唯一路由装配点 + 前端资产交付 `web*`）。三者各自一个层带（8 / 8 / 9）、各一行角色卡，`delivery/` 这个概念已不存在。
      问"HTTP 相关放哪"按准入回答：新端点 → `router/routes_<领域>.go` 挂路由 + `controller/ctr<领域>` 收参数；横切拦截 → `middleware`；
      要引 `service/svc<领域>` 的只有 `controller`。**三个目录都不放业务判断**（判断进 `service/svc<领域>`），准入与判定同 v30。
    - 持久侧：`dbclient → persistence/dao → persistence/store`，`model` 被三者依赖且自身不依赖任何一层；`dao` 不得 import `store`（会成环），`model` 不得 import `dao`/`store`。
    - **厚域与用例都走窄端口，不 import 门面**（P4）：`domain/session` 声明 `Store` 端口（`store_port.go`），`domain/interaction` 零 `store`；用例层同理声明 `svcmodel.ModelStore` / `svcsession.EventReader` / `svcsystem.SourceFactsReader`。`*store.Store` 结构上满足它们、由装配根交下来；**交下来时必须现判 nil**——typed-nil 装进接口不是 nil，会让"内存模式 40501 / 只读镜像"那几条判据静默失效（`assembly/services.go` 的 `eventReader`/`sourceFactsReader`/`modelStore` 就是这条规矩的落点）。`model` 仍可被 `domain/session` 引用：它是纯列形状（无行为），把它也搬走只会换来 30 多个镜像结构。
    外部协议同样走端口（A2，2026-10-22）：`domain/mcp` 声明 `LiveClient`（探测/工具调用/资源/提示词，签名只用 `protocol.McpServer` + `protocol.Mcp*` 平台值），实现体是 `infra/protocol/mcp`，由 `drivers/registry` 接上——域因此**零 `infra/*`**（`TestInnerCircleAvoidsInfraProtocol` 已是硬判据）。网关条目的 argv 形状（A1）同批进了 `core/protocol/gateway.go`。**这两处的留用理由、代价与推翻条件登记在决策 v31**（`../decisions.md`），本节只留做法。
    `infra/protocol/{acp,mcp}` 里的类型别名（`aliases.go` / `values.go` / `server.go` 的网关转发块）只是**协议包内部的读法**：事实源在 `core/protocol`，别名不多一份定义、不多一行行为。**协议包以外一律用 `core/protocol`**——内圈（`domain` / `sessionruntime` / `runtime`）用了等于重新认识协议实现（G2 会红），装配面/驱动面/测试用了则是把整条协议实现拖进一条只需要值类型的边（G6 `TestProtocolAliasesStayInProtocolPackages` 会红）。为什么保留而不是删、什么条件下该整块删，见决策 v32（`../decisions.md`）。
  - **新增一层 / 一个接口 / 一个抽象之前，先回答五问**（2026-10-21，来源是一份外部"轻量化"方案里唯一被完整采纳的判据段）：
    ① 它是否有独立、稳定的职责？② 是否有多个真实实现，或确实需要替换实现？③ 是否能隔离外部技术依赖？
    ④ 是否有明确的测试、事务、生命周期或安全边界？⑤ 去掉它后，是否会导致调用方直接耦合不希望暴露的细节？
    大多答"否" ⇒ **不加这一层**。反过来也成立：一个模块已经有复杂状态转换、多条用例、明确事务边界或
    多个 Runtime 实现，**多几个文件或子目录不算架构过重**——这条防的是把五问读成"越少越好"。
    顶层目录本身的角色另有一张卡（`core` / `domain` / `infra` 各自的准入与禁止）与四条机器判据，
    见决策 v30 与 `guards.md` 的「顶层目录角色」段。
  - **Runtime 侧的职责归属（回归检查项，不是新判据）**：改 `sessionruntime` / `drivers/runtimeconn` /
    `infra/protocol` / `infra/dsh` 时对照下表，确认每件事仍只有一个归属——
    | 职责 | 唯一归属 | 证据锚点 |
    | --- | --- | --- |
    | 长命底座的子进程 spawn | `infra/protocol/acp` 的 stdio transport | `infra/protocol/acp/transport.go` 的 `exec.CommandContext` / `cmd.Start()` |
    | 一次性子进程（config 探测，须在 staging home 上跑） | `infra/dsh` | `infra/dsh/config.go` 的 `exec.CommandContext` + 隔离 env 注释 |
    | 宿主短命进程（选目录面板） | `infra/host` | `infra/host/runner.go` 的 `Run` |
    | 连接生命周期（拨号 / 断连 / 空闲回收 / 意外断开判定） | `drivers/runtimeconn` | `runtimeconn/dial.go` 的 `Pool.Dial` + `watchConn` |
    | 协议事件 → 平台事件 | `drivers/runtimeconn` | 内圈对 `acp` 的依赖只能经它翻译（决策 v30 的 G2） |
    | 底座差异（启动参数 / 能力 / 注入 / 产品身份） | `runtime.Adapter` 端口 | 五张端口表：syncer / 原生权限 / 模型值 / 工具失败归类 / 产品身份 |
    | 会话编排（何时重建、何时收成 interrupted） | `sessionruntime`（L7） | `guards.md` 的「连接拆除语义」段 + `disconnectSession` |
    判据清单的唯一事实源仍是 [`guards.md`](guards.md)；本表只回答"这件事归谁"，不新增守卫。
  - **用例层（`service/svc<领域>`）的三条硬规矩**（2026-10-16 定案；违反者由 `internal/archtest` 拦，别靠注释）：

    1. **只有 `internal/controller/ctr<领域>` 可以引用 `internal/service/svc<领域>`**——「引用」包括调用、构造与**写类型名**。
       `app` / `router` / `sessionruntime` / `domain` / `mechanism` / `dao` / `store` / `pkg` 一律不许出现 `svc*` 类型。
       连带的装配口径：**`app` 不引用 controller**（`cmd → app → router → controller` 是有向的），
       `router` 拿到的只是依赖事实；**用例实例只在装配根（`internal/bootstrap/assembly`）造**，controller 只收接口。
    2. **`service/svc*` 之间零互相 import、零互调用**（同层的另外半边由既有守卫钉）。需要别的域的事实时，
       在自己包里声明一个**窄接口**（如 `automation.Host`），由接线点（controller / router）把实现接上；
       需要共享的纯值或判决下沉到 `domain/*` / `internal/core/object/*`。
    3. **不许别名与转发骚操作**：不写 `type XxxSvc = svcxxx.XxxSvc` 这类「端口别名」，
       也不要「只做一行转发」的适配器层。controller 直接持有 **service 包内声明的接口**，一个类型只有一处定义。
        （2026-10-16 的 P4a 一批曾用别名把 8 个端口挂在 `router.Ports` 上，事后被判定为骚操作，已删。）

  - **`service/svc<领域>` 的形状**（新写一个域时照抄）：接口（`XxxSvc`，消费方只认它）+ 实现（导出结构体或私有结构体）
    + 构造函数 `NewXxx(deps...) XxxSvc`；校验、错误码、响应装配、渲染都在这一层；controller 里只做
    参数绑定、简单校验、调 service、回响应（**任何业务判断都不许写在 controller**）。
    往下游传 ctx 时 controller 写 `c.Request.Context()`、service 签名写 `ctx context.Context`——Gin 的 `*gin.Context`
    是池化对象，不能跨出入站带；口径与依据见 [`context.md`](context.md)。
  - **2026-10-20：provider 侧并入 `svcmodel` / `ctrmodel` / `dtomodel`**——
    provider 与 model 同属 `domain/provider` 一个域（`models.provider_id` 是外键式的挂靠）。
    **包名取 `model` 系**（用户面主名词是「设置 · 模型」），域仍是 `domain/provider`；注意 `internal/persistence/model`
    是数据库实体包，不要和 `dtomodel`/`svcmodel`/`ctrmodel` 混为一谈。合并的是**目录**，不是接口：
    `ProviderSvc` / `Prober` / `DeclaredSvc` / `ModelSvc` / `Host` / `ModelStore` 仍按**面**切开，
    一个控制器只认自己那片。

- `frontend/`：pnpm workspace（`apps/web` = **入口 + 组装**（`main.tsx` 挂载 + vite 构建配置）；`packages/{console,ui,types,api,desktop-contract,tsconfig}`——**界面本身在 `packages/console`**，2026-10-11 P5 从 `apps/web/src` 搬过来，行为零变化：搬迁保留原目录结构，所以 `internal/archtest` 里禁 `packages/*` 反向引 `apps/*` 的那条从"挡路的墙"变成了"搬迁方向的保证"）。Node 看 `frontend/.nvmrc`（24.x，团队用 fnm）、pnpm 看 `frontend/package.json` 的 `packageManager`；`engines` + Makefile 守卫 node 主版本不符即失败。
- `docs/` 是**当前有效的对外/产品面文档**（2026-10-16 重建：`positioning` / `decisions` / `architecture` / `roadmap` / `unimplemented` / `acceptance-matrix` / `dsh-acp-adaptation` / `deployment`；2026-10-18 起另有 `agents/`——仓库根 `AGENTS.md` 的分册，见 [`docs/agents/README.md`](README.md)）。旧设计文档中仍有效的内容已抽出，历史整体留在 git 里；**外部产品的对照与逆向分析不进 `docs/`**，一律落 `.dsh/docs/`。`docs/**` 的相互引用由 `TestDocsReferencesResolve` 守卫（指向不存在的文件即红）。对外契约以 README「控制面接口摘要」与 `backend/internal/router/router.go` 为准。
- `docs/assets/`：文档的**静态资源唯一收口**（图片/矢量图/后续其他静态文件），不散落在 `docs/` 根下；文档内用相对路径引用（`![alt](assets/xxx.svg)`）。
