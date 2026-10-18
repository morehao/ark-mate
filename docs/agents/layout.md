# 布局与目录边界

> 从仓库根 `AGENTS.md` 拆出（2026-10-18）——本文件是分册后的唯一事实源；`AGENTS.md` 只保留入口与硬约束。
> 分层的机器判据在 `backend/internal/archtest`（清单见 [`guards.md`](guards.md)）。

- `backend/`：唯一 Go module（`github.com/morehao/ark-mate`，go 1.26.1）。**仓库根没有 go.mod / go.work——所有 go 命令必须在 `backend/` 下运行**。
  - **`backend/pkg` 已全量并入 `internal/`**（v3.1 §3.1.12，P2）：仓库只有一个代码层，不再有"可复用基础设施 vs 应用专属"的目录分界。原先在 `pkg/` 的包按职责落到 `internal/` 下：
    - `internal/infra/{acp,mcp,dsh,logging,credentials,dbclient,procreg}`：协议客户端与运行期基础设施。`infra/dsh` 是**唯一 adapter**（实现 `runtime.Adapter`：`Spec()` + `RegisterPorts()`，五张端口表：syncer / 原生权限 / 模型值 / 工具失败归类 / 产品身份）。
    - `internal/runtime`：注册表 + `Spec` 契约 + `Adapter` 接口 + 隔离投影。**不得 import 任何 adapter**（`TestRuntimeDoesNotImportRuntimeAdaptersV31`）。
    - `internal/{code,config,capability,automation,identity,resource,redact,object/*}`：错误码、进程配置、能力装载、自动化解析、身份常量、跨层值对象等。
    - 新增/搬迁包时只问一句：**它在层表的哪一层**（见下），不再问"进 pkg 还是 internal"。
  - **依赖方向单一**：
    - 应用侧：`cmd → app → router → controller → service → dao/store`；`dto` 被 **controller 与 service 共用**（跨这两条边传的就是 dto，service 也收 dto、回 dto），`dto` 自身不依赖应用层。
    - **装配只能自上而下，不许反向**：`assembly` 构造用例与控制器，`router` 只收已构造的控制器（`router.Deps`），`app` 只消费 `assembly.BuildResult`。**`app`/`router`/`cli` 都不得引用 `service/svc*`**，控制器也不得 import `domain`/`store`/`model`——判据见 `layers_v31_test.go`。
    - 持久侧：`dbclient → dao → store`，`model` 被三者依赖且自身不依赖任何一层；`dao` 不得 import `store`（会成环），`model` 不得 import `dao`/`store`。
    - **厚域与用例都走窄端口，不 import 门面**（P4）：`domain/session` 声明 `Store` 端口（`store_port.go`），`domain/interaction` 零 `store`；用例层同理声明 `svcmodel.ModelStore` / `svcsession.EventReader` / `svcsystem.SourceFactsReader`。`*store.Store` 结构上满足它们、由装配根交下来；**交下来时必须现判 nil**——typed-nil 装进接口不是 nil，会让"内存模式 40501 / 只读镜像"那几条判据静默失效（`assembly/services.go` 的 `eventReader`/`sourceFactsReader`/`modelStore` 就是这条规矩的落点）。`model` 仍可被 `domain/session` 引用：它是纯列形状（无行为），把它也搬走只会换来 30 多个镜像结构。
  - **用例层（`service/svc<领域>`）的三条硬规矩**（2026-10-16 定案；违反者由 `internal/archtest` 拦，别靠注释）：

    1. **只有 `internal/controller/ctr<领域>` 可以引用 `internal/service/svc<领域>`**——「引用」包括调用、构造与**写类型名**。
       `app` / `router` / `sessionruntime` / `domain` / `mechanism` / `dao` / `store` / `pkg` 一律不许出现 `svc*` 类型。
       连带的装配口径：**`app` 不引用 controller**（`cmd → app → router → controller` 是有向的），
       `router` 拿到的只是依赖事实，controller 在自己的构造函数里把 service 造出来。
    2. **`service/svc*` 之间零互相 import、零互调用**（同层的另外半边由既有守卫钉）。需要别的域的事实时，
       在自己包里声明一个**窄接口**（如 `automation.Host`），由接线点（controller / router）把实现接上；
       需要共享的纯值或判决下沉到 `domain/*` / `internal/object/*`。
    3. **不许别名与转发骚操作**：不写 `type XxxSvc = svcxxx.XxxSvc` 这类「端口别名」，
       也不要「只做一行转发」的适配器层。controller 直接持有 **service 包内声明的接口**，一个类型只有一处定义。
        （2026-10-16 的 P4a 一批曾用别名把 8 个端口挂在 `router.Ports` 上，事后被判定为骚操作，已删。）

  - **`service/svc<领域>` 的形状**（新写一个域时照抄）：接口（`XxxSvc`，消费方只认它）+ 实现（导出结构体或私有结构体）
    + 构造函数 `NewXxx(deps...) XxxSvc`；校验、错误码、响应装配、渲染都在这一层；controller 里只做
    参数绑定、简单校验、调 service、回响应（**任何业务判断都不许写在 controller**）。

- `frontend/`：pnpm workspace（`apps/web` = **入口 + 组装**（`main.tsx` 挂载 + vite 构建配置）；`packages/{console,ui,types,api,desktop-contract,tsconfig}`——**界面本身在 `packages/console`**，2026-10-11 P5 从 `apps/web/src` 搬过来，行为零变化：搬迁保留原目录结构，所以 `internal/archtest` 里禁 `packages/*` 反向引 `apps/*` 的那条从"挡路的墙"变成了"搬迁方向的保证"）。Node 看 `frontend/.nvmrc`（24.x，团队用 fnm）、pnpm 看 `frontend/package.json` 的 `packageManager`；`engines` + Makefile 守卫 node 主版本不符即失败。
- `docs/` 是**当前有效的对外/产品面文档**（2026-10-16 重建：`positioning` / `decisions` / `architecture` / `roadmap` / `unimplemented` / `acceptance-matrix` / `dsh-acp-adaptation` / `deployment`；2026-10-18 起另有 `agents/`——仓库根 `AGENTS.md` 的分册，见 [`docs/agents/README.md`](README.md)）。旧设计文档中仍有效的内容已抽出，历史整体留在 git 里；**外部产品的对照与逆向分析不进 `docs/`**，一律落 `.dsh/docs/`。`docs/**` 的相互引用由 `TestDocsReferencesResolve` 守卫（指向不存在的文件即红）。对外契约以 README「控制面接口摘要」与 `backend/internal/router/router.go` 为准。
- `docs/assets/`：文档的**静态资源唯一收口**（图片/矢量图/后续其他静态文件），不散落在 `docs/` 根下；文档内用相对路径引用（`![alt](assets/xxx.svg)`）。
