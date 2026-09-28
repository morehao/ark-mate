//go:build unix

package command

import (
	"os/signal"
	"syscall"
)

// ignoreSIGPIPE 让"往没有读者的管道写日志"变成一次普通的写错误，而不是立刻终止进程。
//
// 为什么必须显式忽略（M1-6 实测抓到）：桌面壳被强杀后，核心的 stdout/stderr 管道就没有读者了；
// Go 运行时对**写入 fd 1/2** 的 EPIPE 会按默认动作处理——**直接以 SIGPIPE 终止进程**，
// 于是"父进程消失 → 优雅停机"这条链根本跑不完（HTTP 没停、编排器没关、runtime 子进程没收），
// 表现为"核心确实退了，但是以最粗暴的方式退的"。忽略之后写失败只是一个 error（slog 会吞掉），
// 停机流程照常走完。
func ignoreSIGPIPE() {
	signal.Ignore(syscall.SIGPIPE)
}
