package config

import (
	"fmt"
	"os"
)

// MustLoad 读配置；失败时打印可操作的提示并**直接退出**（码 2）。
//
// 归在这里而不是命令层：所有命令入口（serve / migrate / doctor / …）都需要同一条失败处置，
// 两份实现迟早在"提示文案"或"退出码"上分叉。它只服务命令行，服务端启动路径不用它。
func MustLoad() (Config, Sources) {
	cfg, sources, err := LoadResolved("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置错误: %v\n\n配置目录: %s（MATE_CONFIG_DIR 可改）\n", err, ConfigDirFromEnv())
		os.Exit(2)
	}
	return cfg, sources
}
