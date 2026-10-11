# AGENTS.md — ark-mate

两层：**底座层**（`mate` 内核：多 runtime 统一接入 + 会话与能力数据面 + REST/WS 契约 + 交付形态）**与其上的产品层**（面向用户的**个人 Agent 助手**，**聚焦桌面端**）。底座支持多个 agent runtime 接入，当前 `dsh` / `opencode` 两个一等适配——**不追求数量、不承诺接入任意 ACP 生态**；浏览器入口只是**开发形态**，不是交付面（定位口径的唯一事实源 = `docs/design-and-roadmap.md` 文首「决策变更 v8」）。前后端平级 monorepo。代码注释与 commit message 用简体中文；commit 用 conventional 前缀（`feat`/`fix`/`refactor` + 中文 scope）。

## 外部形态的参照实现（DSH）

**`/Users/morehao/Documents/study/ai-agent/deepseek-harness`（下称 DSH）是本仓"外部形态"的重要参考**：交付形态、桌面与 web 两个入口的关系、宿主能力的边界、目录/工作区选择这类**产品交互**，取口径时先读它，再回来对齐。它在这些题上比我们想得全，而且**两个入口都实现了**，所以它的选择本身就是证据。

**最该记住的一条**（2026-10-11 连错两次换来的）：**"客户端能不能看见宿主屏幕"这类判定属于宿主，不属于客户端。** DSH 的做法是宿主侧解析 + 能力声明：

- `packages/host/directory-picker-auto` 在启动时采样**宿主事实**（`webServer.host` 是否回环、是否 SSH 启动、有没有可服务的显示会话），用纯函数在 `native` / `browse` 两套实现里**互斥挂载**一套；
- 原生那半是**宿主进程**抬的（macOS `osascript`、Linux zenity/kdialog、Windows COM），所以 **`dsh web` 的本机浏览器入口也用系统面板**；只有远程/SSH/无显示会话才退化成应用内浏览；
- 两个实现是**一对可替换的 occupant**，不是"两套并存"。

本仓的对应物（照抄过来的形状）：判定与命令配方在 `backend/internal/manager/workspace_picker.go`（纯函数）、声明走 `GET /capabilities` 的 `native-dialog`、执行面是 `POST /workspaces/pick-directory`。**别放错边**：判据一旦落到客户端（UA、`window.electron`、"有没有 Electron 桥"），本机浏览器就会被静默降级——这正是我们踩过的那个坑，现在有守卫 `TestDirectoryPickerIsTwoStateHostResolved` 钉着。

**其余同样值得先查再定的题**：目录选择器自身的交互（面包屑/双栏/新建文件夹/隐藏项开关，见 `packages/client/ui-directory-picker-browse`）、工作区作为持久实体的形态（`packages/workspace`）、会话与工作区的归属关系、以及"桌面只能多给、多给的那半必须有服务端等价路径"这条一致性纪律（它的 web bundle 与 desktop bundle 各有一份 cordis 配置，可逐项对照）。**取舍照旧**：DSH 没有的概念（例如我们的工作区起始点偏好）不要硬套，写清偏离理由即可。

## 布局与目录边界

- `backend/`：唯一 Go module（`github.com/morehao/ark-mate`，go 1.26.1）。**仓库根没有 go.mod / go.work——所有 go 命令必须在 `backend/` 下运行**。
  - **`pkg/` 与 `internal/` 的分界**（对齐 ark-iam）：可复用基础设施进 `pkg/`，应用专属逻辑留 `internal/`。`pkg/` 内部**不得 import `internal/`**（由 `internal/archtest` 守卫）。
    - `pkg/`：`code`（对外业务错误码唯一事实源：常量 + 注册表 + `FromError` 归一）、`acl`（ACP 客户端）、`runtime/{opencode,dsh}`（adapter + 注册表，另有 `runtime` 根包的 Spec/投影/指纹）、`mcp`（自研 MCP 客户端）、`capability`、`config`（进程配置）、`procreg`（进程注册表）、`dbclient`（gorm 句柄装配 + SQLite URL 拼装）、`logging`（第三方 slog 边界桥）、`object/objusage`（跨层共享值对象）。
    - `internal/`：`app`（装配 + 启停生命周期）、`app/command`（CLI 子命令）、`router`（唯一路由装配点，按领域分 `routes_*.go`）、`controller/ctr<领域>`（传输层控制器）、`middleware`、`dto/dto<领域>`（请求/响应 DTO）、`service/svc<领域>`（领域端口）、`manager`（领域实现：Service / Orchestrator / 事件 / 权限；**按领域分文件，不设 god file**——`service.go`/`orchestrator.go` 只留类型、构造与装配，会话动作在 `conversations.go`、轮次在 `turn.go`、连接在 `connection.go`、进程回收在 `orphan_reap.go`、事件出口在 `event_sink.go`、权限/提问在 `interaction.go`/`permission_flow.go`）、`model`（持久层实体 + 列/取值约定）、`dao`（单表数据访问，基于 golib gormdao）、`store`（持久层门面：句柄 + 跨表事务 + 读模型 API）、`eventbus`、`testutil`（跨包复用的测试夹具；**不得 import `manager`**——manager 的测试是内部测试包，反向依赖会形成 `import cycle not allowed in test`，manager 专属组合留在其 `testrig_test.go`，由 `archtest` 守卫）、`archtest`（架构守卫测试）。
  - **依赖方向单一**：
    - 应用侧：`cmd → app → router → controller → service(端口) ← manager(实现)`；`dto` 被两侧共用但自身不依赖应用层。
    - 持久侧：`dbclient → dao → store`，`model` 被三者依赖且自身不依赖任何一层；`dao` 不得 import `store`（会成环），`model` 不得 import `dao`/`store`。
  - 以上六条边界都由 `internal/archtest` 的守卫测试强制（`go test ./internal/archtest/`）；同目录还另有**四条**契约/出口守卫：事件 type 冻结快照（`TestEventTypeContractFrozen`）、前端事件同步边（`TestFrontendEventTypesMatchRegistry`）、控制器错误出口带码（`TestControllerErrorExitsCarryCode`）、**能力绑定粒度不得超出已实测范围**（`TestCapabilityScopeDeclaration`：未 `Tested` 的 runtime 不得声明比 `process` 更细的 `CapabilityScope`，否则平台可能把 A 会话的能力投给 B 会话）；再加 **4 条前端侧边界守卫**（`frontend_boundary_test.go`，Go 读 TS 源码、不引前端测试框架）：`apps/web` 的产物包纪律（`dependencies` 空 / `exports` 只开产物 / `files` 排 map / 非 private）、`apps/desktop` 只消费 `apps/web` 的 dist 产物（不引其源码）、共享 UI 够不到 Electron 与 Node API（不引 `electron`/`node:*`/`@ark-mate/desktop`、不嗅探 `window.electron`）、`packages/*` 不反向相对引入 `apps/*`；再加 **3 条渲染进程/资产契约守卫**（`hardening_test.go`：① CSP 双源**逐字**一致 —— 前端契约 `CSP_POLICY` ↔ 核心 `router.ContentSecurityPolicy` —— 以及 `webPreferences` 的四项加固取值与"不得出现 `enableRemoteModule`"；② 版本闸门的"无法校验"口径（`router.BuildIDUnknown` ↔ 前端契约 `UNKNOWN_BUILD_ID`）与 `versionGate` 纯函数必须在契约包里；③ 静态资产的 Content-Type 映射两侧逐字一致（核心 `router.MimeOverrides` ↔ 前端契约 `ASSET_CONTENT_TYPES`））；再加 **1 条桌面载荷解析守卫**（`desktop_payload_test.go`：`apps/desktop/src/payload.ts` 与 `node-runtime.ts` 剥注释后**不得**出现 `which` / `command -v` / `LookPath` 等本机安装探测——桌面态的解析链是显式的（设计 §6.5），探测会让"机器上恰好装过这个底座"静默顶掉随包那份，而 `/meta` 的 `payloads[]` 仍报成随包命中；文件不存在即判失败，fail-closed）；再加 **2 条隔离表一致性守卫**（`isolation_parity_test.go`：「哪个底座认哪些 home 变量」这张表在仓库里有**三份**——Go 侧 `pkg/runtime/isolation.go`（核心拉 ACP 时注入）、`frontend/apps/desktop/scripts/gen-runtime-bin.mjs`（壳直接跑随包载荷时在私有启动器里兜底）、`frontend/scripts/desktop-runtime-smoke.mjs`（V9 探针自己以子进程拉起载荷）；只修一侧的后果是**静默**的：底座的读根与平台写根悄悄分叉、skill 不投放而平台侧自报正常，或者探针去读写**用户真实 home**（2026-10-09 两起实测）。① 启动器：只许兜底核心也认的变量（⊆），且核心新增而启动器没兜底的必须**显式登记**在 `launcherCoreOnly` 里（写明理由，例如通用 XDG 三兄弟只在核心按 runtime 注入）；② V9 探针：与核心**逐条对齐**（两侧相等，因为探针自己就是注入者——只查子集会漏掉"核心新加了变量、探针没跟"这条真正危险的方向）；再加 **1 条桌面运行期宿主绑定守卫**（`desktop_host_path_test.go`：**进包并在运行期生效**的代码——`apps/desktop/src/**` 整棵 + `gen-runtime-bin.mjs`——剥注释后**不得**出现宿主工具绑定：`/opt/homebrew/`、`/usr/local/bin/`、`/usr/bin/<工具>`、`.nvm/.volta/.asdf/.fnm`、`#!/usr/bin/env node`、`env [-i KEY=VALUE] node` 形态；**脚本自身首行**的 `#!/usr/bin/env node` 例外——它不进包，进包的是模板串里生成的那份启动器，照扫不误；**构建期脚本刻意不扫**（它们跑在开发者机器上，依赖宿主 node 是正常的，扫了只会变噪声，理由写在该文件注释里）；判据是"今天零命中"，与动态那半（`desktop-no-host-acceptance.mjs` 的诱饵 + 最小 `PATH` + 真会话）配对——一个管"有没有这条路"，一个管"这条路今天没被走到"）；再加 **1 条交付矩阵完整性守卫**（`delivery_matrix_test.go`：`pkg/object/objdelivery` 的静态交付表必须覆盖 `internal/model/enum.go` 的全部 `CapabilityKind*`，且未知 kind 必须查到 `false`——它挡的是「新增一种资源却漏了交付通道声明」这种静默缺口：写面照常成功、界面显示「已保存」，而用户永远等不到它生效；故意删掉表里一条声明 → 红）；再加 **1 条设置 · 模型的文案守卫**（`settings_copy_test.go` 的 `TestSettingsModelCopyAvoidsProvider`：设置「模型」那一节的**标题**里不得出现 `provider`/`mcp`/`skill`/`token` 这类代码词——那一节的用户词是「供应商」「模型」，代码词只许出现在指向具体资源/接口的地方（表格列、错误出路）；它读 `console/settingsView.tsx` 与 `components/settings/ModelSettingsPanel.tsx` 的 TS 源码，**解析不到就失败**（fail-closed，避免改名后守卫静默失效），并断言「供应商」「模型」两块都在；故意把节标题改回 `provider 档案` → 红）；再加 **1 条设置 · 模型表单字段守卫**（`settings_form_fields_test.go` 的 `TestSettingsModelFormRendersEveryField`：`ModelSettingsPanel.tsx` 里两个表单接口（`ProviderFormValues` / `ModelFormValues`）的**每个键**都必须在同文件里出现 `<Form.Item name="<键>">`——antd 的 `validateFields()` **只返回已注册字段**，没渲染出来的键恒为 `undefined`，于是"用户以为填了""用户以为没改"与"其实发的是默认值"看起来一模一样；先剥注释再解析，**解析不到接口即失败**（fail-closed，两个接口的成员体读成空也当场判失败）。挡的是 2026-10-11 的真实事故：`ModelFormValues.image_input` 三处被读写、弹窗里却没有对应的 Form.Item，于是**只改一次备注**就把该模型已声明的图片能力静默改回仅文本）；再加 **3 条底座知识归属守卫**（v12 应用层资源与底座同步，`knowledge_boundary_test.go` + `syncer_contract_test.go`：① `TestInternalHasNoRuntimeConfigLiterals`（A9）扫 `internal/**`（**跳过 `internal/archtest` 自身**——它的禁词表就写着这些字面量）剥注释后**不得**出现底座配置格式与机制字面量（`opencode.json` / `OPENCODE_CONFIG_CONTENT` / `mate-models.patch.yml` / `.credentials.yaml` / `--dump-config` / `llm-pi-ai` / `opencode_json` / `dsh_patch_yaml`）：命中即意味着**底座私有知识回流到了平台内核**（T1/T2 的机器判据），替身是把它提到 adapter 并导出常量（`opencode.EnvConfigContent`）；② `TestAdapterHasNoStoreLiterals` 扫 `pkg/runtime/<adapter>/**` 剥注释后**不得**出现 SQL/DAO 标记（`SELECT `/`INSERT INTO`/`UPDATE `/`DELETE FROM`/`gorm`/`dao.`）与 `internal/{store,dao,model}` 的 import 路径——adapter 只认端口传入的 `Set`，它没有、也不该有读库的能力（等价性 golden 依赖这条）；**刻意不 ban `providers`/`models`**（那正是它要生成的配置键）；③ `TestSyncerContractEmptySet` 用 stub `Syncer` 断言 `Set` 为空时编排层**确实调用**了 `SyncModels`（而不是"看到空就跳过"）——跳过等于退回 v11 的真实缺陷（空清单什么都不做 ⇒ 上一份配置原样留在生效状态，而外观上一切正常），契约的正确实现只能在 adapter 里（只有它知道要撤销哪些字节）；反证档同时断言「未登记 syncer 的 runtime 不该被调用」）；再加 **1 条日志出口守卫**（`logging_boundary_test.go` 的 `TestOnlyLoggingPackageImportsSlog`：剥注释后全仓只许 `pkg/logging` import `"log/slog"`——本仓日志出口是 glog/zap，那一处导入是「只认 `*slog.Logger` 的第三方库（ACP SDK 的 `SetLogger`）」的边界桥；别处再写一个 `slog.Info` 照样能编译、也「看起来在工作」，但记录会绕过 glog 落到 std log 的 stderr，级别/格式/轮转/目标全不一样，只有出事那天才被注意到；跳过 `internal/archtest` 自身——它的判据字面量就写着 `"log/slog"`）。再加 **3 条构建期底座守卫**（`runtime_pin_test.go`：「用户不能选 runtime，只能在构建时指定，不指定默认 dsh」这条产品约束的三个危险失效方式**都不会让别的测试变红**，所以各有一条机器判据：① `TestBuildRuntimeDefaultIsDsh` 读 `pkg/runtime/build.go`，断言 `DefaultBuildRuntime = dsh.ID` 且注入变量初值为空串（**解析不到即失败**——改名/搬迁会让它静默空转）；② `TestNoAgentRuntimeSelectionSurface` 扫 agent 定义/请求/响应、automation 目标、会话请求与前端 `Agent` 类型，剥注释后不得出现选择面字段声明（`Runtimes []string` / `RoutingPolicy string` / `RuntimeHint string`；**拒绝用的 `json.RawMessage` 与 `ConversationRec.RuntimeHint`（执行事实）刻意放行**，全仓扫会误伤投放声明）；③ `TestRuntimeSelectionRejectionsExist` 断言写面的**显式拒绝**真的存在——agent 的 `rejectRuntimeSelection`（1 处定义 + Create/Update 各 1 处调用）与 automation 的 `rejectRuntimeHintInPatch` + `dropLegacyRuntimeHint`：删掉它们请求照样 200，只是键被 JSON 绑定悄悄丢掉，用户以为"我改了底座"而什么都没发生（B1 类静默 no-op））。再加 **1 条资源表形态守卫**（`provider_model_tables_test.go`：读 `internal/store/migrate.go` 的 v13 建表语句（剥注释），断言 `providers_v13` / `models_v13` 的列集合 == 白名单，且**不含** `body` / `key_ref` / `key` / `is_default` / `runtime` 这些**刻意没有**的列；同时断言两个实体的 gorm 标签里不再出现 `column:body`，**解析不到即失败**（fail-closed）。它挡的是「键集合封闭却仍停在载体形态」这条静默回退：加一列、或把删掉的 `key_ref` 加回来，别的测试都不会红，而那正是两表列化要消灭的东西）；再加 **1 条静态能力基线守卫**（`caps_images_boundary_test.go`：剥注释后 `.Caps.Images` 只许出现在 `pkg/runtime/adapters.go`（静态基线的汇入点，只做值拷贝、不做任何判据）——`dsh.Caps.Images=false` 是**按源码声明的保守基线**而不是事实，真值取决于附件仓与**精确路由**，拿它当门禁会把保守基线变成错误结论：用户会在一个其实能收图的 dsh 上被告知「不支持」（ADR-14）。它同时**正向**断言连接期事实 `Capabilities().Images` 真的有消费者——否则「以连接期事实为准」只是一句没人执行的话）。再加 **1 条全局浮层层号守卫**（`toast_layer_test.go` 的 `TestGlobalToastSitsAboveModals`：全局 toast 的层号必须有单一常量（`apps/web/src/app.tsx` 的 `TOAST_Z`）且 ≥ 2000——antd 的 Modal 基础层是 1000、每嵌套一层 +200（本仓「设置」+「供应商/模型」两层实测就是 1000/1200），toast 低于它就被遮罩压暗到看不清：2026-10-11 用户报的「点保存没有反应」，一半原因正是**唯一能解释原因的那句提示画在遮罩底下**（设置弹窗里**每一个**动作的成败提示都共享这一个浮层）。它同时断言 toast 元素用的是这个常量而不是字面量——`zIndex: 90` 那种"看着够高"的值不会让任何别的测试变红，只会让提示静静失效）；再加 **1 条目录选择器形态守卫**（`hardening_test.go` 的 `TestDirectoryPickerIsTwoStateHostResolved`：选目录用系统面板还是应用内浏览，**判据必须在宿主侧**——绑回环 / 非 SSH / 有显示会话，经 `GET /capabilities` 的 `native-dialog` 声明下发，执行面是 `POST /workspaces/pick-directory`（核心 spawn `osascript`/zenity）。它同时**禁止**前端拿 `__MATE_DESKTOP__` / `isDesktopCarrier()` 判形态。挡的是 2026-10-11 连错两次的同一个坑：先把原生那半整个漏掉（`native.pickDirectory` 全仓零调用点），再把它判在客户端（于是**本机浏览器**入口被误判成没有系统面板，而参照实现的 `dsh web` 恰恰在那种场景下用系统面板）。**共 35 条**——加守卫时记得同步这句话。
- `frontend/`：pnpm workspace（`apps/web` 控制台 + `packages/{ui,types,api,tsconfig}`）。Node 看 `frontend/.nvmrc`（24.x，团队用 fnm）、pnpm 看 `frontend/package.json` 的 `packageManager`；`engines` + Makefile 守卫 node 主版本不符即失败。
- `docs/` 设计文档是**历史记录**（会话早期叫 sessions，P0 起为 conversations）；对外契约以 README「控制面接口摘要」与 `backend/internal/router/router.go` 为准。
- `docs/assets/`：文档的**静态资源唯一收口**（图片/矢量图/后续其他静态文件），不散落在 `docs/` 根下；文档内用相对路径引用（`![alt](assets/xxx.svg)`）。

## 错误码（唯一事实源 `backend/pkg/code`）

- 信封 `code` 是**对外契约**，三处必须同步：`pkg/code`（事实源：常量 + 默认文案）、README「业务错误码」表（文档）、前端 `packages/types/src/response.ts` 的 `BizCode`（消费方）。三条边都由守卫钉住——`TestReadmeCodesMatchRegistry`（pkg/code ↔ README）、`TestFrontendBizCodesMatchRegistry`（pkg/code ↔ 前端，**用 Go 读 TS 源码**，不引前端测试框架，忘记同步前端同样红）。新增一个码 = 加常量 + 默认文案 + README 行 + 前端常量。
- 编号沿用既有 5 位 HTTP 语义码（`40000/40001/40101/40401/40501/40901/50000/50001/50201`），**不按 ark-iam 的 6 位领域段重编**——它们已经是对外契约。ark-iam 借的是结构（`code.go` 注册表 + 按领域分文件），不是编号。
- `50000` 是**唯一兜底码**：任何未归类 error 经 `code.FromError` 出口都落成它。历史上有 `1`（控制器手写）与 `50000`（领域层）两个泛化码，已统一为 `50000`。
- **控制器错误出口一律带码**：`gincontext.Fail(c, code.FromError(err))` 或 `code.Error(code.X, …)`，不得透传裸 `err`——gincontext 对不带 `gerror.Error` 的错误填 `code: -1`，客户端拿不到可判定的码。由 `TestControllerErrorExitsCarryCode` 静态守卫。
- acl 分类 → 码的映射表在 `pkg/code/acl.go`（不在 `pkg/acl`：acl 是可复用的 ACP 客户端，不该知道 ark-mate 的信封编号）。`agent_not_found`/`conversation_not_found`/`runtime_not_found`/`mcp_not_found`/`provider_not_found` 从 `ClassRequest` 提档到 `40401`。

## 常用命令（仓库根）

- **桌面是主线（决策 v8③）**：`make desktop/dev`（起 Electron 壳 + 真核心）/ `make desktop/accept`（真 Electron + 真核心验收）/ `make desktop/payloads`（随包载荷，冷缓存需联网）/ `make desktop/dist`（出安装包）/ `make verify/desktop-dod`（桌面 DoD 汇总）/ `make verify/no-host-dep`（R2：**不依赖宿主机环境**——最小 `PATH`（诱饵 + `/usr/bin:/bin`）+ 私有 `HOME`/`MATE_HOME` 下真跑「一轮 → 退出 → 重开续聊」并断言诱饵零命中；静态那半是 archtest 第 21 条，打包态那半是 `pnpm ui:accept:desktop-nohost:packaged`，挂 `verify/hardened-runtime`；**干净 VM 自 2026-10-10 起不再是必做**，见设计 R2/V1 与计划 §12.32）。**浏览器入口（`make dev`）是开发形态**（唯一 HMR 循环，改 UI 走它最快），**不是交付面**；服务端形态（容器 / 自建二进制 + 外挂 dist）为维护态。
- `make dev` 前后端联调（go run serve → 等 :10001 health → vite :5173，Ctrl-C 一起收）；`make server` 仅后端；`make web` **只**构建前端产物（`frontend/apps/web/dist`，唯一一份——核心从不自带资产，跑时用 `--web-dir` / `MATE_WEB_DIR` 指过去，没给就拒启退出码 3）。**这两条都认 `OPENCODE_BIN=<可执行文件>`**：核心拉底座是写死的名字 `opencode`、由 `exec` 走 `PATH` 解析，所以 dev/server 默认命中**本机装的那份**（桌面态相反：壳解析随包载荷、**不回落** `PATH`）；本机装的是 v1 而仓库取证以 v2 为准时（2026-10-10），用 `make dev OPENCODE_BIN=frontend/apps/desktop/resources/payloads/opencode/<ver>/bin/opencode` 显式指定随包那份即可，不必动本机安装。语义是**显式覆盖、不探测**（不填 = 与今天逐字相同；填了只把**核心子进程**的 `PATH` 前置到一个只含 `opencode` 的转发目录 `.cache/dev-bin/`，前端不拿；填了不可执行当场失败）。也可以写进仓库根的 `.env` 一次配好（那正是 compose 用的同一个文件；Makefile **只挑 `OPENCODE_BIN` 一个键**、不做整份 `-include`），优先级 **命令行 > 进程环境变量 > `.env` > PATH**，两种形态启动时都会回显生效的那一份与来源 `[命令行]/[环境变量]/.env`。**注意 `.env` 也被 `dsh` 自己读**（真实运行时读「调用目录」的 `.env`；`--version` 快路径不读）：前缀 `DSH_`/`XDG_`/`DYLD_`/`BASH_FUNC_` 与 49 个固定启动键（`PATH`/`HOME`/`NODE_OPTIONS`/`SSL_CERT_*`/代理四项…）写进去会让 dsh **拒绝启动**——要给 compose 钉底座版本就走 `export`（compose 与 dsh 都是"继承环境优先"），别写进 `.env`。
- `make build && make vet && make test` = `cd backend && go build|vet|test ./...`（注意**没有** `build/vet/test` 这种合并目标：make 会把斜杠当成目标名的一部分而报 `No rule to make target`）。无独立 lint。**全仓只有这一个构建配置**：`-tags release` 这个维度已于 2026-10-10 随 `go:embed` 删除（连带 `make test/release` 与 `make all` 单二进制形态），不再有"只有发布才编译、默认 `go test` 覆盖不到"的代码。
- 容器：`make image`（默认最小镜像）/ `make image/runtimes WITH_DSH=true WITH_OPENCODE=true`（内置底座）/ `IMAGE_TAG=runtimes make image/run`；开关只认 `true|false` 且必须与 `--target` 配套（最小 target 传开会构建失败，不静默忽略）。基础镜像与仓库地址可用变量覆盖（`NODE_IMAGE/GO_IMAGE/RUNTIME_IMAGE/GO_PROXY/NPM_REGISTRY`），细节见 `docs/deployment.md` §4。
- 单包 / 单测试：`cd backend && go test ./internal/manager -run TestName -v`。全量 `go test ./...` 约 2 分钟、**不依赖外部 runtime**（夹具用 `testutil.StubRegistry()` 提供的 stub runtime 注册表，需要真 ACP 语义的用例用自建最小 responder，见 `pkg/runtime/acp_testserver_test.go` / `pkg/acl/elicitation_e2e_test.go`）。
- 前端：`cd frontend && pnpm dev / typecheck / build`。
- 界面验收（前端无测试框架，这是"看过界面"的唯一路径）：`cd frontend && pnpm ui:accept [cap|cmd|auto|model|wsdir]`——真 Chrome（CDP，零新依赖）+ 真服务，脚本自造自清夹具（验收用 skill / 自动化 / 会话），如实录见 `.dsh/docs/notes/2026-09-24-panel-ui-acceptance.md`。默认四条（`cap cmd auto model`）；`wsdir`（目录浏览对话框）**要在核的进程环境里给一个 `SSH_CONNECTION`** 才跑——那条判定决定"用系统面板还是应用内浏览"，而自动验收只能在**应用内浏览**那一态上跑（系统面板要真人回答）。**不进 CI**（要真浏览器 + 真底座 + 会真跑模型）。桌面入口的对应靶是 `cd frontend && pnpm ui:accept:desktop`（真 Electron + 真核心，**脚本自己起壳并自造自清**，跑之前别让 `make dev` 的核同时活着——两个核心共用 `~/.ark-mate/mate.db`）；两者合起来由 `make verify/desktop-dod` 串成桌面 DoD。

## 本机隔离纪律（开发期硬约束）

**开发、构建、验收、手工探针，都不得读写用户自己安装与正在使用的 dsh / opencode**——那是用户的环境，
不是本仓的测试场地。这条是硬约束，不是取向：opencode v2 换实现时新增了一个没被隔离的 home 变量，
差一点就让平台去复用/替换用户自己的后台服务（2026-10-09，全量实测见
`.dsh/docs/notes/2026-10-09-opencode-v2-payload-verification-notes.md`）。

- **不许碰的目录**：`~/.dsh`、`~/.config/opencode`、`~/.local/share/opencode`、`~/.local/state/opencode`、
  `~/.opencode`。`~/.agents/skills` 是**平台与底座的共享投放面**：可以读，写只由平台的物化负责
  （探针别往那儿写，也别去"顺便修一下"）。
- **平台拉起底座一律经隔离表**（`backend/pkg/runtime/isolation.go` → `<MATE_HOME>/runtime/<id>/…`）。
  **换底座版本时必须重查它新引入的 home/状态变量**：v2 就新增了 `XDG_STATE_HOME`
  （`<XDG_STATE_HOME ?? ~/.local/state>/opencode/service.json` 的服务登记面）——不注它，平台拉起的
  opencode 会读用户自己的 `service.json`：同版本同 url 直接复用用户的服务，版本不符还会 SIGTERM
  旧进程再顶掉登记文件。
- **同一张隔离表在仓库里有三份**（Go 核心 `backend/pkg/runtime/isolation.go`、桌面私有启动器
  `apps/desktop/scripts/gen-runtime-bin.mjs`、V9 探针 `frontend/scripts/desktop-runtime-smoke.mjs`）：
  **改一处就得改三处**。护栏三条：`pkg/runtime/isolation_test.go`（逐变量逐取值钉住核心表）、
  `TestDesktopLauncherIsolationMatchesCore`（启动器 ⊆ 核心，且核心新增而启动器没兜底的必须登记进
  `launcherCoreOnly`）、`TestDesktopSmokeIsolationMatchesCore`（探针与核心**逐条对齐**）。
  新增一条 home 变量时，三侧都要过一遍守卫——别只修核心那侧。
- **手工探针**：私有 `HOME` + 私有 `MATE_HOME`，目录一律建在工作区内（`.dsh/tmp/...`），结束杀进程 + 清目录；
  起 Electron 前清掉宿主环境里的 `ELECTRON_RUN_AS_NODE`（它会以纯 Node 跑）。注意 opencode 的
  `debug`/`serve` 会起**托管服务**并在自己的 state 目录里登记 pid——收尾要按 `service.json` 里的 pid 收干净。
- **验收脚本**必须核对用户真实目录指纹零变化。外部写者（用户自己正跑着 dsh/opencode）在场时，
  如实报"不可判定/跳过"并打印写者：**既不许假绿，也不许假红**——`desktop-ui-acceptance.mjs`
  的 isolation 场景就是这么区分"外部写者"与"mate 写入"的（`~/.dsh` 是 DSH 宿主会话自己的状态目录，
  它本来就会被持续改写）。
- **沙箱/临时的绕道不许留在工作区**：为绕开沙箱而加的 `MATE_PAYLOAD_STORE_DIR`（或任何自定义
  store / cache 目录）会写进 `apps/desktop/runtime/<name>/node_modules/.modules.yaml` 的 `storeDir`，
  下次**在用户默认环境**里跑 `make desktop/payloads` 时 pnpm 判定"模块目录要重建"，而在没有 TTY 的
  构建里直接以 `ERR_PNPM_ABORTED_REMOVE_MODULES_DIR_NO_TTY` 中止——2026-10-09 就是这么把用户的
  `make desktop/dist-run` 弄红的。规矩：绕道用完就还原环境并删掉那些中间产物（`runtime/*/node_modules`、
  仓库根的自定义 store），且**必须在默认环境下再跑一遍**验证。另外 `prepare-runtime.mjs` 已显式
  `--config.confirmModulesPurge=false`（构建步骤不该依赖"终端恰好是 TTY"），这条兜底不替代上面的清理。

## 构建形态

- **底座由构建期固定**（2026-10-10 产品约束「用户不能选 runtime」）：`pkg/runtime/build.go` 的 `BuildRuntime` 由 `-X` 注入（`RUNTIME=<id>`；Makefile 的 `build/dev/server/core/desktop` 与 Dockerfile 的 `MATE_RUNTIME` 都透传它），**不注入 = `dsh`**。产品装配走 `runtime.ProductRegistry`：只注册选中的那一个（可以是内置 adapter，也可以是 `runtimes.json` 里同 id 的远程端点），未注册即**启动失败**；`AllBuiltinRegistry`/`AllBuiltinSpecs` 只服务测试与诊断（医生与能力矩阵守卫要看**全部**适配面，否则默认 dsh 的构建会让 opencode 的守卫静默失效）。用户面零入口：`GET /runtimes` 已删，agent 无 `runtimes`/`routing_policy`，automation 无 `target.runtime_hint`，写面带这些键一律 40001 `runtime_selection_removed`（**显式拒绝**，不是静默忽略）。两个 adapter、注册表、远程接入与隔离表三份**原样保留**。
- **核心从不自带前端资产**（2026-10-10：`//go:embed`、`-tags release`、`pkg/web` 与"单二进制"形态整体删除）：资产只有一份 `frontend/apps/web/dist`，由调用方用 `--web-dir` / `MATE_WEB_DIR` 指给核心，**没给就拒绝启动（退出码 3）**，不存在回落来源；`/meta` 的资产面是 `webPath`/`webDigest`/`webBuildId`（`webSource` 字段已删——来源只剩一种，恒量字段不携带信息）。于是全仓只有一个构建配置，桌面核、容器镜像与 `make dev` 跑的是同一份代码路径。**形态优先级（决策 v8③）**：桌面（`make desktop/*`）是主线交付；容器 / 自建二进制 + 外挂 dist / 浏览器入口属服务端与开发形态（维护态）。理由、代价与实测见 `.dsh/docs/notes/2026-10-10-embed-removal-notes.md`。
- `mate` CLI 无默认子命令：`serve / demo / migrate / transcripts prune / config / capability migrate / doctor`（`capability migrate` 把 skill 定义里的旧 `sources[]` 转成 `runtimes[]`，`--dir` / `--dry-run` 可选；`doctor` 是**只读体检**——资产目录与逐文件摘要、随包载荷清单与入口存在性、载荷 `nodeAbi` 与 `release.json` 的一致性、数据目录可写性、端口占用，`--verify-payload` 对载荷树做全量复算，`--json` 供诊断包/CI 消费；"无法校验"记警告、事实与声明不符才退出 1，见计划 M4-5），另有 `mate --version [--json]`（核心版本 + API 契约代数；桌面打包期 `release.json` 的 `coreVersion`/`apiContract` **唯一取法**，见计划 M4-1）。`mate demo` 需本机装 opencode/dsh（`echo` 演示底座及其 `acp-serve`/`acp-demo` 子命令已于 M0 删除）。

## session 与 conversation：两个名字，一个东西（外加一个同名不同物）

平台聚合的概念从 P0 起叫 **conversation**，但**对外 wire 上它叫 `session`**——这是冻结的历史词汇，不是待修的笔误：

- **wire 用 `session`**：事件 type 20/24 条是 `session.*`（`session.created`/`session.message`/`session.permission_resolved`…；计数随事件新增而变，以 `internal/manager/events.go` 为准）、6 个 JSON 字段是 `session_id`、transcript 落盘 header 也是 `session_id`。前端 `@ark-mate/types` 的 `events.ts` 与 `conversation.ts`、磁盘上既有的 transcript、`mate migrate` 全都依赖它。
- **平台领域词用 `conversation`**：REST 资源路径 `/conversations/:id`、Go 类型 `Conversation`/`ActiveConversation`、`conversation_start.go`。
- **`Session` 还另指一样东西**：**runtime 侧的 ACP 会话**。`ProviderSessionID`（原生底座会话 id）是它，`LoadSession`/`SessionCapabilities`/`SessionOptions`/`SessionInfo`/`NewSession`/`ResumeSession`/`SessionsPersistent`/`MultiSessionConcurrent` 也都是它——**这批不能改名**，改了就是把两个概念混成一个。注意 `sessions_persistent`/`multi_session_concurrent` 还是 health/diagnostics 的 wire key。

**最容易踩的一处**：DTO / 前端里的 `SessionID` 指的是**平台会话 id**（= conversation id），而 `ProviderSessionID` 才是 runtime 的。两者在 `ConversationResp` 上并存（`service.go`）。

**决策**：`session` 作为对外词汇**冻结**，与 ACP 自身协议一致（`NewSession`、`Acp-Session-Id` 响应头），改名是破坏性变更且零功能收益。只在**纯 Go 内部名**上保持「操作 `ActiveConversation` 的函数/文件就叫 conversation」（如 `applyModelOnConversation`、`conversation_start.go`）。
事件契约的三条边由 `TestEventTypeContractFrozen`（常量 ↔ 冻结清单）与 `TestFrontendEventTypesMatchRegistry`（清单 ↔ 前端 `Ev`）共同钉住；错误码同理见下一节。

## 一致性守卫 / 测试约定

- `TestReadmeRoutesMatchRouter`（`internal/router/router_test.go`）把 README 反引号内的 `/v1/mate` 路径与真实注册路由做**双向**比对：README 引用的路径必须注册（抓改名/删除），每条注册路由也必须在 README 出现（抓新增）。所以**改路由与改文档必须同一次提交**——新增端点不写文档同样红。因此没有豁免名单：**兼容别名也是端点，也要成行写进路由表**（`/permissions/always` 两个别名就是这么被补进文档的）。README 里也不得再出现历史名 `/sessions`。
- 域事件 type 权威定义在 `backend/internal/manager/events.go`。
- **连接拆除语义（`manager`，改动前先读 `disconnectConversation` 注释）**：连接断开只是连接事实，绝不等于轮次失败。主动拆除（`Close` 停机 / `stopAgent` / 空闲回收）在途轮次归 `interrupted`（可续聊重发）、空闲会话状态**原样保留**；只有对端或子进程意外断开才可能把在途轮次收成失败，空闲会话同样不被改写（只落 agent 诊断 `connection_lost`）。拆除原因由 `runtimeConn.intentional` 承载（`watchConn` 观察到对端结束 = 意外），断连时句柄摘除但会话记录不动——下次 prompt 经 `ensureConversation` 按 native resume / handoff memo 重建（ADR-4：执行可丢弃、会话平台拥有），**可续聊性只看会话是否存在**，上一轮的终态不是作废判据。
- WS / REST 契约改动优先看 `internal/router/routes_*.go` 与 `internal/controller/ctr*`，测试多为走真实装配路径
  （`manager.NewService` + `router.New`）的进程内测试；`TestReadmeRoutesMatchRouter` 走的就是真实引擎。

## 前端风格约束（frontend/DESIGN.md）

- 颜色/圆角/间距唯一事实源 = `packages/ui/src/theme.ts` 的 `tokens`；改设计值须同步 DESIGN.md front matter（两处人工一致），业务代码禁止硬编码色值。
- 交互态 CSS 唯一收口在 `apps/web/src/console.css.ts`，不新增 css 文件；聊天正文样式由 `Transcript.tsx` 自绘，勿搬进 antd。

## 变更授权

- **允许破坏性调整**：删除/重命名既有对外形态（兼容别名如 `/permissions/always`、历史路径）、改造本地持久数据（SQLite schema、transcript 格式、agents definitions）与大范围重构（包边界、装配路径）均被允许，**不要求保留过渡性兼容层**。
- 例外（仍强制）：改路由须同步 README 接口摘要（`TestReadmeRoutesMatchRouter` 守卫保持绿）；改事件 type / 数据 schema 须同步 `manager/events.go`、`internal/model` 实体与 `internal/store` 迁移内核（三处手工一致）；改动后测试与前端 typecheck 保持通过。

## 运行配置要点

- 默认仅回环 `127.0.0.1:10001`，无鉴权（对外需先设 `MATE_TOKEN`；REST 走 `Authorization: Bearer`，事件流的令牌走**带内首帧 attach**——`?token=` 已于 M1-1 删除）。桌面态是两个互斥凭据（`restToken` 只给壳反代 / `streamToken` 只被事件流接受），由壳经 `--auth-stdin` 从 stdin 首行下发，不进 argv/env/文件/URL。部署见 `docs/deployment.md`。
- **回环面的 Host 白名单 + Origin 矩阵（M3-5，设计 §7.2/§9.1）**：只在回环监听时启用（非回环如 `--addr 0.0.0.0` 不启用并打 WARN，理由见设计 §9.4）——`Host` 的主机部分须是 `127.0.0.1`/`[::1]`/`localhost` **且端口 == 实际监听端口**（取自 `Listener.Addr()`，桌面态是内核分配的随机端口；所以装配顺序是"先绑定 → 再构造策略 → 再装路由"，见 `internal/app`）否则 421；`Origin` 只放行无 Origin（壳内反代/curl/导航）、`mate-app://app` 与自身监听端口上的 http 同源，其余（含 `Origin: null`）403。跨源开发/自建反代用 `MATE_ALLOWED_ORIGINS`（纯 origin，**显式登记口不是默认放开**）登记。判定只有一份实现 `middleware.OriginPolicy.Decide`：REST/`health` 走 v1 组中间件，WS 由 `ctrws.Options.OriginCheck` 在 `websocket.Accept` 之前调同一个对象（WS 路由**不挂**该中间件，与"事件流凭据走带内"同一分线；coder/websocket v1.8.15 的 AcceptOptions **没有** CheckOrigin 字段，库自带校验比矩阵更松）。
- 持久层实现：gorm（`pkg/dbclient` 装配 + `internal/model` 实体 + `internal/dao` 访问）。**SQLite 用 golib 的 `dbgorm/driver/sqlite`**（匿名引入在 `pkg/dbclient`，随 driver 一起注册；URL 由 `dbclient.Open` 拼成 `sqlite://`）。
  **已知代价（2026-10-10 决策：用户明确选择「照字面用 golib 的 driver」）**：该 driver 底层是 **CGO** 的 mattn/go-sqlite3——`CGO_ENABLED=0` 能编译，注册的却是 stub，**运行期**连库即报 `Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work`（实测：`CGO_ENABLED=0` 编出的核跑 `mate capability import` 直接失败）。因此桌面核（`make core/desktop`，Makefile 写死 `CGO_ENABLED=0`）与容器镜像的 SQLite 面**目前不可用**；要用就得把那两处改成 `CGO_ENABLED=1`（动态链接、需要 C 工具链、不能再交叉编译）。
  行为上还丢了三样（原先自注册的纯 Go dialector 在 DSN 里注入过）：不注入 `foreign_keys`（**外键不再由库兜底**，`events`/`runs`/`messages` 可以写进孤儿行）、不注入 WAL/synchronous、不固定 busy_timeout；内存库统一命名 `file::memory:?cache=shared`，**同进程多次 `Open(":memory:")` 会落到同一个库**（测试夹具因此一律用 `t.TempDir()` 下的文件库，见 `internal/testutil.TempDBPath`、`internal/store` 的 `openTestDB`）。这几条由 `pkg/dbclient/dbclient_test.go` 显式钉住。
- **落库格式是硬契约**（`internal/store/format_test.go` 锁定）：时间列一律 TEXT + RFC3339Nano(UTC)，布尔列一律 INTEGER 0/1。两者都由 `internal/model` 的 serializer 保证——实体照常写 `time.Time`/`bool`，但**裸 SQL 的入参不经过 serializer**，时间要先用 `model.FormatTime` 格式化、布尔要用 `model.FlagValue` 转换。schema 仍由 `internal/store` 的自管迁移内核创建（不用 gorm AutoMigrate：它表达不了有序版本、版本拒绝启动与导入 staging 回滚）。
- 持久层 `MATE_STORE=sqlite|memory|auto`：make dev/server 默认 sqlite（库 `~/.ark-mate/mate.db`）；sqlite 才有 runs 读模型与 `/usage/summary`（memory → 40501）。memory 模式以 transcript（`~/.ark-mate/transcripts`）为事实源，`mate migrate` 幂等导入 sqlite。
- 配置优先级：默认值 < `~/.ark-mate/config/config.json` < `~/.ark-mate/config/env` < 进程 `MATE_*` < CLI flag；非法键名与非法取值启动即报错（枚举型键如 `native_permission`/`mcp_auto_probe`/`store` 都按 `Valid*Modes` 校验并报错，提示里列出合法取值）。**没有例外**：`MATE_STORE` 的非法值同样报错，只有**空/未设**才当 auto（无偏好）——`auto` 本身也是合法取值，含义同未设。`mate config --json` 可查最终值与来源。

  这条早先是反的（`MATE_STORE` 非法值静默回落 auto）。改掉的直接理由是**用户无从察觉**：回落后的 `mate config` 输出与合法的 `auto` 逐字相同；而 auto 会按"DB 文件在不在"解析成 sqlite 或 memory，一个错字（`sqlit`）就可能把人悄悄丢进 memory，表现为"数据丢了"。且查历史，`MATE_STORE` 从引入那版起的取值集就是 `sqlite|memory|auto`，**从未有过今天已非法的取值**，严格化不淘汰任何曾经可用的配置。
