// Package dbclient 装配控制面的数据库句柄（gorm + 纯 Go SQLite）。
//
// 与 ark-iam 的 `pkg/dbclient` 的差异：ark-iam 是「一进程一库」的多租户服务，
// 因此在包里持有 `dbMap[string]*gorm.DB` 全局单例；ark-forge 的持久层是**按实例**
// 打开的（`store.Open(path)`，且测试大量使用各自独立的内存库），所以这里不持有
// 单例，只提供 Open + dialector 注册两件事。
package dbclient

import (
	"fmt"

	"github.com/morehao/golib/dbaccess/dbgorm"
	"github.com/morehao/golib/glog"

	// glog 的日志实现按 LoggerType 注册：不 import 具体 driver 时 New 会直接报
	// "unknown LoggerType"。本仓的日志出口是 log/slog，故用 slog driver 而非 zap。
	_ "github.com/morehao/golib/glog/driver/slog"
	"gorm.io/gorm"
)

const (
	// serviceName ORM 日志里的服务名。
	serviceName = "ark-forge"
	// maxOpenConns SQLite 是单写者模型：单连接即天然串行化所有事务，
	// 规避多连接下的 `database is locked` 与读写竞态（与原 database/sql 实现一致）。
	maxOpenConns = 1
)

// Open 打开（必要时创建）SQLite 库并返回 gorm 句柄。
// path 为文件路径；空串或 ":memory:" 表示进程内临时库（测试用）。
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

// ormLogConfig 是 ORM 层日志配置。
//
// 默认压到 warn：本仓的日志出口是 `log/slog`，从未初始化 golib 的 glog，
// 因此 glog 会退化成「默认 debug + 控制台」——若不显式提级，gorm 会把**每一条 SQL**
// 打到 stdout（原 database/sql 实现完全不打印），既污染服务日志也淹没测试输出。
// 提到 warn 后语义等价于「只报真错误」：Trace 在未配 SlowThreshold 时走 Debugw，
// 被过滤；执行出错走 Errorw，照常可见。
func ormLogConfig() *glog.LogConfig {
	return &glog.LogConfig{
		Service:    serviceName,
		Level:      glog.WarnLevel,
		Writers:    []glog.WriterConfig{{Type: glog.WriterConsole, Level: glog.WarnLevel}},
		LoggerType: glog.LoggerTypeSlog,
	}
}
