// Package dbclient 装配控制面的数据库句柄（gorm + golib dbaccess 的 SQLite 驱动）。
//
// 与 ark-iam 的 `pkg/dbclient` 的差异：ark-iam 是「一进程一库」的多租户服务，
// 因此在包里持有 `dbMap[string]*gorm.DB` 全局单例；ark-mate 的持久层是**按实例**
// 打开的（`store.Open(path)`，且测试大量使用各自独立的内存库），所以这里不持有
// 单例，只提供 Open 一件事。
package dbclient

import (
	"fmt"

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
	//     都在父行之后写，因此这是净收益；`internal/store` 里"因为驱动不注入 foreign_keys、
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
// 迁移不在这里做：schema 版本由 store 的自管迁移内核负责（见 internal/store/migrate.go）。
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
	return db, nil
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
