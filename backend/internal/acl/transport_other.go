//go:build !unix

package acl

import "os/exec"

func setupProcAttr(cmd *exec.Cmd) {}

func killProc(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
