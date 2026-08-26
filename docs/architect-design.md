# ark-forge 架构设计

> 面向 Agent 应用开发的通用底座/平台（monorepo）：创建、运行、扩展与统一管理 Agent，
> 作为 ACP Host，经 stdio 连接 opencode 与 deepseek-harness 等多个 Agent Runtime。

## 1. 定位与目标

一款可自托管的 Agent 平台底座。与前缀 `ark-`（个人品牌标识象征）下的其他项目（如 `ark-iam`）共享品牌，
本项目是其中的「Agent 锻造所」，聚焦：

- **Create**：定义、注册、复用 Agent。
- **Run**：在统一控制面下跨越 Runtime 运行 Agent 会话。
- **Extend**：通过 ACP 协议接入多种 Runtime（首版：opencode、deepseek-harness）。
- **Manage**：生命周期、Registry、会话与配置的统一管理。

元定位：**是底座/平台，不是单纯的 Agent SDK**。核心差异化是靠统一协议（ACP）承载多个外部 Agent 运行时，
让用户在同一面板内切换与控制不同的底层 Agent。

## 2. 关键决策（已确认）

| 决策 | 结论 | 说明 |
|------|------|------|
| 仓库结构 | **monorepo** | 前后端在同一仓库 |
| 后端 | **Go** | daemon / 控制面 / ACP Host |
| 前端 | **TypeScript + Vite(React)** | 面板 |
| ACP 角色 | **Host（Client 侧）** | 拉起并连接 Agent 为 ACP server |
| ACP 协议版本 | **v1** | 稳定版；v2 为草案，暂不涉及 |
| 传输 | **stdio**（本地子进程） | 两 runtime 均原生 ACP server over stdio |
| ACP Client 实现 | **引入现有库 `github.com/coder/acp-go-sdk`** | 227★、Coder 官方团队维护、覆盖 client/server 侧 |
| 交付 | 单二进制（前端经 `go:embed` 嵌入） | 参照 OpenBee |

### ACP 传输现状（为何首版只做 stdio）
ACP 官方 v1 传输：

- **stdio**：成熟、是 SHOULD 支持的标准，用 client 拉起 agent 子进程、stdin/stdout 走 JSON-RPC（`\n` 分隔、UTF-8）。
- **Streamable HTTP / WebSocket**：草案草稿阶段，适配远程/云场景。
- **自定义传输**：协议允许且 transport-agnostic。

opencode 与 deepseek-harness 都是本地子进程形态，走 stdio 是零摩擦标准路径。
`internal/acl` 内部预留 `Transport` 抽象，未来若要接入远程 Agent 再补充 HTTP/WS 实现，首版不过度设计。

## 3. 技术栈选型

### 后端
- Go 1.26+，标准库为主；HTTP 用 `net/http`（控制面 REST + WebSocket，首版负载小，暂不引入重型 Web 框架）。
- ACP Client：`github.com/coder/acp-go-sdk`。
- 持久化：`database/sql` + SQLite（agents、sessions、runtime 配置）；无外部中间件，契合自托管底座。

### 前端
- Vite + React + TS；UI 轻量（不绑定重型组件库）。
- 实时会话通过 WebSocket 消费后端转发的 ACP notification 流。

## 4. 架构总览

```mermaid
graph TB
    subgraph FE[前端面板 (frontend/apps/panel)]
        PA[Agents 页]
        PS[Sessions 页]
        PR[Runtimes 页]
    end

    subgraph BE[Go daemon (cmd/forge)]
        API["server/http + ws 控制面 API"]
        MGR["manager (agent 生命周期/会话编排)"]
        ACL["acl (ACP client: transport + 状态机)"]
        subgraph RT[runtime adapters]
            O["opencode adapter"]
            H["deepseek-harness adapter"]
        end
        INFRA["infra: config/store/logger"]
    end

    subgraph AGENTS[外部 Agent Runtime 进程]
        OPROC["opencode (ACP server / stdio)"]
        HPROC["deepseek-harness (ACP server / stdio)"]
    end

    FE -->|REST/WS| API
    API --> MGR
    MGR --> ACL
    ACL --> O
    ACL --> H
    O -.拉起子进程/stdin|stdout.-> OPROC
    H -.拉起子进程/stdin|stdout.-> HPROC
    MGR --> INFRA
```

## 5. 目录结构（双层 monorepo）

本项目采用**双层 monorepo**：

- **Go 侧**：用 `go.work`（Workspace）管理多个独立 Go module，`internal/` 内每一层（acl / runtime / manager /
  server / infra）各自 `go.mod`，方便独立的 `acl` 等层对外复用。
- **前端侧**：`frontend/` 内部是独立的 pnpm workspace monorepo（apps + packages），与 Go 无 module 耦合。

```
ark-forge/
├── go.work                # Go workspace：union(acl, runtime, manager, server, infra, cmd/forge)
├── Makefile               # make server / make web / make all
│
├── cmd/
│   ├── forge/             # module: forge —— daemon 入口，依赖其余各 module
│   │   ├── main.go
│   │   └── go.mod
│   └── forge-cli/         # module: forge-cli（可选、二期）
│
├── internal/              # 每层一个独立 Go module（业务模块）
│   ├── acl/               # module: forge-acl —— ACP Host 接入层（可独立复用）
│   │   ├── go.mod
│   │   ├── transport.go   #   Transport 抽象（首版 StdioTransport）
│   │   ├── client.go      #   ACP client 句柄（封装 coder/acp-go-sdk）
│   │   └── request.go     #   请求/通知分发、会话生命周期辅助
│   ├── runtime/           # module: forge-runtime —— runtime adapter 层
│   │   ├── go.mod
│   │   ├── contracts.go   #   Runtime 统一接口
│   │   ├── registry.go    #   runtime 注册表（可插拔）
│   │   ├── opencode/      #   opencode adapter
│   │   └── deepseekharness/  # deepseek-harness adapter
│   ├── manager/           # module: forge-manager —— Agent 生命周期 + Registry
│   │   ├── go.mod
│   │   ├── agent.go
│   │   ├── lifecycle.go
│   │   └── session.go
│   ├── server/            # module: forge-server —— 控制面 API
│   │   ├── go.mod
│   │   ├── http.go        #   REST 端点
│   │   └── ws.go          #   WebSocket 流式推送
│   └── infra/             # module: forge-infra —— 基础设施
│       ├── go.mod
│       ├── config/
│       ├── store/         #   SQLite
│       └── logger/
│
├── frontend/              # 前端 monorepo（pnpm workspace）
│   ├── pnpm-workspace.yaml
│   ├── package.json
│   ├── apps/
│   │   └── panel/         #   主面板（Vite + React + TS）
│   │       ├── dist/      #   构建产物，供后端 go:embed 嵌入
│   │       └── src/
│   │           ├── pages/ #   Agents / Sessions / Runtimes
│   │           └── api/
│   └── packages/          #   共享前端包（api client、ui 等）
│
├── docs/
│   └── architect-design.md # 本文件
├── LICENSE                # MIT（已就位）
└── README.md              # 中英简介（已就位）
```

### 前后端交付关系
`frontend/apps/panel` 构建产物（`dist/`）由 Go 侧通过 `go:embed` 打入最终二进制，产出单文件自托管底座
（参照 OpenBee 的 `web/embed.go` 模式）。二者通过 **REST + WebSocket** 通信，不共享代码/依赖。

## 6. 模块职责与数据流

### 6.1 `internal/acl` — ACP Host 接入层
- `Transport` 接口：`Start(ctx) `、`Send`、`Read`、`Close`。首版仅 `StdioTransport`：拉起子进程，接入其 stdin/stdout（`\n` 分帧），stderr 透出为日志。
- `Client`：封装 `coder/acp-go-sdk` 的 client API，提供 `Initialize`、`NewSession`、`Prompt`、`Cancel`、`Close` 等高级方法；内部完成 ACP v1 版本协商与能力断言。
- 依赖方向：`internal/runtime` → 使用 `acl.Client`；`acl` 不依赖 `runtime`，保持可独立复用。

### 6.2 `internal/runtime` — Runtime adapter
- `Contracts` 定义统一接口，例如：
  ```go
  type Runtime interface {
      Name() string
      Start(ctx, spec Spec) (Session, error) // 拉起子进程 + ACP initialize
  }
  type Session interface {
      Prompt(ctx, PromptRequest) (PromptHandle, error)
      Cancel(ctx) error
      Close(ctx) error
  }
  ```
- `Registry`：`map[string]Factory`，注册 opencode / deepseekharness 两个工厂。
- `opencode/` 与 `deepseekharness/`：各自提供**命令名与启动参数**、**ACP 初始化能力断言**（如 opencode 是否声明 `loadSession`、MCP 支持）。

### 6.3 `internal/manager` — 生命周期与编排
- `Agent` 模型：`{ ID, Name, Runtime, Config, State }`。
- 生命周期状态机：`created → running → paused → stopped`，由 `lifecycle.go` 驱动。
- `Session`：把对 Agent 的一次会话编排为「ACP 连接 → session/new → prompt → 流式事件 → cancel/close」，是**流式数据的主要来源**。

### 6.4 `internal/server` — 控制面 API
- REST：管理类操作（列/创建 Agent、列 Runtime、启停、列会话）。
- WebSocket：**流式会话**优先落地——前端发起 prompt，daemon 将 ACP 的 notification（token 块、thought、tool_call、message chunk）实时推给前端。这是首版的第一优先级能力。

### 6.5 `frontend/` — 前端 monorepo
- `apps/panel`：Sessions 页为主（输入 → WS 流式出结果）；Agents 页、Runtimes 页为辅。

## 7. 首版范围（MVP）

| 模块 | 首版包含 | 首版不包含 |
|------|----------|-----------|
| ACP 接入 | stdio 拉起 + v1 协商 + 会话 CRUD + prompt 流式 | HTTP/WS 远程传输、v2、认证流程 |
| Runtime | opencode、deepseek-harness 两个 adapter | 更多 runtime |
| 管理 | 创建/启停/销毁、会话列表、runtime 列表 | 权限/审计、多用户、RBAC |
| 前端 | Sessions 流式页 + Agents 列表 + Runtimes 列表 | 复杂编排画布、多语言 |
| 扩展 | 无（不实现 MCP-append） | MCP、插件系统、skill |

## 8. 验证方式（实现后执行）

- `go build ./...` && `go vet ./...` && `go test ./...`
- `cd web && pnpm build`
- `make all`：产出单二进制，`go run ./cmd/forge` 拉起。
- 手工冒烟：配置一个 runtime（如已安装 opencode），`ark-forge` 拉起其 ACP server，经面板发送 prompt，观察 WS 流式回包；再切换 deepseek-harness 验证跨 runtime 可切换。

## 9. 待办 / 风险

- `coder/acp-go-sdk` 对 **client 侧完整度**需实现阶段验证；若某方法缺口，由 `internal/acl` 抽象兜底（自写该点）。
- opencode / harness 各自的 ACP **启动命令与参数**须在实现时逐一确认（`opencode` 的 acp 入口子命令、harness 的 ACP run 入口），写入各 adapter。
- ACP 协议版本在 init 阶段协商；若某 runtime 仅支持更新版本，需在 adapter 声明及降级策略中处理。
