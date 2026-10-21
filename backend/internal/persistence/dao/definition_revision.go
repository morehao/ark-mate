package dao

import (
	"context"

	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DefinitionRevisionCond 是 definition_revisions 表的查询条件。
//
// 本表主键叫 `seq`（不是 `id`），所以按定义域筛选只能走 GetListByCond。
// `Kind` 与 `DefinitionID` **总是**参与筛选（空串 = 查空值，与原先的复合谓词一致）。
type DefinitionRevisionCond struct {
	*gormdao.BaseCond
	Kind         string
	DefinitionID string
}

// BuildCondition 实现 gormdao.Cond。
func (c *DefinitionRevisionCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "kind"}, Value: c.Kind})
	db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "definition_id"}, Value: c.DefinitionID})
}

// DefinitionRevisionDao 访问 definition_revisions 表（定义变更审计；v16 前叫 capability_revisions）。
//
// 本表**没有 `id` 列**（主键叫 `seq`），因此 gormdao 那批以 `id` 为主键的泛型方法
// （GetByID/UpdateByID/UpdateMap/Delete）一律不可用；条件查询走 GetListByCond +
// DefinitionRevisionCond，追加与聚合才用自定义方法（见包注释的复用判据）。
type DefinitionRevisionDao struct {
	*gormdao.Dao[model.DefinitionRevisionEntity, model.DefinitionRevisionEntityList, int64]
}

// NewDefinitionRevisionDao 构造 DefinitionRevisionDao（getDB 必填，见包注释）。
func NewDefinitionRevisionDao(getDB gormdao.DBGetter) *DefinitionRevisionDao {
	return &DefinitionRevisionDao{Dao: newDao[model.DefinitionRevisionEntity, model.DefinitionRevisionEntityList, int64]("DefinitionRevisionDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *DefinitionRevisionDao) WithTx(tx *gorm.DB) *DefinitionRevisionDao {
	return &DefinitionRevisionDao{Dao: d.Dao.WithTx(tx)}
}

// Append 追一条审计。审计是**只追加**的：不提供更新与删除。
//
// 刻意不显式写入 seq：留空（零值）由 SQLite 的 AUTOINCREMENT 分配，这样"分配序号"与
// "插行"是同一个原子动作，并发下不可能出现两条相同的 seq，也不可能出现空洞被复用。
//
// 这是本 DAO 唯一保留的写原语；读面（按定义列历史、最近 N 条、最大 seq）是业务口径，
// 在 `internal/persistence/store/capability.go` 用 `DefinitionRevisionCond`/泛型方法表达。
func (d *DefinitionRevisionDao) Append(ctx context.Context, rev *model.DefinitionRevisionEntity) error {
	return d.DB(ctx).Omit("seq").Create(rev).Error
}
