# 变更授权

> 从仓库根 `AGENTS.md` 拆出（2026-10-18）——本文件是分册后的唯一事实源；`AGENTS.md` 只保留入口与硬约束。
> 默认允许破坏性调整；仍强制的例外写在下面。

- **允许破坏性调整**：删除/重命名既有对外形态（兼容别名如 `/permissions/always`、历史路径）、改造本地持久数据（SQLite schema、transcript 格式、providers 档案）与大范围重构（包边界、装配路径）均被允许，**不要求保留过渡性兼容层**。
- 例外（仍强制）：改路由须同步 README 接口摘要（`TestReadmeRoutesMatchRouter` 守卫保持绿）；改事件 type / 数据 schema 须同步 `domain/session/events.go`、`internal/model` 实体与 `internal/store` 迁移内核（三处手工一致）；改动后测试与前端 typecheck 保持通过。
