// Package dao 是持久层的**列词汇层**：一个实体一个 DAO，构建在 golib 的泛型
// `gormdao.Dao` 之上（`internal/model` 提供实体）。
//
// # 分层：DAO 不认识业务
//
// 本包**只回答"这张表有哪些列可用于筛选"**（`XxxCond`），并把 gormdao 的泛型方法原样交给
// 上层。排序、筛选组合、状态迁移、聚合、跨表不变量——这些**业务口径**——一律写在
// `internal/store`，那里用 `GetByCond`/`GetListByCond`/`CountByCond`/`UpdateMap` 直接表达。
//
// 例：`WorkspaceCond` 在本包（`path` / `is_default` 是列）；"展示顺序是 `list_order ASC,
// created_at ASC`"在 `store.ListWorkspaces`（那是读面口径）。**不要**再把 `ListByOrder` 这类
// 方法加回 DAO：它会让"顺序"这个决定散落在两层，且没有任何测试能证明两处一致。
//
// # 复用判据：先找 gormdao 的方法，再写自定义方法
//
//	要表达的东西                        用什么
//	按主键取一行 / 更新 / 删除          GetByID / UpdateByID / UpdateMap / Delete
//	按任意列取一行                      GetByCond（自定义 Cond 内嵌 *BaseCond）
//	按任意列取列表（含排序）            GetListByCond（排序写 BaseCond.OrderField）
//	分页                                GetPageListByCond
//	计数                                CountByCond
//	插入 / 批量插入                     Insert / BatchInsert（空列表即 no-op）
//	绑定事务                            WithTx（子类型转出自己的类型）
//
// 本包保留的自定义方法都是**机械原语**，没有业务词汇（对应方法上都写了理由）：
//
//   - **CAS**（`DefinitionDao`/`RowDao` 的 `CreateCAS`/`UpdateCAS`/`DeleteCAS`/
//     `UpdateColumnsCAS`）：判据是 `RowsAffected == 0`，泛型方法不返回它；
//   - **KV / 只追加插入**（`MetaDao` 的 Get/Set/Delete、`DefinitionRevisionDao.Append`、
//     `SchemaMigrationDao.Record`）：表本身就是键值/账本，没有排序或业务筛选；
//   - **取号器**（`EventSequenceDao.NextSeq`）：单条原子 upsert，是位次分配的唯一入口；
//   - **主键列不叫 `id` 的表**：泛型方法把主键列硬编码成 `id`（见下）。
//
// # 与 ark-iam 的 `pkg/iam/dao` 的差异
//
// ark-iam 的构造器签名为 `NewXxxDao(opts ...DaoOption)`，未显式注入时回落到**进程级单例库**
// （`dbclient.IamDB`）。ark-mate 的库是按实例打开的（`store.Open(path)`，且测试各自使用独立内存库），
// 没有进程级默认库；若保留同样的可选注入 + 回落形态，就只能伪造一个全局库，从而引入
// 「忘记注入 → 静默读写另一个库」的失败模式。因此这里把 `gormdao.DBGetter` 定为**必填参数**，
// DAO 永远绑定在明确的库上。
//
// # 主键列名假设
//
// gormdao 的泛型方法（GetByID/UpdateByID/UpdateMap/Delete）把主键列硬编码为 `id`。
// 本仓的 `meta.key`、`schema_migrations.version`、`mcp_secrets.ref`、
// `event_sequence.session_id` 都不叫 id，因此这些实体**不能**直接调用上述四个方法
// （会报 "no such column: id"）；它们的**条件查询**一律走 `GetByCond`/`GetListByCond`
// （自定义 Cond 里写真实主键列），写面只用 Insert/upsert 与自定义方法。
// （`events` 自 v17 起主键列就叫 `id`，按 id 取行直接走 `GetByID`。）
//
// # 软删除
//
// 本仓所有表都没有 `deleted_at` 列，故每个 DAO 都以 `gormdao.WithoutSoftDelete()` 构建——
// 不这样做，gormdao 会给每条查询附加 `deleted_at IS NULL`，直接报 "no such column"。
package dao

import "github.com/morehao/golib/dbaccess/gormdao"

// newDao 是各 DAO 构造器的共用装配点，集中两件容易漏的事：表名取自实体自身、
// 以及统一关闭软删除。
func newDao[T gormdao.Entity, L ~[]T, ID gormdao.IDType](daoName string, getDB gormdao.DBGetter) *gormdao.Dao[T, L, ID] {
	var zero T
	return gormdao.NewDao[T, L, ID](zero.TableName(), daoName, getDB, gormdao.WithoutSoftDelete())
}
