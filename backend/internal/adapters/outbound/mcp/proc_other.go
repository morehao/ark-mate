//go:build !unix

package mcp

import "os/exec"

// setupProcAttr 非 unix 平台不提供进程组（回收退化为单进程）。
func setupProcAttr(cmd *exec.Cmd) {}

// killProc 非 unix 平台只杀直接子进程。
func killProc(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
