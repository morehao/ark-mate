// Package dbclient 装配控制面的数据库句柄（gorm + golib dbaccess 的 SQLite 驱动）。
//
// 与 ark-iam 的 `pkg/dbclient` 的差异：ark-iam 是「一进程一库」的多租户服务，
// 因此在包里持有 `dbMap[string]*gorm.DB` 全局单例；ark-mate 的持久层是**按实例**
// 打开的（`store.Open(path)`，且测试大量使用各自独立的内存库），所以这里不持有
// 单例，只提供 Open 一件事。
package dbclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/morehao/golib/dbaccess/dbgorm"
	"github.com/morehao/golib/glog"
	"gorm.io/gorm"

	// SQLite dialector 直接取 golib 的封装（URL 归一化 + dialector 注册）。
	//
	// 与 glog 的 driver 不同，dbgorm 的 driver 是**数据面**的注册表：`dbgorm.New`
	// 按 URL 前缀在注册表里匹配，不 import 具体驱动就报
	// "no registered dialector matches url"。它属于本包自己的依赖，因此匿名引入
	// 留在这里（与 ark-go-starter 的 pkg/dbclient 一致）；日志出口的 driver 才归装配根。
	//
	// 驱动形态（2026-10-10 决策：用户明确选择"照字面用 golib 的 driver"；
	// 2026-10-12 决策：随 golib v1.32.22 回到**纯 Go**）：
	//   - 引擎是 `github.com/ncruces/go-sqlite3`（SQLite 编译成 wasm 再翻译回 Go），
	//     因此**不依赖 CGO**：`CGO_ENABLED=0` 可编可跑，编译不需要 C 工具链
	//     （`CC=false` 也编得出），`GOOS=linux` 交叉编译即得静态产物、不需要 C 交叉
	//     工具链（反过来 `CGO_ENABLED=1` 的交叉编译直接编不过）。注意"更静态"这个收益
	//     **按平台成立**：macOS 上 0 与 1 编出的二进制逐字节同大小、`otool -L` 相同
	//     （都只链系统库），差异只在需要动态 libc 的目标上。四个构建面（桌面核
	//     `Makefile` 的 `core/desktop`、容器 `Dockerfile` 的 build 阶段、两份桌面验收
	//     脚本的 `rebuildCore`）因此一律 `CGO_ENABLED=0`。
	//   - 这个"取值一致"不是风格问题：v1.32.21 及以前的 golib 方言落在 CGO 的
	//     mattn/go-sqlite3 上，那时的 `CGO_ENABLED=0` 会编出一个**运行期连不上库**的
	//     stub（编译通过、连库才报 "go-sqlite3 requires cgo to work"）。谁把 golib 降级
	//     回去而构建面仍是 0，就会原样复现那个静默故障——所以依赖与构建面两侧由
	//     internal/archtest 的 TestSqliteDriverStaysPureGo 一起钉着。
	//   - 相比原先自注册的纯 Go dialector，连接级行为仍由 golib 决定，且 v1.32.22 换了
	//     一批事实（下面每条都由 pkg/dbclient/dbclient_test.go 显式记录）：
	//     注入 `busy_timeout(5000)`（排在所有 PRAGMA 之前，调用方显式传入时不覆盖）、
	//     文件库注入 `_txlock=immediate`（BEGIN IMMEDIATE，避开锁升级绕过 busy_timeout
	//     直接报 database is locked）；**不注入** journal_mode/synchronous（仍是
	//     `journal_mode=delete` + `synchronous=FULL`）。
	//   - **外键从"写了也不生效"变成真的生效**：wasm 构建带
	//     `SQLITE_DEFAULT_FOREIGN_KEYS=1`（SQLite 官方推荐编译项），DDL 里的 REFERENCES
	//     会真的拦住孤儿行（旧 mattn 构建下 SQLite 默认关闭）。仓库里所有写子表的路径
	//     都在父行之后写，因此这是净收益；`internal/persistence/store` 里"因为驱动不注入 foreign_keys、
	//     所以写了也不生效"的老注释已随之改正。
	//   - 内存库：每次 `dbgorm.New` 分配一个**新**库名（`file:/mem_<n>?vfs=memdb`），
	//     所以同进程两次 `Open(":memory:")` 拿到两个互不可见的空库（旧 driver 是固定名
	//     `file::memory:?cache=shared`，同进程**共享**同一个库）。要跨连接池共享就用命名
	//     内存库 `sqlite://file:memdb1?mode=memory`：同名即同库。
	_ "github.com/morehao/golib/dbaccess/dbgorm/driver/sqlite"
)

const (
	// serviceName ORM 日志里的服务名。
	serviceName = "ark-mate"
	// scheme 是 golib SQLite dialector 认领的 URL 前缀。
	scheme = "sqlite://"
	// memoryDB 是 SQLite 的内存库标记；空路径等价于它。
	memoryDB = ":memory:"
	// maxOpenConns SQLite 是单写者模型：单连接即天然串行化所有事务，
	// 规避多连接下的 `database is locked` 与读写竞态（与原 database/sql 实现一致）。
	maxOpenConns = 1
)

// Open 打开（必要时创建）SQLite 库并返回 gorm 句柄。
// path 为文件路径（可带 `?mode=ro` 这类 SQLite URI 参数）；空串或 ":memory:" 表示内存库。
//
// 迁移不在这里做：schema 版本由 store 的自管迁移内核负责（见 internal/persistence/store/migrate.go）。
func Open(path string) (*gorm.DB, error) {
	if path == "" {
		path = memoryDB
	}
	db, err := dbgorm.New(&dbgorm.Config{
		URL:          scheme + path,
		Service:      serviceName,
		MaxOpenConns: maxOpenConns,
		MaxIdleConns: maxOpenConns,
	}, dbgorm.WithLogConfig(ormLogConfig()))
	if err != nil {
		return nil, fmt.Errorf("dbclient: open %q: %w", path, err)
	}
	tightenFilePerms(path)
	return db, nil
}

// dbFilePerm 是库文件及其事务旁车文件的权限（v20 起库内含明文密钥）。
const dbFilePerm = os.FileMode(0o600)

// tightenFilePerms 把库文件收口到 0600，并顺手收口已存在的 `-wal` / `-shm` 旁车。
//
// **为什么必须在"打开处"做**：v20 让 `providers.key` 进了库，于是 `mate.db` 从此与
// `<MATE_HOME>/credentials.json` 同级敏感。而 SQLite 按**进程 umask** 建文件——常见的 `022`
// 就是 `0644`，于是同一台机器上的其他用户能直接读走明文密钥。这件事**完全静默**：迁移备份
// （`VACUUM INTO` + 显式 chmod）早就是 0600 了，只有"正在用的那个库"不是，而它恰恰是最常被拷贝、
// 被同步、被塞进诊断包的那一份。实测 2026-10-14：用户真实数据目录里 `mate.db` 就是 `0644`。
//
// 收口失败**只 WARN 不致命**：为了一个权限位拒绝启动整台平台，代价比权限宽更大；事实由
// `mate doctor` 的敏感文件清单（`providers.sensitiveFiles[]`，`expected` = `0600`）按密钥面报出。
//
// 只读挂载（`?mode=ro`）**不动**：那是用户显式声明的只读语义，改权限不是我们该做的事。
func tightenFilePerms(dsn string) {
	fsPath, readOnly := splitDSN(dsn)
	if fsPath == "" || fsPath == memoryDB || readOnly {
		return
	}
	// 只对**能确认是文件落点**的路径动手：绝对路径，或至少带一级目录。
	// 反例是实测出来的：`file:memdb1?mode=memory` 在 `?` 之前那半长得像个文件名，于是
	// 第一版收口逻辑在包目录里凭空造出一个空文件 `file:memdb1`。
	if !filepath.IsAbs(fsPath) && filepath.Dir(fsPath) == "." {
		return
	}
	// 旁车先看一遍（它们可能与库文件不同权：SQLite 用库文件的模式建旁车，但库文件被我们
	// 上一版改成 0644 之前建出来的旁车会留在旧模式上）。
	for _, p := range []string{fsPath, fsPath + "-wal", fsPath + "-shm"} {
		info, err := os.Stat(p)
		if err != nil {
			// 库文件不存在 = 还没落盘（首次连接尚未建文件）：按 0600 建一个空文件，
			// SQLite 把 0 字节文件当空库，于是"第一次建库"也是 0600。
			// **父目录必须已经存在**：路径写错时不该被我们"修"成一个真实文件。
			if os.IsNotExist(err) && p == fsPath {
				if dir := filepath.Dir(fsPath); dir != "" {
					if _, dErr := os.Stat(dir); dErr == nil {
						if wErr := os.WriteFile(p, nil, dbFilePerm); wErr != nil {
							glog.Warnw(context.Background(), "dbclient: 建库文件失败（权限将按 umask 走）",
								"path", p, "err", wErr)
						}
					}
				}
			}
			continue
		}
		if info.Mode().Perm() == dbFilePerm {
			continue
		}
		if cErr := os.Chmod(p, dbFilePerm); cErr != nil {
			glog.Warnw(context.Background(), "dbclient: 收口库文件权限失败（库里含明文密钥）",
				"path", p, "perm", info.Mode().Perm().String(), "err", cErr)
			continue
		}
		glog.Warnw(context.Background(), "dbclient: 库文件权限过宽，已收口到 0600（v20 起库里含明文密钥）",
			"path", p, "was", info.Mode().Perm().String())
	}
}

// splitDSN 把调用方给的 DSN 拆成（文件系统路径, 是否只读）。
//
// 只按 `?` 切（SQLite URI 的参数都在 `?` 之后），并认出 `mode=ro`——**不解析 SQLite URI 的
// 全部语义**：这里只回答"要 chmod 哪个文件"与"要不要动它"。
func splitDSN(dsn string) (string, bool) {
	// 内存库的几种写法都要按**整条 DSN** 认出来，不能只看 `?` 之前那半——`file:memdb1?mode=memory`
	// 的前半截看起来就像个文件名，第一版就是这么在包目录里造出空文件的。
	lower := strings.ToLower(dsn)
	if lower == "" || lower == memoryDB || strings.Contains(lower, ":memory:") ||
		strings.Contains(lower, "mode=memory") || strings.Contains(lower, "vfs=memdb") {
		return "", false
	}
	fsPath, query, _ := strings.Cut(dsn, "?")
	readOnly := false
	for _, kv := range strings.Split(query, "&") {
		if strings.EqualFold(strings.TrimSpace(kv), "mode=ro") {
			readOnly = true
		}
	}
	return fsPath, readOnly
}

// ormLogConfig 是 ORM 层日志配置（zap driver，与全仓 golog 出口一致）。
//
// 默认压到 warn：glog 的内置默认是「debug + console」，若不显式提级，gorm 会把
// **每一条 SQL** 打到 stdout（原 database/sql 实现完全不打印），既污染服务日志也淹没测试输出。
// 提到 warn 后语义等价于「只报真错误」：Trace 在未配 SlowThreshold 时走 Debugw，
// 被过滤；执行出错走 Errorw，照常可见。
//
// 这里只声明 LoggerType，不 import driver：driver 由装配根
// （cmd/mate，见其中的匿名引入）与测试夹具注册，本包不替调用方决定日志出口。
func ormLogConfig() *glog.LogConfig {
	return &glog.LogConfig{
		Service:    serviceName,
		Level:      glog.WarnLevel,
		Writers:    []glog.WriterConfig{{Type: glog.WriterConsole, Level: glog.WarnLevel}},
		LoggerType: glog.LoggerTypeZap,
	}
}
