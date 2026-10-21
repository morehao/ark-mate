//go:build unix

package acp

import (
	"os/exec"
	"syscall"
)

// setupProcAttr 让子进程成为独立进程组，便于 Close 时整树回收（含孙进程）。
func setupProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProc 杀整个进程组（负数 PID = 进程组）。
func killProc(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// 进程组不存在时退化为杀直接子进程。
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// procPgid 取进程组 id（失败返回 0；供 PidInfo 登记进程组用）。
func procPgid(pid int) int {
	pg, err := syscall.Getpgid(pid)
	if err != nil {
		return 0
	}
	return pg
}
