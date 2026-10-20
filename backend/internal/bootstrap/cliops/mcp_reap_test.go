//go:build unix

package cliops

// ——— M7-5：网关的进程回收（父进程消失 → 自己退出）———
//
// 为什么要一条**真进程**的用例：这条链的两端都是内核行为（reparent 导致 ppid 变化、管道 EOF），
// 进程内调函数测不到。要在真环境里区分"是 EOF 让它退的"还是"是 ppid 变化让它退的"，就必须把
// stdin **一直开着**——否则退出可以完全由 EOF 解释，兜底逻辑等于没验。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMcpBridgeExitsWhenParentDies(t *testing.T) {
	bin := buildMateCLI(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "gateway.pid")

	// stdin 用一根**真 OS 管道**、且测试期间只关读端不关写端：EOF 永远到不了子进程，于是
	// "网关退出了"只能由 ppid 兜底解释。这里刻意不用 `io.Pipe`/`cmd.Output()` 之类的封装——
	// 它们会在 Wait 时等拷贝 goroutine，而后台网关正继承着那根管道的写端（wait 会挂到天荒地老，
	// 301s 的超时踩过一次）。
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pw.Close() }()
	defer func() { _ = pr.Close() }()

	// 中间壳：把网关挂到后台后自己立刻退出。网关随即被 reparent（ppid 变成 1/launchd），
	// 这正是"底座被强杀、来不及发信号"的形态。stdout/stderr 置 nil（= /dev/null）：否则后台
	// 网关会继承测试的管道，`sh` 的 Wait 又要等它结束。
	script := fmt.Sprintf("'%s' mcp bridge --agent a1 --runtime r1 & echo $! > '%s'; exit 0", bin, pidFile)
	sh := exec.Command("sh", "-c", script)
	sh.Env = isolatedCLIEnv(t)
	sh.Stdin = pr
	if err := sh.Run(); err != nil {
		t.Fatalf("中间壳应立刻退出: %v", err)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("应拿到网关 pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("pid 不合法: %q", raw)
	}
	// 兜底清理：用例失败时别把孤儿留在机器上。
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	deadline := time.Now().Add(20 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return // 已自行退出：兜底生效
		}
		if time.Now().After(deadline) {
			t.Fatalf("父进程消失后网关应自行退出（ppid 轮询兜底）；pid %d 仍存活", pid)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
