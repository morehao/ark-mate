// Package ops 承载 `mate` 子命令的**实现**（doctor / migrate / capability / provider /
// automation / mcp / demo / config / transcripts / serve）。
//
// 为什么它不是 `internal/adapters/inbound/cli/cmd`：v3.1 §3.2.1 要求命令树（`cli`）不得 import store/domain/mechanism；
// 而这些实现大量使用它们（doctor 是最大的一个）。所以命令树只做**参数分发**，实现落在
// `internal/adapters/inbound/cli/ops`（层 16，位于 `app` 之上、`cli` 之下）。这是一处刻意的分层扩展，
// 理由见 `.dsh/docs/plans/2026-10-19-backend-v3.1-execution-plan.md`「与设计文档的已知偏离」。
package ops

import (
	"github.com/morehao/ark-mate/internal/config"
)

// mustConfig 解析配置（含 .ark-mate/config 目录化）。配置文件非法时打印明确错误并退出 2
// （配置错误必须在启动阶段暴露，而不是静默回落默认值）。
//
// 连 Sources 一起返回：来源不只是排障信息，还是**权限事实**——被进程 env/flag 锁定的配置键，
// 面板写入必然被覆盖，serve 据此把它转成"只读设置"（见 config.NewSettingsStore）。
func mustConfig() (config.Config, config.Sources) { return config.MustLoad() }
