package dbclient

import (
	"os"
	"path/filepath"
	"testing"

	// 本包的测试要注册 glog 的 zap driver：日志出口归装配根（cmd/mate 的匿名引入），
	// 而 test 二进制不含 cmd/mate，而 Open → dbgorm.New → glog.NewLogger 需要它。
	// pkg/ 不能 import internal/testutil（那才是跨包夹具），所以在这里就地注册。
	_ "github.com/morehao/golib/glog/driver/zap"
)

// 本文件记录 SQLite 句柄的**真实**行为（2026-10-10 起 driver 由 golib 提供）。
//
// 原先本仓自注册了一个纯 Go dialector（glebarez/sqlite），它在 DSN 里注入
// foreign_keys / busy_timeout / journal_mode(WAL) / synchronous，并用
// `arkmate_mem_<n>` 保证内存库唯一命名。改用 golib 的 `dbgorm/driver/sqlite`
// 之后这些都不再成立——下面几条守卫把新行为钉住，**红了就说明行为又变了**，
// 不要默默放宽，先回来看这段注释。

// TestOpenMemoryIsReallyInMemory 内存库必须真的是内存库。
//
// `file:name` 只表示「以 URI 形式给出文件名」，SQLite 会把它当成**磁盘上的相对路径**
// 开库——只有 `:memory:` 这个名字或显式 `mode=memory` 才是内存库。一旦退化成相对
// 路径文件，测试会跨运行读到上一次的数据（表现为「凭空多出来的历史行」）。
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

// TestOpenMemoryIsProcessShared 记录 golib driver 的一个真实陷阱：**内存库是进程内共享的**。
//
// golib 把内存库统一改写成 `file::memory:?cache=shared`（固定名字），因此同进程内两次
// `Open(":memory:")` 拿到的是**同一个库**，第二个句柄能看到第一个建的表。
//
// 这不是我们想要的行为，是"照字面改用 golib driver"的既定代价（自注册 dialector 曾用
// `arkmate_mem_<n>` 保证唯一命名）。实际后果：**任何依赖 ":memory:" 隔离的测试都会互相污染**，
// 所以测试夹具一律走 t.TempDir() 下的文件库（见 internal/testutil.TempDBPath）；
// internal/store 里沿用 `:memory:` 的夹具必须为每个测试打开独立文件库。
//
// 这条守卫的作用是反向的：它**故意钉住共享语义**。哪天 golib（或本仓）改成唯一命名，
// 它会红——那时把上面那句结论一起改掉，而不是只改测试。
func TestOpenMemoryIsProcessShared(t *testing.T) {
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
	// b **能**看到 a 的表：两个句柄落在同一个共享缓存库上。
	var n int64
	if err := b.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE name = 't'`).Scan(&n).Error; err != nil {
		t.Fatalf("query b: %v", err)
	}
	if n == 0 {
		t.Fatalf("内存库不再是进程内共享的（driver 改名了？见本测试注释）")
	}
}

// TestOpenConnectionPragmasAreGolibDefaults 钉住 golib driver 的连接级默认值。
//
// 自注册 dialector 时代这里是：foreign_keys=1、busy_timeout=5000、文件库 WAL + synchronous=NORMAL。
// 现在只剩 mattn 自己的默认值——**外键约束不再由库兜底**（events/runs/messages 仍以 FK
// 引用 sessions，但违反时不会报错），文件库也不再是 WAL。
func TestOpenConnectionPragmasAreGolibDefaults(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "mate.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	conn, err := sqlDB.Conn(t.Context())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer func() { _ = conn.Close() }()

	var fk int
	if err := conn.QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 0 {
		t.Errorf("foreign_keys=%d，期望 0（golib driver 不注入 foreign_keys；若已改为 1，请连同本文件顶部注释一起更新）", fk)
	}
	var timeout int
	if err := conn.QueryRowContext(t.Context(), `PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Errorf("busy_timeout=%d，期望 mattn 的默认 5000", timeout)
	}
}

// TestOpenReadOnly 只读 URL（`?mode=ro`）必须能开、能读、不能写。
//
// internal/store 的只读句柄就是走这条路（readonly.go），它是「只读体检」类能力的底座。
func TestOpenReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mate.db")
	rw, err := Open(path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	if err := rw.Exec(`CREATE TABLE t(v TEXT)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := rw.Exec(`INSERT INTO t(v) VALUES('x')`).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}

	ro, err := Open(path + "?mode=ro")
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	var v string
	if err := ro.Raw(`SELECT v FROM t`).Scan(&v).Error; err != nil {
		t.Fatalf("read ro: %v", err)
	}
	if v != "x" {
		t.Errorf("只读句柄读到 %q，期望 x", v)
	}
	if err := ro.Exec(`INSERT INTO t(v) VALUES('y')`).Error; err == nil {
		t.Error("只读句柄不应能写入")
	}
}
