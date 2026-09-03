// Package config 提供进程级配置加载。
// M0 起步：CLI flag（cmd/forge 解析）+ 环境变量兜底；配置文件随 M2 的 `.forge/config`
// 目录化引入（见 docs/design-and-roadmap.md §6 M2）。
package config

import "os"

// Config 是进程级配置。
type Config struct {
	// Addr HTTP 监听地址。
	Addr string
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
	return c
}
