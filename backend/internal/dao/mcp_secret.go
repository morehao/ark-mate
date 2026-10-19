package dao

import (
	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// McpSecretCond 是 mcp_secrets 表的查询条件。
//
// 本表主键列叫 `ref`（不是 `id`），不能用 BaseCond.ID（固定生成 `<table>.id = ?`）。
// `Ref` **总是**参与筛选（空串 = 查空引用串，与自定义查询时期一致）。
type McpSecretCond struct {
	*gormdao.BaseCond
	Ref string
}

// BuildCondition 实现 gormdao.Cond。
func (c *McpSecretCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	db.Where(clause.Eq{Column: clause.Column{Table: tableName, Name: "ref"}, Value: c.Ref})
}

// McpSecretDao 访问 mcp_secrets 表（v20：引用串 → 明文值）。
//
// 本 DAO 只提供列词汇（`McpSecretCond`）与泛型方法；"覆盖时 revision +1"、
// "删除返回是否命中"这些口径在 `internal/store/credentials.go`。
type McpSecretDao struct {
	*gormdao.Dao[model.McpSecretEntity, model.McpSecretEntityList, string]
}

// NewMcpSecretDao 构造 McpSecretDao。
func NewMcpSecretDao(getDB gormdao.DBGetter) *McpSecretDao {
	return &McpSecretDao{Dao: newDao[model.McpSecretEntity, model.McpSecretEntityList, string]("McpSecretDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *McpSecretDao) WithTx(tx *gorm.DB) *McpSecretDao {
	return &McpSecretDao{Dao: d.Dao.WithTx(tx)}
}
