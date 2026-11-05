// Package llm 是平台**内部模型调用**的统一契约层（接口 + 纯值，零 IO、零外部依赖）。
//
// # 它解决什么
//
// 平台此前只有一条调模型的路径——经 ACP 会话 `Prompt`（会话 × 轮次）。凡"不创建用户会话"的
// 辅助调用（摘要 / 判别 / 抽取 / 标题 / 向量化 / 改写…）在结构里没有落点，也不进用量统计。
// 本包把"调模型"收口成一个引擎内的网关：业务只依赖 `Gateway`，不关心底层是"内部复用 ACP
// 会话"（`ExecutorStateful`）还是"HTTP 直连 provider"（`ExecutorStateless`）。
//
// # 三条不变量（本包的设计地基）
//
//  1. **调用即流水**：任何一次对模型的请求，无论有没有会话，都要产出一条 `LLMCall`。
//     会话调用的流水 = `SessionID != ""` 那批；旁路调用 = `""` 那批。`SessionID` 可空是
//     **明确设计**，不是妥协——它表达"计量不与会话绑定"。
//  2. **网关是唯一咽喉**：选 provider/model、重试、超时、限流、脱敏、计量全在网关内，
//     调用方没有机会漏记账。`Gateway` 的默认实现保证成败都记一条。
//  3. **快照 ≠ 流水**：`TokenUsage`（本包）是**单次调用/单轮**的累加型明细，可相加；
//     而 `internal/core/object/objusage` 的 `UsageInfo` 是**上下文窗口快照**（同会话后到覆盖
//     先到，不可相加）。二者是两个类型、两张表、两套口径，**不得互相替代**。
//
// # 落点（为什么在 L1）
//
// 接口与值放 `internal/core/llm`（层带 L1）：L2 `domain`、L4 `service` 要依赖这些抽象，
// 放在 infra 层会让它们反向 import 基础设施（形成 L2→L5 越层边）。
// 具体实现分居：HTTP 直连在执行面（infra，L5，随步骤 4 落地），会话执行器在 `internal/sessionruntime`
// （L7，经窄端口吃 `Orchestrator`），`Recorder` 的持久实现在 `internal/persistence/store`（L5），
// 由装配根接线。这与 `runtime`（L1 契约）↔ `infra/dsh`（L5 实现）是同一种依赖倒置。
//
// 设计文档：`.dsh/docs/specs/2026-11-05-internal-llm-gateway-design.md`。
package llm
