// Package acp implements the ACP (Agent Client Protocol) host layer:
//
//   - transport: 拉起 runtime 子进程并接入其 stdio（首版仅 StdioTransport）
//   - client:    基于 github.com/coder/acp-go-sdk v0.13.x 的 ClientSideConnection，
//     提供 Initialize / NewSession / Prompt / Cancel / Close 与
//     Load/Resume/List/Fork（按能力断言门控）等高层方法
//   - caps:      initialize 阶段一次性冻结的能力断言（session/prompt/mcp/auth），供上层裁剪接口
//   - events:    ACP notification → 归一化事件（message/thought/tool_call/...），
//     经 ClientOptions.OnEvent 交给注入的回调（manager 的 event sink），acp 不感知总线
//   - errors:    连接/ACP 错误归类为可操作分类（connection/access/limit/request/service/unsupported）
//
// 依赖方向：internal/runtime → acp；acp 不依赖 runtime（见 docs/architecture.md §4.1）。
package acp
