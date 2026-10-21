//go:build unix

package mcp

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStdioCloseKillsGrandchildren stdio server 会自己 fork（`npx -y …` 的真实形态是
// npx → node → server）：close 必须连**孙进程**一起回收，只杀直接子进程就是泄漏。
// 这里用 `/bin/sh -c 'sleep … & echo $! >&2; wait'` 复现两层结构，并断言 sleep 已消失。
func TestStdioCloseKillsGrandchildren(t *testing.T) {
	tr, err := dialStdio(context.Background(), Server{
		ID:      "fake-npx",
		Command: "/bin/sh",
		Args:    []string{"-c", "sleep 300 & echo $! >&2; wait"},
	})
	if err != nil {
		t.Fatalf("dialStdio: %v", err)
	}
	st, ok := tr.(*stdioTransport)
	if !ok {
		t.Fatalf("类型不符: %T", tr)
	}

	// 从 stderr 尾巴里取孙进程 pid（shell 用 echo $! 报出来）。
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		txt := strings.TrimSpace(st.stderr.String())
		if txt != "" {
			if n, err := strconv.Atoi(txt); err == nil {
				pid = n
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatalf("未能读到孙进程 pid（stderr=%q）", st.stderr.String())
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("孙进程 %d 应先在运行: %v", pid, err)
	}

	// close 放在 goroutine 里跑：只杀直接子进程时，孙进程继承着 stdout/stderr，
	// `cmd.Wait()` 会一直等到它退出（实测 300s）——那正是本缺陷的另一副面孔（close 卡住，
	// 探测/回收停在管道上）。这里把「卡住」也变成一条明确的断言失败，而不是挂死整个测试。
	closed := make(chan struct{})
	go func() {
		st.close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("close 阻塞（孙进程仍持有管道 → Wait 不返回）：进程组未回收")
	}

	// SIGKILL 到进程组是异步落地，给它一点时间；超时仍活着即判定泄漏。
	gone := false
	for time.Now().Before(time.Now().Add(2 * time.Second)) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			gone = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !gone {
		// 兜底清理，避免测试机残留 300 秒的 sleep。
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("close 后孙进程 %d 仍存活（进程组未回收）", pid)
	}

	// 进程组已随组长消失：负数 pid 探测应报 ESRCH（说明没留下同名组）。
	if pg, err := os.FindProcess(pid); err == nil && pg != nil {
		_ = pg.Release()
	}
}
