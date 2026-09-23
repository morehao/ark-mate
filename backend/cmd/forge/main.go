// forge 是 ark-forge 控制面的单二进制入口（无默认子命令）。
//
// 本文件只做进程级收尾：把参数交给 internal/app/command 分发，并把返回码交还 shell。
// 各子命令实现（serve/demo/migrate/transcripts/config）见
// internal/app/command/。
package main

import (
	"os"

	// 内嵌 IANA 时区库（约 +450KB 二进制）。
	//
	// 定时任务（M4）**必须**能把 `Asia/Shanghai` 这类时区名解析成一个真实时刻，而
	// `CGO_ENABLED=0` 的容器镜像里可能根本没有 `/usr/share/zoneinfo`（scratch/distroless 尤其）。
	// 没有它，`time.LoadLocation` 会失败，表现是"预览/调度在一台机器上对、在容器里错"——
	// 这是最不该靠"部署时记得挂 zoneinfo"来兜的一类问题：时区错了，任务就会在错误的时间
	// 干有副作用的事。内嵌一份，行为与宿主机无关。
	"github.com/morehao/ark-forge/internal/app/command"
	_ "time/tzdata"
)

func main() {
	os.Exit(command.Run(os.Args[1:]))
}
