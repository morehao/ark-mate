package dbclient

import (
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/glebarez/sqlite"
	"github.com/morehao/golib/dbaccess/dbgorm"
	"gorm.io/gorm"
)

// 自注册的纯 Go SQLite dialector。
//
// 为什么不直接用 golib 的 `dbgorm/driver/sqlite`：它底层是 `gorm.io/driver/sqlite`
// （CGO 的 mattn/go-sqlite3），而本仓以 `CGO_ENABLED=0` 构建单二进制（Dockerfile /
// Makefile），引入即构建失败。这里复用它的 URL 归一化规则，只把 dialector 换成
// glebarez 的 modernc 纯 Go 实现，从而同时满足「用 dbgorm 装配」与「无 CGO」。
//
// 另注：`dbgorm.matchDialector` 遍历的是注册表 map，两个都能匹配 `sqlite://` 的
// factory 会导致匹配结果不确定；因此本包**不得**再 import golib 的 sqlite driver。
const (
	// scheme 是本 dialector 认领的 URL 前缀，匹配与剥离均忽略大小写。
	scheme = "sqlite://"
	// fileScheme 是 SQLite 的 URI 文件名前缀。
	fileScheme = "file:"
	// memoryDB 是 SQLite 的内存库标记。
	memoryDB = ":memory:"
	// defaultTxLockValue 让事务以 BEGIN IMMEDIATE 开始，避免 BEGIN DEFERRED 在读写锁
	// 升级时绕过 busy_timeout 直接报 "database is locked"。
	defaultTxLockValue = "immediate"
	// txLockParam 是驱动认领的事务锁模式查询参数。
	txLockParam = "_txlock"
	// sharedMemoryBase 内存库的 file: URI 前缀。
	sharedMemoryBase = "file:"
	// memoryNamePrefix 内存库名前缀（配合 memSeq 保证同进程内每次 Open 各自独立）。
	memoryNamePrefix = "arkmate_mem_"
	// memoryMode 是 SQLite 的内存库 URI 参数。
	memoryMode = "memory"
	// sharedCache 让连接池内的所有连接共享同一个内存库。
	sharedCache = "shared"
	// pragmaParam 是驱动认领的 PRAGMA 查询参数（可重复）。
	pragmaParam = "_pragma"
)

// pragmaStatements 是每个连接建立时都要生效的 PRAGMA。
//
// 必须走 DSN 而不是 open 后 `db.Exec("PRAGMA ...")`：连接池会回收并重建连接，
// PRAGMA 是**连接级**状态，只在 open 后执行一次时新连接拿不到设置。
//   - busy_timeout：写锁竞争时的等待上限（驱动默认 5000，这里显式固定，避免随驱动变化）；
//   - foreign_keys：events/runs/messages 均以 FK 引用 conversations，必须开启才由库兜底。
var pragmaStatements = []string{
	"busy_timeout(5000)",
	"foreign_keys(1)",
}

// filePragmaStatements 仅文件库可用：内存库不支持 WAL。
var filePragmaStatements = []string{
	"journal_mode(WAL)",
	"synchronous(NORMAL)",
}

// memSeq 为内存库名提供进程内唯一后缀。
//
// 内存库若不唯一命名，同进程内两次 Open(":memory:") 会落到**同一个**共享缓存库
// （SQLite 以 URI 名认库），测试之间将互相污染；原实现用裸 `:memory:` 不受影响，
// 换成 file::memory:?cache=shared 后必须自行保证唯一。
var memSeq atomic.Uint64

type dialector struct{}

func init() {
	dbgorm.Register("sqlite", &dialector{})
}

func (d *dialector) Name() string { return "sqlite" }

func (d *dialector) MatchURL(urlStr string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(urlStr)), scheme)
}

func (d *dialector) Dialector(urlStr string) gorm.Dialector {
	return sqlite.Open(buildDSN(urlStr))
}

func (d *dialector) ParseURL(urlStr string) (string, error) {
	path, _ := splitPathQuery(stripScheme(urlStr))
	path = stripFileScheme(path)
	if path == "" || strings.EqualFold(path, memoryDB) {
		return memoryDB, nil
	}
	return path, nil
}

// buildDSN 把 `sqlite://` URL 归一化为驱动 DSN。
//
// 文件库统一改写成 file: URI 并转义路径——不这样做时 SQLite 会把 mode、cache
// 等连接参数静默丢弃；随后挂上 PRAGMA 与事务锁模式。
func buildDSN(urlStr string) string {
	path, rawQuery := splitPathQuery(stripScheme(urlStr))
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		// 查询串不可解析时退回不带的形态：宁可用默认行为，也不构造出危险的 DSN。
		values = url.Values{}
	}

	// 已是 file: URI 时保留调用方的命名与转义，避免二次改写。
	if strings.HasPrefix(strings.ToLower(path), fileScheme) {
		return joinQuery(path, values, false, filePragmaStatements)
	}
	if path == "" || strings.EqualFold(path, memoryDB) {
		name := sharedMemoryBase + memoryNamePrefix + strconv.FormatUint(memSeq.Add(1), 10)
		return joinQuery(name, values, true, nil)
	}
	return joinQuery(fileScheme+escapePath(path), values, false, filePragmaStatements)
}

// joinQuery 归一化查询串：内存库显式声明 mode=memory + 共享缓存，补默认事务锁，再追加 PRAGMA。
//
// mode=memory 不能省：`file:name` 只表示「以 URI 形式给出文件名」，SQLite 会把它当
// **磁盘上的相对路径**建库——只有 `:memory:` 这个特殊名字、或显式 `mode=memory`，
// 才是内存库。少了它，唯一命名会把内存库变成当前工作目录下的一堆库文件。
func joinQuery(base string, values url.Values, memory bool, pragmas []string) string {
	if memory {
		if values.Get("mode") == "" {
			values.Set("mode", memoryMode)
		}
		if values.Get("cache") == "" {
			values.Set("cache", sharedCache)
		}
	}
	if values.Get(txLockParam) == "" {
		values.Set(txLockParam, defaultTxLockValue)
	}
	for _, p := range append(append([]string(nil), pragmas...), pragmaStatements...) {
		values.Add(pragmaParam, p)
	}
	return base + "?" + values.Encode()
}

// —— 以下为纯字符串工具，语义与 golib/dbaccess/dbgorm/driver/sqlite 保持一致 ——

func stripScheme(urlStr string) string {
	trimmed := strings.TrimSpace(urlStr)
	if len(trimmed) >= len(scheme) && strings.EqualFold(trimmed[:len(scheme)], scheme) {
		return trimmed[len(scheme):]
	}
	return trimmed
}

func stripFileScheme(path string) string {
	if len(path) >= len(fileScheme) && strings.EqualFold(path[:len(fileScheme)], fileScheme) {
		return path[len(fileScheme):]
	}
	return path
}

func splitPathQuery(rest string) (path, rawQuery string) {
	if idx := strings.Index(rest, "?"); idx >= 0 {
		return rest[:idx], rest[idx+1:]
	}
	return rest, ""
}

// escapePath 转义会改变 file: URI 语义的字符（file: URI 按 URI 规则做百分号解码）。
func escapePath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}
