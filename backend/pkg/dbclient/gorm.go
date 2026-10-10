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
	// 已知代价（2026-10-10 决策，用户明确选择"照字面用 golib 的 driver"）：
	//   - 底层是 **CGO** 的 mattn/go-sqlite3。`CGO_ENABLED=0`（桌面核 Makefile
	//     `core/desktop`、容器镜像 Dockerfile）也能编译，但注册的是 stub，
	//     **连库时**才报 "Binary was compiled with 'CGO_ENABLED=0', go-sqlite3
	//     requires cgo to work"——编译期无感、运行期致命。
	//   - 相比原先自注册的纯 Go dialector，连接级行为由 golib 决定：不注入
	//     journal_mode(WAL)/synchronous、不固定 busy_timeout、不开启 foreign_keys，
	//     且内存库统一命名 `file::memory:?cache=shared`（同进程多次 Open(":memory:")
	//     会落到**同一个**库）。这些差异由 pkg/dbclient/dbclient_test.go 显式记录。
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
