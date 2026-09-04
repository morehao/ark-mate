// Package config 提供进程级配置加载。
// M0 起步：CLI flag（cmd/forge 解析）+ 环境变量兜底；配置文件随 M2 的 `.forge/config`
// 目录化引入（见 docs/design-and-roadmap.md §6 M2）。
package config

import (
	"os"
	"path/filepath"
	"time"
)

// Config 是进程级配置。
type Config struct {
	// Addr HTTP 监听地址。
	Addr string
	// TranscriptDir 会话 wire transcript 落盘目录（M1 #5；空 = 禁用）。
	TranscriptDir string
	// TurnTimeout 单轮 prompt 看门狗超时（0 = 关闭）。runtime 吞错/挂起（如 opencode 模型限流
	// 只记自己日志不上报）时，超时自动 cancel 并发终态事件，避免 busy 永久占用。
	TurnTimeout time.Duration
}

// Default 返回默认配置。
func Default() Config {
	return Config{Addr: ":8099"}
}

// Load 在默认值之上应用环境变量兜底（flag 优先级最高，由 cmd 层覆盖）。
func Load() Config {
	c := Default()
	if v := os.Getenv("FORGE_ADDR"); v != "" {
		c.Addr = v
	}
	if v := os.Getenv("FORGE_TRANSCRIPTS"); v != "" {
		c.TranscriptDir = v
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		c.TranscriptDir = filepath.Join(home, ".forge", "transcripts")
	} else {
		c.TranscriptDir = ".forge/transcripts"
	}
	// FORGE_TURN_TIMEOUT：Go duration 文本（如 10m/1h）；非法值视为 0（关闭看门狗）。
	if v := os.Getenv("FORGE_TURN_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.TurnTimeout = d
		}
	}
	return c
}
