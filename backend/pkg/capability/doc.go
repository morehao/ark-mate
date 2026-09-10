// Package capability 实现 P1 能力数据面（docs/agent-runtime-refactor-design-20260908.md §4.4）：
// Skill 目录 + MCP 注册表 + Tool 目录（平台唯一事实源）。
//
// 与 Agent definitions / provider 档案同范式：目录 + JSON 文件为事实源，
// 进程内 Registry 聚合（启动加载合并 + Register* 供平台种子/未来运行态注入）。
// P1 只做数据面与「REST 可列出能力」；注入（mcpServers → session/new）、skill 物化
// 属 P2 投影层，本包仅保留其所需字段（Skill.Sources / McpServer.Headers / Tool.Schema）。
package capability
