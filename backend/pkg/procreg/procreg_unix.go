//go:build unix

package procreg

import (
	"errors"
	"syscall"
	"time"
)

// alive 进程是否仍存活。
//
// 注意：已被杀死但父进程尚未 wait 的**僵尸进程**对 kill(pid, 0) 仍返回成功，
// 会被误判为存活（曾导致回收后仍判「存活」→ 误发 SIGKILL）。因此先对本进程的子进程做
// 非阻塞收尸（Wait4 WNOHANG）：收尸成功即视为已消失；非本进程子进程返回 ECHILD，退化为
// signal 0 探测（EPERM 视为存在——存在但不属于当前用户）。
func alive(pid int) bool {
	if reaped(pid) {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// reaped 对自己 fork 的子进程做非阻塞回收；wpid == pid 表示已退出（僵尸已收尸）。
func reaped(pid int) bool {
	var ws syscall.WaitStatus
	wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	return err == nil && wpid == pid
}

// gone 判断信号错误是否表示目标已不存在（回收后重试场景）。
func gone(err error) bool { return errors.Is(err, syscall.ESRCH) }

// pgidOf 取进程当前进程组 id（失败返回 -1）。
func pgidOf(pid int) int {
	pg, err := syscall.Getpgid(pid)
	if err != nil {
		return -1
	}
	return pg
}

// killGroup 向登记条目对应的进程组（缺 pgid 时退化为单进程）发信号。
func killGroup(e Entry, sig syscall.Signal) error {
	if e.PGID > 0 {
		return syscall.Kill(-e.PGID, sig)
	}
	return syscall.Kill(e.PID, sig)
}

func termSignal() syscall.Signal { return syscall.SIGTERM }
func killSignal() syscall.Signal { return syscall.SIGKILL }

// waitGone 轮询等待进程消失，最多等待 d。
func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !alive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
