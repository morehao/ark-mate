# AGENTS.md — ark-mate

两层：**底座层**（`mate` 内核：多 runtime 统一接入 + 会话与能力数据面 + REST/WS 契约 + 交付形态）**与其上的产品层**（面向用户的**个人 Agent 助手**，**聚焦桌面端**）。底座支持多个 agent runtime 接入，当前内置适配只有 `dsh` 一个（第二个适配已在验证阶段下线，**adapter 层完整保留**：接一个新底座 = 新建 `pkg/runtime/<id>/` + 一个 `<id>Spec()` + 挂端口实现，不动框架）——**不追求数量、不承诺接入任意 ACP 生态**；浏览器入口只是**开发形态**，不是交付面（定位口径的唯一事实源 = `docs/decisions.md`（定位陈述见 `docs/positioning.md`））。前后端平级 monorepo。代码注释与 commit message 用简体中文；commit 用 conventional 前缀（`feat`/`fix`/`refactor` + 中文 scope）。

**本文件只保留入口、分册索引与硬约束。** 细节（判据、口径、命令配方、每条规则换来的事故）按主题拆在 `docs/agents/`（2026-10-18 拆出）；**改到哪一块，先读对应那一份**，不要靠猜。

## 分册索引

| 分册 | 讲什么 | 什么时候必须读 |
| --- | --- | --- |
| [`docs/agents/layout.md`](docs/agents/layout.md) | 目录边界、`pkg/` ↔ `internal/` 分界、依赖方向、`service/svc<领域>` 的三条硬规矩与形状 | 新增/搬迁包、改装配或分层 |
| [`docs/agents/guards.md`](docs/agents/guards.md) | `internal/archtest` 当前判据清单与测试约定（**守卫计数只在这里维护**） | 改任何被守卫钉住的代码、加/改守卫 |
| [`docs/agents/guards-history.md`](docs/agents/guards-history.md) | 守卫沿革：每条新增挡的静默失效与实跑反证 | 想知道某条守卫为什么存在、怎么反证 |
| [`docs/agents/commands.md`](docs/agents/commands.md) | 常用命令（dev / desktop / 验收 / 容器）、`RUNTIME_BIN`、界面验收 | 跑构建、开发循环或验收 |
| [`docs/agents/isolation.md`](docs/agents/isolation.md) | 本机隔离纪律（开发期硬约束） | 起进程、写探针、跑验收 |
| [`docs/agents/build-and-runtime.md`](docs/agents/build-and-runtime.md) | 构建形态（构建期固定 runtime、无嵌入资产）与运行配置（鉴权 / Origin / SQLite / CGO） | 改构建、配置或持久层 |
| [`docs/agents/vocabulary.md`](docs/agents/vocabulary.md) | 一个东西一个词：`session` / `definition` / `run` / 凭据 / 模型 / agent 面收口 | 改表名、列名、DTO、前端类型 |
| [`docs/agents/error-codes.md`](docs/agents/error-codes.md) | 错误码的唯一事实源与三处同步 | 新增或修改错误码 |
| [`docs/agents/frontend.md`](docs/agents/frontend.md) | 前端风格约束（token 与交互态 CSS 的收口） | 改界面样式 |
| [`docs/agents/authorization.md`](docs/agents/authorization.md) | 变更授权（允许破坏性调整的边界与仍强制的例外） | 决定要不要留兼容层 |
| [`docs/agents/positioning.md`](docs/agents/positioning.md) | 外部形态参照实现（DSH）与本仓对应物 | 定交付形态、宿主能力边界 |

分册的总目录与维护约定见 [`docs/agents/README.md`](docs/agents/README.md)。

## 硬约束速查

- **Go 命令一律在 `backend/` 下跑**：仓库根没有 `go.mod` / `go.work`（唯一 module 是 `github.com/morehao/ark-mate`，go 1.26.1）。
- **守卫即判据**：`cd backend && go test ./internal/archtest/`。新增/删除守卫时同步 [`docs/agents/guards.md`](docs/agents/guards.md) 的「共 N 条」（按 `grep -c "^func Test" backend/internal/archtest/*.go` 的实际值改写，别手写）。
- **文档引用不得悬空**：`TestDocsReferencesResolve` 扫 `README.md` + `AGENTS.md` + `docs/**/*.md` + `backend/**/*.go` + 界面源码；改名/搬迁文档时把引用一起改（含本文件与 `docs/agents/` 内的链接）。
- **本机隔离**：开发、构建、验收、手工探针都不得读写用户自己安装与正在使用的底座（`~/.dsh` 等）；私有 `HOME` + `MATE_HOME`，目录建在工作区 `.dsh/tmp/` 下，收尾杀进程 + 清目录。细节见 [`docs/agents/isolation.md`](docs/agents/isolation.md)。
- **词汇收口**：平台聚合概念只有一个词 `session`；`conversation` 零命中；底座侧原生会话 id 叫 `RuntimeSessionID`（`rt_` 壳），不得命名为 `session_id`。细节见 [`docs/agents/vocabulary.md`](docs/agents/vocabulary.md)。
- **错误码三处同步**：`pkg/code`（事实源）↔ README「业务错误码」↔ 前端 `packages/types/src/response.ts` 的 `BizCode`；控制器错误出口一律带码（`code.FromError`），不得透传裸 `err`。见 [`docs/agents/error-codes.md`](docs/agents/error-codes.md)。
- **改路由 / 事件 type / 数据 schema 必须同一次提交同步**：README 路由表（`TestReadmeRoutesMatchRouter` 双向比对）、`internal/domain/session/events.go`、`internal/model` 实体与 `internal/store` 迁移（三处手工一致）。
- **交付形态**：桌面是主线（`make desktop/*`），浏览器入口（`make dev`）只是开发形态；底座由构建期固定（默认 `dsh`），用户面**没有**选择入口，被撤掉的选择键在各写面**显式拒绝**而不是静默丢弃。见 [`docs/agents/build-and-runtime.md`](docs/agents/build-and-runtime.md)。
- **语言**：代码注释与 commit message 用简体中文；commit 用 conventional 前缀（`feat`/`fix`/`refactor` + 中文 scope）。

## 维护约定

- 本文件与 `docs/agents/` 都进仓库；**内部工作笔记仍放 `.dsh/docs/`**（gitignore，不进仓库）。
- **一处事实源**：同一段口径只写在一个分册里；新增主题分册时，在 `docs/agents/README.md` 与上面的索引表各登记一行，并从原文件删掉对应段落。
- 分册之间可以互相链接：**markdown 链接目标必须相对本文件存在**（`docs/agents/` 内直接写 `xxx.md`，到 `docs/` 其他文件写 `../xxx.md`）；正文里提到仓库路径时照常写 `docs/...`（纯文本不是链接）。两种引用都由 `TestDocsReferencesResolve` 校验。
