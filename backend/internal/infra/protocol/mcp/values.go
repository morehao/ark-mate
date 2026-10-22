package mcp

// ——— 结果值的转发（A2，2026-10-22）———
//
// 事实源在 `internal/core/protocol`（平台值）：域、用例层、执行面都必须认它，协议包只负责
// "线与平台值之间的翻译"。这里保留别名，让本包的实现代码与测试继续用 `mcp.X` 读起来自然
// ——**类型只有一处定义**，别名不新增行为，也不构成第二事实源（同 `infra/protocol/acp` 的做法）。
//
// 为什么必须搬：`domain/mcp` 曾经直连本包取这几个返回值形状，于是"换底座只动 adapter"这句
// 承诺对 MCP 面是假的；`infra` 也因此被迫跨带（L1 + L5）。搬走之后 `infra` 是**单层带**。
//
// 内圈（`domain` / `sessionruntime` / `runtime`）请直接 import `core/protocol`：走别名等于
// 让域重新认识协议实现，G2 会红。
import "github.com/morehao/ark-mate/internal/core/protocol"

type (
	// Tool 见 `protocol.McpTool`。
	Tool = protocol.McpTool
	// Result 见 `protocol.McpToolList`。
	Result = protocol.McpToolList
	// CallResult 见 `protocol.McpCallResult`（`TextOf()` 随类型一起在协议包）。
	CallResult = protocol.McpCallResult
	// Resource 见 `protocol.McpResource`。
	Resource = protocol.McpResource
	// ResourceTemplate 见 `protocol.McpResourceTemplate`。
	ResourceTemplate = protocol.McpResourceTemplate
	// ResourceList 见 `protocol.McpResourceList`。
	ResourceList = protocol.McpResourceList
	// ResourceContent 见 `protocol.McpResourceContent`。
	ResourceContent = protocol.McpResourceContent
	// ReadResourceResult 见 `protocol.McpReadResourceResult`。
	ReadResourceResult = protocol.McpReadResourceResult
	// PromptArgument 见 `protocol.McpPromptArgument`。
	PromptArgument = protocol.McpPromptArgument
	// Prompt 见 `protocol.McpPrompt`。
	Prompt = protocol.McpPrompt
	// PromptMessage 见 `protocol.McpPromptMessage`。
	PromptMessage = protocol.McpPromptMessage
	// GetPromptResult 见 `protocol.McpGetPromptResult`。
	GetPromptResult = protocol.McpGetPromptResult
)
