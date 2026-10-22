// Package protocol 是**底座协议的平台值**：会话事件、权限裁决、结构化提问、配置项、
// 命令与能力断言。它们是"平台自己认的形状"，不是任何一家的线协议。
//
// 它**负责**：跨层传递这些载荷（域、用例层、执行面、REST/WS 出口共用同一份）。
// 它**不负责**：与 ACP（或任何具体协议）的相互翻译——那在连接边界
// （`internal/drivers/runtimeconn` 与 `internal/infra/protocol/acp`）。
//
// 为什么单独成包（P3，2026-10-21）：这些类型原先住在 `infra/protocol/acp` 里，于是
// `domain` 的 36 个文件、`sessionruntime`、`runtime` 都 import 了协议包——"换底座只动
// adapter"这个承诺在那时是假的。搬出来之后：域只认识本包，翻译只在连接边界。
//
// 判据：本包**不得** import 任何 `infra/*`（含协议绑定），也不得 import `domain`/`dto`。
package protocol
