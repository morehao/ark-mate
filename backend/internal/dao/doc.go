// Package dao 是持久层的数据访问层：一个实体一个 DAO，构建在 golib 的泛型
// `gormdao.Dao` 之上（`internal/model` 提供实体）。
//
// 与 ark-iam 的 `pkg/iam/dao` 的差异：ark-iam 的构造器签名为 `NewXxxDao(opts ...DaoOption)`，
// 未显式注入时回落到**进程级单例库**（`dbclient.IamDB`）。ark-mate 的库是按实例打开的
// （`store.Open(path)`，且测试各自使用独立内存库），没有进程级默认库；若保留同样的
// 可选注入 + 回落形态，就只能伪造一个全局库，从而引入「忘记注入 → 静默读写另一个库」
// 的失败模式。因此这里把 `gormdao.DBGetter` 定为**必填参数**，DAO 永远绑定在明确的库上。
//
// 主键列名假设：gormdao 的泛型方法（GetByID/UpdateByID/UpdateMap/Delete）把主键列硬编码为
// `id`。本仓的 `meta.key`、`schema_migrations.version` 都不叫 id，
// 因此这些实体只用 Insert/自定义方法，**不要**对其调用上述四个方法（会报 "no such column: id"）。
// （`events` 自 v17 起主键列就叫 `id`，但它只用 Insert 与自定义游标方法，见 event.go。）
//
// 软删除：本仓所有表都没有 `deleted_at` 列，故每个 DAO 都以
// `gormdao.WithoutSoftDelete()` 构建——不这样做，gormdao 会给每条查询附加
// `deleted_at IS NULL`，直接报 "no such column"。
package dao

import "github.com/morehao/golib/dbaccess/gormdao"

// newDao 是各 DAO 构造器的共用装配点，集中两件容易漏的事：表名取自实体自身、
// 以及统一关闭软删除。
func newDao[T gormdao.Entity, L ~[]T, ID gormdao.IDType](daoName string, getDB gormdao.DBGetter) *gormdao.Dao[T, L, ID] {
	var zero T
	return gormdao.NewDao[T, L, ID](zero.TableName(), daoName, getDB, gormdao.WithoutSoftDelete())
}
