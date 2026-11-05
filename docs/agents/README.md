# docs/agents — `AGENTS.md` 的分册

仓库根的 [`AGENTS.md`](../../AGENTS.md) 只保留入口、索引与硬约束；具体口径按主题拆到本目录。**本目录是按需阅读的分册，不自动进入上下文**——改到哪一块，先读对应分册；每一册都写着规则换来的事故与机器判据，靠猜会重踩。

| 文件 | 讲什么 |
| --- | --- |
| [`layout.md`](layout.md) | 目录边界、`pkg/` 与 `internal/` 分界、依赖方向、`service/svc<领域>` 的三条硬规矩与形状 |
| [`guards.md`](guards.md) | `internal/archtest` 当前判据清单与测试约定（守卫计数的唯一维护处） |
| [`guards-history.md`](guards-history.md) | 守卫沿革：每条新增挡的静默失效与实跑反证 |
| [`commands.md`](commands.md) | 常用命令、`RUNTIME_BIN`、界面验收 |
| [`testing.md`](testing.md) | 测试分层：L1–L5 各层职责、新增测试放哪、e2e/组件测试边界与 CI 取舍 |
| [`isolation.md`](isolation.md) | 本机隔离纪律（开发期硬约束） |
| [`build-and-runtime.md`](build-and-runtime.md) | 构建形态与运行配置（构建期固定 runtime、鉴权/Origin/SQLite/CGO） |
| [`vocabulary.md`](vocabulary.md) | 一个东西一个词：`session` / `definition` / `run` / 凭据 / 模型 / agent 面 |
| [`context.md`](context.md) | 请求上下文传递：handler 传 `c.Request.Context()`、service 签名是 `context.Context`、`ContextWithFallback` 不得打开 |
| [`error-codes.md`](error-codes.md) | 错误码的唯一事实源与三处同步 |
| [`frontend.md`](frontend.md) | 前端风格约束（token 与交互态 CSS 的收口） |
| [`time.md`](time.md) | 时间口径：REST/WS 返回 Unix 毫秒、前端按需格式化、例外与历史事件兼容 |
| [`authorization.md`](authorization.md) | 变更授权（允许破坏性调整的边界） |
| [`positioning.md`](positioning.md) | 外部形态参照实现（DSH）与本仓对应物 |
| [`llm-capability.md`](llm-capability.md) | 内部模型调用接入约定：prompt 单点、必带 Purpose、agent 护栏、结构化/降级语义 |

## 维护约定

- **一处事实源**：同一段口径只写在一个分册里，别处链接过去；新增分册时同步 `AGENTS.md` 的索引与本表。
- **引用必须可解析**：`TestDocsReferencesResolve` 扫 `README.md` + `AGENTS.md` + `docs/**/*.md` + `backend/**/*.go` + 界面源码；改名/搬迁时把引用一起改（本目录内的相对链接相对本文件解析）。
- **守卫计数只在 [`guards.md`](guards.md) 维护**：加/删守卫后按 `grep -c "^func Test" backend/internal/archtest/*.go` 的实际值改写「共 N 条」。
- **内部工作笔记不放这里**：外部产品的对照与逆向分析、计划、草稿一律落 `.dsh/docs/`（gitignore，不进仓库）。
