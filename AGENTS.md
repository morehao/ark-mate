# AGENTS.md — ark-forge

ACP 兼容 runtime（opencode / deepseekharness / echo）的统一控制面平台。前后端平级 monorepo。代码注释与 commit message 用简体中文；commit 用 conventional 前缀（`feat`/`fix`/`refactor` + 中文 scope）。

## 布局与目录边界

- `backend/`：唯一 Go module（`github.com/morehao/ark-forge`，go 1.26.1）。**仓库根没有 go.mod / go.work——所有 go 命令必须在 `backend/` 下运行**。
  - **`pkg/` 与 `internal/` 的分界**（对齐 ark-iam）：可复用基础设施进 `pkg/`，应用专属逻辑留 `internal/`。`pkg/` 内部**不得 import `internal/`**（由 `internal/archtest` 守卫）。
    - `pkg/`：`acl`（ACP 客户端）、`runtime/{opencode,deepseekharness,echo}`（adapter + 注册表）、`mcp`（自研 MCP 客户端）、`capability`、`routing`、`config`（进程配置）、`procreg`（进程注册表）、`web`（release 内嵌前端宿主）、`object/objusage`（跨层共享值对象）。
    - `internal/`：`app`（装配 + 启停生命周期）、`app/command`（CLI 子命令）、`router`（唯一路由装配点，按领域分 `routes_*.go`）、`controller/ctr<领域>`（传输层控制器）、`middleware`、`dto/dto<领域>`（请求/响应 DTO）、`service/svc<领域>`（领域端口）、`manager`（领域实现：Service / Orchestrator / 事件 / 权限）、`store`（SQLite 读模型）、`eventbus`、`archtest`（架构守卫测试）。
  - **依赖方向单一**：`cmd → app → router → controller → service(端口) ← manager(实现)`；`dto` 被两侧共用但自身不依赖应用层；`store`/`eventbus` 不依赖 `manager`/`router`。以上四条边界都由 `internal/archtest` 的守卫测试强制（`go test ./internal/archtest/`）。
- `frontend/`：pnpm workspace（`apps/panel` 控制台 + `packages/{ui,types,api,tsconfig}`）。Node 看 `frontend/.nvmrc`（24.x，团队用 fnm）、pnpm 看 `frontend/package.json` 的 `packageManager`；`engines` + Makefile 守卫 node 主版本不符即失败。
- `docs/` 设计文档是**历史记录**（会话早期叫 sessions，P0 起为 conversations）；对外契约以 README「控制面接口摘要」与 `backend/internal/router/router.go` 为准。

## 常用命令（仓库根）

- `make dev` 前后端联调（go run serve → 等 :10001 health → vite :5173，Ctrl-C 一起收）；`make server` 仅后端；`make web` 只构建前端并刷新 embed 宿主；`make all` 出单二进制 `./forge`（先 web 再 `go build -tags release`）。
- `make build/vet/test` = `cd backend && go build|vet|test ./...`。无独立 lint。
- 容器：`make image`（默认最小镜像）/ `make image/runtimes WITH_DSH=true WITH_OPENCODE=true`（内置底座）/ `IMAGE_TAG=runtimes make image/run`；开关只认 `true|false` 且必须与 `--target` 配套（最小 target 传开会构建失败，不静默忽略）。基础镜像与仓库地址可用变量覆盖（`NODE_IMAGE/GO_IMAGE/RUNTIME_IMAGE/GO_PROXY/NPM_REGISTRY`），细节见 `docs/deployment.md` §4。
- 单包 / 单测试：`cd backend && go test ./internal/manager -run TestName -v`。全量 `go test ./...` 约 2 分钟、**不依赖外部 runtime**（echo 底座在进程内驱动）。
- 前端：`cd frontend && pnpm dev / typecheck / build`。

## 构建形态

- embed 仅随 `-tags release` 编译（`backend/pkg/web/web.go` 带 `//go:build release`）；默认构建不依赖前端产物。`backend/pkg/web/dist/` 不入库（由 `make web` 从 `apps/panel` 产物拷贝刷新）。
- `forge` CLI 无默认子命令：`serve / demo / migrate / transcripts prune / config / acp-serve / acp-demo`。`forge demo` 需本机装 opencode/dsh（echo runtime 除外）。

## 一致性守卫 / 测试约定

- `TestReadmeRoutesMatchRouter`（`internal/router/router_test.go`）比对 README 反引号内 `/v1/forge` 路径与真实注册路由——**新增/改名路由必须同步 README 接口摘要**，否则测试红。README 里也不得再出现历史名 `/sessions`。
- 域事件 type 权威定义在 `backend/internal/manager/events.go`。
- **连接拆除语义（`manager`，改动前先读 `disconnectConversation` 注释）**：连接断开只是连接事实，绝不等于轮次失败。主动拆除（`Close` 停机 / `stopAgent` / 空闲回收）在途轮次归 `interrupted`（可续聊重发）、空闲会话状态**原样保留**；只有对端或子进程意外断开才可能把在途轮次收成失败，空闲会话同样不被改写（只落 agent 诊断 `connection_lost`）。拆除原因由 `runtimeConn.intentional` 承载（`watchConn` 观察到对端结束 = 意外），断连时句柄摘除但会话记录不动——下次 prompt 经 `ensureConversation` 按 native resume / handoff memo 重建（ADR-4：执行可丢弃、会话平台拥有），**可续聊性只看会话是否存在**，上一轮的终态不是作废判据。
- WS / REST 契约改动优先看 `internal/router/routes_*.go` 与 `internal/controller/ctr*`，测试多为走真实装配路径
  （`manager.NewService` + `router.New`）的进程内测试；`TestReadmeRoutesMatchRouter` 走的就是真实引擎。

## 前端风格约束（frontend/DESIGN.md）

- 颜色/圆角/间距唯一事实源 = `packages/ui/src/theme.ts` 的 `tokens`；改设计值须同步 DESIGN.md front matter（两处人工一致），业务代码禁止硬编码色值。
- 交互态 CSS 唯一收口在 `apps/panel/src/console.css.ts`，不新增 css 文件；聊天正文样式由 `Transcript.tsx` 自绘，勿搬进 antd。

## 变更授权

- **允许破坏性调整**：删除/重命名既有对外形态（兼容别名如 `/permissions/always`、历史路径）、改造本地持久数据（SQLite schema、transcript 格式、agents definitions）与大范围重构（包边界、装配路径）均被允许，**不要求保留过渡性兼容层**。
- 例外（仍强制）：改路由须同步 README 接口摘要（`TestReadmeRoutesMatchRouter` 守卫保持绿）；改事件 type / 数据 schema 须同步 `manager/events.go` 与 store 迁移逻辑；改动后测试与前端 typecheck 保持通过。

## 运行配置要点

- 默认仅回环 `127.0.0.1:10001`，无鉴权（对外需先设 `FORGE_TOKEN`，WS 经 `?token=` 传）。部署见 `docs/deployment.md`。
- 持久层 `FORGE_STORE=sqlite|memory|auto`：make dev/server 默认 sqlite（库 `~/.forge/forge.db`）；sqlite 才有 runs 读模型与 `/usage/summary`（memory → 40501）。memory 模式以 transcript（`~/.forge/transcripts`）为事实源，`forge migrate` 幂等导入 sqlite。
- 配置优先级：默认值 < `~/.forge/config/config.json` < `~/.forge/config/env` < 进程 `FORGE_*` < CLI flag；非法键名/取值启动即报错。`forge config --json` 可查最终值与来源。
