package procreg_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/morehao/ark-mate/pkg/procreg"
)

// spawnGroup 拉起一个独立进程组的长驻子进程（模拟 runtime 子进程）。
func spawnGroup(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return cmd
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for alive(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

func TestRegistryPersistAndOrphans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "processes.json")
	reg, err := procreg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Instance() == "" {
		t.Fatal("实例 id 应为非空")
	}
	if err := reg.Add(procreg.Entry{PID: 4242, PGID: 4242, Command: "mate", Runtime: "stub"}); err != nil {
		t.Fatal(err)
	}
	// 落盘形状：version + entries。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Version int `json:"version"`
		Entries []struct {
			PID      int    `json:"pid"`
			Instance string `json:"instance"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("注册表非 JSON: %v", err)
	}
	if file.Version != 1 || len(file.Entries) != 1 || file.Entries[0].PID != 4242 {
		t.Fatalf("注册表内容不符: %s", raw)
	}
	if file.Entries[0].Instance != reg.Instance() {
		t.Fatal("条目应带本实例 id")
	}
	// 本实例条目不算孤儿。
	if got := reg.Orphans(); len(got) != 0 {
		t.Fatalf("本实例条目不应是孤儿: %+v", got)
	}

	// 新实例打开同一文件 → 旧条目成为孤儿候选。
	reg2, err := procreg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	orphans := reg2.Orphans()
	if len(orphans) != 1 || orphans[0].PID != 4242 {
		t.Fatalf("新实例应看到 1 条孤儿: %+v", orphans)
	}

	// Remove 落盘。
	if err := reg.Remove(4242); err != nil {
		t.Fatal(err)
	}
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("Remove 后应为空: %+v", got)
	}
}

func TestReapKillsOrphanGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "processes.json")
	// 模拟上一实例：登记后不清理，进程仍在跑。
	child := spawnGroup(t, "sleep 60")
	pid := child.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	old, err := procreg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Add(procreg.Entry{PID: pid, PGID: pgid, Command: "sh", Runtime: "stub"}); err != nil {
		t.Fatal(err)
	}

	// 新实例启动 → 回收孤儿。
	fresh, err := procreg.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := fresh.Reap(500 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Reaped) != 1 || rep.Reaped[0].PID != pid {
		t.Fatalf("应回收 1 个孤儿: %+v", rep)
	}
	if alive(pid) {
		t.Fatalf("孤儿进程 %d 应已被杀", pid)
	}
	if got := fresh.List(); len(got) != 0 {
		t.Fatalf("回收后注册表应清空: %+v", got)
	}
}

func TestReapCleansStaleEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "processes.json")
	old, _ := procreg.Open(path)
	// 999999 远超 macOS/Linux 默认 pid 上限，必然不存在。
	if err := old.Add(procreg.Entry{PID: 999999, PGID: 999999, Command: "gone"}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := procreg.Open(path)
	rep, err := fresh.Reap(100 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Cleaned) != 1 || len(rep.Reaped) != 0 {
		t.Fatalf("不存在的进程只清账: %+v", rep)
	}
	if got := fresh.List(); len(got) != 0 {
		t.Fatalf("清账后应为空: %+v", got)
	}
}

// TestReapSkipsPidReuse PID 复用保护：登记条目存在但 pgid 与登记值不一致（说明 pid 已被
// 无关进程复用）→ 不得杀，只摘除登记。
func TestReapSkipsPidReuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "processes.json")
	child := spawnGroup(t, "sleep 60")
	pid := child.Process.Pid
	realPgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := procreg.Open(path)
	// 故意登记一个错误的 pgid（模拟 pid 复用后 pgid 变化）。
	if err := old.Add(procreg.Entry{PID: pid, PGID: realPgid + 12345, Command: "not-ours"}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := procreg.Open(path)
	rep, err := fresh.Reap(200 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Skipped) != 1 || len(rep.Reaped) != 0 {
		t.Fatalf("pgid 不一致应跳过: %+v", rep)
	}
	if !alive(pid) {
		t.Fatalf("无关进程 %d 不应被杀", pid)
	}
	if got := fresh.List(); len(got) != 0 {
		t.Fatalf("跳过条目也应摘除登记: %+v", got)
	}
}

func TestDisabledAndCorruptRegistry(t *testing.T) {
	// path 为空 → 禁用，全部方法安全 no-op。
	reg, err := procreg.Open("")
	if err != nil || reg != nil {
		t.Fatalf("空路径应返回 nil 注册表: %v %v", reg, err)
	}
	if err := reg.Add(procreg.Entry{PID: 1}); err != nil {
		t.Fatalf("禁用态 Add 应 no-op: %v", err)
	}
	if rep, err := reg.Reap(0); err != nil || !rep.Disabled {
		t.Fatalf("禁用态 Reap 应报 Disabled: %+v %v", rep, err)
	}

	// 损坏文件：备份 + 以空表继续（不阻塞启动）。
	path := filepath.Join(t.TempDir(), "processes.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	reg2, err := procreg.Open(path)
	if err != nil {
		t.Fatalf("损坏文件不应报错: %v", err)
	}
	if got := reg2.List(); len(got) != 0 {
		t.Fatalf("损坏文件应以空表继续: %+v", got)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("应备份损坏文件: %v", err)
	}
}
