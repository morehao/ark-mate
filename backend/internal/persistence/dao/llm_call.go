package dao

import (
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// LLMCallCond 调用流水查询条件。
type LLMCallCond struct {
	*gormdao.BaseCond
	SessionID string
	Purpose   string
}

// BuildCondition 实现 gormdao.Cond。
func (c *LLMCallCond) BuildCondition(db *gorm.DB, tableName string) {
	if c.BaseCond != nil {
		c.BaseCond.BuildCondition(db, tableName)
	}
	if c.SessionID != "" {
		db.Where("session_id = ?", c.SessionID)
	}
	if c.Purpose != "" {
		db.Where("purpose = ?", c.Purpose)
	}
}

// LLMCallDao 访问 llm_calls 表（内部模型调用流水，一次调用一行）。
//
// 本 DAO 只提供列词汇与泛型方法；聚合查询（按 purpose/model/provider/日分桶）在
// `internal/persistence/store/llm_calls.go`（它跨列汇总是业务语义，不属于列词汇）。
type LLMCallDao struct {
	*gormdao.Dao[model.LLMCallEntity, model.LLMCallEntityList, string]
}

// NewLLMCallDao 构造 LLMCallDao（getDB 必填，见包注释）。
func NewLLMCallDao(getDB gormdao.DBGetter) *LLMCallDao {
	return &LLMCallDao{Dao: newDao[model.LLMCallEntity, model.LLMCallEntityList, string]("LLMCallDao", getDB)}
}

// WithTx 返回绑定事务的新 DAO。
func (d *LLMCallDao) WithTx(tx *gorm.DB) *LLMCallDao {
	return &LLMCallDao{Dao: d.Dao.WithTx(tx)}
}
