# 错误码（唯一事实源 `backend/internal/platform/code`）

> 从仓库根 `AGENTS.md` 拆出（2026-10-18）——本文件是分册后的唯一事实源；`AGENTS.md` 只保留入口与硬约束。
> 三处同步：`internal/platform/code` ↔ README「业务错误码」↔ 前端 `BizCode`（两条边都有守卫）。

- 信封 `code` 是**对外契约**，三处必须同步：`internal/platform/code`（事实源：常量 + 默认文案）、README「业务错误码」表（文档）、前端 `packages/types/src/response.ts` 的 `BizCode`（消费方）。三条边都由守卫钉住——`TestReadmeCodesMatchRegistry`（internal/platform/code ↔ README）、`TestFrontendBizCodesMatchRegistry`（internal/platform/code ↔ 前端，**用 Go 读 TS 源码**，不引前端测试框架，忘记同步前端同样红）。新增一个码 = 加常量 + 默认文案 + README 行 + 前端常量。
- 编号沿用既有 5 位 HTTP 语义码（`40000/40001/40101/40401/40501/40901/50000/50001/50201`），**不按 ark-iam 的 6 位领域段重编**——它们已经是对外契约。ark-iam 借的是结构（`code.go` 注册表 + 按领域分文件），不是编号。
- `50000` 是**唯一兜底码**：任何未归类 error 经 `code.FromError` 出口都落成它。历史上有 `1`（控制器手写）与 `50000`（领域层）两个泛化码，已统一为 `50000`。
- **控制器错误出口一律带码**：`gincontext.Fail(c, code.FromError(err))` 或 `code.Error(code.X, …)`，不得透传裸 `err`——gincontext 对不带 `gerror.Error` 的错误填 `code: -1`，客户端拿不到可判定的码。由 `TestControllerErrorExitsCarryCode` 静态守卫。
- **错误分类 → 码**的投影表在 `internal/platform/code/business.go` 的 `codeOf`（不在 `internal/infra/acp`：协议库是可复用的客户端，不该知道 ark-mate 的信封编号）。`session_not_found`/`runtime_not_found`/`mcp_not_found`/`provider_not_found` 从 `ClassRequest` 提档到 `40401`（v19 起 `agent_not_found` 随 agent 概念删除，见「一个东西一个词」那节）。
- **协议错误只在适配器出口折成平台视图**（`runtime.FoldError`，B4）：`acp` 的错误类型不越过 `runtime.Connect` / `drivers/runtimeconn` 这两条出口。内圈用 `code.NewError(class, name, msg, cause)` 构造（载体是 `*code.Coded`），控制器出口一律 `code.FromError`。**漏掉一条出口的表现是静默的**：本该 40401/50201 的错误退化成 50000，编译器不报——所以 `internal/runtime` 的 `TestFoldError*` 与各面的错误码用例是这条边界的机器判据。
