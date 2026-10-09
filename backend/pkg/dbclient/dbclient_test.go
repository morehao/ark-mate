package dbclient

import (
	"os"
	"strings"
	"testing"
)

// TestOpenMemoryIsReallyInMemory 内存库必须真的是内存库。
//
// 这条守卫是有代价换来的：`file:name` 只表示「以 URI 形式给出文件名」，SQLite 会把它
// 当成**磁盘上的相对路径**开库——只有 `:memory:` 这个名字或显式 `mode=memory` 才是内存库。
// 一旦漏掉 mode=memory，唯一命名会把内存库变成工作目录下的一堆库文件，
// 测试还会跨进程读到上一次运行的数据（表现为「凭空多出来的历史行」）。
func TestOpenMemoryIsReallyInMemory(t *testing.T) {
	dir := t.TempDir()
	// 切到临时目录：若 DSN 退化成相对路径文件，就会在这里落地，从而被下面的检查抓到。
	t.Chdir(dir)

	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var dbs []struct {
		Seq  int
		Name string
		File string
	}
	if err := db.Raw("PRAGMA database_list").Scan(&dbs).Error; err != nil {
		t.Fatalf("database_list: %v", err)
	}
	for _, d := range dbs {
		if d.File != "" {
			t.Errorf("内存库的 main 竟然是磁盘文件 %q（DSN 缺 mode=memory）", d.File)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("内存库不应在磁盘留下任何文件，实际留下: %v", names)
	}
}

// TestOpenMemoryIsolated 两次 Open(":memory:") 必须是两个互不可见的库。
// 内存库若共用一个 URI 名，SQLite 的共享缓存会让它们变成同一个库，测试之间互相污染。
func TestOpenMemoryIsolated(t *testing.T) {
	a, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open a: %v", err)
	}
	b, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open b: %v", err)
	}
	if err := a.Exec(`CREATE TABLE t(v TEXT)`).Error; err != nil {
		t.Fatalf("create table on a: %v", err)
	}
	if err := a.Exec(`INSERT INTO t(v) VALUES('x')`).Error; err != nil {
		t.Fatalf("insert on a: %v", err)
	}
	// b 看不到 a 的表（不同库）。
	var n int64
	if err := b.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE name = 't'`).Scan(&n).Error; err != nil {
		t.Fatalf("query b: %v", err)
	}
	if n != 0 {
		t.Fatalf("第二个内存库不应看到第一个库的表，实际命中 %d 张", n)
	}
}

// TestOpenAppliesConnectionPragmas 校验 PRAGMA 走 DSN 对连接池内每条连接都生效。
func TestOpenAppliesConnectionPragmas(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// 连接池会被反复取用；多取几次连接确保每条新连接都带上了 PRAGMA。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	for i := 0; i < 3; i++ {
		conn, err := sqlDB.Conn(t.Context())
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		var fk int
		if err := conn.QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			_ = conn.Close()
			t.Fatalf("read foreign_keys: %v", err)
		}
		if fk != 1 {
			t.Errorf("第 %d 条连接 foreign_keys=%d，期望 1", i, fk)
		}
		var timeout int
		if err := conn.QueryRowContext(t.Context(), `PRAGMA busy_timeout`).Scan(&timeout); err != nil {
			_ = conn.Close()
			t.Fatalf("read busy_timeout: %v", err)
		}
		if timeout != 5000 {
			t.Errorf("第 %d 条连接 busy_timeout=%d，期望 5000", i, timeout)
		}
		_ = conn.Close()
	}
}

// TestOpenEnforcesForeignKeys FK 必须由库兜底（events 引用 conversations）。
func TestOpenEnforcesForeignKeys(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(`CREATE TABLE p(id TEXT PRIMARY KEY)`).Error; err != nil {
		t.Fatalf("create p: %v", err)
	}
	if err := db.Exec(`CREATE TABLE c(id TEXT PRIMARY KEY, pid TEXT NOT NULL REFERENCES p(id))`).Error; err != nil {
		t.Fatalf("create c: %v", err)
	}
	if err := db.Exec(`INSERT INTO c(id, pid) VALUES('c1', 'missing')`).Error; err == nil {
		t.Fatal("引用不存在的父行应当报错（foreign_keys 未生效）")
	}
}

// TestBuildDSN 覆盖 URL 归一化：路径转义、内存库唯一命名、PRAGMA 注入。
func TestBuildDSN(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		wantPrefix string
		wantHas    []string
		wantNotHas []string
	}{
		{
			name:       "绝对路径",
			url:        "sqlite:///tmp/mate.db",
			wantPrefix: "file:/tmp/mate.db?",
			wantHas:    []string{"_txlock=immediate", "journal_mode%28WAL%29", "foreign_keys%281%29"},
		},
		{
			name:       "内存库唯一命名且共享缓存",
			url:        "sqlite://:memory:",
			wantPrefix: "file:arkmate_mem_",
			wantHas:    []string{"mode=memory", "cache=shared", "foreign_keys%281%29"},
			// 内存库不支持 WAL。
			wantNotHas: []string{"journal_mode"},
		},
		{
			name:       "已是 file: URI 时原样保留",
			url:        "sqlite://file:relative.db",
			wantPrefix: "file:relative.db?",
			wantHas:    []string{"_txlock=immediate"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildDSN(tc.url)
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Fatalf("DSN = %q，期望前缀 %q", got, tc.wantPrefix)
			}
			for _, want := range tc.wantHas {
				if !strings.Contains(got, want) {
					t.Errorf("DSN %q 缺少 %q", got, want)
				}
			}
			for _, notWant := range tc.wantNotHas {
				if strings.Contains(got, notWant) {
					t.Errorf("DSN %q 不应包含 %q", got, notWant)
				}
			}
		})
	}
}
