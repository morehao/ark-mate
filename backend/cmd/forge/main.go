// forge 是 ark-forge 控制面的单二进制入口（无默认子命令）。
//
// 本文件只做进程级收尾：把参数交给 internal/app/command 分发，并把返回码交还 shell。
// 各子命令实现（serve/demo/migrate/transcripts/config/acp-serve/acp-demo）见
// internal/app/command/。
package main

import (
	"os"

	"github.com/morehao/ark-forge/internal/app/command"
)

func main() {
	os.Exit(command.Run(os.Args[1:]))
}
