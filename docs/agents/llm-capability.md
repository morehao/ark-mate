# llm 能力接入约定（Capability 层）

内部模型调用统一走 `internal/core/llm` 的 `Gateway`（见 `docs/agents/layout.md` 分层与
`backend/internal/core/llm/doc.go`）。本分册规定**业务侧接入**的三条约定，目的是避免
"每个功能各写一套 prompt、各自调 Gateway"导致的漂移。设计背景见
`.dsh/docs/specs/2026-11-05-llm-stateful-guardrails-design.md`（缺口 C）。

## 三条约定（第 1 个接入者起就遵守）

1. **prompt 只写在一处**：功能内的 prompt 模板集中在功能包的 `prompts.go`（或同包常量），
   不散落到调用点。便于日后抽原语层与统一改。
2. **必带 `Purpose`**：调 `Gateway.Call` 时 `Request.Purpose` 非空，且用 `Purpose*` 已有常量；
   新增用途就新增常量（"未知用途显式存在，不猜"）。
3. **agent 轮次必带护栏**：`PurposeAgentTurn` 必须显式设 `MaxSteps` 与 `Timeout`（并按需缩紧
   `AllowedSkills/AllowedMcps`）。默认下 `PurposeAgentTurn` 且两者皆零会被 `ErrUnboundedAgentTurn`
   拒绝（防内部会话跑飞）；确需无限制的测试/可信内部置 `AllowUnbounded=true`。

## 结构化输出（缺口 B）

- 需要结构化结果 -> 传 `Request.Schema`，取 `Result.Structured`（**已解析校验过的对象**）。
- **只有 Stateless 执行器保证结构化**（OpenAI 兼容 `response_format`）。Stateful 执行器
  （复用 ACP 会话）不支持强制结构化，收到 `Schema` 只告警；结构化需求请用 Stateless
  或默认走 Stateless 的 `PurposeExtract`/`PurposeJudge`。
- 失败语义：默认 `Result.Outcome=Failed` 不抛错（旁路不阻塞主流程）；`Request.Strict=true`
  时失败返回 error（用于"失败必须拒绝"的场景）。

## Capability 原语层（未建，按需抽）

当前**不建**完整原语层（无第二用途前属过度设计）。当出现**第二个同类用途**（如第二个摘要、
第二个抽取）时，在功能侧抽薄封装（`Summarizer`/`Extractor`：固定模板 + `Request` 组装 +
默认 `Schema`/护栏默认值），调用方只传语义参数。

包位（拟）：`internal/service/<svc领域>/llmcap/` 或功能就近包。**新建物理包必须先按
`docs/agents/layout.md` 的层带表在 `backend/internal/archtest/layers_v32_test.go` 登记一行**，
否则守卫报"目标未映射"。
