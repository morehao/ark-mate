package assembly

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/core/scratch"
)

func TestCleanOrphanScratchUnder(t *testing.T) {
	cwd := t.TempDir()
	root := scratch.RootFor(cwd)
	mk := func(id string) {
		if err := os.MkdirAll(filepath.Join(root, id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mk("ses_live")
	mk("ses_orphan1")
	mk("ses_orphan2")

	// 交付目录里的用户文件，清理不能动它。
	userFile := filepath.Join(cwd, "kept.md")
	if err := os.WriteFile(userFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	live := map[string]bool{"ses_live": true}
	a := &assembly{}
	a.cleanOrphanScratchUnder(context.Background(), cwd, live)

	if _, err := os.Stat(filepath.Join(root, "ses_live")); err != nil {
		t.Fatal("存活会话的 scratch 必须保留")
	}
	for _, id := range []string{"ses_orphan1", "ses_orphan2"} {
		if _, err := os.Stat(filepath.Join(root, id)); !os.IsNotExist(err) {
			t.Fatalf("孤儿 scratch %s 应被删除", id)
		}
	}
	if _, err := os.Stat(userFile); err != nil {
		t.Fatal("清理绝不能删除交付目录里的用户文件")
	}
}

func TestCleanOrphanScratchUnderNoDir(t *testing.T) {
	// 没有 .scratch 目录是常态，应静默 no-op（不 panic、不报错）。
	a := &assembly{}
	a.cleanOrphanScratchUnder(context.Background(), t.TempDir(), map[string]bool{})
}
