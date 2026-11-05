# llm 能力接入约定（Capability 层）

内部模型调用统一走 `internal/core/llm` 的 `Gateway`（见 `docs/agents/layout.md` 分层与
`backend/internal/core/llm/doc.go`）。本分册先讲**什么时候该接入**（决策清单），再规定
**业务侧接入**的三条约定，目的是避免"每个功能各写一套 prompt、各自调 Gateway"导致的漂移。
设计背景见 `.dsh/docs/specs/2026-11-05-llm-stateful-guardrails-design.md`（缺口 C）。

## 什么时候该接入（先判断要不要用）

**先问：这件事一定要模型来做吗？**

- 纯规则、本地计算或查库能确定性解决的 → **不要**为用而用 LLM（引入不确定性 + 延迟 + 成本方向的问题）。先证明"必须模型"再接入。
- 确定要模型处理，且是**平台内部发起的、非用户对话驱动**的调用 → 走 `llm.Gateway`，**不要**自己直连 provider（否则重试/超时/记账/脱敏各自一份，必然漂移）。
- 用户与 agent 的**主对话/多轮轮次** → 走会话（`PurposeAgentTurn`，见下"Stateful"），语义上是会话驱动，不是旁路辅助调用。

典型该接入的旁路场景（皆非用户会话驱动）：摘要/归纳、标题生成、意图/相关性判别（judge）、
结构化抽取（extract）、改写/翻译、向量化（embed）。

### 选哪个执行器（Stateful vs Stateless）

| 需求 | 选 | 说明 |
|---|---|---|
| 纯文本一次调用（摘要、改写、标题、embed） | **Stateless** | HTTP 直连，轻、无状态、高频首选 |
| 需要**结构化输出**（JSON/schema 约束） | **Stateless** | 只有 Stateless 保证 `Result.Structured`（`response_format`）；Stateful 不支持强制结构化 |
| 需要**工具 / 多轮 agent 能力**去自主完成任务 | **Stateful** | 复用 ACP 会话跑一轮 agent；**必须带护栏**（MaxSteps/Timeout），见约定 3 |

> 默认策略见 `core/llm` 的 `defaultPolicy`：`agent_turn/summary → Stateful`，其余 → Stateless。
> 一般不用手写 `Prefer`，除非该用途的默认选择明显不合适。

### 结构化 vs 纯文本

- 下游要**按字段消费**结果 → 传 `Request.Schema`、取 `Result.Structured`（已解析校验的对象）。
- 只要一段可读文本 → 不传 `Schema`，取 `Result.Text`。

### 失败语义：Strict vs 默认降级

- **默认（`Strict=false`）**：失败以 `Result.Outcome=Failed` 表达、不抛业务错误 —— 适合"摘要/标题"这类**旁路不阻塞主流程**的场景。
- **`Strict=true`**：失败返回 error —— 适合"**失败必须显式拒绝/中止**"的场景（如风控判别、抽取结果作为后续强依赖）。
- 调用方按需检查 `Outcome`：只在质量敏感处判断 `Outcome != OK`，别无脑当成功用。

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
