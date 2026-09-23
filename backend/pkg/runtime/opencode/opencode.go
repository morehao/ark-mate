// Package opencode 声明 opencode 的 ACP server 启动形态与能力基线（纯数据，不依赖父包）。
package opencode

// ID 是注册表键。
const ID = "opencode"

// Name 是展示名。
const Name = "opencode"

// Version 是展示用启动形态（对应子命令形态）。
const Version = "opencode acp"

// Command / Args：以 `opencode acp` 启动 ACP server over stdio（NDJSON）。
const (
	Command = "opencode"
)

var Args = []string{"acp"}

// Caps 是静态能力基线声明（真实值以 ACP initialize 探测为准）：
// 会话能力完整（load/resume/fork/list/close），promptCapabilities 含 image/embeddedContext。
type DeclaredCapabilities struct {
	SessionsPersistent     bool
	MultiSessionConcurrent bool
	Images                 bool
	EmbeddedContext        bool
}

// Caps 依据 opencode ACP 文档声明的基线。
var Caps = DeclaredCapabilities{
	SessionsPersistent:     true,
	MultiSessionConcurrent: true,
	Images:                 true,
	EmbeddedContext:        true,
}

// SkillFormat 该 runtime 消费的 skill 内容格式（投影 Projection.Format 的取值）。
//
// 取值与 pkg/capability.FormatSkillMDFrontmatter 逐字相同，但**不 import capability**：
// runtime 侧只声明「我要什么形态」，合成由 capability 做（P-1：runtime 不依赖 capability）。
// 两边对不上时由 internal/manager 的编排层报错，而不是靠类型系统假装对得上。
const SkillFormat = "SKILL.md+frontmatter"

// Notes 约束说明（给 README/文档位，暂不外露 REST）。
const Notes = "会话能力完整；依赖本机 opencode（含 ACP 子命令）安装"
