# ark-mate 部署与运维

> 面向**个人 Agent 助手**（两层：底座 + 产品，决策 v8①）的单机部署：单操作员（决策 v3③：无多用户/团队语义、
> 无登录鉴权）。**交付重心是桌面应用**（决策 v8③）——桌面形态见
> [architecture.md](./architecture.md) §2 与 [acceptance-matrix.md](./acceptance-matrix.md)；本文讲的是**服务端形态**与
> 底座运维，它们冻结为**维护态**（保留可用、继续被测试、可修缺陷；**不加新功能、不进主线排期**）。
> **浏览器入口只是开发过程使用的形态**，不是交付面——服务端形态的界面按「自用 / 开发」对待，不承诺 UI 体验。
> 本文只写**可复核**的部署形态与参数；除明确标注**未执行**的一项（systemd 单元）外，命令均在本机实跑过
> （容器镜像 §4 已实跑，见文末「验证记录」）。形态与沿革见
> [architecture.md](./architecture.md) 与 [decisions.md](./decisions.md)。

## 1. 交付形态（服务端形态，维护态）

| 形态 | 命令 | 产物 | 说明 |
| --- | --- | --- | --- |
| **桌面应用（主线交付）** | `make desktop/dist` | dmg / Windows 安装器 | 随包运行时，用户机器不需要 Go / Node / 预装底座；**详见[架构总览](./architecture.md) §2、§8**，本文不复述 |
| 自建二进制（外挂 dist） | `cd backend && CGO_ENABLED=0 go build -o mate ./cmd/mate` | 二进制 + 需自备一份 `apps/web/dist` | 核心**不再内嵌任何前端资产**（单二进制形态已于 2026-10-10 取消）：起服必须 `./mate serve --web-dir /abs/path/to/apps/web/dist`（或 `MATE_WEB_DIR`），没给就拒绝启动（退出码 3）。**构建不需要 CGO**（2026-10-12 起）：SQLite dialect 随 golib v1.32.22 换成纯 Go 的 `ncruces/go-sqlite3`，`0` 可编可跑、编译不需要 C 工具链（实测 `CC=false` 也编得出）、`GOOS=linux` 交叉编译得到静态产物（`file` 报 `statically linked`，于是下面那条 `FROM scratch` 重新可用），`=1` 则要 C 交叉工具链（实测直接编不过）；代价是体积（golib 实测最小程序 +10.99 MiB）。2026-10-11 前后那版 driver 是 CGO 的，那时的 `0` 会编出运行期连不上库的 stub |
| 仅后端内核 | `make build` | 无独立产物 | 任何构建都不带资产，`make build` 只编后端；面板要由 `--web-dir` / `MATE_WEB_DIR` 提供，没给即拒启（退出码 3） |
| 前端产物独立托管 | `make web` | `frontend/apps/web/dist` | 前端可独立部署（需自行把 `/v1` 反代到后端，含 WebSocket 升级） |
| 容器 | `make image` / `docker build -t ark-mate .` | 镜像（约 140 MB） | 多阶段：Node 构建前端 → Go 编译 → 镜像内 `COPY` 一份 dist 并设 `MATE_WEB_DIR` 指过去 → 精简运行时；两个 target，可选内置 dsh（见 §4） |

> 服务端形态的界面走浏览器（**开发 / 自用**路径）：前端开发态是 `make dev`（vite :5173 + 代理 `/v1`，
> 唯一 HMR 循环）；这条路径**不构成交付承诺**。

前置：Go（见 `backend/go.mod`）、Node（`frontend/.nvmrc`）+ pnpm（`frontend/package.json` 的
`packageManager`）。`make web` 会校验 Node 主版本与 `.nvmrc` 一致。

## 2. 运行

```bash
# 最小启动（数据默认落 ~/.ark-mate/）
GIN_MODE=release ./mate serve --addr 127.0.0.1:10001 --web-dir /abs/path/to/apps/web/dist
curl -s http://127.0.0.1:10001/v1/mate/health     # {"code":0,"msg":"ok"}
```

浏览器打开 <http://127.0.0.1:10001>（面板就是 `--web-dir` / `MATE_WEB_DIR` 指的那份 `apps/web/dist`；
核心不自带资产，没给目录它根本不启动）。

### 2.1 监听地址与安全边界（重要）

- `--addr` 缺省时读 `MATE_ADDR`，其**默认值为 `127.0.0.1:10001`（仅回环）**——`mate serve` 裸跑
  只监听本机（实测 `lsof -nP -iTCP -sTCP:LISTEN` 显示 `127.0.0.1:10001`，非 `*:10001`）。控制面
  **无鉴权**，因此：
  - 单机自用：保持默认即可，无需额外参数；
  - 需跨机访问：**先设 `MATE_TOKEN`（A6 单操作员令牌，见 §2.3.2）**，再显式
    `--addr 0.0.0.0:10001`（或 `MATE_ADDR=0.0.0.0:10001`）并置于反向代理/内网之后，用防火墙或
    安全组限制来源；**不要**无令牌直接暴露到公网。容器场景需显式 `MATE_ADDR=0.0.0.0:10001`（见 §4）。
- 生产建议 `GIN_MODE=release`（否则每次启动打印 debug 模式告警）。
- 会话 wire transcript（`~/.ark-mate/transcripts/*.jsonl`）与 SQLite 库含**完整对话原文**，按敏感数据
  对待（文件权限）；**v20 起库里还有明文密钥**，所以 `backups/` 目录 0700 / 备份文件 0600 是硬要求（§6），
  且**共享 / 多用户机器上不再安全**。

### 2.2 数据目录与持久层

| 路径 | 默认 | 覆盖 | 内容 |
| --- | --- | --- | --- |
| SQLite 库 | `~/.ark-mate/mate.db` | `MATE_DB` | 会话/事件/runs/messages/权限记忆（读模型与事实源） + capability/automation 定义 + **v11 起 provider 档案（`providers` 表）与平台模型清单（`models` 表）**。**v11 起它是 provider 档案的权威**——所以它必须在备份清单里（见 §6），"只备 `providers.json`"从这一版起不再等价 |
| transcript | `~/.ark-mate/transcripts/` | `MATE_TRANSCRIPTS` | 每会话一个 `<id>.jsonl`（schema v1，append-only） |
| provider 档案（导入/导出） | `~/.ark-mate/providers/providers.json` | `MATE_PROVIDERS` | **v11 起它就不是档案的事实源**（权威在 DB 的 `providers`/`models` 两表）；v19 起这个目录**只剩这一件事**（此前叫 `agents/`，因为当时还装 agent 定义——agent 概念已整体并入 runtime，见设计「决策变更 v19」）。旧目录里的 `providers.json` 会被装配期**一次性导入**并打 WARN 指向新路径，**旧文件不删**。写面一个字节都不回写它，回退旧二进制前要 `mate provider export` 回灌，见 §6；迁移前的快照是 `providers.json.bak-v11`（**建议一起备份**，它是回退窗口里唯一那份"迁移前真相"）；v20 的凭据镜像 `<MATE_HOME>/credentials.json` 是**另一份落点**（默认 `~/.ark-mate/credentials.json`，见下一行），不再从本目录派生 |
| 凭据镜像（v20） | `~/.ark-mate/credentials.json` | 无（**没有环境变量覆盖项**） | provider 密钥与 MCP 托管密钥的**严格 `ref → value` 映射**（`providers/<id>` 与 `store:<ns>/<name>` 两类引用装在同一份文件里），权限 **0600** 且**不合即失败**（fail-loud），写入用临时文件 + rename 的原子替换。**事实源是库**（`providers.key` 列 + `mcp_secrets` 表）：写路径先落库、再双写它，镜像写失败只 WARN 并记进 `problems[]`（删掉它不丢任何东西）；memory 模式没有库，那时文件即事实源、写失败就是整笔失败。**库里没有而文件里有 → 不自动回灌**：`mate doctor` 报出差异并指路显式导入。读值走**四层回落**：**继承环境（只读，最高）> 托管文件 > `<cwd>/.env` > `<MATE_HOME>/.env`**（只作用于 provider 拨号密钥；MCP 的 `env:<NAME>` 是用户在定义里显式点名，不再套四层）。`<cwd>` = **用户启动 `mate serve` 的那个目录**（不是各进程各自的 cwd）：MCP 网关是底座 spawn 的子进程、cwd 常常是会话工作区，所以核心在生成网关条目时把凭据 cwd 显式交下去（`MATE_MCP_CRED_CWD`），网关拿不到时回落到自己的 cwd 并**打一条 WARN**（落在 `<MATE_HOME>/logs/<日期>/ark-mate-mcp_full.log`，网关诊断不走 stderr）。**它取代了 v13 的主密钥文件 + `vault/` 密文面**（两者在 v20 迁移后整体删除） |
| 自动化定义 | `~/.ark-mate/automations/<id>.json` | `MATE_AUTOMATIONS` | 定时任务。**`MATE_STORE=sqlite`（默认）时这个目录不是事实源**：定义与运行态存在 DB 的 `automations` 表里（**定义列** `body`+`revision` 与**运行态列** `last_fired_at`/`last_status`/`last_reason`/`last_conversation_id`/`consecutive_failures`/`recent_runs` 分列；`state` 属于定义，因为启用/停用是用户操作），本目录只作**导入/导出**位置（装配期自动导入一次、`mate automation import\|export` 手动搬运；导入会把文件里的运行态键切进运行态列，导出时再合成同一个文件）。memory 模式下 `<id>.json` 仍是唯一定义 + 运行态载体（`schedule`/`target` 是用户写的，`last_*`/`recent_runs` 是平台写回的）——**备份它就是备份任务**（含运行史）。两种模式下手工编辑后用 `GET /automations` 确认装载成功（坏定义会出现在列表里并带 `error`，不会静默消失） |
| 能力定义 | `~/.ark-mate/capabilities/` | `MATE_CAPABILITIES` | `skills/`、`mcp/`、`tools/` 目录。**`MATE_STORE=sqlite`（默认）时这个目录不是事实源**：能力定义存在 DB 的 `skills`/`mcp_servers` 表里，本目录只作**导入/导出**位置（装配期自动导入一次、`mate capability import\|export` 手动搬运；`tools/` 不参与持久化）。skill 定义可带 `enabled:false`（**禁用 = overlay**：正文与 `runtimes` 保留，只停止投放；`PATCH /skills/:id` 写回此键）。skill 的投放对象是 `runtimes: []`（空 = 全部 runtime；旧的 `sources[]` 已退役，定义里还有它会在装载期挂 `legacy_sources_key` 告警并提示 `mate capability migrate`）。MCP 定义里 `env`/`headers` 是明文，密钥放 `secret_env`/`secret_headers` 且值只能是 `store:<ns>/<name>`（v20 起；托管密钥在库的 `mcp_secrets` 表，旧前缀 `vault:` 指的是已删除的机制，导入时会被显式翻译成 `store:` 并 WARN）或 `env:<ENV_NAME>`（§13.3 M1） |
| 投影根覆盖 | 空（用 runtime 声明的默认根） | `MATE_PROJECTION_DIRS` | `runtime:kind=dir` 列表（如 `myrt:skill=/data/agents/skills`；`myrt` = 在册的那个底座 id，内置的是 `dsh`）。**只覆盖根、不改形态**：落到哪个文件名、写成哪种格式由 runtime 的投影声明决定。空 = 全用 adapter 声明的原生根（内置 `dsh` 的原生根是 `~/.agents/skills` + `<id>/SKILL.md`）。**取代**了旧的 `MATE_SKILLS_MATERIALIZE_DIR` / `_DIRS`（旧键仍报错，但报错会认出它并给出新键与等价写法） |
| 默认工作空间 | `~/.ark-mate/workspaces/default/` | `MATE_WORKSPACE_DIR` | 建会话未指定 `cwd` 时的落点（v14）。它是**一条落库的工作空间登记**（`workspaces` 表，`is_default=1`），不是"每会话一个目录"——同一台机器上所有未指定目录的会话共享它。目录由装配期建立（`<MATE_WORKSPACE_DIR>/default`），**只在登记表为空时**创建：删掉它之后不会自动重建 |
| 工作区起始位置偏好 | 空（无偏好） | `MATE_WORKSPACE_ROOTS` | 逗号分隔绝对路径（config.json 亦可写 JSON 数组）。**它是"目录浏览从哪儿起步"的偏好，不是允许范围**（v14.5 起：选工作区是全系统，任何已存在的目录都能浏览、都能当工作空间）。**代建根由平台隐式并入**，无需手写进来；面板「设置」页可改（落盘到 `<配置目录>/config.json`） |

持久层开关：`MATE_STORE=sqlite|memory|auto`（空或 `auto` = 按库文件定：已存在且非空 → sqlite，
否则 memory）。**非法值启动即报错**并列出合法取值——不静默回落 auto，否则一个错字就可能悄悄
切到另一种持久化模式。**memory 模式无 runs 读模型**：`GET /usage/summary` 返回 40501
`usage_stats_unsupported`（明确降级，不返回假零值）。

### 2.2.1 工作空间（会话沙箱边界）的运维要点

会话工作空间 = 该会话的 ACP `cwd`，**创建时定稿、之后不可变**（要换目录就新开会话）。

- **默认行为（v14 变更）**：`POST /conversations` 不带 `cwd`/`workspace_id` → 会话落在
  **默认工作空间**（`<MATE_WORKSPACE_DIR>/default`，一条落库的登记），响应回
  `workspace_temporary=true`。**不会**回落 serve 进程的工作目录——那会让所有会话共用
  一个沙箱边界（旧行为，已移除）。
  > **v14 之前是按会话 id 代建独占目录**（`<MATE_WORKSPACE_DIR>/<会话id>/`）。现在不再这么做：
  > 两个未指定目录的会话会**共享**默认工作空间，因此**能读到对方的文件**。要隔离就在新建时
  > 选一个自己的目录（或 `POST /conversations` 传 `cwd`/`workspace_id`）。旧版本留下的
  > `<会话id>/` 目录**不会被删**（删除用户数据不是升级该做的事），新会话也不再新增这类目录。
  > 注意平台**不登记**它们在哪：迁移发生在配置装载之前（拿不到 `MATE_WORKSPACE_DIR`），
  > 所以这件事没有可靠的机器可读出口——磁盘敏感的场景请自行按目录形状清理那些空目录。
  > `meta.migration_notes_v14` 只如实记录迁移时的会话数与"没有创建任何登记行"。
- **关闭代建**：`MATE_WORKSPACE_DIR` 设为空 / `off` / `disable` / `none` → 不带
  `cwd`/`workspace_id` 的建会话请求报 `workspace_required`（40001）。适合"目录必须由调用方
  显式指定"的受控部署。此时也**没有**默认工作空间（`GET /workspaces` 的 `default_id` 为空）。
- **工作空间登记表（v14）**：`GET /workspaces` 列出用户登记的工作空间（顺序、目录现状、
  会话数），`POST` 登记一个已存在的目录（默认 get-or-create），`PATCH` 改名，`DELETE`
  只删登记（**目录与会话一概不动**，其会话回到默认工作空间），`POST /workspaces/:id/move` 重排。
  它要求 `MATE_STORE=sqlite`：memory 模式下读面返回空列表 + `writable:false`，写面 40501
  `workspace_schema_read_only`（面板据此不渲染创建入口）。
- **登记与建会话共用同一条校验**：`POST /workspaces` 走的是 `ValidateWorkspace`（绝对 / 存在 /
  是目录 + 符号链接规范化），所以不会出现"能登记、建不了会话"。**这条校验里没有名录这道门**
  （v14.5 起）：任何可用目录都能登记、都能当工作空间。
- **目录浏览面**：`GET /workspaces/browse` 是 Web 端替代原生目录选择器的必要能力（只列目录，
  不列文件）。**它可以枚举任意可读目录**——这是产品口径（v14.5：选工作区是全系统），不是配置疏漏。
  桌面入口更进一步：那个动作直接调**宿主原生选择器**（`__MATE_DESKTOP__.native.pickDirectory`），
  与应用内浏览**互斥编排**（载体在走原生、不在走浏览；判定只有一份 `directoryPicker.ts`）。
  对外提供服务（反代/容器/多人）时的实际防护只剩一层：
  1. 设 `MATE_TOKEN`（平台默认只绑回环，对外必须先鉴权）——**这是唯一的一道闸**；
  2. `MATE_WORKSPACE_ROOTS` 现在只影响"目录浏览从哪儿起步"，**不构成边界**：设了它，能选的目录也不会变少。
     要真正限制可写范围，请用运行用户（容器 / 系统账号）的文件权限来收。
  符号链接按真身判定（`entries` 与 `files` 都跟随真身取属性）。
- **"看资料"面**：`GET /workspaces/files?path=` 列文件 + 大小 + 修改时间，与只列目录的 `browse`
  （选工作空间）分工：**files 的条目不可作为 `cwd`**（文件不是合法工作空间）。
  旧的 `GET /workspaces/libraries`（资料库 = 名录下的一级子目录）已随名录语义一并删除。
- **容器化部署的两个硬要求**：① `MATE_WORKSPACE_DIR` 必须落在**可写挂载**上（默认
  `<数据目录>/workspaces`，即 `~/.ark-mate/workspaces`；容器里若 `HOME` 不在卷上，代建目录会写进
  可写层，容器重建即丢）；② 能选的工作空间只能是**容器挂载视图内**的目录——宿主机目录要当工作
  空间，必须先 bind mount 进来，并以**容器内路径**作为 `cwd`（agent 也在同一容器内，故这才是
  对 agent 正确的那个路径）。
- **已知限制**：平台注册表 MCP 随 `session/new` 下发，其**相对路径参数仍以 agent 进程的 cwd
  解析**（serve 进程目录），而非会话工作空间。需要工作空间相对路径的 MCP 请写绝对路径。

### 2.2.2 起始位置偏好的设置面（面板可改）

`GET · PUT /settings/workspace` 让面板直接查看/改写**工作区起始位置偏好**，不必再登机器改环境变量。
**它不是安全边界**（见上一节）：这份名单只决定目录浏览从哪儿起步、以及面板里的快捷入口。

- **写在哪**：`<配置目录>/config.json` 的 `workspace_roots` 键（默认 `~/.ark-mate/config/config.json`）。
  写回是 read-modify-write + 原子替换：文件里其它键原样保留，写的是 JSON 数组（含逗号的路径也能
  原样往返），空名单 = 删掉该键（回到"没有偏好"）。**没有第二份 settings 文件**——它只应有一个事实源。
- **写不进去的时候**（这正是以前最容易踩的坑：面板显示保存成功、重启后被覆盖）：
  `config.json` 之上还有 env 文件、进程 `MATE_*`、CLI flag 三层，任一层设过该键，写配置文件都无效。
  所以接口**拒绝写**并回 `settings_locked`，同时 GET 回 `locked_by`（指出是哪个来源锁定：环境变量名 /
  `<配置目录>/env` / 命令行参数），面板据此转只读。要交还控制权，就把该来源里的 `MATE_WORKSPACE_ROOTS`
  去掉。`settings_readonly` 则是另一种情况：该进程没有配置目录（如 `demo` 冒烟进程），不落盘。
- **写入的校验比配置文件严**：面板设的每一条都要求「绝对路径 + 存在 + 是目录」（配置文件只校验绝对路径，
  允许挂载晚于进程出现）。交互式设置的反馈必须在当下给——否则保存成功、下次进面板就是错误页。
- **影响面**：只影响"目录浏览从哪儿起步"与面板里的快捷入口列表。已建会话的 `cwd` 创建即定稿，
  平台不回头否决；目录浏览与建会话都不因它被拒。
- **防护等级（务必读）**：能调到这条接口的人可以改偏好，但那已经无关紧要——**真正的闸只有
  `MATE_TOKEN`**（以及运行用户的文件权限）。对外提供服务时请设 `MATE_TOKEN`；
  `MATE_WORKSPACE_ROOTS` 设在**进程环境变量**里仍会让面板变只读（`mate config` 会打印
  `面板设置 只读（由该来源锁定）`），但那只锁住"改偏好"，不锁可访问的目录范围。

### 2.3 环境变量（全部来自 `backend/internal/config/config.go`）

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `MATE_ADDR` | `:10001` | 监听地址（见 §2.1） |
| `MATE_STORE` | auto | `auto` / `sqlite` / `memory`（空同 `auto`）；非法值报错 |
| `MATE_DB` | `~/.ark-mate/mate.db` | SQLite 路径 |
| `MATE_TRANSCRIPTS` | `~/.ark-mate/transcripts` | wire transcript 目录 |
| `MATE_PROVIDERS` | `~/.ark-mate/providers` | provider 档案的**导入/导出目录**（权威在 DB 的 `providers` 表；只在 sqlite 模式当载体，memory 模式下文件仍是事实源）；v19 起它只装 `providers.json` 一件事（旧目录名 `agents/` 里的那份会被启动时**一次性导入**并 WARN，旧文件不删）。v20 的凭据镜像落在 `<MATE_HOME>/credentials.json`（默认 `~/.ark-mate/credentials.json`），**没有配套的环境变量覆盖项** |
| `MATE_CAPABILITIES` | `~/.ark-mate/capabilities` | 能力定义目录：memory 模式是装载源，sqlite 模式是**导入/导出目录**（定义在 DB 里） |
| `MATE_COMMAND_POLICY` | `~/.ark-mate/command_policy.json` | 命令门禁规则文件（A2，见 §2.3.3）。**文件不存在 = 未启用**；存在但 JSON/规则非法 = **拒绝启动**；空 / `off` / `disable` / `none` = 关闭 |
| `MATE_AUTOMATIONS` | `~/.ark-mate/automations` | 自动化定义目录：memory 模式是装载源（`<id>.json`，定义与运行态 overlay 同文件，手工可读），sqlite 模式是**导入/导出目录**（定义与运行态在 DB 的 `automations` 表里） |
| `MATE_AUTOMATION_TICK` | `30s` | 调度循环 tick 间隔；**`0` = 关闭整个 automation 调度**（维护窗口用，不是"最快跑"） |
| `MATE_PROJECTION_DIRS` | 空 | 投影根覆盖：`runtime:kind=dir` 逗号列表（`config.json` 可写 JSON 数组），键如 `dsh:skill`（`<id>` = 在册的那个底座 id；旧写法 `deepseekharness:skill` 会被归一，见下）；值是**绝对**目录。空 = 用 adapter 声明的默认根。只覆盖根，Layout/Format 仍由 runtime 声明。旧键 `MATE_SKILLS_MATERIALIZE_DIR`/`_DIRS` 已改名：仍报错，但报错含新键与等价写法 |
| `MATE_WORKSPACE_DIR` | `~/.ark-mate/workspaces` | 代建工作空间根目录；**空/off/disable/none = 关闭代建**（建会话未指定 `cwd` 时报 `workspace_required`，不回落 serve 进程目录） |
| `MATE_WORKSPACE_ROOTS` | 空（无偏好） | **工作区起始位置偏好**（逗号分隔绝对路径；`config.json` 里可写 JSON 数组，含逗号的路径只有数组能表达）。**它不是允许范围**（v14.5 起：选工作区是全系统，任何已存在的目录都能浏览、都能当工作空间，不再有 `workspace_outside_roots` 这类拒绝）：这份名单只决定目录浏览从哪儿起步、以及面板里列哪些快捷入口。**平台代建根（`MATE_WORKSPACE_DIR`）恒被隐式并入生效名单**，无需手写。在**进程 env** 里设本变量会把面板设置面锁成只读（见 §2.2.2） |
| `MATE_RUN_TIMEOUT` | 0（关闭） | 单轮看门狗（如 `30m`），超时自动 cancel 并发终态事件 |
| `MATE_PERMISSION_TIMEOUT` | `30m` | 挂起权限自动拒绝超时 |
| `MATE_ELICITATION_TIMEOUT` | `30m` | 挂起结构化提问自动取消超时 |
| `MATE_IDLE_TIMEOUT` | `5m` | 暖进程空闲回收（0 = 关闭） |
| `MATE_REAP_INTERVAL` | `5m` | 空闲回收扫描间隔 |
| `MATE_HANDOFF_MAX_CHARS` | `6000` | 跨 runtime / 无原生 resume 时的历史摘要预算（0 = 关闭） |
| `MATE_GENERATION_LIMIT` | `4` | 同一配置（runtime + 原生权限 + provider）下允许并存的**能力代际**上限（0 = 不限制）。代际按能力指纹内容寻址、指纹不同即各起一个进程（能力集是进程级的）；达上限时先淘汰「无会话引用且空闲」的代际，仍超限则**带码拒绝**新建（`40901 generation_limit_reached`），绝不静默复用错误的能力集 |
| `MATE_PROC_REGISTRY` | `<dir(MATE_DB)>/processes.json` | runtime 子进程注册表（孤儿回收用）；`off`/`disable`/`none` = 关闭登记与回收 |
| `MATE_REAP_GRACE` | `2s` | 孤儿回收 SIGTERM → SIGKILL 的宽限期 |
| `GIN_MODE` | `debug` | 设 `release` 关闭 gin debug 告警 |

### 2.3.0 时区与内嵌 tzdata（M4 起必读）

定时任务（`schedule.timezone`）用 **IANA 时区名**（`Asia/Shanghai`），平台把它算成真实时刻。
`mate` 二进制**内嵌了一份 IANA 时区库**（`cmd/mate` 里的 `import _ "time/tzdata"`，实测
**+403 KB**），因此容器里**不需要** `/usr/share/zoneinfo`：

```bash
# 在只有二进制、零 zoneinfo 文件的 scratch 镜像里（实测）
docker run --rm -p 127.0.0.1:10098:10001 --tmpfs /data \
  -e MATE_ADDR=0.0.0.0:10001 -e MATE_STORE=memory mate-tzcheck serve
curl -s -X POST http://127.0.0.1:10098/v1/mate/automations/preview \
  -H 'content-type: application/json' \
  -d '{"schedule":{"kind":"daily","timezone":"Asia/Shanghai","hour":9,"minute":0},"count":1}'
# → {"code":0,...,"at":"2026-09-24T01:00:00Z","wall":"2026-09-24 09:00 CST","timezone":"Asia/Shanghai"}
```

这条不是"锦上添花"：**去掉那个 import 的同一份二进制在同一镜像里报
`40001 未知时区 Asia/Shanghai`**（对照实测）。失败形态是最难查的一类——任务不是不跑，而是在
**错误的时间**跑（或直接拒绝执行定义），且宿主机上完全正常。所以：

- 换用 `scratch` / `distroless` / 任何不带 tzdata 的基础镜像时**照常工作**，无需额外挂载；
- 需要在时区库里加自定义规则（如临时改某地 DST）时才需要挂载/覆盖，内嵌库是**兜底**而非唯一来源
  （Go 的查找顺序：`ZONEINFO` → 系统目录 → 内嵌）；
- 二进制体积比不带时区库大约 400 KB，可接受（见 §1 的镜像体积）。

### 2.3.1 配置目录 `.ark-mate/config`（A4）

环境变量多到需要脚本管理时，改用配置目录（目录不存在 = 完全维持环境变量行为）：

```
~/.ark-mate/config/config.json   # 主配置（JSON 对象，键名可用 addr 或 MATE_ADDR）
~/.ark-mate/config/env           # dotenv 风格（KEY=VALUE；# 注释、空行、可选 export 前缀与成对引号）
```

`config.json` 里多值键（`workspace_roots`、`projection_dirs`）可以写成 **JSON 数组**，
也可以照旧写成逗号分隔字符串；单值键写数组会报错。数组不是语法糖：路径里含逗号时只有数组
能表达，而面板保存起始位置偏好用的就是数组形式（见 §2.2.2）。

同目录可选 `runtimes.json`（A5）：远程 runtime 声明数组
`[{"id","name","transport":"ws|http","endpoint","headers"}]`，启动时注册进 runtime 注册表，
skill 定义的 `runtimes[]` 候选集即可引用它；与内置 id 冲突、传输/URL 形态非法都会让 `mate serve`
启动即退出（退出码 2）。远程 runtime 无本地命令，可用性判定只看端点是否声明，可达性在拨号
阶段判定（失败为 `50201` 连接类错误，端点地址出现在错误信息里）。

支持的键（与 §2.3 表一一对应）：`addr` `transcripts` `db` `store` `providers` `capabilities`
`automations` `automation_tick` `workspace_dir` `workspace_roots` `projection_dirs`
`idle_timeout` `reap_interval` `run_timeout` `handoff_max_chars` `generation_limit`
`permission_timeout` `elicitation_timeout` `proc_registry` `reap_grace` `token` `mcp_auto_probe`
`asset_watch`
`native_permission` `mcp_gateway` `identity_mode`；另可用 `MATE_*` 原名、`-` 写成分隔符，以及少量别名
（`transcript`/`db_path`/`provider_dir`/`capability_dir`）。

`identity_mode`（`auto` 默认 | `system` | `user` | `off`）控制**产品身份的交付面**：`auto` 把身份
写进系统提示面（平台自有 overlay，经底座官方 `--patch` 递入）并保留首轮块兜底；`system` 只走系统提示
（失败即无身份，不静默回退）；`user` 只走首轮块；`off` 都不交付。后两者会在启动时**主动撤回**已经装上的
身份 overlay（删文件 + 摘启动参数）——只"不写"会让上一份继续生效，而界面上没有任何一行会说这件事。
身份只影响模型的自述，不影响任何会话能否运行；交付失败只留一条诊断（`identity_patch_failed`），不拒绝启动。

`mcp_gateway`（`auto` 默认 | `off`）控制**远程 MCP 是否经平台侧网关**：`auto` 时远程传输
（`sse`/`http`/`streamable_http`）的 MCP 由平台代连——底座只看见一条 stdio 条目
（`mate-gateway` = `<平台可执行文件> mcp bridge --runtime <id>`），凭据不进底座可见面，
且条目**恒定**（不随"有几台远程 server"变化）⇒ 增删改远程 server 都不换代际，网关下一次取数即生效；
`off` 回退到"远程按底座声明的承载能力直连注入"的老路。stdio server 两种模式下都由底座直连。

已改名的键仍会让启动失败（"非法键名即报错"这条规则不软化），但报错会**认出它**并给出新键与
等价写法：`skills_materialize_dir` / `skills_materialize_dirs` → `projection_dirs`
（`skills_materialize_dirs=myrt=/x` ≡ `projection_dirs=myrt:skill=/x`，`myrt` = 在册的那个底座 id）；
`agents` 与旧的 agent 目录键（`agent` 前缀那几个别名）→ `providers` / `provider_dir`（v19：那个目录里只剩 `providers.json` 一件事）。
升级动作就是改这一行——不做静默等价，否则用户会以为已经改完。

`workspace_roots` 键**可能被面板改写**（`PUT /settings/workspace`，原子写 + read-modify-write，
保留文件里其它键），所以手工编辑时留意别和面板同时改；被更高优先级来源锁定时面板只会拒绝写。

**优先级**：内置默认值 < `config.json` < `env` < 进程环境变量 `MATE_*` < CLI flag。所以
「文件里写基线，环境变量做覆盖，flag 做临时调试」三段式可用。目录路径由 `MATE_CONFIG_DIR`
决定（默认 `~/.ark-mate/config`）。

**严格性**：未知键、非法 JSON、非法 duration/整数都会让 `mate serve` 在启动阶段退出（退出码
2，stderr 带文件路径与合法键清单）——不静默回落，避免「改了配置却没生效」。核对用
`mate config`（文本）或 `mate config --json`（含每项来源 `default|config.json|env|env(process)`）。

systemd 场景可直接用同一目录：`EnvironmentFile=-%h/.ark-mate/config/env`（§3 单元里已注释示例）。

### 2.3.2 单操作员令牌（A6）

`MATE_TOKEN`（或配置目录 `token`）非空时，除 `/health` 外**全部** `/v1/mate` 路由要求令牌：

```
MATE_TOKEN=$(openssl rand -hex 24) ./mate serve --addr 0.0.0.0:10001
curl -H "Authorization: Bearer $MATE_TOKEN" http://127.0.0.1:10001/v1/mate/sessions
```

- REST：`Authorization: Bearer <token>`（大小写不敏感，允许多余空白）；失败返回 HTTP 401 +
  信封 `{code:40101,...}`（不是 HTTP 200 的业务错误——鉴权失败在传输层就该是 401）。
- WS `/v1/mate/ws/events`：浏览器无法为握手设置请求头，故接受 `?token=<token>`。
- `/health` 保持免鉴权（容器编排/反代 liveness 探针）。
- 恒定时间比较；令牌不出现在日志里，`mate config` 只打印掩码。
  这里的「不出现在日志里」由 golib 的访问日志默认脱敏保证（`ginmiddleware.AccessLog`
  自 golib v1.32.16 起按字段名脱敏 `*token*`/`*secret*`/`*password*` 等，`?token=` 落进
  日志时值为 `***`）。**反代/网关自身的访问日志不在其内**，对外暴露时仍需在那一层
  自行处理查询串。
- 面板：顶栏「令牌」按钮填写（localStorage，仅本浏览器）；401 时自动弹出提示。
- 这是**单操作员**令牌，不是多用户/RBAC：无用户、无角色、无审计区分。多人共用请置于
  带认证的反向代理之后（或等后续切片）。

### 2.3.3 命令门禁（A2）

门禁在**权限请求的裁决点**上运行：先按 `toolCallId` 回查同一次工具调用的入参，取出
`{"command": "..."}`，再按规则表裁决。规则文件默认 `<MATE_HOME>/command_policy.json`
（`MATE_COMMAND_POLICY` 可改）。**文件不存在 = 不启用**，行为与没有这条门禁逐字相同；
**文件存在但 JSON/规则非法 = 拒绝启动**（坏文件不能被当成"没有规则"）。

```json
{
  "rules": [
    {
      "name": "deny rm",
      "pattern": [["rm"]],
      "decision": "forbidden",
      "match_samples": [["rm", "-rf", "/tmp/x"]],
      "not_match_samples": [["git", "status"]]
    },
    {
      "name": "allow git status",
      "pattern": [["git"], ["status"]],
      "decision": "allow",
      "match_samples": [["git", "status"]]
    }
  ]
}
```

语义（每一条都有判据）：

- **有序**：第一条命中的规则说了算；`pattern` 是**前缀 token 模式**，每个位置可写一组备选
  （`[["git","hg"],["status"]]`）；`host_executable` 列出的绝对路径额外允许按 basename 回退
  （`["/bin/bash"]` 能匹配 `bash`）。
- `decision`：`allow`（直接放行，不问人）/ `prompt`（走既有审批）/ `forbidden`（直接拒绝，连审批都不给）。
- `match_samples` / `not_match_samples` 在**加载期**求值：样例不通过的规则**整表拒绝**——
  一条写错的前缀规则在运行时的表现是"该拦的没拦"或"该放的全拦"，两种都不报错。
- **只对简单命令自动裁决**：命令里出现引号、转义、变量、管道、`;`/`&&`、重定向、子 shell、
  通配符、换行等任一字符 → 判为复杂命令，**一律 prompt**。拿空白切分去匹配
  `echo a; rm -rf /` 会得到错误的 token，那正是"以为门禁看过了"的来源。
- **顺序**：`forbidden` 先于会话级记忆——用户说过"这类工具始终允许"，也不能放行规则里
  明确禁止的命令；`allow` 直接放行，`prompt`/未命中才回到记忆与人工审批。
- **编辑面**：设置 · 命令规则（`GET /command-policy` / `PUT /command-policy`）。保存**先校验再原子落盘**并立即生效；`match_samples` / `not_match_samples` 仍只能手写文件（面板原样保留，不提供编辑）。

### 2.4 底座前置条件

- `dsh`：需本机可执行 `dsh --profile acp`（同 `$DSH_HOME` 持久化根）。

构建期固定的底座为 `dsh`（默认）——**要求本机装有对应底座并完成 provider 登录**。

> **`echo` 演示底座已删除（M0）**：此前 daemon 会自派生 `mate acp-demo` 子进程充当免安装、免模型
> key 的 ACP 对端（用于冒烟与权限/提问演示），`mate acp-serve` 还能把它暴露成远程端点。这两条
> 路径已随 echo **彻底移除**，内置 runtime 收敛为唯一内置的 `dsh`。远程传输**功能**
> 不受影响（`.ark-mate/config/runtimes.json` 声明远程端点照常工作），失去的只是开箱演示；覆盖缺口
> 台账见仓库 `.dsh/docs/notes/2026-09-22-echo-removal-coverage-ledger.md`。

> **没有 agent 定义面（v19）**：agent 概念已整体并入 runtime（底座由**构建期固定**、用户不可自定义），
> 所以此前那条"内置 agent 只读"的约束随表 `agents` 与 `/agents/*` 一并消失——没有可编辑的 agent
> 资源，也就没有"只读与否"这个问题。会话身份只剩 `runtime_id`，诊断见 `GET /v1/mate/runtime/diagnostics`。

## 3. systemd（裸机/VM）

`/etc/systemd/system/ark-mate.service`：

```ini
[Unit]
Description=ark-mate 控制面 daemon（单操作员 ACP 会话底座）
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=mate
Group=mate
WorkingDirectory=/opt/ark-mate
Environment=GIN_MODE=release
Environment=MATE_ADDR=127.0.0.1:10001
# 数据根显式给死：ProtectHome=read-only 会把家目录变成只读，而 v20 的凭据镜像
# 落在 <MATE_HOME>/credentials.json —— 不设它就会落到 `~/.ark-mate` 下写不进去。
# ReadWritePaths=/var/lib/ark-mate 正好覆盖它（库、transcript、providers、镜像同根）。
Environment=MATE_HOME=/var/lib/ark-mate
Environment=MATE_DB=/var/lib/ark-mate/mate.db
Environment=MATE_TRANSCRIPTS=/var/lib/ark-mate/transcripts
Environment=MATE_PROVIDERS=/var/lib/ark-mate/providers
Environment=MATE_CAPABILITIES=/var/lib/ark-mate/capabilities
ExecStart=/opt/ark-mate/mate serve
Restart=on-failure
RestartSec=3
# 单进程守护：会话/事件在 SQLite 与 transcript 中，重启后自动恢复
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/var/lib/ark-mate

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd --system --home /var/lib/ark-mate --shell /usr/sbin/nologin mate
sudo install -d -o mate -g mate /var/lib/ark-mate /opt/ark-mate
sudo install -o mate -g mate -m 0755 ./mate /opt/ark-mate/mate
sudo systemctl daemon-reload && sudo systemctl enable --now ark-mate
systemctl status ark-mate --no-pager
```

> 注意：底座子进程（`dsh`）由 daemon 拉起，需对 `mate` 用户可见（PATH 与配置）。
> `ProtectHome=read-only` 下默认 `~/.ark-mate` 不可写，故示例把数据目录改到 `/var/lib/ark-mate`。

## 4. 容器

`Dockerfile` 是多阶段构建（Node 构建前端 → Go 编译 → 镜像内 `COPY` 一份 dist 到 `/usr/local/share/mate/web` 并以 `MATE_WEB_DIR` 指过去 → 精简运行时），**两个最终 target**：

| target | 内容 | 体积（本机实测） | 适用 |
| --- | --- | --- | --- |
| `mate`（默认） | 只含 `mate` 本体 + `curl`/`tini` + 非 root 运行时 | 141 MB | **不含任何底座**：底座由宿主机挂载、派生镜像或远程 runtime 端点提供；容器内**无法自检**（自检须挂真实底座，见 §4.2） |
| `mate-runtimes` | 额外内置 `dsh`（npm `@deepseek-ai/dsh`） | 671 MB | 想让容器开箱就能跑真实 agent |

```bash
# 最小镜像
docker build -t ark-mate:local .
# 含底座（内置 dsh）
docker build -t ark-mate:runtimes --target mate-runtimes \
  --build-arg WITH_DSH=true .
# 等价的 make 封装
make image                                             # 最小
make image/runtimes WITH_DSH=true                      # 含底座
make image RUNTIME=<id>                                # 核心认哪一个底座（构建期固定，默认 dsh）
make image/run IMAGE_TAG=runtimes                      # 起容器
```

`MATE_RUNTIME`（默认 `dsh`）与 `WITH_DSH` 是**两件事**：
后者决定镜像里**装不装**内置底座二进制，前者决定**核心认哪一个 id**（编译期 `-X` 注入）。用户面
（REST / 控制台）改不了它——换底座要重新构建。两者不一致是允许的（最小镜像不装任何底座时核心照样
认 dsh，只是拉不起来，诊断面会说清）；选中的 id 谁都不是则**核心启动即失败**（fail-closed，
不给"能启动但没底座"的进程）。

开关语义（刻意 fail closed，不静默降级）：

- `WITH_DSH` 只认 `true|false`（`1|0` 亦可）；取值拼错 → **构建即失败**并列出合法取值。
- 开关必须与 target 配套：`--build-arg WITH_DSH=true` 传给默认（最小）target → 构建失败并提示改用
  `--target mate-runtimes`（否则会得到一个「以为有底座、其实没有」的镜像）。`WITH_DSH=false` 传给
  `mate-runtimes` 同样失败（该 target 不含任何底座时没有存在意义）。
- `MATE_RUNTIME` 只做**词法**校验（`[a-z0-9][a-z0-9_-]*`，取值拼错 → 构建即失败）；id 是否存在
  交给核心启动时的 `runtime.ProductRegistry` 判定（远程 runtime 的 id 由 `runtimes.json` 声明，
  构建期写不出起始位置偏好）。
- 版本可用 `--build-arg DSH_VERSION=…` 钉住，默认 `0.2.1-rc.2`（与随包载荷 pin 同源）。**dsh 需含 acp profile
  模板**（`>= 0.1.2-rc.1`，见 `internal/runtime/adapters.go` 的能力说明），默认值满足。
- 镜像只装 mate 与所选底座；agent 真要用的工具（git / ripgrep / 语言 SDK 等）请在派生镜像里补：
  `FROM ark-mate:runtimes` + `USER root` + `apt-get install -y …`。
- **容器里给底座模型凭据**（不内置、也不该内置）：容器以 `uid=10001(mate)`、`HOME=/data` 运行，
  凭据要落在 `/data` 下且对 `mate` 可读。**注意 bind mount 通常不行**——宿主机上的凭据文件常是
  `600` 且属主为宿主用户，容器里的 `mate` 读不到（实测踩到）；用 `docker cp` + `chown` 才稳，
  也可自行用 secret / 初始化脚本实现等价效果。

  **下面用「某个走配置文件的底座」举例**（`~/.config/<id>/` + `~/.local/share/<id>/` 这种布局，
  这里按 `opencode` 的路径写——它是**用户自己装的那个**，不在本仓随包清单里；换成你实际用的底座时
  把路径换成它自己的读根即可，**别把示例路径当成本仓会随包带的东西**）：

  ```bash
  docker exec -u root <容器> mkdir -p /data/.config/opencode /data/.local/share/opencode
  docker cp ~/.config/opencode/opencode.json <容器>:/data/.config/opencode/opencode.json
  docker cp ~/.local/share/opencode/auth.json <容器>:/data/.local/share/opencode/auth.json
  docker exec -u root <容器> sh -c 'chown -R mate:mate /data/.config /data/.local && chmod 600 /data/.config/opencode/opencode.json /data/.local/share/opencode/auth.json'
  ```

  本仓当前随包只带 `dsh`，而它**不走这条路**——它的 provider 凭据走环境变量，见下：

  dsh 侧的 provider 凭据走**环境变量**（平台把 `provider` 档案的密钥以 `<TYPE>_API_KEY`
  注入子进程；容器里用 `-e` 传同名变量即可）。**平台不写 dsh 自己的 home patch**（`~/.dsh/settings.yaml`
  那些 profile/plugin 层仍是用户与 dsh 自己的事）：dsh 侧的**模型清单**走平台自有的 overlay
  `<MATE_HOME>/runtime/dsh/mate-models.patch.yml`（容器里 `/data/runtime/dsh/mate-models.patch.yml`），
  由平台按"文件存在且非空"幂等地作为 `--patch <绝对路径>` 递进启动参数——坏了把参数摘掉就回到出厂配置，
  不需要修复 dsh 的任何文件。**当前该落点的 `Tested=false`，所以是只读的**：平台会生成候选内容，
  但**不落盘、也不改启动参数**（`mate doctor` 的 `models dsh` 那一条会如实报"未实测 ⇒ 只读"）。
  没有对应变量时 dsh 仍能起来并完成 ACP 握手，但第一轮请求会失败——
  这是如实语义，不是平台静默降级（实测见 §8）。

### 4.1 运行

```bash
# 一条命令（仓库根 docker-compose.yml：具名卷 + 只绑回环 + /data 全集持久化）
docker compose up -d --build
# 或纯 docker run
docker run -d --name ark-mate -p 127.0.0.1:10001:10001 \
  -v ark-mate-data:/data -e MATE_ADDR=0.0.0.0:10001 ark-mate:local
curl -s http://127.0.0.1:10001/v1/mate/health     # {"code":0,"msg":"ok"}
```

compose 的全部可调变量（端口/令牌/工作区起始位置/镜像形态/镜像站）都有默认值，模板见仓库根
`.env.example`：`cp .env.example .env` 后放开需要的行即可。**但 `.env` 不只 compose 读——`dsh`
底座自己也会读它**（真实运行时读「调用目录」的 `.env`），而 `DSH_VERSION` 属于它的保留键
（`DSH_` 前缀，另有 49 个固定名单与 `XDG_`/`DYLD_`/`BASH_FUNC_` 四个前缀）：写进 `.env` 会让
dsh **拒绝启动**（`export DSH_VERSION instead of putting it in a .env file`）。要钉底座版本请走
shell 环境（`export DSH_VERSION=…` 或 `DSH_VERSION=… docker compose up -d --build`）——compose 的
插值优先级本来就是 shell 环境 > `.env`，两边都生效。判据与完整名单见 README「前后端分离开发」。

镜像内的既定行为（都可用 `-e` / compose 覆盖）：

| 项 | 值 | 原因 |
| --- | --- | --- |
| `MATE_ADDR` | `0.0.0.0:10001` | 二进制默认只绑回环，容器里不改就 `-p` 不进来 |
| `HOME`、`MATE_CONFIG_DIR` | `/data`、`/data/config` | 库 / transcript / providers / capabilities / 配置 / 代建工作空间全在 `/data` 一个卷里（§2.2 的默认路径被显式改到卷上） |
| `MATE_STORE` | `sqlite` | 才有 runs 读模型与 `/usage/summary`（§2.2） |
| `MATE_WORKSPACE_DIR` | `/data/workspaces` | 代建工作空间必须落在可写挂载上，否则容器重建即丢（§2.2.1） |
| PID 1 | `tini` | daemon 会派生底座子进程（`dsh`），容器里需要 init 回收孤儿进程 |
| 运行用户 | `mate`（uid/gid 10001，`--build-arg MATE_UID/MATE_GID` 可改） | 非 root；bind mount 的属主需与之一致 |
| `HEALTHCHECK` | `curl` 打 `/v1/mate/health` | 端口从 `MATE_ADDR` 现取；`/health` 免鉴权，配了 `MATE_TOKEN` 也通（§2.3.2） |

### 4.2 容器里的底座与工作空间（两个硬要求）

1. **底座**：最小镜像**不含任何底座**，因此容器内**没有免 key 自检手段**（此前靠内置 `echo` 自检，
   它已随 M0 删除）。要跑真实 agent，三选一——用 `mate-runtimes` 镜像（`dsh` 已在 `PATH`
   上，容器内自检即在此形态下做）、自建派生镜像（`FROM ark-mate:local` + 自己装），或 **Linux 宿主机**
   下把本机可执行文件 bind mount 进最小镜像
   （`-v /usr/local/bin/dsh:/usr/local/bin/dsh:ro`，agent 需要的配置/密钥也要一并进来）。
   注意 macOS/Windows 上的可执行文件**不能**塞进 Linux 容器（平台/架构/glibc 都不匹配），这类机器请走前两条。
   模型密钥经 `-e DEEPSEEK_API_KEY=…` 由 daemon 透传给 runtime 子进程；dsh 的 profile 根固定在
   `DSH_HOME=/data/dsh`（落在卷上，重启不重装）。
2. **工作空间**：能选的工作空间只能是**容器挂载视图内**的目录（§2.2.1）。宿主目录要当工作空间，
   先 bind mount 进来，会话 `cwd` 用**容器内路径**（agent 也在同一容器里，这才是对 agent 正确的路径）：

   ```bash
   docker run -d --name ark-mate -p 127.0.0.1:10001:10001 \
     -v ark-mate-data:/data \
     -v "$PWD:/workspaces/repo" \
     -e MATE_ADDR=0.0.0.0:10001 \
     -e MATE_WORKSPACE_ROOTS=/workspaces \
     -e MATE_WORKSPACE_DIR=/workspaces/.mate-tmp \
     ark-mate:local
   ```

   `MATE_WORKSPACE_ROOTS`/`MATE_WORKSPACE_DIR` 一起给：起始位置偏好指向**用户目录**（方便浏览），
   代建根（`/workspaces/.mate-tmp`）由平台自动并入生效名单，**不必**手写进去（§2.2.1）。
   这两项都在进程 env 里给还有个副作用：面板「设置」页的起始位置随之变只读（§2.2.2），
   要在面板里维护它就别在 env 里设 `MATE_WORKSPACE_ROOTS`。
   注意它**不再是边界**：容器里能选到的目录由挂载视图与运行用户权限决定，不由这份名单决定。
   bind mount 的写权限按宿主 uid 判定，与容器内 10001 不一致时用 `--build-arg MATE_UID=$(id -u)` 重建。
   对外暴露端口（反代 / 跨机 / 多人）**先设 `MATE_TOKEN`**，否则等于把控制面与整个挂载视图开放出去（§2.1）。

### 4.3 受限网络（镜像站 / 代理 / npm 镜像）

基础镜像与仓库地址都是 build-arg，默认走官方源（`node:24-bookworm` / `golang:1.26-bookworm` /
`debian:bookworm-slim` / `proxy.golang.org` / `registry.npmjs.org`）；在拉不到 Docker Hub 或
`proxy.golang.org` 的网络里整组覆盖即可：

```bash
make image \
  NODE_IMAGE=docker.m.daocloud.io/library/node:24-bookworm \
  GO_IMAGE=docker.m.daocloud.io/library/golang:1.26-bookworm \
  RUNTIME_IMAGE=docker.m.daocloud.io/library/debian:bookworm-slim \
  GO_PROXY=https://goproxy.cn,direct \
  NPM_REGISTRY=https://registry.npmmirror.com
```

compose 路径把同名变量写进 `.env` 即可（`NODE_IMAGE`/`GO_IMAGE`/`RUNTIME_IMAGE`/`GO_PROXY`/`NPM_REGISTRY`
/`DSH_VERSION`）。`GO_PROXY` 走 `goproxy.cn` 时不依赖 `sum.golang.org`
（`GONOSUMDB`/`GOSUMDB` 无需改动，`go.sum` 校验仍在本地完成）。


## 5. 健康检查与可观测

- 存活探针：`GET /v1/mate/health`（`code:0`）。
- 就绪判据（无独立 readiness 端点，按下列信号组合）：health 通 + `GET /v1/mate/meta` 的 `runtime.id`
  等于构建期注入的那个 id 且 `runtime.version` 非空（v19 起底座身份由构建期固定，`GET /runtimes`
  与 `GET /agents` 两条"在册面"都已删除；会话诊断走 `GET /v1/mate/runtime/diagnostics`）。
- 日志：stdout（zap 编码的 JSON 行，由 golib 的 `glog` 输出，级别固定 info）。例外两条：
  `mate mcp` 的 stdout 是 JSON-RPC 协议流，日志改落 `<MATE_HOME>/logs/<日期>/ark-mate-mcp_full.log`；
  `mate --json` 系子命令不装日志出口（保住 stdout 的机器可读性）。
  权限自动决策、MCP 探测、断连回收等关键路径均有日志
  （如 `permission: auto-decided by agent policy`、`mcp: probe ...`）。
- 事件留痕：会话事件同时落 SQLite `events` 表与 transcript 文件，可离线复盘。

## 6. 备份 / 升级 / 回滚

```bash
# 备份（停服或在线 SQLite 备份均可；transcript/providers/capabilities/credentials.json 是纯文件）
# ⚠️ 必须备份 MATE_DB（默认 ~/.ark-mate/mate.db）：**v11 起 provider 档案的权威在这里**
#     （providers 表），会话/事件/各域定义也都在这里——"只备 providers.json"从这一版起不再等价。
# ⚠️ **v20 起这份库与它的每一份备份里都有明文密钥**（`providers.key` 列 + `mcp_secrets` 表）：
#     "库被读走也拿不到密钥"这条保护随 v20 消失，**共享 / 多用户机器上不再安全**
#     （本机单用户桌面形态下可接受）。因此 **`backups/` 目录 0700、备份文件 0600 是硬要求**，
#     不是整洁偏好：迁移备份落 `<db 目录>/backups/`（v19 起的布局），它和诊断包一样按密钥面看待。
# ◯ 必须一起收进备份清单的：库（含 `backups/` 里的迁移前快照）、`~/.ark-mate/credentials.json`
#   · v20 起这些文件都含**明文密钥**，权限是硬要求：库与备份 0600、`backups/` 0700。
#     平台在**打开库时**会自动收口到 0600（`internal/infra/dbclient` 的 tightenFilePerms，含首次建库），
#     收口过会打一条 WARN；`mate doctor` 的敏感文件清单把库与备份一并按密钥面报出（宽权限 = fail）。
#     拷贝/同步整个数据目录 = 连密钥一起带走，所以权限收口之后还要注意**同步目标**的权限
#     凭据镜像、`<ProviderDir>/providers.json` 与它的 providers.json.bak-v11（v11 的迁移前快照）、
#     `~/.ark-mate/transcripts/`（对话原文）。
# ◯ `credentials.json`（严格 ref → value、0600）是**可重建的镜像**：只备库也能恢复密钥（写路径
#     先落库再写镜像）；反过来"库没了、只剩镜像"要按 `mate doctor` 指路的**显式导入**回灌，
#     **不自动回灌**（否则会被一份陈旧文件悄悄撤销你在面板上做过的清除）。它权限不合即 fail-loud。
sudo systemctl stop ark-mate
tar czf ark-mate-$(date +%F).tgz -C /var/lib ark-mate
sudo systemctl start ark-mate

# 升级：三步——① 先干跑迁移看一眼 ② 换二进制（SQLite 迁移在启动时自动执行，幂等）
#              ③ 若你有 skill 定义用了已退役的 sources[]，改一行 projection_dirs 后重校
sudo install -o mate -g mate -m 0755 ./mate /opt/ark-mate/mate.new
./mate.new capability migrate --dry-run       # 只报告会改哪些定义文件，不落盘
sudo mv /opt/ark-mate/mate.new /opt/ark-mate/mate
sudo systemctl restart ark-mate
# 能力/自动化定义的目录布局没有变化：定义文件仍是唯一事实源，升级不重写它们
# （skill 的 `sources[]` 与 automation 的 `schedule/target` 都由装载期读，缺键即缺省）。

# 回滚：换回旧二进制重启。**但只要新版本已经把库迁移到更高 schema 版本，旧二进制会拒绝启动**
# （store.migrate: schema vN 高于本二进制支持的 vM），此时唯一的回滚路径是"恢复库备份 + 换回旧二进制"。
# 本仓当前最新 schema = **v20**（v11 = 设置 · 模型的 providers/models 两张表 + 索引；v20 = 给 `providers`
# 加 `key` 列 + 建 `mcp_secrets` 表）。
# v11 与 v20 各带一次不可逆动作，都值得单独记住：
#   · v11：provider 档案的事实源从 `<ProviderDir>/providers.json` 搬进了库，升级后旧二进制读到的是
#     **导入时刻的快照**。所以 v11 的回退除了换二进制，还要先 `mate provider export` 把库回灌成文件
#     （步骤与检查项见 §6.1）。所以：
#     - v10 二进制 → v11 库：停服 → `mate provider export --dir <ProviderDir>` 回灌文件 →
#       `tar xzf <升级前的备份>` 覆盖 `MATE_DB`（v11 两张表没了，provider 档案回到文件侧，这是备份点的
#       状态，不是数据损坏）→ 换回 v10 二进制 → 起服
#     - 跨过 v11 的回滚**不要**试图手工改 `schema_migrations` 表：迁移是有序的，抹掉版本号会让下一次
#       升级重放 v11（两张表重新建一遍），而中间版本的任何数据改造都不会被撤销
#     - 只想"临时退一版"而不动库：保持 v11 库、换回**仍然支持 v11** 的二进制（v11 之后的修复版本都属于这一类）
#   · v20：旧 `vault/` 密文被解密后写进 `providers.key` / `mcp_secrets` 与凭据镜像，`vault/` 与
#     它的主密钥文件整体删除——**不留两个事实源**，回退靠升级前那份库备份（`backups/` 里就有）。
# 备忘：`MATE_DB` 默认 `~/.ark-mate/mate.db`；备份命令见本节开头（库 + 镜像 + providers.json + transcript）。
```

重启恢复语义：SQLite 模式从 `conversations`/`events` 恢复会话与历史；`interrupted` 状态表示重启前
被中断的轮次（无终态 → 可续聊重发），而**已完成的轮次保持 `completed`**——停机是运维动作，不是故障，
不会改写会话状态（见 §8 连接拆除语义）。所有已存在的会话（不论上一轮终态）在重启后都可继续提问：
`dsh` 走原生 resume，未声明原生恢复能力的底座走 handoff memo。transcript 模式（memory）
启动时回读 JSONL 重建会话列表。

### 6.1 供应商档案换事实源（v11）：不可逆动作、执行前检查项、回退

**为什么这一段单独写**：v11 之前，provider 档案的事实源是 `<ProviderDir>/providers.json` 这个**文件**；
v11 之后 sqlite 模式下换成了 `mate.db` 的 `providers` 表，文件降级为**导入/导出载体**——
于是"回退旧二进制"这件事多了一个前提：**旧版本读的是那份文件，不是库**。
切换与 `mate provider export` 之外没有第二条回灌路径，所以它是一个**不可逆动作**（在文件被导出覆盖
之前，窗口是关着的）。

**执行前检查项**（六条；`sqlite` 模式下装配期的自动导入会在这个窗口里发生，所以要在**换二进制之前**跑）：

| # | 检查 | 命令 / 判据 | 不满足时说明什么 |
| --- | --- | --- | --- |
| ① | 备份存在且可读 | `test -r <ProviderDir>/providers.json.bak-v11 && wc -c < <ProviderDir>/providers.json.bak-v11`（能读出来、非空） | 没有备份就没有回退路径：先跑一次导入（或手工复制 `providers.json`）再继续 |
| ② | 模型清单为空 | `models` 表行数 == 0（`mate doctor --json` 的 `providers.modelRows`） | 非空说明这台机器已经在用 v11 的模型清单：这时候回退旧二进制会连它一起丢，先确认不需要 |
| ③ | 无待物化配置 | `mate doctor --json` 的 `providers.runtimeConfigDigestWritten == false`（`meta.runtime_config_digest` 未被写过） | 已物化过说明模型清单的落盘/注入路径已经在跑：回退前先记下 `<MATE_HOME>/runtime/<id>/…` 里那几份文件的状态 |
| ④ | 凭据镜像合规 | `mate doctor --json` 的 `providers.credentialsPermOk == true`（逐字字段：`credentialsPath` / `credentialsExists` / `credentialsPerm` / `credentialsPermOk` / `credentialsNote` / `credentialsRefs`），且 `providers.sensitiveFiles[]` 里 `credentials.json` 那条 `ok == true`（`expected` = `0600`） | 三个字段各答一件事：`credentialsExists=false` = 还没生成（**不算问题**：平台第一次写密钥时会建，库里那份不受它影响）；`credentialsPermOk=false` = 权限过宽/形状坏 ⇒ doctor **判 fail**（v20 起权限不合是 fail-loud，平台拒绝读它，修法 `chmod 600`）；`credentialsRefs` = 镜像里能数出几条引用（形状是否可解析的旁证）。注意"**库里没有而镜像里有**"（`providers.missingInStore` 非空）只**报出并指路**，既不算 `fail`、也**不自动回灌**——库才是事实源，回灌要用显式导入 |
| ⑤ | 对账干净 | `mate doctor --json` 的 `providers.importMismatch` 为空（`meta.providers_import_mismatch` 无值） | 有值说明库与文件已经不等价：按 doctor 逐条报出的内容处理（`mate provider export` 同步文件，或查为什么文件里有而库里没有） |
| ⑥ | 导出可用 | `mate provider export --dry-run` 成功（打印将要写出的内容，不改文件） | 导出失败 = 回退路径本身不通：先修它（DB 不可写/目录不可写）再切 |

**回退步骤**（换回旧二进制**之前**必须先做第 1 步）：

```bash
# 1) 把 DB 回灌成 providers.json——这是回退窗口的**唯一**回灌路径。
#    旧二进制只读这个文件；不做这一步，它读到的就是"导入那一刻的快照"，
#    此后在面板/REST 里改过的凭据、新建的档案、改过的 type 全部会丢。
mate provider export --dir <ProviderDir>       # 先 --dry-run 看一眼也行（它只打印不落盘）
# 2) 确认导出物对：条数与库一致，且**导出物里没有任何明文密钥**（"导出不回写明文"是硬不变量）。
# 3) 换回旧二进制重启。若库已经被新版本迁到更高 schema（本仓 v20），
#    旧二进制会拒绝启动（store.migrate 的拒绝启动保护）——那时唯一的路径是
#    "恢复升级前的库备份（§6 开头那份 tar）+ 换回旧二进制"；v11 之前的库本身没有
#    providers/models 两张表，旧二进制读 providers.json 照常工作。
```

**别把"删掉平台写的 overlay"当成一步恢复**（2026-10-10 端到端实测）：T5 与 T2 是两条同时生效的路径。
去掉 `--patch`（删掉 `<MATE_HOME>/runtime/<id>/<overlay>` 或让平台不再注入它）之后，**老的进程**照常跑，
但**新会话会失败**——因为平台侧仍声明着那条默认模型，T2 会把它下发下去，而底座此刻已经不认识它
（dsh 逐字：`Invalid params: unknown model option: ["<provider>","<model>"]`）。设计上这是**有意的**：
"运行时拒绝该值 → 建会话失败，绝不静默回落到出厂默认"（静默回落会让用户以为模型还在按他设的跑）。
所以恢复动作是**两步**：

```bash
# 1) 去掉平台写的配置面（删 overlay / 清掉启动参数 / 或把清单清空——平台见清单为空就什么都不写）
rm -f <MATE_HOME>/runtime/<id>/<overlay>
# 2) 把平台的那条默认模型取消掉（否则它还会被下发给每条新会话）
curl -X PATCH "$MATE/v1/mate/models/<id>" -H 'content-type: application/json' -d '{"is_default":false}'
# 或者：在面板「设置 · 模型」里点掉那颗星（同一件事）。
# 之后新会话按底座自己的默认起；要彻底复原就把清单条目删掉。
```

`mate doctor` 在**任一条**仍处于这个状态时会报出来（`providers.runtimes[].advertisedKnown=true` 而
`declared` 非空的条目不落在 advertised 里 → 差集非空 + 高危档），所以"哪台机器正卡在这两步之间"是
可查的，不用靠记。

**为什么这一段的检查项不放进 README**：README 的「存量迁移」那几段讲的是**机制**（谁搬到哪、
幂等靠什么、为什么不覆盖），而这里六条是**发布会话里要照着敲的命令**，它们的判据（备份在哪、
`mate doctor --json` 的哪两个字段、`--dry-run` 成功）与本节开头的备份/回滚命令是同一套东西。
放在一起，执行的人一屏就能看完"查什么 → 做什么 → 怎么退回去"；机制叙述里插一张命令表反而会把
两件事搅在一起（README 那一侧只留一句指针）。

## 7. 排障

| 现象 | 排查 |
| --- | --- |
| 面板打不开 / `GET /` 404 | 核心没拿到资产（没给 `--web-dir`/`MATE_WEB_DIR`）会**拒绝启动**并打印它要的那份路径，能起服说明资产已就位 → 那 404 通常是访问了错的端口，或给的目录里没有 `index.html`（SPA 回退无处可回） |
| 启动即退出（退出码 3）「没有前端资产」 | 没给 `--web-dir`/`MATE_WEB_DIR`，或给的路径不是目录 / 里面没有 `index.html`；刻意**不回落**任何来源——"配错却照常跑起来"会让人看着旧界面以为新版本已经生效 |
| 会话建不了、`runtime_not_found` | 构建期固定的那个底座没有注册（`runtimes.json` 里同 id 的远程端点缺失，或本机没装那个底座）——v19 起没有 agent 定义这层中间物，会话直接落在 runtime 身份上 |
| 轮次一直转、无输出 | 底座进程未起（`dsh --profile acp` 不在 PATH）；设 `MATE_RUN_TIMEOUT` 让看门狗兜底 |
| 权限卡不出现 | v19 起平台侧**不再有策略来源**（per-agent `permission_policy` 随 agent 面删除）：会话级「征询 + 记忆」就是全部拦截面——命中了该会话的「始终允许」记忆就直接放行、不弹卡（`session.permission_policy` 事件常量仍在冻结契约里，但已无生产者）；另确认没设 `MATE_NATIVE_PERMISSION=off`（它关掉原生权限投影——平台不再向底座注入原生权限开关） |
| `GET /usage/summary` 40501 | 跑在 memory 模式（无 runs 读模型）→ `MATE_STORE=sqlite` |
| 上一 daemon 被 `kill -9` 后疑似残留 runtime 进程 | 启动日志看 `procreg: 启动孤儿回收完成`；注册表在 `<dir(MATE_DB)>/processes.json`（含 pid/pgid/runtime/agent），可人工核对；`MATE_PROC_REGISTRY=off` 可关闭 |
| 启动日志 `跳过疑似 PID 复用的登记` | 正常保护：登记条目的 pgid 与当前进程不一致（PID 被复用）→ 只清账不杀进程 |
| 启动报 `schema vN 高于本二进制支持的 vM` | 库被更新版本迁移过 → 恢复对应备份或升级二进制（拒绝启动是刻意保护） |
| 定时任务报 `未知时区 Asia/Shanghai` | 二进制没带内嵌 tzdata（`cmd/mate` 的 `import _ "time/tzdata"` 被删）或传了库外的自定义名 → 从官方构建取二进制；内嵌库缺失时容器里 `time.LoadLocation` 会失败，宿主机却可能正常 |
| 定时任务在预期之外的时间跑 / 预览与预期不符 | 先对 `POST /automations/preview` 核对墙钟；DST 空档（不存在的本地时刻）会被**向后顺移**并在 `warnings` 里说明，回拨（重复时刻）取**第一次**。`schedule.timezone` 必须是 IANA 名，不能写 `CST`/`GMT+8` 这类别名 |
| 定时任务重跑/卡住不跑 | `last_fired_at` 是认领游标（**平台写回，写面拒收**）；改它是手工干预手段：置空可让下一次 tick 重新认领该 occurrence，置成未来会让它一直不跑（memory 模式改定义文件、sqlite 模式改 `automations.last_fired_at` 列）。别在调度运行中改定义——用 `PUT /automations/:id`（并发改动由 mtime / 行 `revision` 挡住） |
| 数据库锁/写入失败 | 单实例语义（未实测多实例并发）；勿让两个 daemon 共用同一 `MATE_DB` |
| `docker restart` / `compose up -d` 后会话是否还在 | 在。停机只作废 runtime 执行句柄（日志 `停机：会话句柄作废（会话与状态保留…）`）：已完成的轮次保持 `completed`，被停机打断的在途轮次为 `interrupted`，重启后同一会话可直接继续提问（`session.resumed`；dsh 走原生 `resume`，其余走 handoff memo） |
| 重启后某会话仍是 `in_progress`（未收敛为 `interrupted`） | 极少数时序：进程在在途轮次收尾前退出。下次启动的恢复会把残留 `in_progress` 收敛为 `interrupted`（可续聊），见 §7 上一条 |
| 会话继续提问报 `40401 未知或不可恢复会话` | 只可能是会话 id 不存在（历史 id 打错 / 库换过卷）；已存在的会话（含 `completed`/`failed`/`cancelled`）都可续聊。 |
| 重启后 runtime 子进程没了 / `connected=false` | 预期：连接不在卷上，重启后按需重拨（暖进程空闲回收同理）。会话数据在卷里，不受影响 |
| 容器里 `-p` 映射了端口但面板打不开 | 容器内必须 `MATE_ADDR=0.0.0.0:10001`（镜像已默认）；`MATE_ADDR` 改了端口时 `-p` 与 HEALTHCHECK 一并跟（后者从该变量现取） |
| 容器里建会话报 `workspace_invalid`（路径不存在 / 不是目录） | 工作空间只能是挂载视图内的路径：`-v 宿主机目录:容器内路径`，会话 `cwd` 用**容器内路径**（agent 也在同一容器内，故这才是对 agent 正确的那个路径）。v14.5 起不再有"越界"这类拒绝，剩下的只有"这个路径在容器里根本不存在" |
| 容器里 agent 起不来 / diagnostic `connected=false` | 最小镜像不含任何底座 → 用 `--target mate-runtimes` 重建，或把可执行文件 + 配置 bind mount 进容器；看 `GET /v1/mate/runtime/diagnostics` 的 `stderr_tail` |
| `docker build` 卡在拉基础镜像 / `go mod download` 超时 | 受限网络用 §4.3 的 build-arg（`NODE_IMAGE`/`GO_IMAGE`/`RUNTIME_IMAGE`/`GO_PROXY`/`NPM_REGISTRY`）整组覆盖 |
| 把 `WITH_DSH=true` 传给默认 target 构建失败 | 预期行为：开关必须配 `--target mate-runtimes`（或 `make image/runtimes`），不做静默忽略 |

### 6.2 runtime 标识归一（`deepseekharness` → `dsh`）：老数据自动继续生效

2026-10-11 起注册表键统一为 **`dsh`**（Go 包 `internal/infra/dsh`、展示名 `dsh`）——它本来就是同一个
东西在其余六个面上的名字：二进制 `dsh`、隔离根 `<MATE_HOME>/runtime/dsh`、随包载荷 `payloads/dsh/`、
桌面启动器分支 `runtimeId === 'dsh'`、环境变量前缀 `DSH_`、构建期固定的 runtime id `dsh`。构建参数随之变成
`RUNTIME=dsh` / `--build-arg MATE_RUNTIME=dsh`（不指定时的默认值）。

**升级到本版之后，老数据不需要任何手工动作**——5 处持久化面在读时归一、并在**原有的写路径**上自愈：

| 面 | 处理 |
| --- | --- |
| `conversations.runtime_hint`（sqlite 列） | 读回时归一；下次落库写新值 |
| transcript header 的 `runtime`（append-only 历史文件） | **读时归一，文件不重写**（历史就是历史） |
| skill 定义的 `runtimes[]`（文件 / DB 行） | 装载与写面归一（不归一的后果是技能**不再投放**而界面仍显示"已启用"） |
| `config` 的 `projection_dirs` 键（`<runtime>:<kind>`） | 解析时归一（否则自定义投影根**静默失效**、回落到默认根） |
| `~/.agents/skills/*/.mate-managed` 的归属集合 | 物化重校时**认领**旧名字并在写回时替换成新名字 |

> 最后一条要特别说明：若不认领旧名字，归属集合里那条旧名字会永远留着，于是"集合空了才删目录"
> 永不成立——**禁用/删除技能时副本删不掉，底座照旧读到已禁用的技能**。这是本次归一里唯一一处
> 会静默出错的地方，护栏见 `internal/core/capability` 的 `TestOwnerAliasClaimsLegacyMarker`（回退那行即变红）。

**别名什么时候可以删**：当 `mate doctor` 能报告"本机没有任何老数据引用它"之后，
`internal/runtime/legacy.go` 的 `LegacyDeepseekharnessID` / `NormalizeID` / `LegacyAliasesOf` 与它们的
用例可一并删除。在此之前请把别名当成**数据兼容面**对待：它只出现在读路径上，新写入的任何地方都不会再有旧值。

## 8. 验证记录（本机实跑）

> **历史记录说明（M0 后阅读）**：本节是各里程碑**当时**在本机实跑的原始记录，其中不少用例以
> `echo` 演示底座为对端（`mate acp-demo` / `mate acp-serve`）。`echo` 已随 M0 彻底删除，
> **这些记录不再可原样复现**，保留在此仅作历史证据（与 `docs/` 下历史设计文档同一口径）。
> 仍可复现的部分（`make all`、种子只读、重启恢复、检索导出、MCP 调用等）不受影响，只是把
> `echo` agent 换成 `dsh` 或任意自定义 agent 即可。当前契约以 README 与源码为准。
>
> **配置键也已变（M1 后阅读）**：下面 C4/C6 两条例子里用的
> `MATE_SKILLS_MATERIALIZE_DIR`/`_DIRS` 已被 `MATE_PROJECTION_DIRS` 取代（旧键现在会让启动
> 失败并给出等价写法），单 root 的"兼容路径"也已不存在——物化落点只由各 runtime 的投影声明
> 决定，配置只能覆盖根。当时记录里"归属标记 / 共享 root 只摘自己 / 空归属即残留清理"这些**不变式**
> 仍成立，由 `internal/core/capability/skill_materialize_test.go` 与 `internal/sessionruntime/skills_test.go` 覆盖。
>
> **底座面也已变（2026-10 后阅读）**：本节 C4/C6/C7 与容器那几条里出现的 `opencode`，指的是
> **当时在册的第二个一等适配**——它已下线（决策 v18），随包载荷、`Dockerfile` 的 `WITH_OPENCODE`
> 与 `OPENCODE_VERSION` 一并删除，今天的随包清单只有 `dsh`。因此这些记录里"两个底座各自…"的
> 对照结论**不再可原样复现**，只作当时的取证；而**机制那半仍然成立**（隔离表、投影声明、原生
> 权限端口、模型值端口、能力粒度声明都是**按 adapter 声明**驱动的，接回一个底座不需要改内核），
> 由 `internal/archtest` 的 v18 三条守卫与 `internal/testutil/runtimetest` 的合成适配钉住。
> 本节里 `~/.config/opencode`、`~/.local/share/opencode` 这类**用户自己机器上的目录**照旧不许碰。
>
> **agent 面也已变（2026-10 后阅读）**：下面记录里成批出现的 `/agents`、`/agents/dsh/start`、
> `/agents/<id>/diagnostics`、`seed_agent_readonly`、`agents/echo.json` 等，指的是**当时还在的
> agent 概念**——它已整体并入 runtime（决策 v19）：底座的**身份**就是构建期固定的那个 runtime，
> 会话直接落在 `runtime_id` 上。所以这些记录只作当时的取证；现行的对应面是 `GET /meta` 的
> `runtime{id,name,version}`、`GET /runtime/diagnostics`、以及 README 路由表里的现行路由。
> 记录里的 `/conversations` 同理（v15 起统一为 `/sessions`）。

环境：macOS（无 systemd）、Go 1.26、Docker 守护进程可用（Docker Hub 直连受限，容器验证经镜像站覆盖
基础镜像，见 §4.3）。

- `make all` → 仓库根 `./mate`（40 MB，含内嵌前端）；`GIN_MODE=release ./mate serve --addr
  127.0.0.1:18140`（`MATE_STORE=sqlite` + 独立数据目录）→ `GET /v1/mate/health` = `{"code":0,
  "msg":"ok"}`；`GET /` = 200 `text/html`，`<title>ark-mate · Agent 问答控制台</title>`，资产
  `/assets/index-*.js` 可访问；release 模式无 gin debug 告警。
- 按 §2.4 命令建 `echo` agent → `POST /conversations` 跑通一轮（权限卡经 REST 决策）→ 终态
  `completed`；`GET /usage/summary` 返回 `runs=1 completed=1 duration_ms=105 context_used=137
  usage_reported=1 cost_amount=0.00137 USD`。
- 种子只读：`DELETE /agents/dsh` 与 `PATCH /agents/dsh {"runtimes":["echo"]}` 均 `code=40001`
  `seed_agent_readonly`；`PATCH /agents/dsh {"name":…}` 成功且 `seed:true` 保持；`POST
  /agents/nope/start` = `code=40401`。（此前 agent 端点裸错误会漏成 `code=-1`，已修并加断言。）
- 重启恢复：kill daemon 后以同一 `MATE_DB` 重启，`GET /conversations` 仍列出该会话（标题保留）。
- 裸跑 `mate serve`（不带 `--addr`）→ `lsof -nP -iTCP -sTCP:LISTEN` 实测 `127.0.0.1:10001`
  （仅回环；此前默认 `:8099` 会绑所有网卡，已按安全要求收窄）。
- 自动守卫：`go test -race ./...` 全绿；`TestReadmeRoutesMatchRouter` 校验 README 引用的 29 条
  路由全部命中注册表（反向注入错误路径会红）。
- systemd 单元：**未执行**（macOS 无 systemd）——仅 Python INI 解析校验语法与关键指令齐备
  （3 个 section、6 条 `Environment=`、`ExecStart` 绝对路径、`MATE_ADDR` 收在本机；**v20 后该单元另有 `MATE_HOME`**，故"6 条"是当时快照）。上线前请在
  目标机 `systemd-analyze verify` 并实测。
- 容器镜像（§4）：`registry-1.docker.io`/`auth.docker.io` 在本机持续超时，故基础镜像改用镜像站覆盖
  （`docker.m.daocloud.io/library/{node:24-bookworm,golang:1.26-bookworm,debian:bookworm-slim}` +
  `GO_PROXY=https://goproxy.cn,direct`），其余与默认路径一致：
  - **最小镜像**：`docker build -t ark-mate:local .` 成功（141 MB）。容器 `-p 127.0.0.1:18001:10001
    -v ark-mate-test-data:/data -e MATE_ADDR=0.0.0.0:10001`：`GET /v1/mate/health` =
    `{"code":0,"msg":"ok"}`；`GET /` = 200 `text/html` + `<title>ark-mate · Agent 问答控制台</title>`；
    容器内 `id` = `uid=10001(mate) gid=10001(mate)`、`HOME=/data`、PID 1 为 `tini`；
    `mate config` 全部来源 `env(process)`：`/data/mate.db`、`/data/transcripts`、`/data/agents`、
    `/data/capabilities`、`/data/workspaces`、配置目录 `/data/config`、`proc_registry=/data/processes.json`。
  - **容器内跑通一轮真实会话**（echo 底座，daemon 派生自己的 `mate acp-demo`）：`POST /agents`
    （echo）→ `POST /conversations`（响应 `cwd=/data/workspaces/<会话id>`、`workspace_temporary=true`）
    → 挂起权限 `POST /conversations/:id/permission`（`call_id` + `option_id=allow-all`，
    `outcome=selected`）→ `status=completed`；`GET /usage/summary` = `source=sqlite runs=1 completed=1
    duration_ms=13248 context_used=134 cost_amount=0.00134 USD`。卷内出现
    `mate.db`、`agents/echo.json`、`processes.json`、`workspaces/<id>/`。
  - **重启恢复**：`docker restart` 后同一卷 → `GET /conversations` 仍列出该会话、`GET /agents` 含
    `dsh,echo,opencode`、`docker ps` 的 health 列由 `starting` 转 `healthy`（镜像内 HEALTHCHECK 生效）。
  - **重启不损伤会话**（连接拆除语义修复后实测）：容器内 echo 会话跑完一轮（`completed`）→
    `docker restart` → 会话仍为 `completed`、事件表无任何 `*error*` 事件；随后 `POST
    /conversations/:id/prompt` = `code:0`（不再 `40401 未知或不可恢复会话`）→ 再次 `completed`，
    新增 `session.resumed` 事件（echo 无原生 resume → handoff memo 兜底）。停机日志：
    `停机：会话句柄作废（会话与状态保留，重启后按需续聊） runtime=echo cause=intentional
    conversations=1`。
  - **鉴权**：`-e MATE_TOKEN=test-token-abc` → `/health` 200（免鉴权）；`/agents` 无令牌 = **401**、
    错令牌 = **401**、正确 `Bearer` = 200。
  - **开关守卫**：`docker build --build-arg WITH_DSH=true .`（默认最小 target）**构建失败**，报
    「需要底座 target：请加 `--target mate-runtimes`」——刻意不静默忽略。
  - **含底座镜像**：`--target mate-runtimes --build-arg WITH_DSH=true --build-arg WITH_OPENCODE=true`
    构建成功（671 MB）。容器内 `node -v`=v24.21.0、`dsh --version`=0.1.5-rc.1、`opencode --version`=
    1.18.30（均在 `PATH` 上，以 `mate` 用户可执行）；`POST /agents/dsh/start` → `GET
    /agents/dsh/diagnostics` = `connected=true pid=15`，握手能力 `sessions_persistent=true resume=true
    list=true`；`POST /agents/opencode/start` → `connected=true pid=35`；`DSH_HOME=/data/dsh` 落卷
    （`/data` 下出现 `dsh/`、`.config/`、`.local/`）。**当时未跑真实模型轮次**：本机无
    `DEEPSEEK_API_KEY` / `OPENAI_API_KEY`（**2026-09-24 已补上，见下面 T-5 那条**）。
  - **compose 路径**：`docker compose up -d --build`（同一组镜像站变量）→ `docker compose ps` =
    `127.0.0.1:18003->10001/tcp`、health OK、`docker compose down -v` 干净回收容器与卷。
- **含底座镜像用当前代码重建 + 容器内跑通一轮真实会话（2026-09-24 补做，T-5 收口）**：
  - 构建：`GO_PROXY="https://goproxy.cn,direct" DSH_VERSION=0.2.1-rc.2 OPENCODE_VERSION=1.18.32
    make image/runtimes WITH_DSH=true WITH_OPENCODE=true` → `ark-mate:runtimes`（**673 MB**，
    d1c6252c）。顺带修掉 Makefile 与文档不一致：该目标原来固定产出 `$(IMAGE_TAG)-runtimes`
    （默认 `ark-mate:local-runtimes`），而文档成对写的 `make image/run IMAGE_TAG=runtimes` 找的是
    `ark-mate:runtimes`——**开箱即用不了**；现由独立的 `RUNTIMES_TAG`（默认 `runtimes`）决定。
  - 开关组合：只有 opencode 的一版也构建成功（`RUNTIMES_TAG=runtimes-oc`，**450 MB**，
    容器内 `opencode --version`=1.18.32、`dsh` 不存在），证明按 runtime 的开关确实各自生效。
  - 镜像内版本：`node -v`=v24.21.0、`dsh --version`=**0.1.5-rc.3**、`opencode --version`=**1.18.32**
    （注意 `sh -lc` 会重置 `PATH`，校验请用 `sh -c`；daemon 不走 login shell，不受影响）。
  - 内嵌前端是当前代码：`GET /` 命中 `/assets/index-CW2VHftr.js`，其中含 `fg-cmd-pop`（M5′ 命令补全）。
  - **真实一轮**（`make image/run IMAGE_TAG=runtimes MATE_PORT=18011` + §4 的凭据投递步骤）：
    `POST /agents/opencode/start` → `connected=true pid=87`；`POST /conversations`
    （`cwd=/data/workspaces`）→ `POST /conversations/:id/prompt`（字段名是 **`prompt`**，不是 `text`）
    → `status=completed`、`title` 落定、`history` 里 `session.message` 正文为 `ok`、
    `GET /usage/summary` = `source=sqlite runs=1 completed=1`。
  - **M5′ 在打包镜像里同样成立**：同一条会话的 `history` 里出现 `session.commands`
    （容器内 3 条：`customize-opencode`/`init`/`review`——比宿主机少，因为镜像里没有
    `~/.agents/skills`，这是如实差异）。`docker ps` 的 health 列由 `starting` 转 `healthy`。
  - dsh 侧：`POST /agents/dsh/start` → `connected=true pid=160`（能起、能握手）；**仍无真实轮次**——
    dsh 的 provider 凭据走环境变量（`apiKeyEnv: YYUG_API_KEY`），本机未导出该变量。故本轮"容器内跑通
    一轮真实对话"由 opencode 达成。
- 连接拆除语义（上述验证顺带复现并**已修复**）：daemon 收到 SIGTERM 关闭 runtime 连接时，曾走
  `Orchestrator.disconnectConversation` 的「空闲会话 → 标 `failed`」分支，于是**上一轮已 `completed`
  的会话在 `docker restart` / 重启 daemon 后变成 `failed`**（同机 `kill -TERM ./mate serve` 同样复现；
  容器部署里 `docker restart` / `compose up -d` 是常规操作，影响面很大）。根因是「连接生命周期」与
  「轮次结果」共用一条写状态的路径，把**主动停机**当成了**连接故障**。现按拆除原因分流：
  主动拆除（daemon 停机 / `POST /agents/:id/stop` / 空闲回收）在途轮次归 `interrupted`（无终态、可续聊
  重发），空闲会话**保持原状态**、不发 `run.error`；只有对端/子进程意外断开才可能把在途轮次收成
  失败，且空闲会话同样不被改写（只落 agent 诊断 `last_error.code=connection_lost`）。会话句柄在断连时
  作废但**会话本身归平台所有**（执行可丢弃）→ 下一次 prompt 经 native resume / handoff memo
  重建，因此重启后历史会话可继续提问。
- 孤儿回收（A1）：真实 daemon 下 `POST /agents/:id/start`（opencode）→ 注册表出现条目
  （`pid/pgid/command/runtime/agent/instance`）；`stop` 后条目摘除。启动回收用「上一实例登记 +
  独立进程组 + 忽略 SIGTERM」的替身进程组验证：日志 `reaped=1`、进程消失（SIGTERM 超时后升级
  SIGKILL）、注册表清空；PID 复用保护用「错误 pgid」条目验证：日志 `skipped=1`、无关进程存活。
  另实测：echo/opencode 子进程在 daemon 被 `kill -9` 后随 stdin EOF 自行退出（孤儿在此传输形态下
  少见，注册表是兜底防线）。

- 消息级检索 + 批量导出（A2）：真实 daemon（sqlite）跑两轮 echo 会话后 `GET /search/messages
  ?q=quasar-7731` → `source=sqlite total=4`，命中含 `conversation_id/role/seq/message_id/run_id/
  snippet`；逐个锚点回查 `GET /conversations/:id/history?after=<seq-1>&limit=1` 均落在同一事件
  （user 命中 = `session.user_message`；assistant 命中 = 该消息行首事件，可能为其前置
  `session.thought`）。真 `dsh` 会话（`明白`）实测 assistant 命中 `seq=3` 与 `session.message`
  事件 seq 完全一致。`POST /conversations/export` 三格式均通过：markdown 含标题/元信息/`## 用户`
  /`## 助手`/`> 思考`/`- 工具 …`，text 为纯文本行，json 可 `json.loads`（`conversation` +
  `events`）；未知会话进 `skipped` 且 `exported=0`；`format=pdf` → `code=40001
  [invalid_export_format]`；缺 `q` → `code=40001 [invalid_search_query]`。memory 模式
  （`MATE_STORE=memory`）实测 `source=transcript` 且导出正常（无 DB 时回扫 transcript 的明确
  降级面）。`pnpm smoke:e2e` 新增 4 项断言全 PASS；真会话 `e2e-live-run.mjs dsh` 收到
  `run.ended`。前端 `typecheck` + `build` 通过；`console/searchView.ts` 纯函数用 Node 24 原生
  类型擦除加载实跑 17 条断言（锚点定位/去重/导出摘要）。

- agent 诊断面（A3）：`GET /agents/:id/diagnostics` 实测——未启动 `dsh` → `connected=false
  stderr_source=none stderr_tail=[]`；`POST /agents/dsh/start` → `connected=true pid=91586`；
  `kill -9 91586` → 历史出现 `run.error connection_lost`，`last_error` 记录 `code/class/
  conversation_id`。`GET /agents/nope/diagnostics` → `code=40401`。真实 dsh/opencode **不写
  stderr**（`stderr_source=none`，0 行），非空尾巴的证据来自真实子进程单测（python ACP 桩 /
  `sh` 失败进程）。`pnpm smoke:e2e` 新增 2 项断言全 PASS。

- 配置目录化（A4）：`MATE_CONFIG_DIR=/tmp/a4/cfg mate config` 实测打印 15 项与来源
  （`addr`/`db`/`store`/`idle_timeout`/`handoff_max_chars` 来自 `config.json`；`transcripts`/
  `agents`/`reap_grace` 来自 `env`；`MATE_ADDR=…` 时 `addr` 来源变 `env(process)`）；
  `mate serve` 按文件生效（监听 `127.0.0.1:10008`、sqlite、DB 落 `/tmp/a4/data/mate.db`，
  经该 daemon 建的会话在 DB 中可查、events 5 行）。非法配置 `{"adr":…}` / `idle_timeout:"5
  minutes"` → `mate config` 与 `mate serve` 均退出码 2 并给出文件路径与合法键清单。目录缺失
  = 默认值（`127.0.0.1:10001`，来源全 `default`）。顺带修：sqlite 模式启动前 `MkdirAll` DB
  父目录（此前 DB 路径父目录不存在会以 `unable to open database file` 失败）。

- 远程 runtime 传输（A5）：`mate acp-serve --transport ws --listen 127.0.0.1:10020` +
  `--transport http --listen 127.0.0.1:10021`，`.ark-mate/config/runtimes.json` 声明两个端点后
  `mate serve` 注册成功（日志 `注册远程 runtime`），`GET /runtimes` 返回 5 个（3 内置 + 2 远程）。
  两个远程 agent 各建一次会话：事件链 `session.created → user_message → thought → message →
  permission_request`，`POST /conversations/:id/permission`（`allowed=true, option_id=allow-all`）
  → `permission_resolved → permission → tool_call → tool_update → message → usage → run.ended`
  （echo 远端底座回显「已授权（始终允许）。模拟执行完成」）。`GET /agents/:id/diagnostics`
  显示远程 agent `connected=true, pid=None`（无子进程）。把远端 `acp-serve` 杀掉后重建会话 →
  `code=50201`「连接远程 ACP 端点失败（ws: ws://127.0.0.1:10020/acp）」，诊断 `connected=false,
  stderr_source=last_error`，尾巴含 `ws read: ... EOF`，`last_error.code=connection_lost`。
  能力探测（`/agents/:id/capabilities`）经远程传输正常返回。

- 定时任务（M4，本机实跑）：`mate serve`（memory 模式、临时目录、`MATE_TOKEN=smoke-token`）+
  `E2E_BASE=http://127.0.0.1:10001 MATE_SMOKE_TOKEN=smoke-token node frontend/scripts/smoke-e2e.mjs`
  → 「定时任务面」一节全 PASS：`POST /automations/preview`（`Asia/Shanghai` 09:00 →
  `2026-09-24T01:00:00Z` + 墙钟 `09:00 CST`，3 次严格升序）、非法 schedule → `40001`、
  新建回执带 `next_due_at`、重复 id → `40901`、写 `last_fired_at` → `40001`（运行态键不可写）、
  空运行史返回 `[]` 且 `limit=20`、手动触发打不存在的 agent → **报错且留痕**
  （`runs[0].status=skipped` 带原因）、删除幂等（`deleted:true` → `deleted:false`）。
  **这条冒烟抓到一个真实漂移**：空运行史原先是 `"runs": null`，与前端声明的数组类型不符。
- 容器时区（M4-7，本机实跑，口径是 `CGO_ENABLED=0` 的**静态**构建）：`CGO_ENABLED=0 GOOS=linux
  GOARCH=arm64` 构建后放进
  `FROM scratch`（镜像里只有二进制，`docker save | tar -t | grep -c zoneinfo` = **0**）→
  `preview` 与宿主机逐字一致（同上 `09:00 CST`）；**去掉 `import _ "time/tzdata"` 的同一份二进制
  在同一镜像里报 `40001 未知时区 Asia/Shanghai`**——对照证明这个 import 是承重的，而不是装饰。
  内嵌带来的体积增量实测 **+403 KB**（40,291,941 → 40,686,100 字节）。
  注：默认最小镜像 `debian:bookworm-slim` 自带 `/usr/share/zoneinfo`，所以"默认镜像里能跑"并不能
  证明这条；上面的验证特意用了零 zoneinfo 的镜像。
  **口径沿革（别把这段读成"今天仍逐字如此"）**：2026-10-11 当天起构建一律 `CGO_ENABLED=1`
  （那时的 SQLite driver 是 CGO 的），产物动态链接 glibc，这条 `FROM scratch` 的路径当天起不可用；
  2026-10-12 随 golib v1.32.22 换回纯 Go 方言，`CGO_ENABLED=0` 的静态产物**重新成立**（§1 与
  `AGENTS.md` 已同步）。但上面那组数字**没有在纯 Go 方言下重跑过**，重跑时以实测为准：引擎让二进制
  明显变大——2026-10-12 实测 `mate` 在 darwin/arm64 下 **55,141,554 字节**、`GOOS=linux GOARCH=amd64`
  静态构建 **56,963,717 字节**（golib 文档给的最小程序增量是 **+10.99 MiB**）；`time/tzdata` 那
  **+403 KB** 的增量虽小，但"内嵌 tzdata 是承重的"这个结论与体积无关，不受影响。
- 最小镜像本体（M4-7 补跑）：`GO_PROXY=https://goproxy.cn,direct make image` 成功产出
  `ark-mate:local`（**142 MB**）；`docker run --tmpfs /data -e MATE_STORE=memory
  -e MATE_TOKEN=... -p 127.0.0.1:10077:10001 ark-mate:local` 起得来且
  `GET /v1/mate/health` → **200 `{"code":0,"msg":"ok"}`**。
  **默认 `GO_PROXY` 是 `proxy.golang.org`，在拉不到它的网络里会卡在 Go 构建阶段**——按 §4
  整组覆盖镜像源即可（本机就是走 `goproxy.cn` 才构建成功的）。
  **这条只能证明"daemon 起得来 + health 通"**：最小镜像不含任何底座，容器内**跑不通一轮对话**
  （`echo` 演示底座已于 M0 删除，容器里没有免 key 自检手段）；要验内置底座用
  `make image/runtimes` + 容器内真实一轮。
- 单操作员令牌（A6）：`token: cfg-token-9f3a`（config.json）→ `mate config` 输出掩码
  `cf…3a（14 字符）`；`/health` 无令牌 200；`/agents` 无令牌/错令牌 → **HTTP 401 +
  `code=40101`**（信封含 requestID 与提示文案），正确 `Bearer` 与 `?token=` 均 200；
  `POST /conversations/export` 无令牌 401、带令牌 200。WS 探针（Node 24 内置 WebSocket）：
  无令牌 `ERROR → CLOSE 1006`、错令牌同样被拒、带 `?token=` `OPEN → {"type":"pong"}`。
  `MATE_SMOKE_TOKEN=… pnpm smoke:e2e` 全 PASS（含 WS 带令牌升级）；`MATE_SMOKE_TOKEN=…
  node scripts/e2e-live-run.mjs dsh …` 收到 `run.ended`（REST+WS 均带令牌的真实 dsh 轮次）。

- MCP 工具调用（B1）：真实 stdio MCP server（`/tmp/b1/mcp_pyecho.py`，`MATE_CAPABILITIES` 指向
  含 `pyecho.json` 的目录）→ `POST /mcps/pyecho/probe` `added=3`（echo/boom/slow）；
  `POST /mcps/pyecho/tools/echo/call` body `{"arguments":{"msg":"你好","n":42}}` → `code=0`、
  `content=[{type:text,text:"echo: {...}"}]`、`structured_content.echoed` 保真、`duration_ms=22`；
  `boom` → **`code=0` + `is_error=true`**（工具级失败）；未知工具 → `50201`
  `[mcp_tools_call_failed] JSON-RPC 错误 code=-32602`；未知 server → `40401`；`slow` +
  `timeout_ms=300` → `40901` `[mcp_timeout]`；`arguments` 传字符串 → `40001`
  `[mcp_tool_args_invalid]`；`arguments:null` / 空 body → 无参数调用成功。
  `pnpm smoke:e2e`（E2E_BASE 指向该 daemon）新增两条契约断言并全 PASS。

- MCP 资源与提示词（B2，同一 stdio server）：`GET /mcps/pyecho/resources` → 2 条资源
  （uri/name/mime_type/size 保真）+ 1 条 `resourceTemplates`；`POST /mcps/pyecho/resources/read`
  `{"uri":"file:///tmp/b2/notes.txt"}` → `text="hello b2\n"`；blob 资源 → `blob="AAECAwQ="`
  （base64 原样，不自动解码）；未知 uri → `50201 [mcp_resource_read_failed]`。
  `GET /mcps/pyecho/prompts` → 1 条模板（含 `arguments` 的 `required` 标注）；
  `POST /mcps/pyecho/prompts/greet/get` `{"arguments":{"who":"世界","lang":"zh"}}` →
  `messages[0].content.text="你好 世界（zh）"`；未知提示词 → `50201 [mcp_prompt_get_failed]`。
  未知 server（资源/提示词）→ `40401`。**模板降级实测**：另一个条目
  `pyecho_notpl`（脚本返回 -32601）→ `code=0`、`resources=2`、`templates=0`、
  `templates_error="resources/templates/list 失败：JSON-RPC 错误：code=-32601 …"`。
  注：新增能力目录文件需重启 daemon（目录启动时加载，无热重载）。
  `pnpm smoke:e2e` 新增两条 B2 契约断言，全 PASS。

- MCP sse 传输（B3）：python 写的 legacy SSE 服务端（`GET /sse` 发 `endpoint` 事件 + `message`
  事件，`POST /messages` 收请求）→ `POST /mcps/py-sse/probe` `code=0`、`added=1`、
  `server=py-sse/0.1.0`、`tools=[sse_echo]`（连续 3 次探测稳定）；`POST /mcps/py-sse/tools/sse_echo/call`
  → `text='sse: {"msg": "你好 SSE"}'`（连续 3 次调用均成功）；未知工具 → `50201
  [mcp_tools_call_failed] JSON-RPC 错误 code=-32602`；端点不可达 → `50201
  [mcp_sse_connect_failed] sse GET 连接失败：connection refused`。

- MCP 自动探测（B4）：默认 `off` → 启动后 `/mcps` 全为 `tool_count=0`、`/tools` 为空（无行为变化）；
  `MATE_MCP_AUTO_PROBE=startup` → 启动 5s 内 `/mcps` 出现 `last_probe_at`（pyecho `tool_count=3`、
  down 记 `last_probe_error` 连接失败）、`/tools` 自动含 `boom/echo/slow`，无需任何手动 POST；
  日志 `mcp 自动探测开始 servers=2` → 完成/失败各一行 → `mcp 自动探测结束 ok=1 failed=1`。
  **不阻塞启动实测**：慢 server（initialize 睡 4s）→ t+2s `GET /health` 立即 200（27ms）、快 server
  的工具已入目录，t+7s 慢 server 也完成（`slow_tool`）。`MATE_MCP_AUTO_PROBE=bogus` → 启动即
  exit 2 并列出合法取值。
- 原生权限映射（B5）：4 个 agent 的 runtime 子进程 env 实测——`dsh-ro-deny`/`dsh-native-ro`
  `DSH_PERMISSION_MODE=read-only`、`dsh-native-full` `=danger-full-access`、`oc-nobash`
  `OPENCODE_CONFIG_CONTENT={"permission":{"*":"ask","bash":"deny","edit":"deny"}}`（`ps eww -p <pid>` 读取）；
  同一 runtime 不同原生配置**各自独立进程**（连接缓存键含原生 env 签名）。效果实测：
  ① 隔离 ACP 客户端（无平台介入）read-only/workspace-write 写工作区外路径 →
  `bash: ...: Operation not permitted` + `[sandbox: file access denied under <mode> mode]`，
  danger-full-access 同命令直接成功；② 平台内 opencode 对照：无原生配置的 agent 收到
  `session.permission_request`（`echo hi`），`bash:deny` 的 agent **无任何权限请求**；
  ③ 平台内 dsh `default:deny` + read-only → 写入被沙箱拒绝、文件未生成、无挂起请求。
  `MATE_NATIVE_PERMISSION=off` → 两个 runtime 的子进程均无原生权限 env（实测）。
- 前端用量视图 + 权限策略展示（C1）：`GET /usage/summary` 实测 `code=0 source=sqlite group_by=agent`
  （`totals runs=5 completed=4 failed=1 duration_ms=25029 context_used=28577`，3 个 agent 桶）；
  `group_by=day&from=2026-09-08` → `buckets=[("2026-09-09",5)]`；`group_by=tool` → `40001
  [invalid_usage_group_by]`。`make all` 后 release 二进制同源服务：`/` 200、bundle
  `/assets/index-74hlEOI0.js` 含「用量」「权限策略」「danger-full-access」字样。策略 PATCH 实测（前端同形载荷）
  → `code=0` 且 `GET /agents` 回读 `{"default":"allow","tools":{"bash":"deny"},"native_mode":"auto"}`；
  非法 `native_mode` → `40001 permission_policy.native_mode: 取值 "yolo" 非法（auto|off|read-only|workspace-write|danger-full-access）`。
  `scripts/smoke-e2e.mjs` 新增 3 项用量断言全 PASS（含 group_by=day 与非法分组）。
  视图纯函数 Node 侧校验 34 项 PASS（`fmtDuration/fmtCost/fmtContext/successRate/usageRows/parsePolicyTools/policySummary/policyColor`）。
- `/history` before 游标（C2）：真进程（release 二进制 127.0.0.1:10019，sqlite，单会话 65540 条事件，
  会话 id 为平台 uuid `b937db79-750c-4406-ae6c-9cfa91916605`）
  `?before=0&limit=3` → `first=65538 last=65540 texts=[m65538,m65539,m65540] has_more=true`；
  连续 `before=<first>` 逐页回退（65535→65532→65529…）；`limit=1000` 翻 66 页到 `first_seq=1`（`m1`）且
  `has_more=false`。同库对照：WS `attach(since_seq=0)` 回 `snapshot` 且重放事件数 0（eventbus 上限 65536，
  seq 1..4 已被淘汰）——REST 仍能翻到 seq 1，证明历史读 SQLite events 表而非 bus。
  `after=1&before=2` → `40001 after 与 before 互斥`；`before=-1` → `40000 before 需为非负整数`。
- elicitation scope 归属（C3）：真进程（release 二进制 127.0.0.1:10020，echo agent）
  `POST /conversations`（prompt 含「表单」）→ `GET /conversations/:id/elicitations` 回带
  `conversation_id=d24de5af-… scope_source=inferred_inflight mode=form`；用**另一真实会话**
  `POST /conversations/5a393b1e-…/elicitation` 决策同一 `elicitation_id` → `40001 结构化提问 "…" 不存在或已过期`
  且归属会话挂起项仍在（1 条），服务端日志 `WARN elicitation: 决策会话与归属会话不符，拒绝（scope mismatch）
  owner=d24de5af-… caller=5a393b1e-…`；归属会话决策 → `code=0`，随后挂起 0 条。
  未知会话 → `40401 未知会话 "…"`（先于 elicitation 校验）。WS 事件 `session.elicitation_request` 载荷实测
  `{"session_id":"3adc5250-…","conversation_id":"3adc5250-…","scope_source":"inferred_inflight","mode":"form"}`。
  同连接两会话并发在途时仍按取消处理并留 WARN（SDK 无 scope，不猜测）。
- 多 root skill 物化（C4）：真进程（release 二进制 127.0.0.1:10021，
  `MATE_SKILLS_MATERIALIZE_DIRS=opencode=/tmp/c4/oc,deepseekharness=/tmp/c4/dsh`，三个 skill：
  both/oc-only/dsh-only）→ 启动日志 `skill 物化重校完成 dir=/tmp/c4/dsh runtime=deepseekharness written=2` +
  `dir=/tmp/c4/oc runtime=opencode written=2`；产物：`/tmp/c4/oc/{both,oc-only}`、
  `/tmp/c4/dsh/{both,dsh-only}`（归属标记第二行分别为 `opencode`/`deepseekharness`）。
  **真实底座解析**：`XDG_CONFIG_HOME` 隔离下 `opencode debug skill`（v1.18.29）列出
  `both`(/tmp/c4/oc/both/SKILL.md)、`oc-only`(/tmp/c4/oc/oc-only/SKILL.md)；
  真实 `@deepseek-ai/dsh-skill-filesystem` provider 解析 `/tmp/c4/dsh` → `both`、`dsh-only`，**零告警**。
  删除联动：移除 `dsh-only.json` 重启 → `dir=/tmp/c4/dsh runtime=deepseekharness written=0 removed=1`，
  oc root 不受影响。兼容路径：`MATE_SKILLS_MATERIALIZE_DIR=/tmp/c4/shared` 单 root → 3 个 skill 全物化
  （同一目标文件多 format 时按既有约定取首个并告警）。
- smoke 脚本模式自适应（C4 附带）：`E2E_BASE=http://127.0.0.1:10001 node frontend/scripts/smoke-e2e.mjs`
  在**内存模式**（默认启动）下 `/usage/summary` 按设计返回 `40501 usage_stats_unsupported`，
  脚本记 PASS 并注明降级（不再误报 FAIL）；**sqlite 模式**下做完整形状断言
  （`source=sqlite`、`group_by=agent|day` 分桶、非法 `group_by=tool` → 40001）。两种模式实测均
  `✅ 通路全部 PASS`。
- 权限决策回执（C5）：真进程（release 二进制 127.0.0.1:10023，echo agent，选项
  `allow-once/allow-all/reject-once/reject-all`）：
  - **放行但不传 option_id** → `{"allowed":true,"rejected":false,"option_id":"","outcome":"selected",
    "selected_option_id":"allow-once"}`——回执给出兜底选中的选项（证明不是参数回显）；
    同一会话事件里 agent 回「已授权（允许一次）。模拟执行完成：`echo hello` → hello。」
  - **拒绝且传无效 option_id（`nope`）** → `{"rejected":true,"option_id":"nope","outcome":"cancelled",
    "selected_option_id":""}`，agent 回「未获授权（决策被拒绝或取消，按 ACP 语义即 Cancelled）」；
  - **拒绝且传有效 `reject-once`** → `outcome=selected`、`selected_option_id=reject-once`，
    agent 回「已拒绝（拒绝一次）」——回执与 agent 侧行为一致。
- 共享 root 归属集合与残留清理（C6）：真进程（127.0.0.1:10025，
  `MATE_SKILLS_MATERIALIZE_DIRS=opencode=/tmp/c6/shared,deepseekharness=/tmp/c6/shared`，两 runtime 同 root）
  首轮标记 `both|deepseekharness,opencode`、`dsh-only|deepseekharness`、`oc-only|opencode`；
  移除 `oc-only.json` 重启 → `runtime=opencode written=0 removed=1`，`both` 归属不变；
  去掉 both 的 opencode 源重启 → 标记降为 `both|deepseekharness`（目录保留，他方仍在用）；
  再删 `both.json` 重启 → `runtime=deepseekharness written=0 removed=1`，`/tmp/c6/shared` 仅剩 `dsh-only`，无残留。
