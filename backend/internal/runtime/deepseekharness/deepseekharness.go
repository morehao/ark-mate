// Package deepseekharness 声明 deepseek-harness（dsh-acp-demo）的 ACP server
// 启动形态与能力基线（纯数据，不依赖父包）。
package deepseekharness

// ID 是注册表键。
const ID = "deepseekharness"

// Name 是展示名。
const Name = "deepseek-harness"

// Version 是展示用启动形态。
const Version = "dsh-acp-demo --config <cordis.yml>"

// Command：独立 bin `@deepseek-ai/dsh-acp-demo`（非 dsh 子命令），stdio NDJSON 承载 ACP server。
const Command = "dsh-acp-demo"

// Args 模板：--config <cordis.yml>（实际路径由启动方替换 {config} 占位）。
var Args = []string{"--config", "{config}"}

// Caps 是静态能力基线声明（真实值以 ACP initialize 探测为准）：
// 仅 fresh session，不支持 load/list/resume/fork/delete；断连即释放。
type DeclaredCapabilities struct {
	SessionsPersistent     bool
	MultiSessionConcurrent bool
	Images                 bool
	EmbeddedContext        bool
}

// Caps 依据 deepseek-harness README 声明的最小能力。
var Caps = DeclaredCapabilities{
	SessionsPersistent:     false,
	MultiSessionConcurrent: false,
	Images:                 false,
	EmbeddedContext:        false,
}

// Notes 约束说明（给 README/文档位，暂不外露 REST）。
const Notes = "仅 fresh session；同连接会话断连即失效；需注入 DEEPSEEK_API_KEY；接口迭代快可能 breaking"
