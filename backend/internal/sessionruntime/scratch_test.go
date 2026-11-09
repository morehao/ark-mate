package sessionruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCwdScratchEnsureAndClean(t *testing.T) {
	cwd := t.TempDir()
	sid := "ses_test01"
	s := cwdScratch{}

	if err := s.Ensure(cwd, sid); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	dir := filepath.Join(cwd, ".scratch", sid)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("scratch 目录应存在: %v", err)
	}
	// 幂等。
	if err := s.Ensure(cwd, sid); err != nil {
		t.Fatalf("Ensure 幂等: %v", err)
	}

	// 在交付目录放一个用户文件，Clean 不能动它。
	userFile := filepath.Join(cwd, "report.docx")
	if err := os.WriteFile(userFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.Clean(cwd, sid); err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("Clean 后 scratch 目录应被删除")
	}
	if _, err := os.Stat(userFile); err != nil {
		t.Fatal("Clean 绝不能删除交付目录里的用户文件")
	}
}

func TestCwdScratchEmptyNoop(t *testing.T) {
	s := cwdScratch{}
	if err := s.Ensure("", "sid"); err != nil {
		t.Fatalf("空 cwd Ensure 应 no-op: %v", err)
	}
	if err := s.Clean("", "sid"); err != nil {
		t.Fatalf("空 cwd Clean 应 no-op: %v", err)
	}
	if err := s.Clean(t.TempDir(), "a/b"); err != nil {
		t.Fatalf("含分隔符 sid Clean 应 no-op: %v", err)
	}
}

func TestCwdScratchPlatformRoot(t *testing.T) {
	// 二期：root 非空 → 落在 <root>/<sid>（在 cwd 之外），与 cwd 无关。
	root := filepath.Join(t.TempDir(), ".scratch")
	cwd := t.TempDir()
	sid := "ses_root01"
	s := cwdScratch{root: root}

	if p := s.sessionDir(cwd, sid); p != filepath.Join(root, sid) {
		t.Fatalf("平台根模式下 sessionDir=%q，期望 %q", p, filepath.Join(root, sid))
	}
	if err := s.Ensure(cwd, sid); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(root, sid)); err != nil || !fi.IsDir() {
		t.Fatalf("应建在平台根下: %v", err)
	}
	// cwd 内不应出现 .scratch。
	if _, err := os.Stat(filepath.Join(cwd, ".scratch")); !os.IsNotExist(err) {
		t.Fatal("平台根模式下 cwd 内不应有 .scratch")
	}
	if err := s.Clean(cwd, sid); err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, sid)); !os.IsNotExist(err) {
		t.Fatal("Clean 后平台根下该会话目录应被删")
	}
}
