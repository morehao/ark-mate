//go:build unix

package mcp

import (
	"os/exec"
	"syscall"
)

// setupProcAttr 让 stdio MCP server 子进程成为独立进程组，便于 close 时整树回收。
//
// 与 agent 侧（`internal/infra/protocol/acp/transport_unix.go:12`）同一套做法。为什么必须做：`npx -y @modelcontextprotocol/
// server-*` 这类命令自己会再 fork（npx → node → 真实 server），只杀直接子进程会留下**孤儿孙进程**
// ——它们继续持有 stdio 管道与网络连接，探测结束即泄漏。
func setupProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProc 杀整个进程组（负数 PID = 进程组）；进程组不存在时退化为杀直接子进程。
func killProc(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
