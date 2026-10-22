package dbclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	// 本包的测试要注册 glog 的 zap driver：日志出口归装配根（cmd/mate 的匿名引入），
	// 而 test 二进制不含 cmd/mate，而 Open → dbgorm.New → glog.NewLogger 需要它。
	// pkg/ 不能 import internal/testutil（那才是跨包夹具），所以在这里就地注册。
	_ "github.com/morehao/golib/glog/driver/zap"
)

// 本文件记录 SQLite 句柄的**真实**行为。
//
// 三次换驱动的历史：① 本仓自注册的纯 Go dialector（glebarez/sqlite，注入
// foreign_keys / busy_timeout / journal_mode(WAL) / synchronous，并用 `arkmate_mem_<n>`
// 保证内存库唯一命名）→ ② golib v1.32.21 的 mattn/go-sqlite3 方言（CGO，上面前三条全丢、
// 内存库还变成固定名共享）→ ③ golib v1.32.22 的 ncruces/go-sqlite3 纯 Go 方言（本次）。
//
// 下面几条守卫把 ③ 的取值钉住，**红了就说明行为又变了**：不要默默放宽，先回来看这段注释，
// 想清楚新事实是不是我们想要的，再连同 internal/infra/dbclient/gorm.go 的注释一起改。

// TestOpenMemoryIsReallyInMemory 内存库必须真的是内存库。
//
// ncruces 方言把匿名内存库改写成 `file:/mem_<n>?vfs=memdb`，于是 `PRAGMA database_list`
// 报出来的 main **不是空串**（旧 driver 是 `file::memory:?cache=shared`，同样是空 File 之外
// 的写法）。所以判据不能再看"File 是否为空"，而是：名字在 memdb 的命名空间里、
// 磁盘上既没有这个文件、临时目录也一个文件都没多——文件库一旦被静默当成内存库（或反过来），
// 症状是跨运行读到上一次的数据，表现为"凭空多出来的历史行"。
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
	if len(dbs) == 0 {
		t.Fatal("database_list 为空，判据本身失效")
	}
	for _, d := range dbs {
		if !strings.HasPrefix(d.File, "/mem_") {
			t.Errorf("内存库的 main 报成 %q，期望 memdb 命名空间里的 /mem_<n>", d.File)
		}
		if _, err := os.Stat(d.File); err == nil {
			t.Errorf("内存库的 main 在磁盘上真实存在：%q", d.File)
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

// TestOpenMemoryIsIndependent 同进程的两次匿名内存库**互不可见**。
//
// 这条守卫原来的方向是反的：golib v1.32.21 把内存库统一改写成固定的
// `file::memory:?cache=shared`，于是两个句柄落在**同一个**库上——"任何依赖 :memory:
// 隔离的测试都会互相污染"是那时的既定代价（测试夹具因此一律走 t.TempDir() 下的文件库）。
// v1.32.22 的方言每次 Open 都给匿名内存库分配新库名（`file:/mem_<n>?vfs=memdb`），
// 共享缓存也不再被驱动编译进来，所以这个坑没了。
//
// 反过来钉住它同样重要：若哪天又退回固定名，"两次 Open 互不可见"的假设会让
// 依赖隔离的测试**静默**读到别的用例的数据。
func TestOpenMemoryIsIndependent(t *testing.T) {
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
	// b **看不到** a 的表：两个句柄是两个独立的空库。
	var n int64
	if err := b.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE name = 't'`).Scan(&n).Error; err != nil {
		t.Fatalf("query b: %v", err)
	}
	if n != 0 {
		t.Fatalf("两次 Open(\":memory:\") 又落到同一个库上了（driver 改回固定名了？见本测试注释）")
	}
}

// TestOpenNamedMemoryIsShared 命名内存库仍然**按名字共享**（需要共享时的唯一正路）。
//
// 匿名内存库改成互相隔离之后，"跨句柄共享一个内存库"这件事没丢，只是要显式写名字：
// `file:memdb1?mode=memory`（golib 的方言会把它改写到 memdb VFS 并按名字对齐）。
// 不钉这条的话，下一个人只能靠翻 driver 源码判断"还能不能共享"。
func TestOpenNamedMemoryIsShared(t *testing.T) {
	a, err := Open("file:memdb1?mode=memory")
	if err != nil {
		t.Fatalf("open a: %v", err)
	}
	b, err := Open("file:memdb1?mode=memory")
	if err != nil {
		t.Fatalf("open b: %v", err)
	}
	if err := a.Exec(`CREATE TABLE t(v TEXT)`).Error; err != nil {
		t.Fatalf("create table on a: %v", err)
	}
	var n int64
	if err := b.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE name = 't'`).Scan(&n).Error; err != nil {
		t.Fatalf("query b: %v", err)
	}
	if n != 1 {
		t.Fatalf("同名内存库应当共享；看到 %d 张表（driver 改写了命名内存库的语义？见本测试注释）", n)
	}
}

// TestOpenConnectionPragmasFollowGolibAndDriver 钉住 golib v1.32.22 方言的连接级取值。
//
// 自注册 dialector 时代这里是：foreign_keys=1、busy_timeout=5000、文件库 WAL + synchronous=NORMAL。
// v1.32.21（mattn）时代是：foreign_keys=0、busy_timeout 是 mattn 的隐式 5000、非 WAL。
// v1.32.22 是第三条组合，逐条钉住：
//
//   - `busy_timeout=5000` 由 golib 显式注入（它必须排在所有 PRAGMA 之前，否则并发写会直接
//     退化成 SQLITE_BUSY）；
//   - `foreign_keys=1` **不是 golib 注入的**，是 wasm 构建带的 `SQLITE_DEFAULT_FOREIGN_KEYS=1`
//     ——即 DDL 里的 REFERENCES 从"写了也不生效"变回**真的会咬人**（孤儿行会被拒）；
//   - `journal_mode` 仍是 SQLite 默认的 `delete`：我们不注入 WAL（要 WAL 得自己往 URL 上
//     加 `_pragma=journal_mode(WAL)`，只影响那个句柄）。
func TestOpenConnectionPragmasFollowGolibAndDriver(t *testing.T) {
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
	if fk != 1 {
		t.Errorf("foreign_keys=%d，期望 1（ncruces 的 wasm 构建带 SQLITE_DEFAULT_FOREIGN_KEYS=1；"+
			"若驱动换了，请连同本文件顶部注释一起更新）", fk)
	}
	var timeout int
	if err := conn.QueryRowContext(t.Context(), `PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Errorf("busy_timeout=%d，期望 golib 注入的 5000", timeout)
	}
	var journal string
	if err := conn.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(journal, "delete") {
		t.Errorf("journal_mode=%q，期望 delete（golib 不注入 WAL；若已改成 WAL，请连同本文件顶部注释一起更新）", journal)
	}
}

// TestOpenReadOnly 只读 URL（`?mode=ro`）必须能开、能读、不能写。
//
// internal/persistence/store 的只读句柄就是走这条路（readonly.go），它是「只读体检」类能力的底座。
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

// TestOpenTightensDBFilePerms 库文件（及其事务旁车）必须收口到 0600：v20 起库内含明文密钥。
//
// 失效方式是**静默**的：SQLite 按进程 umask 建文件（常见的 022 就是 0644），于是同一台机器上的
// 其他用户能直接把 `providers.key` 的明文读走；而迁移备份与 `<MATE_HOME>/credentials.json` 都是
// 0600，谁检查都会觉得"已经收口了"。实测 2026-10-14：用户真实数据目录里 `mate.db` 就是 `0644`。
//
// 三段判据：① 旧版本建出来的 0644 库文件在打开后被收到 0600（这是真实的升级路径，不是假想）；
// ② 全新库（打开时文件还不存在）也要 0600；③ `?mode=ro` 是用户显式声明的只读语义，**不动权限**。
func TestOpenTightensDBFilePerms(t *testing.T) {
	dir := t.TempDir()

	// ① 已有库文件：模拟旧版本按 umask 建出来的 0644。
	old := filepath.Join(dir, "old.db")
	if err := os.WriteFile(old, nil, 0o644); err != nil {
		t.Fatalf("准备旧库文件: %v", err)
	}
	db, err := Open(old)
	if err != nil {
		t.Fatalf("open old: %v", err)
	}
	if err := db.Exec(`CREATE TABLE t(v TEXT)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := db.Exec(`INSERT INTO t(v) VALUES('x')`).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got := mustPerm(t, old); got != 0o600 {
		t.Errorf("库文件权限 = %o，期望 600（v20 起库内含明文密钥，同机其他用户不该读得到）", got)
	}
	// 旁车装的是同一批页面：已出现就必须同权。
	for _, p := range []string{old + "-wal", old + "-shm"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if got := mustPerm(t, p); got != 0o600 {
			t.Errorf("旁车 %s 权限 = %o，期望 600（它装的是同一批明文页面）", filepath.Base(p), got)
		}
	}

	// ② 全新库：文件在打开时还不存在（SQLite 尚未落盘或由我们按 0600 建）。
	fresh := filepath.Join(dir, "fresh.db")
	if _, err := Open(fresh); err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	if got := mustPerm(t, fresh); got != 0o600 {
		t.Errorf("新库文件权限 = %o，期望 600（第一次建库也必须按密钥面走）", got)
	}

	// ③ 只读挂载不动权限：那是用户显式声明的语义。
	ro := filepath.Join(dir, "ro.db")
	roDB, err := Open(ro)
	if err != nil {
		t.Fatalf("open ro(prepare): %v", err)
	}
	if err := roDB.Exec(`CREATE TABLE t(v TEXT)`).Error; err != nil {
		t.Fatalf("create table(ro prepare): %v", err)
	}
	if err := os.Chmod(ro, 0o644); err != nil {
		t.Fatalf("chmod ro prepare: %v", err)
	}
	if _, err := Open(ro + "?mode=ro"); err != nil {
		t.Fatalf("open ro: %v", err)
	}
	if got := mustPerm(t, ro); got != 0o644 {
		t.Errorf("只读挂载下权限被改成了 %o——`?mode=ro` 是用户显式声明的只读语义，不该由我们改权限", got)
	}
}

// TestOpenMemoryDSNLeavesNoFile 内存库的 DSN 不得在磁盘上留下任何东西。
//
// 这条是**实测补的**：收口逻辑按 `?` 切开 DSN 取路径，而 `file:memdb1?mode=memory` 在 `?` 之前
// 那半看起来就是个文件名——于是第一次跑测试就在**包目录**里凭空造出一个空文件 `file:memdb1`
// （它随后出现在 `git status` 的未跟踪清单里）。内存库没有文件落点，判据是"它也不该被造出来"。
func TestOpenMemoryDSNLeavesNoFile(t *testing.T) {
	const dsn = "file:memdb1?mode=memory"
	stray := "file:memdb1"
	defer os.Remove(stray) // 万一判据失效，别把垃圾留在工作区
	if _, err := os.Stat(stray); err == nil {
		os.Remove(stray)
	}
	if _, err := Open(dsn); err != nil {
		t.Fatalf("open memdb: %v", err)
	}
	if _, err := os.Stat(stray); err == nil {
		t.Errorf("内存库 DSN %q 在磁盘上留下了文件 %q——收口逻辑把 DSN 当成了路径", dsn, stray)
	}
}

// mustPerm 取一个文件的权限位（读不到即失败：判据的输入必须存在）。
func mustPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}
