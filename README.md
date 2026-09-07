# ark-forge

面向 Agent 应用开发的通用底座/平台：创建、运行、扩展与统一管理 Agent，可接入 OpenCode、DeepSeek Harness 等 ACP 兼容的多种 Runtime。

> A self-hostable forge for building, running, extending, and managing AI agents. Hosts multiple agent runtimes — OpenCode, DeepSeek Harness, and ACP-compatible executors — under one unified control plane.

**定位边界**（详见 [docs/design-and-roadmap.md](docs/design-and-roadmap.md)「决策变更 v3」）：
- **能力不重造**：skills / MCP / 推理等执行能力直接复用底层 runtime 原生实现，本项目不做运行时功能；
- **数据管理是平台职责**：统一管理会话数据（wire transcript / 历史回放）、skill 数据、MCP 服务器数据；
- **单机单操作员**：不做多人协作 / 多用户鉴权 / 远程聊天通道（跨 agent 协作亦不在定位内）。

## 特性（当前形态）

- **ACP 直连运行时**：内置 `opencode`（`opencode acp`）、`deepseekharness`（`dsh --profile acp`）与本地自包含 `echo`（`forge acp-demo`，权限演示）三个 runtime adapter；子进程 stdio 拉起、能力探测、权限请求归一。
- **权限确认通道（ACP 一致）**：agent 发起 `session/request_permission` → 服务端发布 `session.permission_request` 事件（含 options：allow/reject × once/always）→ 用户经 `POST /sessions/:id/permission` 决策 → 决策回流给 agent（Selected/Cancelled）；支持会话级「始终允许」记忆与超时自动拒绝（默认 30min，`FORGE_PERMISSION_TIMEOUT` 可覆盖）；`session/cancel` 按 ACP 规范回 Cancelled 全部挂起。「始终允许」记忆**可见可撤销**：`GET /sessions/:id/permissions/always` 列出、`POST …/always/revoke` 单条或一键撤销（详情面板可操作）。
- **权限真实链路演示（echo 演示底座）**：内置 `echo` runtime——daemon 自派生 `forge acp-demo` 子进程充当 ACP server（与 opencode/dsh 同构的 stdio NDJSON 链路），每轮 prompt 发起一次真实 `session/request_permission`（allow/reject × once/always），按决策回显授权/拒绝与模拟工具卡；**无需安装 opencode/dsh、无需模型 key**，即可在 Web 面板全链路点选权限确认卡验证通道。
- **控制面 REST**（`/v1/forge`）：agent/runtime/会话列表 + 会话动作——`POST /sessions`（指定 agent + prompt 后台起真实会话）、`GET /sessions/:id`（详情 + 挂起权限）、`GET /sessions/:id/history`（事件回放分页）、`POST /sessions/:id/prompt`（同会话多轮）、`POST /sessions/:id/cancel`、`PATCH /sessions/:id`（title/pinned/archived 元数据）、`GET /sessions/:id/permissions`、`POST /sessions/:id/permission`（决策）。
- **轻量会话管理面**：会话元数据（title 由首条用户消息自动推导、pinned/archived、created_at/updated_at）经列表/详情暴露；runtime 能力基线（capabilities）只读暴露；前端侧栏按「置顶 / 今天 / 昨天 / 更早」分组并支持置顶。
- **实时事件流 WS**（`/v1/forge/ws/events`）：attach/detach 订阅（按会话聚合过滤），`since_seq` 游标续传/全量回放，历史缓冲超限自动淘汰并「快照回退」，心跳与断线重连；传输层 `github.com/coder/websocket`。
- **会话 wire transcript（重启存活）**：每 session 一个 JSONL（header + prompt/update 原语，schema v1，append-only 不重写）；daemon **启动时回读 transcript 重建会话列表与元数据（标题/状态/时间），并把历史事件注入 eventbus**——管理面在重启后仍有会话可见、旧会话可经 WS/history 回看，会话列表不随进程失联（轻量「重启存活」，不引入 DB）；目录 `FORGE_TRANSCRIPTS` 或默认 `~/.forge/transcripts`。
- **Web 问答控制台**（浅色 modern-minimal 应用壳，视觉基准 ark-forge-console.html；frontend 为 `apps/*` + `packages/*` 多包 workspace，React 19 + antd 6 + zustand + axios，结构与脚本对齐 ark-iam/frontend）：主视图即会话问答——会话侧栏（**会话/归档双视图** · 按日分组 · 置顶 · 行内更多操作=重命名/归档/恢复 · 新建会话 · 选 Agent 底座）、流式正文（思考 / 工具卡 / 权限确认卡 / meta 行 / **失败轮就地「重试该轮」**）、composer 追加、停止、权限角标（含超时/取消自动收敛）、**会话详情面板**（元数据 + **运行统计**：轮次/失败/取消/工具数 + 失败轮列表一键重试 + 挂起权限就地决策 + 会话级「始终允许」记忆逐条/一键撤销）、断线自动重连 + reconnecting 横幅（重连按水位续传，浏览器原生 WebSocket）；`/sessions/:id` 直达激活，刷新/冷启动即续看；`/agents`、`/runtimes`、`/settings` 为管理次要页。经真实 Chrome 驱动的 DoD 验收（新建→多轮→刷新续看→cancel 全过）。
- **单二进制交付**：`make all` 把面板产物内嵌进 `./forge`（release tag + `go:embed`），一个文件起全部（REST + WS + 静态页面）。
- **无外部依赖进程**：会话/事件为进程内内存态，但**会话列表与历史经 transcript 落盘在重启后重建**（不引入 DB）；持久化落库仍属 M2 规划。

里程碑与后续规划见 [docs/design-and-roadmap.md](docs/design-and-roadmap.md)；harness adapter 的源码级适配核实记录见 [docs/deepseekharness-acp-adaptation.md](docs/deepseekharness-acp-adaptation.md)。

## 快速开始

### 1. 前置

| 依赖 | 说明 |
| --- | --- |
| Go ≥ 1.26 | `go.mod` 声明 `go 1.26.1` |
| Node 24.x（LTS） | 见 `frontend/.nvmrc`（精确版本，唯一事实源） |
| pnpm 11.x | 见 `frontend/package.json` 的 `packageManager` 字段 |
| opencode | 本机安装且支持 `opencode acp` 子命令 |
| dsh（deepseek-harness） | npm 包 `@deepseek-ai/dsh`，见下方版本注意 |
| echo（演示底座） | 无需安装/模型 key；`acp-demo` 子命令内建于单二进制（本 runtime 自派生） |

**前端工具链约定（Node / pnpm）**：版本声明全部收在 `frontend/` 内、仓库根不持有——
Node 看 `frontend/.nvmrc`（现为 `24.16.0`），pnpm 看 `frontend/package.json` 的 `packageManager`（现为 `pnpm@11.24.0`）。

- 团队统一用 **fnm** 管理 Node（`fnm env --use-on-cd` 后进目录自动按 `.nvmrc` 切换）；用其它管理器（nvm/Volta/mise）不破坏仓库，只要 node 落在 24 主版本即可。
- 选 Node 24 线的原因：Node 24 是 **LTS** 线（2025-10 起），团队/生产默认的稳定选择。注意 Node 24 仍捆绑 corepack（Node ≥ 25 才移除）：fnm + Node 24 下 `pnpm` 由 corepack shim 提供，首次运行会打印一次 `! Corepack is about to download …` 并把 `packageManager` 锁定的 pnpm 下载入缓存，之后静默；想彻底消除这行可机器级二选一：`corepack disable`（改走独立安装的 pnpm）或 `corepack prepare pnpm@11.24.0 --activate`（预热）。pnpm ≥ 11 也会在本机版本与 `packageManager` 不一致时自动按其下载运行。
- 兜底两层：`engines.node`（`>=24 <25`）保证 node 不符时 `pnpm install` 直接失败；`make dev` / `make web` 在启动前端前校验 node 主版本并给出切换提示。CI（`setup-node` 的 `node-version-file: frontend/.nvmrc`）与本地读同一份声明，杜绝漂移。

**dsh 版本注意**：统一启动器 `dsh` 的 npm `latest`（如 0.1.1-rc.2）**缺 `acp` profile 模板**，需 `@next`（≥ 0.1.2-rc.1）。安装：

```bash
npm i -g @deepseek-ai/dsh@next     # 或不经安装：npx --yes @deepseek-ai/dsh@next --profile acp ...
dsh --profile acp --help           # 验证可用（首用会自动初始化 profile）
```

**版本混装排查**：若 forge 日志里 dsh 子进程启动即崩、ACP 连接随即断开（`connection closed … cause="peer connection closed"`），且 stderr 出现
`stdio app: the launcher must provide ctx.appExit and ctx.appReady before the tree mounts`
（堆栈同时含 `…/installation/lib/node_modules/@deepseek-ai/dsh` 与 `~/.npm/_npx/…/@deepseek-ai/dsh-acp-app`），说明 **dsh 启动器与 acp 插件来自两棵不同版本树**（典型：全局 `latest` 0.1.1-rc.2 启动器 + npx 缓存里 @next 的 acp 插件）。修法：`npm i -g @deepseek-ai/dsh@next` 让全局树自洽后 `dsh --version` 应为 ≥ 0.1.2-rc.1（forge 按 PATH spawn dsh，升级后无需重启 serve）；仍报错则备份重建 `~/.dsh/profiles/acp`（曾被混装写坏时）。

模型凭证：harness 读取进程环境 `DEEPSEEK_API_KEY`；若你本机 `dsh` 已配好 profile/home 设置，无 shell 环境变量也能跑。

### 2. 构建单二进制

```bash
make all          # = 前端 pnpm build → 拷贝到 backend/internal/web/dist → go build -tags release
./forge           # 产物在仓库根
```

### 3. 启动并打开面板

```bash
./forge serve --addr 127.0.0.1:8099
```

浏览器打开 <http://127.0.0.1:8099>：

1. 左侧「新建会话」选择 agent——先试 `agent-echo`（本地演示底座，无需模型 key）或 `deepseek-harness-agent` / `agent-opencode`——并输入首轮 prompt；
2. 点击「创建并发送」→ 右侧即时流式渲染 assistant 消息/思考/工具卡；
3. 回复期间可点「⏹ 取消」；结束后可在 composer 输入下一条（多轮同会话）；
4. 断开/刷新后重新点选会话，会按游标全量/增量续传补齐会话视图。

监听地址也可用环境变量覆盖：`FORGE_ADDR=:9999 ./forge serve`。会话 wire transcript 默认落
`~/.forge/transcripts/`，可用 `FORGE_TRANSCRIPTS=/path/to/dir` 改目录（每会话一个
`<sessionID>.jsonl`，schema v1，可直接查看/留档）。

### 4. 命令行冒烟（不经面板）

```bash
# 完整跑一次 fresh 会话并打印事件（超时 5min 默认）
./forge demo --runtime opencode --prompt "你好，请用一句话介绍你自己"
./forge demo --runtime deepseekharness --prompt "你好，请用一句话介绍你自己"

# echo 演示底座：无需外部 runtime/模型 key（--allow-all 即时放行权限请求）
./forge demo --runtime echo --allow-all --prompt "跑个演示"

# REST 触发 + WS 收流（可用任意 WS 客户端，如 wscat / 浏览器控制台）
curl -s localhost:8099/v1/forge/sessions \
  -H 'content-type: application/json' \
  -d '{"agent_id":"agent-opencode","prompt":"请只回复两个字：明白"}'
# → {"code":0,...,"data":{"id":"...","event_seq":7,...}}，随后 WS attach 该会话即可收增量
```

> `demo` 默认拒绝一切工具权限请求（仅回复型 prompt 可直通）；`--allow-all` 仅调试放行。控制面 REST（serve）同样默认拒绝权限请求——当前 M1 只保证对话流，涉及工具调用的会话留待 M2/自定义 adapter（权限通道的零依赖演示见下）。

> **零依赖演示权限通道（echo）**：面板左侧「新建会话」选 `agent-echo` 并发送任意消息 → 右侧即时出现权限确认卡（工具「运行演示命令 echo hello」+ 允许一次/始终允许/拒绝一次/始终拒绝）→ 点选后看到授权消息 + 模拟工具卡（允许）或「未获授权」（拒绝）；「始终允许」记为会话级记忆，后续轮次同键自动放行不再弹卡。对应 REST：`GET/POST /sessions/:id/permissions|permission`。
>
> **零依赖演示失败轮重试**：`agent-echo` 会话中，输入首次含 `boom` 的那一轮会以 `turn.error` 失败（同一会话后续恢复正常）；此时聊天流错误条与详情面板「运行统计」均提供「重试该轮」——重发同一输入即可看到该轮成功完成。

### 5. 前后端分离开发

```bash
# 方式 A：一条命令同时起前后端（Ctrl-C 一起收掉）
make dev

# 方式 B：分开两个终端（日志可分别看）
# 终端 A：后端（默认 :8099）
make server            # = cd backend && go run ./cmd/forge serve
# 终端 B：前端（vite dev :5173，/v1 代理到 127.0.0.1:8099，含 ws）
cd frontend && pnpm dev
```

## 控制面接口摘要

REST 前缀 `/v1/forge`；响应统一信封 `{code, requestID, msg, data}`，`code=0` 成功，非 0 为业务错误（40401 未知 agent/会话、40901 会话正忙、40501 能力不支持、50201 runtime/连接故障等）。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/agents` `/runtimes` `/sessions` | 列表 |
| POST | `/sessions` | `{agent_id, prompt, cwd?}` → `{id, event_seq, …}` |
| GET | `/sessions/:id/history` | 事件回放（`?after=&limit=` 分页，默认 200/上限 1000） |
| POST | `/sessions/:id/prompt` | `{prompt}` → `{id, event_seq}`（多轮） |
| POST | `/sessions/:id/cancel` | 取消进行中的 turn（幂等） |
| GET | `/sessions/:id/permissions` `/sessions/:id/permissions/always` | 挂起权限列表 / 会话级「始终允许」记忆（后者也内嵌于 `GET /sessions/:id`） |
| POST | `/sessions/:id/permission` | 权限决策 `{call_id, allowed, option_id?, always?}` |
| POST | `/sessions/:id/permissions/always/revoke` | 撤销「始终允许」`{title, kind}`；两者均空 = 一键撤销全部 |
| GET | `/ws/events` | 实时事件通道（见下） |

WS 帧契约（详见设计文档 §4.5）：客户端 `{action: attach|detach|ping}`；attach 带 `since_seq`（省略 → snapshot + live；数字含 0 → 按序重放 + live）。服务端 `{type: snapshot|event|detached|pong}`，event 载荷 `{type, seq, agg_id, version, payload}`，type 域见 `backend/internal/manager/events.go`（`session.message/thought/tool_call/tool_update/user_message`、`turn.ended/error`、`run.cancelled` 等）。会话动作的 POST 响应返回当前 `event_seq` 水位，作为 attach 续传起点。

## 开发

```bash
make web      # 仅构建前端并刷新 embed 宿主目录
make build    # 不带 release 的内核编译（无需前端产物）
make vet / test   # go vet / go test ./...
# 前端：cd frontend && pnpm typecheck / pnpm build
```

仓库结构：

```
backend/                单 Go module（github.com/morehao/ark-forge）
  cmd/forge             CLI（serve / demo / acp-demo 子命令）
  internal/acl          ACP 客户端（stdio 传输、能力探测、权限、错误分类）
  internal/runtime/     运行时注册表 + adapter（opencode/、deepseekharness/、echo/，纯数据）
  internal/manager      Service + Orchestrator（会话编排、事件发布）
  internal/server/      REST handler / WS / eventbus / router
  internal/web/         release 内嵌前端产物宿主（go:embed dist）
frontend/               pnpm workspace（apps/* + packages/*）
  apps/panel/           Web 问答控制台（React + antd；src/{store,console,components,pages,transport}）
  packages/tsconfig     @ark/tsconfig（base.json / app.json，同 ark-iam 编译器约定）
  packages/types        @ark/types（REST 信封 / 会话 / WS 帧 / 域事件契约）
  packages/api          @ark/api（axios 客户端 + /v1/forge resources）
  packages/ui           @ark/ui（tokens/themeConfig/AppShell/PageContainer/状态胶囊；DESIGN.md 事实源）
docs/                   设计/路线图 + runtime 适配核实
```

## 协议 / License

[MIT](./LICENSE)
