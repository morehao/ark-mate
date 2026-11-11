# 应用包格式与清单协议（App Package Manifest Protocol）

> **这份文档是什么**：二期 C（运行期装卸）里**运行期应用包**随包携带的 `manifest.json` 的对外契约——
> 包长什么样、字段怎么填、装载时怎么校验、本地目录怎么组织。它是给**写第三方应用包的人**和
> **给平台加装载能力的人**看的唯一口径。
>
> **事实源是代码，不是本文**：类型、解析与校验在 `backend/internal/core/apppkg`（`manifest.go` /
> `load.go` / `capability_bridge.go`），样例包在 `backend/internal/core/apppkg/testdata/minimal-app/`。
> **本文与代码冲突时以代码为准。**
>
> 设计依据：[`decisions.md`](decisions.md) v37（受控豁免三条定位红线的立项与安全基线）；
> 定位见 [`roadmap.md`](roadmap.md) 的 C 段。内部设计/计划文档见 `.dsh/docs/specs/`、`.dsh/docs/plans/`
> （不入库）。

## 1. 这份协议解决什么

一期 A+Slot 的应用**编在二进制里**：`impl` 靠编译期 `RegisterImpl` 登记，前端组件靠编译期注册。
二期 C 让**不属于二进制的应用代码（第三方包）**也能作为应用进来——于是需要一个「包」的分发单位：
一个目录，带一份**清单**声明它提供什么、需要什么权限、入口是什么、内容没被篡改。

**C 消费的仍是 A+Slot 的同一套契约**（见 v37 与设计 §17.1）：应用定义模型、扩展点语义、能力 scope、
归属、`api_version` 兼容门全部不变；C 唯一新增的是「impl 从哪来」——从**运行期包**来。

## 2. 清单与 App 定义的分工（不重复）

| 概念 | 描述什么 | 在哪 | 落库? |
| --- | --- | --- | --- |
| `capability.App`（应用定义） | 一个应用**引用/启用**什么：`members` / `entities` / `tool_domains` / `slots` / `page` / `api_version` | `backend/internal/core/capability/app.go` | 是（`apps` + `app_members`） |
| `apppkg.Manifest`（包清单） | 一个包**提供**什么：`provides{impls,tools,page}` / `permissions` / `entrypoint` / `integrity` / `requires` / 使用说明（`tagline`/`examples`/`usage_guide`，v42） | `backend/internal/core/apppkg/manifest.go` | 否（随包分发） |

二者靠**同一套 id 词法**、同一个 `id` 值关联（包 id = 应用定义 id），但字段**不互相镜像**：清单不重复
App 的 `members`/`entities`，App 也不重复清单的 `provides`/`permissions`/`entrypoint`。装配期（C2 起）
据「这个 app 是否来自包目录」决定它的 `impl` 来源。

## 3. 本地应用目录布局

```
<应用根目录>/
  <app-id>/                       # 每个应用一个子目录
    manifest.json                 # 本协议定义的清单
    <entrypoint>                  # 清单 entrypoint 指向的可执行入口（相对包根）
    ...                           # 其它资源
```

- **根目录**：环境变量 `MATE_APPS_DIR` 优先；否则默认 `~/.dsh/mate/apps`。
- **默认目录为空/不存在 = 空集合、不报错**（对齐「无定义照跑」：没装任何包时平台照常启动）。
- **装载单位是子目录**：根目录下散落的普通文件被跳过（不是包）。
- **失败隔离**：单个包失败（解析/校验/哈希不符/入口缺失）只把它自己标记为 FAILED 并明示原因，
  不影响其它包，也不会让整次扫描报错。

## 4. 清单字段（`manifest.json`）

严格 JSON 解码，**未知字段拒绝**（拼错字段名会启动即失败，而不是静默失效）。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 包标识，同时关联 App 定义。词法 `^[a-z0-9]+(-[a-z0-9]+)*$`，≤64 位（复用 `capability.ValidateApp`）。 |
| `version` | string | 是 | 包自身版本（自述，如 `1.0.0`）。非空、≤64 字符（语义化格式判定属 C8）。 |
| `api_version` | string | 否 | 面向核心的 API 兼容范围，caret（如 `^1` / `^1.5`）。空 = 默认 `^1`。 |
| `requires.min_core_version` | string | 否 | 对核心的最低版本要求（C8 门槛输入）。空 = 不设门槛。 |
| `provides.impls[]` | `{impl_id, kind}` | 否 | 包提供的后端实现。`impl_id` 词法允许 `foo.bar` 分段；`kind` 小写字母/数字/下划线。 |
| `provides.tools[]` | `{domain, tools[]}` | 否 | 包提供的工具域。`domain` **单段、不含下划线**（前缀分派要求）；`tools[]` 为工具名。 |
| `provides.page` | `{nav_label, route}` | 否 | 可选前端页面。`route` 以 `/` 开头、段内小写字母/数字/连字符。 |
| `tagline` | string | 否 | 一句话「对它说什么」（≤40 字），随包装进应用定义的「怎么用」区（v42）。 |
| `examples[]` | string[] | 否 | 可直接发送的示例话术（≤6 条、每条 ≤100 字，空条剔除），教用户怎么开口（v42）。 |
| `usage_guide` | string | 否 | 「怎么用」正文（≤600 字）：适用场景 / 需要提供什么 / 产出 / 边界（v42）。 |
| `permissions[]` | string[] | 否 | 能力白名单枚举，见 §5。 |
| `entrypoint` | string | 是 | 入口脚本，**相对包根**，不含上跳（`..`）/绝对路径。 |
| `integrity.sha256` | string | 是 | 入口文件内容的 sha256（64 位小写十六进制）。见 §6。 |

### 4.1 校验口径复用（不另起一套）

- **`id`**：直接走 `capability.ValidateApp` 的同一 id 判定（用一个只带 id 的最小 App 探针），
  与 skill/expert/app 口径严格同源，不在 apppkg 重写正则。
- **`api_version`**：caret **语法**完全交给 `capability.ValidateApp`；**兼容性**（caret major 是否等于
  核心主版本 `capability.AppAPI`，当前 `1.x`）由 apppkg 按同一 major 判定——`^1`/`^1.5` 兼容，`^2` 不兼容。

## 5. 权限白名单（`permissions`）

最小集，集中声明在 `apppkg.AllPermissions`（新增权限只改这一处）：

| 值 | 含义 |
| --- | --- |
| `read:usage` | 读平台用量摘要（tokens/开销等只读聚合）。 |
| `read:providers` | 读 providers 摘要（模型/网关能力面，只读）。 |
| `net:domain` | 允许网络出站到声明的域（域名细化留给沙箱 `connect-src` 白名单）。 |
| `file:domain` | 允许在声明的文件域内读写（路径细化留给沙箱桥）。 |

未知值**一律拒绝**（fail-closed）。C1 只做「声明 + 校验」；真正的授权收窄在 C7 的沙箱/postMessage 桥
里落地。

## 6. 完整性校验（安全基线第一步）

装载时对 **entrypoint 文件内容**求 sha256，与清单声明的 `integrity.sha256` 比对：

- **一致** → 包通过，进入可装载集合（`Loaded`）。
- **不一致**（被篡改/未正确打包）→ 该包 **FAILED、不装载**，原因明示 `完整性校验失败：…sha256 不符…`。

这是 v37 安全基线（「清单校验 + 沙箱桥」）的第一层。**签名链（第三方作者身份）留 C8/未来**——单机单
操作员前提下，哈希防篡改已够当前基线；本期不做远端市场、不做发布者身份。

完整性校验被抽成**可复用的分发前步骤** `apppkg.VerifyEntrypointIntegrity(pkgDir, manifest)`（C8）：
`load.go` 里的装载期校验与它同口径。需要在「扫描之后、真正执行陌生代码之前」再校一次的场景
（如 C3 拉起 host 子进程前、C6 重装前）应调它——防「扫描通过后、执行前」之间包被替换的窗口。
比对是大小写不敏感的（sha256 规范小写，契约上容忍大小写差异），篡改/入口不可读/路径越界都返回
`*integrityError`（可用 `errors.As` 与其它错误区分）。

> **多文件哈希（增强项，本期不做）**：当前 `integrity.sha256` 只覆盖 **entrypoint 一个文件**。把哈希
> 扩成「声明文件集合（`files: {path: sha256}`）+ 目录树哈希」是自然增强，但会改清单形状，留 C8 之后。
> 单文件哈希在单机单操作员前提下已覆盖「入口被换」这一最危险面（入口是唯一被执行的东西）。

## 6.1 版本门槛（`requires.min_core_version`）

清单可声明 `requires.min_core_version`——「这个包至少需要多新的**核心二进制**」。装载/分发时拿它与
**当前核心版本**比：

- **当前核心版本的唯一来源**是 `internal/version.Version`（构建期 `-ldflags` 注入；开发构建为 `dev`）。
  见 `apppkg.CurrentCoreVersion()`。**注意它不同于 `api_version`**：`api_version` 管「应用 API 契约代
  是否相容」（caret，与 `capability.AppAPI` 比），`min_core_version` 管「核心二进制能力是否够新」——
  两者维度正交、互补，都要满足。
- **核心版本 ≥ 门槛** → 通过。
- **核心版本 < 门槛** → 该包 **FAILED、不装载**，原因明示「核心版本低于包要求的最低版本」（不静默跳过）。
- **版本说不清**（核心 `dev`、或门槛串写得不是语义化版本）→ **fail-closed 拒绝**：不能把「版本说不清」
  当成「满足要求」，否则会让「装在过旧/未知核心上的包静默跑起来」。
- 未声明 `requires` / `min_core_version` 为空 = 不设门槛，恒通过。

版本比较是仓内最小实现（`semver.go`，不引第三方库）：`v?X(.Y(.Z)?)?`，缺省段补 0，预发布/构建后缀
在排序里忽略（单机单操作员无预发布分发场景）。判定函数 `apppkg.CheckMinCoreVersion(manifest, coreVersion)`
是纯函数（测试注入确定版本）。

## 6.2 分发形态（本地目录；明确不做的边界）

本期（二期 C）的分发形态**就是一条本地目录流水线**：

```
扫描 <应用根> 下每个子目录
  → 逐包：解析 + 校验清单（C1）
  → 逐包：完整性 sha256 校验（C1 §6）
  → 逐包：min core version 门槛（C8 §6.1）
  → 汇总为 Distribution{ Loaded, Failed }（单包失败隔离，原因明示）
```

入口是 `apppkg.Distribute(dir, coreVersion)` 与便捷封装 `apppkg.DistributeDefault()`（默认目录 + 当前
核心版本）。装配根启动/刷新时调它扫描已放入的包。

**更新方式**：本期只支持「**本地目录手动放入包 + 重启扫描**」。**自动更新留 `[增强项]`**。

**明确不做**（设计 §17.3 + endgame，单机单操作员不建远端市场；写在这里以免下一个人当成漏做）：
远端应用市场、发布者身份/签名链、自动更新/热更、多租户分发。这些都是「第三方作者成为独立主体」
之后才成立的能力，需要另立决策，不属本期。

## 7. 样例包

最小可装载样例：`backend/internal/core/apppkg/testdata/minimal-app/`（`manifest.json` +
`entrypoint.sh`，其 sha256 与清单声明一致）。可作为写自己包时的起点。

## 8. 校验函数与调用面（给接装载能力的人）

- `apppkg.ParseManifest([]byte) (*Manifest, error)`：解析 + 校验一步到位（严格解码）。
- `apppkg.Manifest.Validate() error`：清单唯一闸门（纯函数）。
- `apppkg.DefaultAppsDir() (string, error)`：`MATE_APPS_DIR` 或 `~/.dsh/mate/apps`。
- `apppkg.Scan(dir) (*ScanResult, error)`：扫描目录 → `Loaded`（成功包）+ `Failed`（失败包带原因）。
- `apppkg.LoadFromDir() (*ScanResult, error)`：`Scan(DefaultAppsDir())` 便捷封装。

C8（分发/版本治理）新增：

- `apppkg.CurrentCoreVersion() string`：当前核心版本（唯一来源 `internal/version.Version`）。
- `apppkg.CheckMinCoreVersion(m, coreVersion) error`：min core version 门槛判定（纯函数，见 §6.1）。
- `apppkg.VerifyEntrypointIntegrity(pkgDir, m) error`：可复用的完整性校验步骤（见 §6）。
- `apppkg.Distribute(dir, coreVersion) (*Distribution, error)`：扫描 + 完整性 + 版本门槛 汇总（见 §6.2）。
- `apppkg.DistributeDefault() (*Distribution, error)`：`Distribute(默认目录, 当前核心版本)` 便捷封装。

C1 做「包长什么样、合法不合法、内容有没有被改」；C8 加上「核心版本够不够新 + 本地目录分发」；
C2（运行期 impl 来源）、C3（隔离子进程）负责「拉起进程/执行代码」。各 WP 职责互不越界。

## 9. 隔离子进程与 host RPC（C3）

> 事实源：`backend/internal/infra/apphost`（父进程侧：进程管理 + RPC 客户端）、
> `backend/internal/infra/apphost/hostkit`（子进程侧 serve 循环 SDK）、`backend/internal/bootstrap/maintenance/apphost.go`
> （`mate app-host` 子命令）。本文与代码冲突时以代码为准。

**每包一进程**：一个应用包对应一个长驻子进程 `mate app-host --pkg=<dir>`。隔离彻底——一个包
panic/崩溃/超限只影响它自己（该包 impl/tool 标 FAILED），内核与其它包不受影响（决策 v37 安全基线：
陌生代码绝不进内核进程）。

**线协议 JSON-lines**：父进程经子进程的 stdin 写请求、读 stdout 的响应；**一行一个 JSON 对象**。
stdout 只走协议帧，诊断一律走 stderr。请求带 `id`（父进程铸造、子进程回带配对），响应二选一：
`{"id":N,"result":{...}}` 或 `{"id":N,"error":{"code","message","class"}}`。

**方法**（`apphost.Method*`）：`handshake`（父告知 `api_version`，子回带包自述）/ `listImpls` /
`listTools` / `invokeTool(name,args)` / `applySlot(impl_id,ctx_snapshot)` / `disposeSlot(impl_id)` /
`health` / `shutdown`。

**资源限额**：单次 RPC 超时（默认 30s）+ 单次响应大小上限（默认 **8 MiB**，超限熔断、该 Host
标 FAILED）。崩溃重启退避（默认连续 3 次失败后永久 FAILED）。流式 credit 分块回传、进程级内存/CPU
配额为 **[增强项]**（本期非流式 + 单次上限已挡住"一帧撑爆内存"的最危险路径）。

**错误分类**：host 错误码映射到 `internal/core/failure` 的 `Class`（如 `unknown_tool`→`request_rejected`、
`tool_failed`→`service`、`api_version_mismatch`→`unsupported`、`rpc_timeout`/`host_gone`/`response_too_large`→`connection`），
见 `apphost.ClassOfCode`。
