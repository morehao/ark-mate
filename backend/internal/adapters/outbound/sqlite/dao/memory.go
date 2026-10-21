package dao

import (
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PermissionMemoryCond 权限记忆查询条件。
type PermissionMemoryCond struct {
	*gormdao.BaseCond
	SessionID string
}

// BuildCondition 实现 gormdao.Cond。
func (c *PermissionMemoryCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.SessionID != "" {
		db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "session_id"}, Value: c.SessionID})
	}
}

// PermissionMemoryDao 访问 permission_memories 表（会话级权限记忆，allow/reject 双侧对称）。
//
// 本 DAO 只提供列词汇（`PermissionMemoryCond`）与泛型方法；upsert 的冲突列与
// "同侧重复授权不刷新授予时间"的 CASE 在 `internal/adapters/outbound/sqlite/store/memories.go`。
type PermissionMemoryDao struct {
	*gormdao.Dao[model.PermissionMemoryEntity, model.PermissionMemoryEntityList, string]
}

// NewMemoryDao 构造 PermissionMemoryDao（getDB 必填，见包注释）。
func NewMemoryDao(getDB gormdao.DBGetter) *PermissionMemoryDao {
	return &PermissionMemoryDao{Dao: newDao[model.PermissionMemoryEntity, model.PermissionMemoryEntityList, string]("PermissionMemoryDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *PermissionMemoryDao) WithTx(tx *gorm.DB) *PermissionMemoryDao {
	return &PermissionMemoryDao{Dao: d.Dao.WithTx(tx)}
}
