//go:build !unix

package acp

import "os/exec"

func setupProcAttr(cmd *exec.Cmd) {}

func killProc(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// procPgid 非 unix 平台不提供进程组（返回 0，回收退化为单进程）。
func procPgid(int) int { return 0 }
