# ark-forge

面向 Agent 应用开发的通用底座/平台：创建、运行、扩展与统一管理 Agent，可接入 OpenCode、DeepSeek Harness 等 ACP 兼容的多种 Runtime。

> A self-hostable forge for building, running, extending, and managing AI agents. Hosts multiple agent runtimes — OpenCode, DeepSeek Harness, and ACP-compatible executors — under one unified control plane.

## 特性（当前形态）

- **ACP 直连运行时**：内置 `opencode`（`opencode acp`）与 `deepseekharness`（`dsh --profile acp`）两个 runtime adapter；子进程 stdio 拉起、能力探测、权限请求归一。
- **控制面 REST**（`/v1/forge`）：agent/runtime/会话列表 + 会话动作——`POST /sessions`（指定 agent + prompt 后台起真实会话）、`POST /sessions/:id/prompt`（同会话多轮）、`POST /sessions/:id/cancel`。
- **实时事件流 WS**（`/v1/forge/ws/events`）：attach/detach 订阅（按会话聚合过滤），`since_seq` 游标续传/全量回放，心跳与断线重连；传输层 `github.com/coder/websocket`。
- **前端面板**：新建会话（选 agent、发首轮 prompt）、会话列表、流式渲染（消息/思考/工具卡）、composer 追加、取消、断线自动重连（浏览器原生 WebSocket）。
- **单二进制交付**：`make all` 把面板产物内嵌进 `./forge`（release tag + `go:embed`），一个文件起全部（REST + WS + 静态页面）。
- **无外部依赖进程**：M0/M1 会话与事件均为进程内内存态（重启即失），持久化属 M2 规划。

里程碑与后续规划见 [docs/design-and-roadmap.md](docs/design-and-roadmap.md)；harness adapter 的源码级适配核实记录见 [docs/deepseekharness-acp-adaptation.md](docs/deepseekharness-acp-adaptation.md)。

## 快速开始

### 1. 前置

| 依赖 | 说明 |
| --- | --- |
| Go ≥ 1.26 | `go.mod` 声明 `go 1.26.1` |
| Node + pnpm | 构建前端面板（仓库为 pnpm workspace） |
| opencode | 本机安装且支持 `opencode acp` 子命令 |
| dsh（deepseek-harness） | npm 包 `@deepseek-ai/dsh`，见下方版本注意 |

**dsh 版本注意**：统一启动器 `dsh` 的 npm `latest`（如 0.1.1-rc.2）**缺 `acp` profile 模板**，需 `@next`（≥ 0.1.2-rc.1）。安装：

```bash
npm i -g @deepseek-ai/dsh@next     # 或不经安装：npx --yes @deepseek-ai/dsh@next --profile acp ...
dsh --profile acp --help           # 验证可用（首用会自动初始化 profile）
```

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

1. 左侧「新建会话」选择 agent（如 `deepseek-harness-agent` / `opencode`）并输入首轮 prompt；
2. 点击「创建并发送」→ 右侧即时流式渲染 assistant 消息/思考/工具卡；
3. 回复期间可点「⏹ 取消」；结束后可在 composer 输入下一条（多轮同会话）；
4. 断开/刷新后重新点选会话，会按游标全量/增量续传补齐会话视图。

监听地址也可用环境变量覆盖：`FORGE_ADDR=:9999 ./forge serve`。

### 4. 命令行冒烟（不经面板）

```bash
# 完整跑一次 fresh 会话并打印事件（超时 5min 默认）
./forge demo --runtime opencode --prompt "你好，请用一句话介绍你自己"
./forge demo --runtime deepseekharness --prompt "你好，请用一句话介绍你自己"

# REST 触发 + WS 收流（可用任意 WS 客户端，如 wscat / 浏览器控制台）
curl -s localhost:8099/v1/forge/sessions \
  -H 'content-type: application/json' \
  -d '{"agent_id":"agent-opencode","prompt":"请只回复两个字：明白"}'
# → {"code":0,...,"data":{"id":"...","event_seq":7,...}}，随后 WS attach 该会话即可收增量
```

> `demo` 默认拒绝一切工具权限请求（仅回复型 prompt 可直通）；`--allow-all` 仅调试放行。控制面 REST（serve）同样默认拒绝权限请求——当前 M1 只保证对话流，涉及工具调用的会话留待 M2/自定义 adapter。

### 5. 前后端分离开发

```bash
# 终端 A：后端（默认 :8099）
make server            # = cd backend && go run ./cmd/forge
# 终端 B：前端（vite dev :5173，/v1 代理到 127.0.0.1:8099，含 ws）
cd frontend && pnpm dev
```

## 控制面接口摘要

REST 前缀 `/v1/forge`；响应统一信封 `{code, requestID, msg, data}`，`code=0` 成功，非 0 为业务错误（40401 未知 agent/会话、40901 会话正忙、40501 能力不支持、50201 runtime/连接故障等）。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/agents` `/runtimes` `/sessions` | 列表 |
| POST | `/sessions` | `{agent_id, prompt, cwd?}` → `{id, event_seq, …}` |
| POST | `/sessions/:id/prompt` | `{prompt}` → `{id, event_seq}`（多轮） |
| POST | `/sessions/:id/cancel` | 取消进行中的 turn（幂等） |
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
  cmd/forge             CLI（serve / demo 子命令）
  internal/acl          ACP 客户端（stdio 传输、能力探测、权限、错误分类）
  internal/runtime/     运行时注册表 + adapter（opencode/、deepseekharness/，纯数据）
  internal/manager      Service + Orchestrator（会话编排、事件发布）
  internal/server/      REST handler / WS / eventbus / router
  internal/web/         release 内嵌前端产物宿主（go:embed dist）
frontend/               pnpm workspace
  apps/panel/           React 面板（vite；src/{api,store,transport/ws,types}.ts）
docs/                   设计/路线图 + runtime 适配核实
```

## 协议 / License

[MIT](./LICENSE)
