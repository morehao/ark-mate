package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ——— 只读打开（`mate doctor` 的唯一入口） ———

// TestOpenReadOnlyDoesNotMigrate 只读打开**不跑迁移**：一个"库文件在、但没有本版本的表"的库
// 必须如实报"读不到"，而不是被顺手升级（升级之后就再也回退不到旧二进制了）。
func TestOpenReadOnlyDoesNotMigrate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mate.db")
	// 造一个"有 SQLite 头、但没有本仓任何表"的库（等价于 v11 之前的老库 / 别人的库）。
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.DB().Exec(`DROP TABLE providers`).Error; err != nil {
		t.Fatalf("drop providers: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("只读打开应成功（能不能读到表是另一回事）：%v", err)
	}
	defer func() { _ = ro.Close() }()
	if _, err := ro.ListProviderDefinitions(ctx); err == nil {
		t.Fatal("表不存在就该报错——只读打开不该把表建出来")
	}
	// schema_migrations 里仍只有那一条（本该由 Migrate 记录的历史），且 providers 表仍不存在。
	var n int64
	if err := ro.DB().Raw(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'providers'`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("只读打开建出了 providers 表（命中 %d 行）——它必须是纯读的", n)
	}
}

// TestOpenReadOnlyRefusesWrites 只读句柄上的写操作必须当场失败（不是静默成功）。
func TestOpenReadOnlyRefusesWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mate.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if got, ok, err := ro.GetMeta(ctx, "k"); err != nil || !ok || got != "v" {
		t.Fatalf("只读句柄应能读：v=%q ok=%t err=%v", got, ok, err)
	}
	if err := ro.SetMeta(ctx, "k", "changed"); err == nil {
		t.Fatal("只读句柄不该写得进去")
	}
}

// TestOpenReadOnlyRejectsBadPaths 三种不是"可用库"的输入各自报错（调用方据此区分
// "还没建库"与"库坏了"）。
func TestOpenReadOnlyRejectsBadPaths(t *testing.T) {
	dir := t.TempDir()

	if _, err := OpenReadOnly("   "); err == nil || !strings.Contains(err.Error(), "路径") {
		t.Fatalf("空路径应报错：%v", err)
	}
	if _, err := OpenReadOnly(filepath.Join(dir, "nope.db")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("不存在的库应返回 NotExist（能被 errors.Is 认出）：%v", err)
	}
	if _, err := OpenReadOnly(dir); err == nil || !strings.Contains(err.Error(), "目录") {
		t.Fatalf("目录应报错：%v", err)
	}
	empty := filepath.Join(dir, "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(empty); err == nil || !strings.Contains(err.Error(), "长度为 0") {
		t.Fatalf("空文件应报错：%v", err)
	}
	// 非 SQLite 内容：打开可能成功、首次查询必然失败——这里只要求**不 panic 且不写**。
	junk := filepath.Join(dir, "junk.db")
	if err := os.WriteFile(junk, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err := OpenReadOnly(junk); err == nil {
		_ = st.Close()
	}
}
